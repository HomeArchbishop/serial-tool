package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const (
	defaultDepthPort   = 47200
	defaultDepthWidth  = 640
	defaultDepthHeight = 400
	defaultDepthFPS    = 30
)

type depthDeviceList struct {
	Devices []DepthCamera `json:"devices"`
}

// depthWorkDir keeps the SDK's relative Log/ output out of the app bundle and
// the current working directory.
func depthWorkDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "serial-tool", "orbbec")
	if err := os.MkdirAll(path, 0o755); err != nil {
		return ""
	}
	return path
}

// depthHelperPath finds the Orbbec helper next to the app, in bin/ during
// development, or through ORBBEC_DEPTH_HELPER.
func depthHelperPath() (helper, config string, err error) {
	name := "orbbec-depth"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	var candidates []string
	if env := os.Getenv("ORBBEC_DEPTH_HELPER"); env != "" {
		candidates = append(candidates, env)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, name),
			filepath.Join(dir, "..", "Resources", name),
			filepath.Join(dir, "..", "..", "..", name), // dev app bundle -> bin/
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "bin", name),
			filepath.Join(cwd, name),
		)
	}

	for _, candidate := range candidates {
		info, statErr := os.Stat(candidate)
		if statErr != nil || info.IsDir() {
			continue
		}
		if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
			continue
		}
		helper = candidate
		break
	}
	if helper == "" {
		return "", "", fmt.Errorf("找不到 Orbbec 深度助手")
	}

	configNames := []string{"OrbbecSDKConfig_v1.0.xml"}
	dirs := []string{filepath.Dir(helper)}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, "bin"), cwd)
	}
	for _, dir := range dirs {
		for _, cfgName := range configNames {
			candidate := filepath.Join(dir, cfgName)
			if _, statErr := os.Stat(candidate); statErr == nil {
				config = candidate
				break
			}
		}
		if config != "" {
			break
		}
	}
	return helper, config, nil
}

