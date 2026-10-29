//go:build windows

package proxy

import "os/exec"

// configureStdioCommand leaves the command unchanged on Windows.
func configureStdioCommand(*exec.Cmd) {}

// killStdioCommand kills the direct child process.
func killStdioCommand(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
