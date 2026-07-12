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

// Node is one entry (file or directory) in the scanned tree. Sizes are in
// bytes and use whatever measure the scanner was configured with (allocated
// disk blocks by default, apparent size with --apparent).
type Node struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	IsDir    bool    `json:"dir"`
	Size     int64   `json:"size"`  // this entry plus everything beneath it
	Self     int64   `json:"self"`  // just this entry (the dir inode, or the file)
	Files    int64   `json:"files"` // regular files in the subtree
	Dirs     int64   `json:"dirs"`  // directories in the subtree, excluding self
	Children []*Node `json:"children,omitempty"`
	Err      string  `json:"error,omitempty"` // why the directory could not be read
}

// scanner holds the configuration and shared counters for one scan. A zero
// scanner is usable; Scan fills in the rest.
type scanner struct {
	apparent bool // measure logical file size instead of disk blocks
	follow   bool // resolve symlinks instead of counting the link itself
	oneFS    bool // do not descend into other filesystems
	excludes []string

	rootDev uint64        // device of the root, for oneFS
	sem     chan struct{} // bounds the number of concurrent directory readers
	scanned atomic.Int64  // entries visited so far (for progress)

	// seen maps dev/ino identities already counted in this scan to the path
	// that claimed them: hardlinked files (so their contents count once, like
	// du) and, with -L, directories (so symlink cycles and repeated targets are
	// walked once). Prepared by scan via reclaim, shared across the workers.
	mu   sync.Mutex
	seen map[[2]uint64]string
}

// scan walks root and returns its tree. Directories are read concurrently up
// to a bound derived from GOMAXPROCS.
func scan(root string, s *scanner) (*Node, error) {
	// Follow the root itself even without -L: the user named it explicitly.
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
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

	if !info.IsDir() {
		n := &Node{Name: filepath.Base(root), Path: root}
		n.Self = s.bytes(info)
		n.Size = n.Self
		n.Files = 1
		s.scanned.Add(1)
		return n, nil
	}
	return s.dir(root, info), nil
}

// bytes reports how many bytes info occupies under the active measure.
func (s *scanner) bytes(info fs.FileInfo) int64 {
	if s.apparent {
		return info.Size()
	}
	if b, _, ok := diskUsage(info); ok {
		return b
	}
	return info.Size()
}

// dir scans a single directory and returns its node, recursing (concurrently)
// into subdirectories.
func (s *scanner) dir(path string, info fs.FileInfo) *Node {
	n := &Node{Name: filepath.Base(path), Path: path, IsDir: true}
	n.Self = s.bytes(info)
	n.Size = n.Self
	s.scanned.Add(1)

	entries, err := os.ReadDir(path)
	if err != nil {
		n.Err = err.Error()
		return n
	}

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	// add folds a finished child into n. Siblings may run concurrently, so all
	// mutations of n go through the mutex.
	add := func(child *Node) {
		mu.Lock()
		n.Children = append(n.Children, child)
		n.Size += child.Size
		n.Files += child.Files
		n.Dirs += child.Dirs
		if child.IsDir {
			n.Dirs++
		}
		mu.Unlock()
	}

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
			if s.oneFS {
				if _, dev, ok := diskUsage(ci); ok && dev != s.rootDev {
					continue
				}
			}
			if s.dupDir(ci, childPath) {
				// -L led back to a directory this scan already walked; list it
				// empty so it still shows up but nothing is counted twice.
				add(&Node{Name: name, Path: childPath, IsDir: true})
				continue
			}
			// Spawn a worker if a slot is free, otherwise recurse inline. The
			// non-blocking select keeps concurrency bounded without deadlock.
			select {
			case s.sem <- struct{}{}:
				wg.Add(1)
				go func(p string, fi fs.FileInfo) {
					defer wg.Done()
					defer func() { <-s.sem }()
					add(s.dir(p, fi))
				}(childPath, ci)
			default:
				add(s.dir(childPath, ci))
			}
			continue
		}

		// Regular, special, or (without -L) symlink file. An extra hardlink to a
		// file already counted is listed at zero bytes, so directory totals
		// match du (which path carries the size depends on scan order).
		child := &Node{Name: name, Path: childPath}
		if !s.dupFile(ci, childPath) {
			child.Self = s.bytes(ci)
		}
		child.Size = child.Self
		child.Files = 1
		add(child)
		s.scanned.Add(1)
	}

	wg.Wait()
	return n
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

// sortChildren orders a node's children largest first, breaking ties by name
// so output is stable.
func sortChildren(n *Node) {
	sort.Slice(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.Size != b.Size {
			return a.Size > b.Size
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
