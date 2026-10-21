//go:build linux

package main

import "syscall"

// Filesystem magic numbers (from linux/magic.h) of the pseudo filesystems the
// kernel uses to expose its own state. The set matches ncdu's --exclude-kernfs,
// plus configfs, efivarfs, and fusectl.
var kernFSMagic = map[uint32]bool{
	0x42494e4d: true, // binfmt_misc
	0xcafe4a11: true, // bpf
	0x0027e0eb: true, // cgroup
	0x63677270: true, // cgroup2
	0x62656570: true, // configfs
	0x64626720: true, // debugfs
	0x00001cd1: true, // devpts
	0xde5e81e4: true, // efivarfs
	0x65735543: true, // fusectl
	0x00009fa0: true, // proc
	0x6165676c: true, // pstore
	0x73636673: true, // securityfs
	0xf97cff8c: true, // selinuxfs
	0x62656572: true, // sysfs
	0x74726163: true, // tracefs
}

// isKernFS reports whether path is on a kernel pseudo filesystem such as /proc
// or /sys. A failed statfs counts as not one, so the directory is scanned as
// before.
func isKernFS(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	// f_type is a 32-bit magic number; its Go type varies by architecture
	// (int32 on some, int64 on others), and the high-bit magics are negative
	// as int32, so truncating to uint32 recovers the value on all of them.
	return kernFSMagic[uint32(st.Type)] // #nosec G115 -- intentional truncation to the 32-bit magic
}