// runDepthHelperCapture keeps stderr so the UI can show why enumeration failed
// instead of silently reporting "no device".
func runDepthHelperCapture(args ...string) ([]byte, string, error) {
	helper, config, err := depthHelperPath()
	if err != nil {
		return nil, "", err
	}
	if config != "" {
		args = append([]string{"--config", config}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, args...)
	if dir := depthWorkDir(); dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, stderr.String(), err
}

// parseDepthDeviceList tolerates SDK log lines on stdout before the JSON.
func parseDepthDeviceList(out []byte) []DepthCamera {
	start := bytes.Index(out, []byte(`{"devices"`))
	if start < 0 {
		return nil
	}
	var list depthDeviceList
	if err := json.Unmarshal(out[start:], &list); err != nil {
		return nil
	}
	return list.Devices
}

func depthHelperStatus() ToolStatus {
	helper, _, err := depthHelperPath()
	if err != nil {
		return ToolStatus{
			OK:      false,
			Message: "找不到 Orbbec 深度助手，无法获取深度",
			Hint:    "bash scripts/build-depth-helper.sh",
		}
	}
	return ToolStatus{OK: true, Path: helper, Message: "Orbbec 深度助手可用: " + helper}
}

// A hung helper means the USB device is wedged; retrying immediately spawns
// more stuck processes, so back off for a while.
var (
	depthListMu       sync.Mutex
	depthListFailTime time.Time
)

const depthListCooldown = 45 * time.Second

// firstDepthCamera returns the first enumerated Orbbec device id, so a camera
// can start even when the UI list is stale.
func firstDepthCamera() string {
	devices := parseDepthDeviceListFromHelper()
	if len(devices) > 0 {
		return devices[0].ID
	}
	return ""
}

func parseDepthDeviceListFromHelper() []DepthCamera {
	out, _, err := runDepthHelperCapture("--list")
	if err != nil {
		return nil
	}
	return parseDepthDeviceList(out)
}

func (s *DeviceService) ListDepthCamerasDetailed() DepthListResult {
	depthListMu.Lock()
	if v := depthListFailTime; !v.IsZero() && time.Since(v) < depthListCooldown {
		depthListMu.Unlock()
		return DepthListResult{Error: "深度助手无响应，请拔插相机 USB 后稍候再试"}
	}
	depthListMu.Unlock()

	out, stderr, err := runDepthHelperCapture("--list")
	if err == nil {
		depthListMu.Lock()
		depthListFailTime = time.Time{}
		depthListMu.Unlock()
	} else {
		depthListMu.Lock()
		depthListFailTime = time.Now()
		depthListMu.Unlock()
	}
	if err != nil {
		msg := "深度助手无响应"
		if last := lastLogLine(stderr); last != "" {
			msg = "深度助手异常: " + last
		} else if last := lastLogLine(string(out)); last != "" {
			msg = "深度助手异常: " + last
		}
		return DepthListResult{Error: msg}
	}
	devices := parseDepthDeviceList(out)
	s.emitServiceLog("枚举", fmt.Sprintf("Orbbec 设备 %d 个", len(devices)))
	if len(devices) == 0 {
		msg := lastLogLine(stderr)
		if msg == "" {
			msg = lastLogLine(string(out))
		}
		if msg == "" {
			msg = "助手未返回设备"
		}
		s.emitServiceLog("枚举", "未枚举到设备: "+msg)
	}
	return DepthListResult{Devices: devices}
}

func (s *DeviceService) CheckDepthHelper() ToolStatus { return depthHelperStatus() }

// startOrbbecCameraLocked runs an Orbbec depth camera next to the normal color
// pipeline: color still goes through the local ffmpeg/UVC path, depth is read
// by the Orbbec helper and reprojected onto the color grid.
func (r *deviceRuntime) startOrbbecCameraLocked() error {
	cfg := r.cfg
	cc := cfg.cameraConfig()

	local := localFFmpegStatus()
	if !local.OK {
		return fmt.Errorf("%s。安装: %s", local.Message, local.Hint)
	}
	helper, helperConfig, err := depthHelperPath()
	if err != nil {
		return fmt.Errorf("%s。构建: bash scripts/build-depth-helper.sh", err.Error())
	}
	r.cleanupRemoteLeftovers()

	r.svc.camerasActive.Add(1)
	started := false
	defer func() {
		if !started {
			r.svc.camerasActive.Add(-1)
		}
	}()

	probe, needsSudo, err := r.prepareRemoteV4L2(cfg, cc)
	if err != nil {
		return err
	}

	ffmpeg, ffOut, mode, err := startLocalCameraResolved(cc)
	if err != nil {
		return err
	}

	align := cfg.DepthAlign
	if align == "" {
		align = "color"
	}
	args := []string{
		"--serial", cfg.DepthSerial,
		"--align", align,
		"--port", strconv.Itoa(cfg.DepthPort),
		"--width", strconv.Itoa(cfg.DepthWidth),
		"--height", strconv.Itoa(cfg.DepthHeight),
		"--fps", strconv.Itoa(cfg.DepthFPS),
	}
	if align == "color" {
		args = append(args, "--out-width", strconv.Itoa(mode.Width), "--out-height", strconv.Itoa(mode.Height))
	}
	if helperConfig != "" {
		args = append([]string{"--config", helperConfig}, args...)
	}
	helperCmd := exec.Command(helper, args...)
	if dir := depthWorkDir(); dir != "" {
		helperCmd.Dir = dir
	}
	prepCmd(helperCmd)
	helperOut, err := helperCmd.StderrPipe()
	if err != nil {
		killCmd(ffmpeg)
		return err
	}
	if err := helperCmd.Start(); err != nil {
		killCmd(ffmpeg)
		return fmt.Errorf("启动深度助手失败: %w", err)
	}
	if err := waitForListener(cfg.DepthPort, 20*time.Second); err != nil {
		detail, _ := io.ReadAll(helperOut)
		killCmd(helperCmd)
		killCmd(ffmpeg)
		return fmt.Errorf("深度助手未在端口 %d 监听: %s", cfg.DepthPort, lastLogLine(string(detail)))
	}

	rcmd := buildRemoteCameraCmd(cc)
	sudoMode, sudoPass := "direct", ""
	if needsSudo {
		rcmd, sudoMode, sudoPass, err = withSudo(cc.sshConfig(), rcmd)
		if err != nil {
			killCmd(helperCmd)
			killCmd(ffmpeg)
			return err
		}
	}
	ssh, sshIn, sshOut, err := startSSHTunnelMulti(cc.sshConfig(),
		[]string{strconv.Itoa(cfg.Port), strconv.Itoa(cfg.DepthPort)}, rcmd)
	if err != nil {
		killCmd(helperCmd)
		killCmd(ffmpeg)
		return err
	}
	if sudoPass != "" {
		if _, err := io.WriteString(sshIn, sudoPass+"\n"); err != nil {
			_ = sshIn.Close()
			killCmd(ssh)
			killCmd(helperCmd)
			killCmd(ffmpeg)
			return fmt.Errorf("写入 sudo 密码失败: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.local, r.aux, r.ssh, r.sshIn, r.cancel = ffmpeg, helperCmd, ssh, sshIn, cancel
	r.localPID, r.auxPID, r.sshPID = pidOf(ffmpeg), pidOf(helperCmd), pidOf(ssh)
	r.port = cfg.Port
	r.running = true
	r.gen++
	gen := r.gen
	r.activity = Activity{Running: true, Listening: true, Message: "等待服务器拉流"}
	r.emitStatus(true)
	r.emitActivity(r.activity)
	r.emitLog(fmt.Sprintf("本机 ffmpeg: %s", local.Path))
	r.emitLog(fmt.Sprintf("深度助手: %s", helper))
	r.emitLog(fmt.Sprintf("远程 ffmpeg: %s (%s)", probe.ffmpegPath, sudoMode))
	r.emitLog(fmt.Sprintf("Orbbec 相机已启动: 彩色 %dx%d@%d -> %s，深度 %s -> %s (tcp %d)",
		mode.Width, mode.Height, mode.FPS, cfg.RemoteDevice, cfg.DepthName, cfg.Host, cfg.DepthPort))

	go r.pump(ctx, helperOut, "orbbec-depth")
	go r.pump(ctx, ffOut, "ffmpeg")
	go r.pump(ctx, sshOut, "ssh")
	go r.watch(ctx, ffmpeg, ssh, "camera", gen)
	go r.monitor(ctx, cfg.Port, DeviceKindCamera, gen)
	started = true
	return nil
}
