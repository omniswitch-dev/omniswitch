//go:build !windows

package proxy

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// TestStdioKillReachesGrandchildren starts a wrapper whose child keeps
// stdout open, like npx or uvx do. Killing the target must end the whole
// process group so the reader sees EOF and close returns promptly.
func TestStdioKillReachesGrandchildren(t *testing.T) {
	client, err := newStdioClient("sh", []string{"-c", "sleep 300 & wait"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	process, err := client.spawn()
	if err != nil {
		t.Fatal(err)
	}
	pid := process.cmd.Process.Pid
	time.Sleep(100 * time.Millisecond) // let sh start the sleep
	started := time.Now()
	process.kill(errors.New("test"))
	select {
	case <-process.done:
	case <-time.After(3 * time.Second):
		t.Fatal("reader still blocked: a grandchild kept stdout open")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("kill took %v", elapsed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(-pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still exists", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
