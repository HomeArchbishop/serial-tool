package main

import (
	"strings"
	"testing"
)

func TestParseSupportedMode(t *testing.T) {
	out := `[in#0 @ 0x1] Selected video size (640x480) is not supported by the device.
[in#0 @ 0x1] Supported modes:
[in#0 @ 0x1]   1920x1080@[15.000000 30.000000]fps
[in#0 @ 0x1]   1280x720@[15.000000 30.000000]fps
`
	mode, ok := parseSupportedMode(out)
	if !ok {
		t.Fatal("expected a supported mode")
	}
	if mode.Width != 1280 || mode.Height != 720 || mode.FPS != 30 {
		t.Fatalf("got %+v, want 1280x720@30", mode)
	}
}

func TestParseSupportedModeNone(t *testing.T) {
	if _, ok := parseSupportedMode("no camera here"); ok {
		t.Fatal("expected no mode")
	}
}

func TestLoopbackVideoNumbers(t *testing.T) {
	nums := loopbackVideoNumbers("/dev/video25")
	seen := map[int]bool{}
	for _, n := range nums {
		if seen[n] {
			t.Fatalf("duplicate video number %d", n)
		}
		seen[n] = true
	}
	if !seen[10] || !seen[19] || !seen[25] {
		t.Fatalf("missing expected video numbers: %v", nums)
	}
	if len(loopbackVideoNumbers("")) != 10 {
		t.Fatalf("default range = %d, want 10", len(loopbackVideoNumbers("")))
	}
}

func TestBuildLoopbackLoadCmd(t *testing.T) {
	cmd := buildLoopbackLoadCmd("/dev/video10")
	for _, want := range []string{
		"modprobe v4l2loopback",
		"video_nr=10,11,12",
		`card_label="RemoteCam","RemoteCam"`,
		"exclusive_caps=1,1",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("command %q missing %q", cmd, want)
		}
	}
}
