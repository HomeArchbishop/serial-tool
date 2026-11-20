package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureUserPathAddsPackageManagerDirs(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	ensureUserPath()

	dirs := filepath.SplitList(os.Getenv("PATH"))

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"/opt/homebrew/bin", "/usr/local/bin"}
	case "linux":
		candidates = []string{"/usr/local/bin", "/snap/bin"}
	default:
		t.Skip("PATH augmentation not implemented on this OS")
	}

	checked := 0
	for _, dir := range candidates {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		checked++
		if !contains(dirs, dir) {
			t.Fatalf("PATH missing existing dir %q: %v", dir, dirs)
		}
	}
	if checked == 0 {
		t.Skip("none of the candidate directories exist")
	}
}

func TestEnsureUserPathNoDuplicates(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	ensureUserPath()
	first := os.Getenv("PATH")
	ensureUserPath()
	if second := os.Getenv("PATH"); second != first {
		t.Fatalf("second call changed PATH: %q -> %q", first, second)
	}

	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(first) {
		if seen[dir] && dir != "" {
			t.Fatalf("duplicate dir %q in %q", dir, first)
		}
		seen[dir] = true
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
