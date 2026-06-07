package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	Err      error   `json:"-"` // set when the directory could not be read
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
	errs    atomic.Int64  // entries we failed to read
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

	if _, dev, ok := diskUsage(info); ok {
		s.rootDev = dev
	}

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
		n.Err = err
		s.errs.Add(1)
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
			s.errs.Add(1)
			continue
		}

		if ci.Mode()&fs.ModeSymlink != 0 {
			if !s.follow {
				// Count the link itself, never its target.
				mu.Lock()
				n.Size += s.bytes(ci)
				n.Files++
				mu.Unlock()
				s.scanned.Add(1)
				continue
			}
			ti, err := os.Stat(childPath)
			if err != nil {
				s.errs.Add(1)
				continue
			}
			ci = ti
		}

		if ci.IsDir() {
			if s.oneFS {
				if _, dev, ok := diskUsage(ci); ok && dev != s.rootDev {
					continue
				}
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

		// Regular or special file.
		child := &Node{Name: name, Path: childPath}
		child.Self = s.bytes(ci)
		child.Size = child.Self
		child.Files = 1
		add(child)
		s.scanned.Add(1)
	}

	wg.Wait()
	return n
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
