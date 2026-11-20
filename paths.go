package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ensureUserPath appends common package-manager bin directories to PATH.
//
// A macOS app launched from Finder/Dock inherits launchd's minimal PATH
// (/usr/bin:/bin:/usr/sbin:/sbin), so tools installed via Homebrew or MacPorts
// are invisible even though the user's login shell finds them. Same class of
// problem exists for desktop launchers on Linux.
func ensureUserPath() {
	var extra []string

	switch runtime.GOOS {
	case "darwin":
		extra = []string{
			"/opt/homebrew/bin",
			"/opt/homebrew/sbin",
			"/usr/local/bin",
			"/usr/local/sbin",
			"/opt/local/bin",
			"/opt/local/sbin",
		}
	case "linux":
		extra = []string{
			"/usr/local/bin",
			"/usr/local/sbin",
			"/snap/bin",
		}
	default:
		return
	}

	if home, err := os.UserHomeDir(); err == nil {
		extra = append(extra, filepath.Join(home, ".local", "bin"))
	}

	current := os.Getenv("PATH")
	seen := make(map[string]bool, len(extra))
	for _, dir := range filepath.SplitList(current) {
		seen[dir] = true
	}

	var add []string
	for _, dir := range extra {
		if seen[dir] {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		seen[dir] = true
		add = append(add, dir)
	}
	if len(add) == 0 {
		return
	}

	if current == "" {
		current = strings.Join(add, string(os.PathListSeparator))
	} else {
		current += string(os.PathListSeparator) + strings.Join(add, string(os.PathListSeparator))
	}
	_ = os.Setenv("PATH", current)
}
