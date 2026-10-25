package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Node is one entry (file or directory) in the scanned tree. Every node carries
// two sizes in bytes: the space allocated on disk (Size and Self, matching du)
// and the apparent, logical length (Apparent and ApparentSelf, matching ls).
// They differ for sparse files, compressed files, cloud-only placeholders, and
// the block rounding of small files. Which one is shown and sorted by is a
// display choice (--apparent, or a key in the explorer), not a scan option.
type Node struct {
	Name         string  `json:"name"`
	Path         string  `json:"path"`
	IsDir        bool    `json:"dir"`
	Size         int64   `json:"size"`         // on disk: this entry plus everything beneath it
	Self         int64   `json:"self"`         // on disk: just this entry (the dir inode, or the file)
	Apparent     int64   `json:"apparent"`     // logical: this entry plus everything beneath it
	ApparentSelf int64   `json:"apparentSelf"` // logical: just this entry
	Files        int64   `json:"files"`        // regular files in the subtree
	Dirs         int64   `json:"dirs"`         // directories in the subtree, excluding self
	Children     []*Node `json:"children,omitempty"`
	Err          string  `json:"error,omitempty"` // why the directory could not be read

	// parent points up the tree so attach can fold each new entry into every
	// ancestor's totals as it is discovered (nil at a scan root).
	parent *Node
}

// bytes is the node's total under the chosen measure: apparent size, or the
// space used on disk.
func (n *Node) bytes(apparent bool) int64 {
	if apparent {
		return n.Apparent
	}
	return n.Size
}

// scanner holds the configuration and shared counters for one scan. A zero
// scanner is usable; Scan fills in the rest.
type scanner struct {
	follow   bool // resolve symlinks instead of counting the link itself
	oneFS    bool // do not descend into other filesystems
	excludes []string

	rootDev uint64        // device of the root, for oneFS
	sem     chan struct{} // bounds the number of concurrent directory readers
	scanned atomic.Int64  // entries visited so far (for progress)

	// treeMu guards every mutation of published nodes (attach and root errors),
	// so snapshot can pause the walk and copy an internally consistent tree.
	treeMu sync.Mutex

	// seen maps dev/ino identities already counted in this scan to the path
	// that claimed them: hardlinked files (so their contents count once, like
	// du) and, with -L, directories (so symlink cycles and repeated targets are
	// walked once). Prepared by scan via reclaim, shared across the workers.
	mu   sync.Mutex
	seen map[[2]uint64]string
}

// scan walks root and returns its finished tree. Directories are read
// concurrently up to a bound derived from GOMAXPROCS.
func scan(root string, s *scanner) (*Node, error) {
	n, done, err := scanStart(root, s)
	if err != nil {
		return nil, err
	}
	<-done
	return n, nil
}

// scanStart begins a scan and returns the root node immediately, filling the
// tree in the background; done closes when the walk finishes. While done is
// open the tree is being mutated concurrently and may only be read through
// snapshot, which pauses the walk for the duration of the copy.
func scanStart(root string, s *scanner) (*Node, <-chan struct{}, error) {
	// Follow the root itself even without -L: the user named it explicitly.
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, err
	}

	workers := runtime.GOMAXPROCS(0) * 2
	if workers < 4 {
		workers = 4
	}
	s.sem = make(chan struct{}, workers)
	s.reclaim(root)

	if _, dev, ok := diskUsage(info); ok {
		s.rootDev = dev
	}
	// Remember the root so a -L symlink pointing back at it is not re-walked.
	s.dupDir(info, root)

	done := make(chan struct{})
	n := &Node{Name: filepath.Base(root), Path: root, IsDir: info.IsDir()}
	n.Self, n.ApparentSelf = measure(info)
	n.Size, n.Apparent = n.Self, n.ApparentSelf
	s.scanned.Add(1)

	if !n.IsDir {
		n.Files = 1
		close(done)
		return n, done, nil
	}
	go func() {
		s.fill(n, root, s.rootDev)
		close(done)
	}()
	return n, done, nil
}

// snapshot deep-copies the tree under the accounting lock, pausing the walk,
// so the copy is internally consistent and immutable from then on.
func (s *scanner) snapshot(n *Node) *Node {
	s.treeMu.Lock()
	defer s.treeMu.Unlock()
	return clone(n, nil)
}

func clone(n, parent *Node) *Node {
	c := *n
	c.parent = parent
	if n.Children != nil {
		c.Children = make([]*Node, len(n.Children))
		for i, k := range n.Children {
			c.Children[i] = clone(k, &c)
		}
	}
	return &c
}

// measure reports both sizes of info: the bytes allocated on disk and the
// apparent length. Without platform block counts the two are the same.
func measure(info fs.FileInfo) (disk, apparent int64) {
	apparent = info.Size()
	if b, _, ok := diskUsage(info); ok {
		return b, apparent
	}
	return apparent, apparent
}

