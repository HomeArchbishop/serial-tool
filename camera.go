package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func startLocalCamera(cfg CameraConfig) (*exec.Cmd, io.ReadCloser, error) {
	input, err := localCameraInputArgs(cfg)
	if err != nil {
		return nil, nil, err
	}
	args := append([]string{"-hide_banner", "-loglevel", "warning"}, input...)
	args = append(args,
		"-vf", "scale=640:-2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-bf", "0",
		"-g", strconv.Itoa(cfg.FPS),
		"-pix_fmt", "yuv420p",
		"-f", "mpegts",
		"-flush_packets", "1",
		"-listen", "1",
		fmt.Sprintf("tcp://127.0.0.1:%d", cfg.Port),
	)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Env = append(os.Environ(), "AV_LOG_FORCE_NOCOLOR=1", "NO_COLOR=1")
	prepCmd(cmd)
	out, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("启动本机 ffmpeg 失败: %w", err)
	}
	return cmd, out, nil
}

type captureMode struct {
	Width  int
	Height int
	FPS    int
}

// startLocalCameraResolved starts capture at the requested mode. Some cameras
// reject 640x480@30 outright; ffmpeg then prints the modes it supports, so
// retry once with the smallest advertised mode and scale it down for encoding.
func startLocalCameraResolved(cfg CameraConfig) (*exec.Cmd, io.ReadCloser, captureMode, error) {
	want := captureMode{Width: cfg.Width, Height: cfg.Height, FPS: cfg.FPS}

	cmd, out, err := startLocalCamera(cfg)
	if err != nil {
		return nil, nil, want, err
	}
	if waitForListener(cfg.Port, 3*time.Second) == nil {
		return cmd, out, want, nil
	}

	detail, _ := io.ReadAll(out)
	killCmd(cmd)

	alt, ok := parseSupportedMode(string(detail))
	if !ok {
		return nil, nil, want, listenError(cfg.Port, string(detail))
	}

	cfg.Width, cfg.Height, cfg.FPS = alt.Width, alt.Height, alt.FPS
	cmd, out, err = startLocalCamera(cfg)
	if err != nil {
		return nil, nil, alt, err
	}
	if waitForListener(cfg.Port, 3*time.Second) != nil {
		detail, _ = io.ReadAll(out)
		killCmd(cmd)
		return nil, nil, alt, listenError(cfg.Port, string(detail))
	}
	return cmd, out, alt, nil
}

func listenError(port int, detail string) error {
	if msg := lastLogLine(detail); msg != "" {
		return fmt.Errorf("本机 ffmpeg 未在端口 %d 监听: %s", port, msg)
	}
	return fmt.Errorf("本机 ffmpeg 未在端口 %d 监听", port)
}

func lastLogLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func localFFmpegStatus() ToolStatus {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ToolStatus{
			OK:      false,
			Message: "本机 PATH 中找不到 ffmpeg，无法推流",
			Hint:    localFFmpegHint(),
		}
	}
	return ToolStatus{OK: true, Path: path, Message: "本机 ffmpeg 可用: " + path}
}

func localFFmpegHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install ffmpeg"
	case "linux":
		return "sudo apt install ffmpeg"
	case "windows":
		return "winget install Gyan.FFmpeg  # 或: choco install ffmpeg"
	default:
		return "请安装 ffmpeg 并确保在 PATH 中"
	}
}
