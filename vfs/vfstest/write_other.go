// +build !linux,!darwin,!freebsd,!openbsd,!windows

package vfstest

import (
	"runtime"
	"testing"

	"github.com/rclone/rclone/vfs"
)

// TestWriteFileDoubleClose tests double close on write
func TestWriteFileDoubleClose(t *testing.T) {
	t.Skip("not supported on " + runtime.GOOS)
}

// writeTestDup performs the platform-specific implementation of the dup() syscall
func writeTestDup(oldfd uintptr) (uintptr, error) {
	return oldfd, vfs.ENOSYS
}
