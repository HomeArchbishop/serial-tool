package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func listSerialDevices() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(ms []string) {
		for _, m := range ms {
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	switch runtime.GOOS {
	case "darwin":
		for _, p := range []string{"/dev/cu.usbmodem*", "/dev/cu.usbserial*", "/dev/cu.SLAB*", "/dev/cu.wchusbserial*"} {
			ms, _ := filepath.Glob(p)
			add(ms)
		}
	case "linux":
		for _, p := range []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/serial/by-id/*"} {
			ms, _ := filepath.Glob(p)
			add(ms)
		}
	case "windows":
		add(listWindowsCOMPorts())
	}
	sort.Strings(out)
	return out
}

func listWindowsCOMPorts() []string {
	b, err := exec.Command("powershell", "-NoProfile", "-Command",
		"Get-CimInstance Win32_SerialPort | Select-Object -ExpandProperty DeviceID").Output()
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "COM") {
			out = append(out, line)
		}
	}
	return out
}
