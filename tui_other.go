//go:build !unix

package main

import "errors"

// runTUI is unsupported off Unix; main falls back to the printed summary.
func runTUI(root *Node, path string, sc *scanner) ([]string, error) {
	return nil, errors.New("interactive mode is only available on Unix systems")
}
