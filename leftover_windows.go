//go:build windows

package main

// Windows stale-process detection would need command-line inspection via WMI;
// port conflicts still fall back to the next free port.
func cleanupLocalStale(kind string, port int) []int {
	return nil
}

func portListenerName(port int) string {
	return ""
}

func cleanupStaleDepthHelpers(serial string) []int {
	return nil
}
