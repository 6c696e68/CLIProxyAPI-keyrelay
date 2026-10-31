//go:build !windows

package keyshop

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in its own session, so a terminal or a signal
// aimed at the plugin process group does not reach a kept-alive child. It never
// sets Pdeathsig: the child must survive the plugin process.
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