// fill reads path's entries into the already-published node n, recursing
// (concurrently) into subdirectories. Directories are attached before they are
// walked, so a snapshot taken mid-scan shows them growing in place. dev is the
// device path lives on, used to spot mount points.
func (s *scanner) fill(n *Node, path string, dev uint64) {
	entries, err := os.ReadDir(path)
	if err != nil {
		s.treeMu.Lock()
		n.Err = err.Error()
		s.treeMu.Unlock()
		return
	}

	var wg sync.WaitGroup
	for _, e := range entries {
		name := e.Name()
		childPath := filepath.Join(path, name)
		if s.excluded(childPath, name) {
			continue
		}
		ci, err := e.Info()
		if err != nil {
			continue
		}

		// Without -L a symlink falls through to the leaf case below and is
		// listed as an entry of its own size, never its target's.
		if ci.Mode()&fs.ModeSymlink != 0 && s.follow {
			ti, err := os.Stat(childPath)
			if err != nil {
				continue // dangling link; nothing to measure
			}
			ci = ti
		}

		if ci.IsDir() {
			childDev := dev
			if _, d, ok := diskUsage(ci); ok {
				if s.oneFS && d != s.rootDev {
					continue
				}
				// A new device means a mount point. Kernel filesystems such as
				// /proc and /sys hold no data and report nonsense sizes, so they
				// are skipped, like ncdu's --exclude-kernfs. Checking only at
				// mount points keeps the extra statfs off the common path, and a
				// scan rooted inside one (diskusage /proc) still works.
				if d != dev && isKernFS(childPath) {
					continue
				}
				childDev = d
			}
			child := &Node{Name: name, Path: childPath, IsDir: true}
			if s.dupDir(ci, childPath) {
				// -L led back to a directory this scan already walked; list it
				// empty so it still shows up but nothing is counted twice.
				s.attach(n, child)
				continue
			}
			child.Self, child.ApparentSelf = measure(ci)
			child.Size, child.Apparent = child.Self, child.ApparentSelf
			s.attach(n, child)
			s.scanned.Add(1)
			// Spawn a worker if a slot is free, otherwise recurse inline. The
			// non-blocking select keeps concurrency bounded without deadlock.
			select {
			case s.sem <- struct{}{}:
				wg.Add(1)
				go func(c *Node, p string, d uint64) {
					defer wg.Done()
					defer func() { <-s.sem }()
					s.fill(c, p, d)
				}(child, childPath, childDev)
			default:
				s.fill(child, childPath, childDev)
			}
			continue
		}

		// Regular, special, or (without -L) symlink file. An extra hardlink to a
		// file already counted is listed at zero bytes, so directory totals
		// match du (which path carries the size depends on scan order).
		child := &Node{Name: name, Path: childPath}
		if !s.dupFile(ci, childPath) {
			child.Self, child.ApparentSelf = measure(ci)
		}
		child.Size, child.Apparent = child.Self, child.ApparentSelf
		child.Files = 1
		s.attach(n, child)
		s.scanned.Add(1)
	}

	wg.Wait()
}

// attach publishes child under parent and folds its own weight (Self, plus one
// file or directory) into every ancestor, so partially scanned directories
// always report the totals discovered so far. Descendants attached later fold
// themselves in the same way, which reproduces the finished-tree invariant
// Size = Self + sum of children's Size.
func (s *scanner) attach(parent, child *Node) {
	s.treeMu.Lock()
	child.parent = parent
	parent.Children = append(parent.Children, child)
	for p := parent; p != nil; p = p.parent {
		p.Size += child.Self
		p.Apparent += child.ApparentSelf
		p.Files += child.Files
		if child.IsDir {
			p.Dirs++
		}
	}
	s.treeMu.Unlock()
}

// dupFile reports whether info is a file whose contents were already counted
// elsewhere in this scan: another hardlink, or — with -L, where every file is
// tracked because symlinks can reach the same target twice — another path
// resolving to the same inode.
func (s *scanner) dupFile(info fs.FileInfo, path string) bool {
	dev, ino, nlink, ok := fileID(info)
	if !ok || (nlink < 2 && !s.follow) {
		return false
	}
	return s.remember(dev, ino, path)
}

// dupDir reports whether a directory was already walked in this scan. Only -L
// can revisit a directory (via a symlink cycle or two links to one target), so
// without it this is a no-op.
func (s *scanner) dupDir(info fs.FileInfo, path string) bool {
	if !s.follow {
		return false
	}
	dev, ino, _, ok := fileID(info)
	if !ok {
		return false
	}
	return s.remember(dev, ino, path)
}

// remember records path as the claimer of a dev/ino identity, reporting
// whether some other path had already claimed it.
func (s *scanner) remember(dev, ino uint64, path string) bool {
	id := [2]uint64{dev, ino}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, claimed := s.seen[id]; claimed {
		return true
	}
	s.seen[id] = path
	return false
}

// reclaim prepares the dedup map for a scan rooted at root. On a rescan the
// identities claimed under root are released so the fresh walk counts them
// again, while claims outside it are kept — a hardlink whose size-carrying
// path lies elsewhere stays deduplicated instead of inflating the totals.
func (s *scanner) reclaim(root string) {
	if s.seen == nil {
		s.seen = make(map[[2]uint64]string)
		return
	}
	for id, claimer := range s.seen {
		if underneath(root, claimer) {
			delete(s.seen, id)
		}
	}
}

// underneath reports whether p is root itself or lies below it.
func underneath(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// excluded reports whether a path or its base name matches any --exclude glob.
func (s *scanner) excluded(path, name string) bool {
	for _, p := range s.excludes {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
		if ok, _ := filepath.Match(p, path); ok {
			return true
		}
	}
	return false
}

// sortChildren orders a node's children largest first under the chosen
// measure, breaking ties by name so output is stable.
func sortChildren(n *Node, apparent bool) {
	sort.Slice(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if as, bs := a.bytes(apparent), b.bytes(apparent); as != bs {
			return as > bs
		}
		return a.Name < b.Name
	})
}

// walk visits n and every node beneath it, depth first.
func walk(n *Node, fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}
