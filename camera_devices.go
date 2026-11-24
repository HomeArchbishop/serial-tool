package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCameraDevice = "/dev/video10"
	defaultCameraPort   = 47100
	defaultCameraWidth  = 640
	defaultCameraHeight = 480
	defaultCameraFPS    = 30
)

var avfDeviceLine = regexp.MustCompile(`\[(\d+)\]\s+(.+)$`)
var avfModeLine = regexp.MustCompile(`(\d+)x(\d+)@\[([0-9.]+)(?:\s+([0-9.]+))?\]`)
var remoteDevicePattern = regexp.MustCompile(`^/dev/video[0-9]+$`)

// parseSupportedMode pulls the smallest advertised resolution out of the
// "Supported modes:" block ffmpeg prints when a camera rejects our settings.
func parseSupportedMode(output string) (captureMode, bool) {
	var best captureMode
	for _, m := range avfModeLine.FindAllStringSubmatch(output, -1) {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		fps, _ := strconv.ParseFloat(m[3], 64)
		if m[4] != "" {
			if maxFPS, err := strconv.ParseFloat(m[4], 64); err == nil {
				fps = maxFPS
			}
		}
		if w <= 0 || h <= 0 || fps < 1 {
			continue
		}
		if best.Width == 0 || w*h < best.Width*best.Height {
			best = captureMode{Width: w, Height: h, FPS: int(fps + 0.5)}
		}
	}
	return best, best.Width > 0
}

// listCameras returns capture-capable video inputs for the host platform.
func listCameras() []CameraDevice {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return listAVFoundationCameras()
	case "linux":
		return listV4L2Cameras()
	case "windows":
		return listDirectShowCameras()
	default:
		return nil
	}
}

func listAVFoundationCameras() []CameraDevice {
	out, _ := exec.Command("ffmpeg", "-hide_banner",
		"-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()

	// ffmpeg writes the device table to stderr. Video devices are listed before
	// the audio block; keep only lines in [N] <name> form from that block.
	var cameras []CameraDevice
	inVideo := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "AVFoundation video devices:") {
			inVideo = true
			continue
		}
		if strings.Contains(line, "AVFoundation audio devices:") {
			break
		}
		if !inVideo {
			continue
		}
		m := avfDeviceLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[2])
		if strings.HasPrefix(name, "Capture screen") {
			continue
		}
		cameras = append(cameras, CameraDevice{ID: m[1], Name: name})
	}
	return cameras
}

func listV4L2Cameras() []CameraDevice {
	paths, _ := filepath.Glob("/dev/video*")
	sort.Strings(paths)

	var cameras []CameraDevice
	for _, path := range paths {
		name := path
		if b, err := os.ReadFile("/sys/class/video4linux/" + filepath.Base(path) + "/name"); err == nil {
			if n := strings.TrimSpace(string(b)); n != "" {
				name = n
			}
		}
		cameras = append(cameras, CameraDevice{ID: path, Name: name})
	}
	return cameras
}

func listDirectShowCameras() []CameraDevice {
	out, _ := exec.Command("ffmpeg", "-hide_banner",
		"-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()

	var cameras []CameraDevice
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "(video)") {
			continue
		}
		first := strings.Index(line, `"`)
		last := strings.LastIndex(line, `"`)
		if first < 0 || last <= first {
			continue
		}
		name := line[first+1 : last]
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		cameras = append(cameras, CameraDevice{ID: "video=" + name, Name: name})
	}
	return cameras
}

func localCameraInputArgs(cfg CameraConfig) ([]string, error) {
	var args []string
	switch runtime.GOOS {
	case "darwin":
		args = []string{"-f", "avfoundation"}
	case "linux":
		args = []string{"-f", "v4l2"}
	case "windows":
		args = []string{"-f", "dshow"}
	default:
		return nil, fmt.Errorf("当前平台不支持相机推流")
	}
	if cfg.FPS > 0 {
		args = append(args, "-framerate", strconv.Itoa(cfg.FPS))
	}
	if cfg.Width > 0 && cfg.Height > 0 {
		args = append(args, "-video_size", fmt.Sprintf("%dx%d", cfg.Width, cfg.Height))
	}
	if runtime.GOOS == "darwin" {
		// uyvy422 is the format AVFoundation is most likely to deliver; ffmpeg
		// falls back automatically when the device reports something else.
		args = append(args, "-pixel_format", "uyvy422")
	}
	args = append(args, "-i", cfg.CameraID)
	return args, nil
}

