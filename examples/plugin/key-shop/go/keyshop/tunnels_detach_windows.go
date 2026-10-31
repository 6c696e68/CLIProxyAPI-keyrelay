//go:build windows

package keyshop

import "os/exec"

// detachProcess is a no-op on Windows. Ownership checks never pass there, so a
// child is always stopped with the manager.
func detachProcess(cmd *exec.Cmd) {}
