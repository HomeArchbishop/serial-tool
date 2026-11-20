//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func prepCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killProcessGroup(cmd.Process.Pid)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}

func killPortListeners(port int) {
	if port <= 0 {
		return
	}
	out, err := exec.Command("cmd", "/C", "netstat", "-ano").Output()
	if err != nil {
		return
	}
	needle := fmt.Sprintf(":%d", port)
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(out), "\n") {
		u := strings.ToUpper(line)
		if !strings.Contains(u, "LISTEN") || !strings.Contains(line, needle) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid := fields[len(fields)-1]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		_ = exec.Command("taskkill", "/F", "/T", "/PID", pid).Run()
	}
}

func notifyQuitSignals() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch
}