// buildRemoteCameraCmd runs ffmpeg on the server, pulling the tunneled stream
// into the v4l2loopback device.
func buildRemoteCameraCmd(cfg CameraConfig) string {
	ffmpegCmd := fmt.Sprintf(
		"AV_LOG_FORCE_NOCOLOR=1 NO_COLOR=1 ffmpeg -hide_banner -loglevel warning -nostats "+
			"-fflags nobuffer -flags low_delay "+
			"-probesize 32 -analyzeduration 0 -i tcp://127.0.0.1:%d "+
			"-vf format=yuyv422 -f v4l2 -y %s",
		cfg.Port, shellQuote(cfg.RemoteDevice),
	)
	script := fmt.Sprintf(
		"%s & pid=$!; trap 'kill -9 $pid 2>/dev/null' EXIT HUP INT TERM; wait $pid",
		ffmpegCmd,
	)
	return "bash -lc " + shellQuote(script)
}

func (c CameraConfig) sshConfig() TunnelConfig {
	return TunnelConfig{
		Host:         c.Host,
		User:         c.User,
		Password:     c.Password,
		IdentityFile: c.IdentityFile,
		SSHPort:      c.SSHPort,
		SudoPassword: c.SudoPassword,
	}
}

type cameraRemoteProbe struct {
	ffmpegPath string
	device     bool
	writable   bool
	module     bool
	ffmpegBusy string
}

func probeRemoteCamera(cfg CameraConfig) (cameraRemoteProbe, error) {
	dev := cfg.RemoteDevice
	script := fmt.Sprintf(`ff=$(command -v ffmpeg 2>/dev/null || true)
[ -n "$ff" ] && echo "ffmpeg=$ff"
[ -e %s ] && echo "device=yes"
[ -w %s ] && echo "writable=yes"
lsmod 2>/dev/null | grep -q '^v4l2loopback' && echo "module=yes"
if command -v fuser >/dev/null 2>&1; then
  for p in $(fuser %s 2>/dev/null); do
    c=$(cat /proc/$p/comm 2>/dev/null || true)
    [ "$c" = "ffmpeg" ] && echo "ffmpeg_busy=$p"
  done
fi
exit 0
`, shellQuote(dev), shellQuote(dev), shellQuote(dev))

	out, err := runSSH(cfg.sshConfig(), "bash -lc "+shellQuote(script))
	if err != nil {
		return cameraRemoteProbe{}, fmt.Errorf("SSH 检查失败: %s", remoteMessage(out, err))
	}

	var probe cameraRemoteProbe
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "ffmpeg="):
			probe.ffmpegPath = strings.TrimPrefix(line, "ffmpeg=")
		case line == "device=yes":
			probe.device = true
		case line == "writable=yes":
			probe.writable = true
		case line == "module=yes":
			probe.module = true
		case strings.HasPrefix(line, "ffmpeg_busy="):
			probe.ffmpegBusy = strings.TrimPrefix(line, "ffmpeg_busy=")
		}
	}
	return probe, nil
}

func checkRemoteCamera(cfg CameraConfig) ToolStatus {
	probe, err := probeRemoteCamera(cfg)
	if err != nil {
		return ToolStatus{OK: false, Message: err.Error(), Hint: "确认主机、账号、密钥或密码正确"}
	}
	if probe.ffmpegPath == "" {
		return ToolStatus{OK: false, Message: "远程主机找不到 ffmpeg", Hint: "sudo apt install ffmpeg"}
	}
	msg := "远程 ffmpeg 可用: " + probe.ffmpegPath
	if !probe.device {
		msg = "远程 ffmpeg 可用；启动时将自动加载 v4l2loopback 并创建 " + cfg.RemoteDevice
	}
	return ToolStatus{OK: true, Path: probe.ffmpegPath, Message: msg}
}

