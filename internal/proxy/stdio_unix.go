//go:build !windows

package proxy

import (
	"os/exec"
	"syscall"
)

// configureStdioCommand starts the server in its own process group so that
// killStdioCommand also reaches the processes it spawns. Wrappers such as
// npx or uvx run the real server as a grandchild that keeps stdout open;
// killing only the direct child would leak it.
func configureStdioCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killStdioCommand kills the server's whole process group.
func killStdioCommand(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
		_ = command.Process.Kill()
	}
}
