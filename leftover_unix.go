//go:build unix

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// cleanupLocalStale kills orphaned processes from a previous run, whether they
// still hold a listening socket or only an established tunnel connection.
func cleanupLocalStale(kind string, port int) []int {
	if port <= 0 {
		return nil
	}

	patterns := []string{fmt.Sprintf("%d", port)}
	if kind == DeviceKindArm {
		patterns = []string{fmt.Sprintf("TCP-LISTEN:%d", port), fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port)}
	} else if kind == DeviceKindCamera {
		patterns = []string{
			fmt.Sprintf("tcp://127.0.0.1:%d", port),
			fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port),
			fmt.Sprintf("orbbec-depth.*--port %d", port),
		}
	}

	seen := map[int]bool{}
	var killed []int
	for _, pattern := range patterns {
		out, err := exec.Command("pgrep", "-f", pattern).Output()
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(out)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 || seen[pid] {
				continue
			}
			seen[pid] = true
			cmdOut, err := exec.Command("ps", "-p", field, "-o", "command=").Output()
			if err != nil {
				continue
			}
			if !isOurStaleCommand(string(cmdOut), kind, port) {
				continue
			}
			killProcessGroup(pid)
			killed = append(killed, pid)
		}
	}
	return killed
}

func isOurStaleCommand(cmd, kind string, port int) bool {
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port)
	if strings.Contains(cmd, "ssh") && strings.Contains(cmd, forward) {
		return true
	}
	switch kind {
	case DeviceKindCamera:
		if strings.Contains(cmd, "ffmpeg") && strings.Contains(cmd, fmt.Sprintf("tcp://127.0.0.1:%d", port)) {
			return true
		}
		return strings.Contains(cmd, "orbbec-depth") && strings.Contains(cmd, fmt.Sprintf("--port %d", port))
	default:
		return strings.Contains(cmd, "socat") &&
			strings.Contains(cmd, fmt.Sprintf("TCP-LISTEN:%d", port))
	}
}

// cleanupStaleDepthHelpers kills other orbbec-depth processes bound to the same
// camera. They hold the USB device and would make the new helper stream zeros.
func cleanupStaleDepthHelpers(serial string) []int {
	if serial == "" {
		return nil
	}
	out, err := exec.Command("pgrep", "-f", "orbbec-depth").Output()
	if err != nil {
		return nil
	}
	var killed []int
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			continue
		}
		cmdOut, err := exec.Command("ps", "-p", field, "-o", "command=").Output()
		if err != nil {
			continue
		}
		cmd := string(cmdOut)
		if !strings.Contains(cmd, "--serial "+serial) {
			continue
		}
		killProcessGroup(pid)
		killed = append(killed, pid)
	}
	return killed
}

// portListenerName returns a short process name for the current listener.
func portListenerName(port int) string {
	out, err := exec.Command("lsof", "-nP", "-t", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	cmdOut, err := exec.Command("ps", "-p", fields[0], "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(cmdOut))
	if name == "" {
		return ""
	}
	return filepath.Base(name)
}