// prepareRemoteLoopback makes sure the target loopback device exists, loading
// (or reloading) the v4l2loopback module as needed. Reloading destroys all
// loopback devices, so it is refused while another camera stream is running.
func prepareRemoteLoopback(cfg CameraConfig, otherCamerasRunning bool) (bool, error) {
	probe, err := probeRemoteCamera(cfg)
	if err != nil {
		return false, err
	}
	if probe.device {
		return false, nil
	}
	if probe.module && otherCamerasRunning {
		return false, fmt.Errorf("远端 v4l2loopback 缺少 %s；请先停止其他相机设备再启动", cfg.RemoteDevice)
	}

	tc := cfg.sshConfig()
	if probe.module {
		out, err := remoteSudo(tc, "modprobe -r v4l2loopback")
		if err != nil {
			return false, sudoError("卸载 v4l2loopback", out, err)
		}
	}
	_, err = remoteSudo(tc, buildLoopbackLoadCmd(cfg.RemoteDevice))
	if err != nil && !errors.Is(err, ErrSudoPasswordRequired) {
		// The 10..19 range can collide with physical cameras; retry with only
		// the device this stream needs.
		altOut, altErr := remoteSudo(tc, buildLoopbackLoadCmdForNumbers([]int{videoNumberInt(cfg.RemoteDevice)}))
		if altErr != nil {
			return false, sudoError("加载 v4l2loopback", altOut, altErr)
		}
	} else if err != nil {
		return false, err
	}

	probe, err = probeRemoteCamera(cfg)
	if err != nil {
		return false, err
	}
	if !probe.device {
		return false, fmt.Errorf("v4l2loopback 已加载，但 %s 未出现", cfg.RemoteDevice)
	}
	return true, nil
}

// makeRemoteDeviceAccessible relaxes v4l2loopback's default root:video 0660
// permissions so non-root readers on the server (OpenCV, ffplay) can use the
// virtual camera, not just the sudo-run writer.
func makeRemoteDeviceAccessible(cfg CameraConfig) error {
	tc := cfg.sshConfig()
	out, err := remoteSudo(tc, "chmod 666 "+shellQuote(cfg.RemoteDevice))
	if err != nil {
		return sudoError("设置 "+cfg.RemoteDevice+" 权限", out, err)
	}
	return nil
}

func unloadRemoteLoopback(cfg CameraConfig, extraSSH ...string) error {
	tc := cfg.sshConfig()
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		out, err := remoteSudoOpts(tc, "modprobe -r v4l2loopback", extraSSH...)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrSudoPasswordRequired) {
			return err
		}
		msg := remoteMessage(out, err)
		if strings.Contains(msg, "not currently loaded") || strings.Contains(msg, "not found in /proc/modules") {
			return nil
		}
		last = fmt.Errorf("%s", msg)
	}
	if last == nil {
		last = fmt.Errorf("未知错误")
	}
	return last
}

func buildLoopbackLoadCmd(target string) string {
	return buildLoopbackLoadCmdForNumbers(loopbackVideoNumbers(target))
}

func buildLoopbackLoadCmdForNumbers(nums []int) string {
	nrs := make([]string, 0, len(nums))
	labels := make([]string, 0, len(nums))
	caps := make([]string, 0, len(nums))
	for _, n := range nums {
		nrs = append(nrs, strconv.Itoa(n))
		labels = append(labels, `"RemoteCam"`)
		caps = append(caps, "1")
	}
	return fmt.Sprintf("modprobe v4l2loopback video_nr=%s card_label=%s exclusive_caps=%s",
		strings.Join(nrs, ","), strings.Join(labels, ","), strings.Join(caps, ","))
}

// loopbackVideoNumbers pre-creates 10..19 so normal multi-camera use needs no
// module reload; a manually chosen device outside that range is added too.
func loopbackVideoNumbers(target string) []int {
	set := map[int]bool{}
	for n := 10; n <= 19; n++ {
		set[n] = true
	}
	if n, err := strconv.Atoi(videoNumber(target)); err == nil && n > 0 {
		set[n] = true
	}
	nums := make([]int, 0, len(set))
	for n := range set {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}

func sudoError(action string, out string, err error) error {
	if errors.Is(err, ErrSudoPasswordRequired) {
		return err
	}
	msg := remoteMessage(out, err)
	if strings.Contains(strings.ToLower(msg), "not found") {
		msg += "；需要安装: sudo apt install v4l2loopback-dkms"
	}
	return fmt.Errorf("%s失败: %s", action, msg)
}

func remoteMessage(out string, err error) string {
	if msg := lastLogLine(out); msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "未知错误"
}

func videoNumber(dev string) string {
	return strings.TrimPrefix(dev, "/dev/video")
}

func videoNumberInt(dev string) int {
	n, _ := strconv.Atoi(videoNumber(dev))
	return n
}
