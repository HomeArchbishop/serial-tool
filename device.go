package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// DeviceService owns every user-added device (serial arm or camera). Each
// runtime has its own processes, so any number of devices can run in parallel.
type DeviceService struct {
	mu      sync.Mutex
	app     *application.App
	devices map[string]*deviceRuntime
	order   []string

	loopMu        sync.Mutex
	camerasActive atomic.Int32
	loopbackOwned atomic.Bool
	loopbackCfgMu sync.Mutex
	loopbackCfg   DeviceConfig
}

type deviceRuntime struct {
	mu       sync.Mutex
	svc      *DeviceService
	cfg      DeviceConfig
	running  bool
	activity Activity
	cancel   context.CancelFunc
	local    *exec.Cmd
	aux      *exec.Cmd
	ssh      *exec.Cmd
	sshIn    io.WriteCloser
	localPID int
	auxPID   int
	sshPID   int
	port     int
	gen      uint64
}

func (s *DeviceService) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	s.app = application.Get()
	s.mu.Lock()
	if s.devices == nil {
		s.devices = map[string]*deviceRuntime{}
	}
	s.loadLocked()
	s.mu.Unlock()

	// A marker left behind by a crash means the module is ours; try to unload
	// it now. Failure keeps the marker for the next stop/launch.
	if cfg, ok := readLoopbackMarker(); ok {
		if err := unloadRemoteLoopback(cfg.cameraConfig(), "-o", "ConnectTimeout=4"); err == nil {
			clearLoopbackMarker()
		}
	}
	return nil
}

func (s *DeviceService) ServiceShutdown() error {
	s.Cleanup()
	return nil
}

func (s *DeviceService) ListDevices() []DeviceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeviceState, 0, len(s.order))
	for _, id := range s.order {
		r := s.devices[id]
		if r == nil {
			continue
		}
		out = append(out, r.state())
	}
	return out
}

func (s *DeviceService) AddDevice(cfg DeviceConfig) (DeviceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.devices == nil {
		s.devices = map[string]*deviceRuntime{}
	}

	cfg = migrateDeviceConfig(cfg)
	if cfg.Kind != DeviceKindArm && cfg.Kind != DeviceKindCamera {
		return DeviceState{}, fmt.Errorf("未知设备类型: %s", cfg.Kind)
	}
	if cfg.ID == "" {
		cfg.ID = newDeviceID()
	}
	if _, exists := s.devices[cfg.ID]; exists {
		return DeviceState{}, fmt.Errorf("设备已存在")
	}

	if cfg.Label == "" {
		cfg.Label = s.nextLabelLocked(cfg.Kind)
	}
	usedPorts := s.usedPortsLocked()

	switch cfg.Kind {
	case DeviceKindArm:
		if cfg.LinkName == "" {
			cfg.LinkName = "/dev/ttyACM0"
		}
		if cfg.TunnelPort <= 0 {
			cfg.TunnelPort = nextFreePort(defaultTunnelPort, usedPorts)
		}
	case DeviceKindCamera:
		if cfg.CameraModel == "" {
			cfg.CameraModel = CameraModelRGB
		}
		if cfg.RemoteDevice == "" {
			cfg.RemoteDevice = s.nextVideoDeviceLocked()
		}
		if cfg.Port <= 0 {
			cfg.Port = nextFreePort(defaultCameraPort, usedPorts)
		}
		if cfg.Width <= 0 {
			cfg.Width = defaultCameraWidth
		}
		if cfg.Height <= 0 {
			cfg.Height = defaultCameraHeight
		}
		if cfg.FPS <= 0 {
			cfg.FPS = defaultCameraFPS
		}
		if cfg.CameraModel == CameraModelOrbbec {
			if strings.TrimSpace(cfg.DepthSerial) == "" {
				cfg.DepthSerial = firstDepthCamera()
			}
			if cfg.DepthAlign == "" {
				cfg.DepthAlign = "color"
			}
			if cfg.DepthPort <= 0 {
				cfg.DepthPort = nextFreePort(defaultDepthPort, usedPorts)
			}
			if cfg.DepthWidth <= 0 {
				cfg.DepthWidth = defaultDepthWidth
			}
			if cfg.DepthHeight <= 0 {
				cfg.DepthHeight = defaultDepthHeight
			}
			if cfg.DepthFPS <= 0 {
				cfg.DepthFPS = defaultDepthFPS
			}
		}
	}

	r := &deviceRuntime{
		svc:      s,
		cfg:      cfg,
		activity: Activity{Message: idleMessage(cfg.Kind)},
	}
	s.devices[cfg.ID] = r
	s.order = append(s.order, cfg.ID)
	s.saveLocked()
	return r.state(), nil
}

func (s *DeviceService) RemoveDevice(id string) error {
	s.mu.Lock()
	r := s.devices[id]
	delete(s.devices, id)
	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	if r != nil {
		r.mu.Lock()
		kind := r.cfg.Kind
		_ = r.stopLocked(false)
		r.mu.Unlock()
		if kind == DeviceKindCamera {
			s.maybeUnloadLoopback()
		}
	}
	s.save()
	return nil
}

func (s *DeviceService) StartDevice(cfg DeviceConfig) (DeviceState, error) {
	s.mu.Lock()
	r := s.devices[cfg.ID]
	s.mu.Unlock()
	if r == nil {
		return DeviceState{}, fmt.Errorf("设备不存在")
	}
	if cfg.Kind == DeviceKindCamera {
		device := cfg.RemoteDevice
		if device == "" {
			device = defaultCameraDevice
		}
		if label, ok := s.cameraRemoteDeviceOwner(device, cfg.ID); ok {
			return r.state(), fmt.Errorf("远程设备 %s 已被设备「%s」使用", device, label)
		}
	}

	for _, p := range devicePorts(cfg) {
		if p <= 0 || s.portOwnedByRunningDevice(p) {
			continue
		}
		if pids := cleanupLocalStale(cfg.Kind, p); len(pids) > 0 {
			s.emitServiceLog(cfg.ID, fmt.Sprintf("已清理本机残留进程: %v", pids))
		}
	}
	if msg := s.resolveDevicePort(&cfg); msg != "" {
		s.emitServiceLog(cfg.ID, msg)
	}
	if cfg.Kind == DeviceKindCamera && cfg.CameraModel == CameraModelOrbbec && cfg.DepthSerial != "" {
		if pids := cleanupStaleDepthHelpers(cfg.DepthSerial); len(pids) > 0 {
			s.emitServiceLog(cfg.ID, fmt.Sprintf("已结束旧的深度助手进程: %v", pids))
		}
	}

	r.mu.Lock()
	state, err := r.startDeviceLocked(cfg)
	r.mu.Unlock()
	if cfg.Kind == DeviceKindCamera {
		s.maybeUnloadLoopback()
	}
	s.save()
	return state, err
}

func (r *deviceRuntime) startDeviceLocked(cfg DeviceConfig) (DeviceState, error) {
	if r.running {
		return r.stateLocked(), fmt.Errorf("%s 已在运行", r.cfg.Label)
	}
	if err := normalizeDeviceConfig(&cfg); err != nil {
		return r.stateLocked(), err
	}
	r.cfg = cfg

	var err error
	switch cfg.Kind {
	case DeviceKindArm:
		err = r.startArmLocked()
	case DeviceKindCamera:
		err = r.startCameraLocked()
	default:
		err = fmt.Errorf("未知设备类型: %s", cfg.Kind)
	}
	if err != nil {
		r.activity = Activity{Message: idleMessage(cfg.Kind)}
		return r.stateLocked(), err
	}
	return r.stateLocked(), nil
}

func (s *DeviceService) StopDevice(id string) error {
	s.mu.Lock()
	r := s.devices[id]
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	kind := r.cfg.Kind
	err := r.stopLocked(true)
	r.mu.Unlock()
	if kind == DeviceKindCamera {
		s.maybeUnloadLoopback()
	}
	return err
}

func (s *DeviceService) StopAll() {
	s.mu.Lock()
	runtimes := make([]*deviceRuntime, 0, len(s.order))
	for _, id := range s.order {
		if r := s.devices[id]; r != nil {
			runtimes = append(runtimes, r)
		}
	}
	s.mu.Unlock()

	for _, r := range runtimes {
		r.mu.Lock()
		_ = r.stopLocked(true)
		r.mu.Unlock()
	}
	s.maybeUnloadLoopback()
}

func (s *DeviceService) Cleanup() {
	s.mu.Lock()
	runtimes := make([]*deviceRuntime, 0, len(s.order))
	for _, id := range s.order {
		if r := s.devices[id]; r != nil {
			runtimes = append(runtimes, r)
		}
	}
	s.mu.Unlock()

	for _, r := range runtimes {
		r.mu.Lock()
		_ = r.stopLocked(false)
		r.mu.Unlock()
	}
	s.maybeUnloadLoopback()
}

func (s *DeviceService) ListSerialPorts() []string   { return listSerialDevices() }
func (s *DeviceService) ListCameras() []CameraDevice { return listCameras() }
func (s *DeviceService) ListSSHHosts() []SSHHost     { return listSSHHosts() }
func (s *DeviceService) ListSSHKeys() []string       { return listSSHKeys() }
func (s *DeviceService) CheckLocalSocat() ToolStatus { return localSocatStatus() }
func (s *DeviceService) CheckLocalFFmpeg() ToolStatus {
	return localFFmpegStatus()
}

func (s *DeviceService) CheckRemoteDevice(cfg DeviceConfig) ToolStatus {
	switch cfg.Kind {
	case DeviceKindArm:
		tc := cfg.tunnelConfig()
		if tc.LinkName == "" {
			tc.LinkName = "/dev/ttyACM0"
		}
		if tc.TunnelPort == 0 {
			tc.TunnelPort = defaultTunnelPort
		}
		return checkRemoteSocat(tc)
	case DeviceKindCamera:
		cc := cfg.cameraConfig()
		if cc.RemoteDevice == "" {
			cc.RemoteDevice = defaultCameraDevice
		}
		return checkRemoteCamera(cc)
	default:
		return ToolStatus{OK: false, Message: "未知设备类型", Hint: "重新添加设备"}
	}
}

func (s *DeviceService) nextLabelLocked(kind string) string {
	base := "机械臂"
	if kind == DeviceKindCamera {
		base = "相机"
	}
	name := func(n int) string { return fmt.Sprintf("%s %d", base, n) }
	existing := map[string]bool{}
	for _, id := range s.order {
		if r := s.devices[id]; r != nil {
			existing[r.cfg.Label] = true
		}
	}
	for n := 1; ; n++ {
		if !existing[name(n)] {
			return name(n)
		}
	}
}

func (s *DeviceService) usedPortsLocked() map[int]bool {
	used := map[int]bool{}
	for _, id := range s.order {
		r := s.devices[id]
		if r == nil {
			continue
		}
		if r.cfg.Kind == DeviceKindArm && r.cfg.TunnelPort > 0 {
			used[r.cfg.TunnelPort] = true
		}
		if r.cfg.Kind == DeviceKindCamera && r.cfg.Port > 0 {
			used[r.cfg.Port] = true
		}
		if r.cfg.Kind == DeviceKindCamera && r.cfg.CameraModel == CameraModelOrbbec && r.cfg.DepthPort > 0 {
			used[r.cfg.DepthPort] = true
		}
	}
	return used
}

func (s *DeviceService) nextVideoDeviceLocked() string {
	used := map[string]bool{}
	for _, id := range s.order {
		if r := s.devices[id]; r != nil && r.cfg.Kind == DeviceKindCamera {
			used[r.cfg.RemoteDevice] = true
		}
	}
	for n := 10; n < 100; n++ {
		dev := fmt.Sprintf("/dev/video%d", n)
		if !used[dev] {
			return dev
		}
	}
	return defaultCameraDevice
}

func nextFreePort(base int, used map[int]bool) int {
	for p := base; p < base+200; p++ {
		if used[p] || !portFree(p) {
			continue
		}
		return p
	}
	return base
}

func newDeviceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("dev-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// migrateDeviceConfig folds the removed standalone depth device into a camera
// device using the Orbbec model, keeping saved configs usable.
func migrateDeviceConfig(cfg DeviceConfig) DeviceConfig {
	if cfg.Kind == DeviceKindCamera {
		if cfg.CameraModel == "" {
			cfg.CameraModel = CameraModelRGB
		}
		cfg.Port = migrateLegacyPort(cfg.Port, 9100)
		cfg.DepthPort = migrateLegacyPort(cfg.DepthPort, 9200)
		return cfg
	}
	if cfg.Kind != DeviceKindDepth {
		return cfg
	}
	cfg.Kind = DeviceKindCamera
	cfg.CameraModel = CameraModelOrbbec
	if cfg.Port <= 0 {
		cfg.Port = defaultCameraPort
	}
	if cfg.DepthWidth <= 0 {
		cfg.DepthWidth = defaultDepthWidth
	}
	if cfg.DepthHeight <= 0 {
		cfg.DepthHeight = defaultDepthHeight
	}
	if cfg.DepthFPS <= 0 {
		cfg.DepthFPS = defaultDepthFPS
	}
	if cfg.DepthAlign == "" {
		cfg.DepthAlign = "color"
	}
	return cfg
}

// migrateLegacyPort moves ports from the old crowded 91xx/92xx defaults into
// the 47xxx range, keeping the per-device offset.
func migrateLegacyPort(port, legacyBase int) int {
	if port >= legacyBase && port < legacyBase+100 {
		return 47100 + (legacyBase - 9100) + (port - legacyBase)
	}
	return port
}

func idleMessage(kind string) string {
	if kind == DeviceKindCamera {
		return "未推流"
	}
	return "未连接"
}

func normalizeDeviceConfig(cfg *DeviceConfig) error {
	*cfg = migrateDeviceConfig(*cfg)
	if cfg.Kind != DeviceKindArm && cfg.Kind != DeviceKindCamera {
		return fmt.Errorf("未知设备类型: %s", cfg.Kind)
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return fmt.Errorf("请填写主机地址")
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.User = strings.TrimSpace(cfg.User)
	cfg.IdentityFile = strings.TrimSpace(cfg.IdentityFile)
	if cfg.SSHPort <= 0 {
		cfg.SSHPort = 22
	}

	switch cfg.Kind {
	case DeviceKindArm:
		if strings.TrimSpace(cfg.SerialDevice) == "" {
			return fmt.Errorf("请选择串口设备")
		}
		if cfg.LinkName == "" {
			cfg.LinkName = "/dev/ttyACM0"
		}
		if cfg.TunnelPort <= 0 {
			cfg.TunnelPort = defaultTunnelPort
		}
		if !portFree(cfg.TunnelPort) {
			return fmt.Errorf("端口 %d 已被占用，请在高级设置中更换", cfg.TunnelPort)
		}
	case DeviceKindCamera:
		if cfg.CameraModel == "" {
			cfg.CameraModel = CameraModelRGB
		}
		if cfg.CameraModel != CameraModelRGB && cfg.CameraModel != CameraModelOrbbec {
			return fmt.Errorf("未知相机型号: %s", cfg.CameraModel)
		}
		if cfg.CameraModel == CameraModelOrbbec {
			if strings.TrimSpace(cfg.DepthSerial) == "" {
				if first := firstDepthCamera(); first != "" {
					cfg.DepthSerial = first
				} else {
					return fmt.Errorf("未发现 Orbbec 深度相机，请连接后点刷新")
				}
			}
			if cfg.DepthPort <= 0 {
				cfg.DepthPort = defaultDepthPort
			}
			if !portFree(cfg.DepthPort) {
				return fmt.Errorf("深度端口 %d 已被占用，请在高级设置中更换", cfg.DepthPort)
			}
			if cfg.DepthWidth <= 0 {
				cfg.DepthWidth = defaultDepthWidth
			}
			if cfg.DepthHeight <= 0 {
				cfg.DepthHeight = defaultDepthHeight
			}
			if cfg.DepthFPS <= 0 {
				cfg.DepthFPS = defaultDepthFPS
			}
			if cfg.DepthAlign != "none" && cfg.DepthAlign != "color" {
				cfg.DepthAlign = "color"
			}
		} else if strings.TrimSpace(cfg.CameraID) == "" {
			return fmt.Errorf("请选择相机")
		}
		if cfg.RemoteDevice == "" {
			cfg.RemoteDevice = defaultCameraDevice
		}
		if !remoteDevicePattern.MatchString(cfg.RemoteDevice) {
			return fmt.Errorf("远程设备需形如 /dev/video10")
		}
		if cfg.Port <= 0 {
			cfg.Port = defaultCameraPort
		}
		if !portFree(cfg.Port) {
			return fmt.Errorf("端口 %d 已被占用，请在高级设置中更换", cfg.Port)
		}
		if cfg.Width <= 0 {
			cfg.Width = defaultCameraWidth
		}
		if cfg.Height <= 0 {
			cfg.Height = defaultCameraHeight
		}
		if cfg.FPS <= 0 {
			cfg.FPS = defaultCameraFPS
		}
	}
	return nil
}

func (c DeviceConfig) sshTunnelConfig() TunnelConfig {
	return TunnelConfig{
		Host:         c.Host,
		User:         c.User,
		Password:     c.Password,
		IdentityFile: c.IdentityFile,
		SSHPort:      c.SSHPort,
		SudoPassword: c.SudoPassword,
	}
}

func (c DeviceConfig) tunnelConfig() TunnelConfig {
	return TunnelConfig{
		Device:       c.SerialDevice,
		Host:         c.Host,
		User:         c.User,
		Password:     c.Password,
		IdentityFile: c.IdentityFile,
		SSHPort:      c.SSHPort,
		TunnelPort:   c.TunnelPort,
		LinkName:     c.LinkName,
		SudoPassword: c.SudoPassword,
	}
}

func (c DeviceConfig) cameraConfig() CameraConfig {
	return CameraConfig{
		CameraID:     c.CameraID,
		CameraName:   c.CameraName,
		RemoteDevice: c.RemoteDevice,
		Port:         c.Port,
		Width:        c.Width,
		Height:       c.Height,
		FPS:          c.FPS,
		Host:         c.Host,
		User:         c.User,
		Password:     c.Password,
		IdentityFile: c.IdentityFile,
		SSHPort:      c.SSHPort,
		SudoPassword: c.SudoPassword,
	}
}

func (r *deviceRuntime) state() DeviceState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stateLocked()
}

func (r *deviceRuntime) stateLocked() DeviceState {
	return DeviceState{Config: r.cfg, Running: r.running, Activity: r.activity}
}

// devicePorts lists the local ports a device listens on.
func devicePorts(cfg DeviceConfig) []int {
	switch cfg.Kind {
	case DeviceKindArm:
		return []int{cfg.TunnelPort}
	case DeviceKindCamera:
		ports := []int{cfg.Port}
		if cfg.CameraModel == CameraModelOrbbec {
			ports = append(ports, cfg.DepthPort)
		}
		return ports
	default:
		return nil
	}
}

func (s *DeviceService) cameraRemoteDeviceOwner(device, exceptID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		if id == exceptID {
			continue
		}
		r := s.devices[id]
		if r == nil {
			continue
		}
		r.mu.Lock()
		kind, dev, label := r.cfg.Kind, r.cfg.RemoteDevice, r.cfg.Label
		r.mu.Unlock()
		if kind == DeviceKindCamera && dev == device {
			return label, true
		}
	}
	return "", false
}

func (s *DeviceService) resolveDevicePort(cfg *DeviceConfig) string {
	port := cfg.TunnelPort
	base := defaultTunnelPort
	if cfg.Kind == DeviceKindCamera {
		port, base = cfg.Port, defaultCameraPort
	}
	if port <= 0 {
		port = base
	}
	if portFree(port) {
		if cfg.Kind == DeviceKindCamera {
			cfg.Port = port
		} else {
			cfg.TunnelPort = port
		}
		if msg := moveDepthPortIfBusy(cfg, s.usedPortsSnapshot()); msg != "" {
			return msg
		}
		return ""
	}

	used := s.usedPortsSnapshot()
	next := nextFreePort(port+1, used)
	if cfg.Kind == DeviceKindCamera {
		cfg.Port = next
	} else {
		cfg.TunnelPort = next
	}
	if msg := moveDepthPortIfBusy(cfg, used); msg != "" {
		owner := portListenerName(port)
		if owner == "" {
			return fmt.Sprintf("%s；端口 %d 已被占用，改用 %d", msg, port, next)
		}
		return fmt.Sprintf("%s；端口 %d 被 %s 占用，改用 %d", msg, port, owner, next)
	}
	owner := portListenerName(port)
	if owner == "" {
		return fmt.Sprintf("端口 %d 已被占用，改用 %d", port, next)
	}
	return fmt.Sprintf("端口 %d 被 %s 占用，改用 %d", port, owner, next)
}

// moveDepthPortIfBusy keeps the depth stream port from colliding as well.
func moveDepthPortIfBusy(cfg *DeviceConfig, used map[int]bool) string {
	if cfg.Kind != DeviceKindCamera || cfg.CameraModel != CameraModelOrbbec {
		return ""
	}
	port := cfg.DepthPort
	if port <= 0 || portFree(port) {
		return ""
	}
	next := nextFreePort(port+1, used)
	cfg.DepthPort = next
	return fmt.Sprintf("深度端口 %d 已被占用，改用 %d", port, next)
}

func (s *DeviceService) usedPortsSnapshot() map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usedPortsLocked()
}

func (s *DeviceService) portOwnedByRunningDevice(port int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		r := s.devices[id]
		if r == nil {
			continue
		}
		r.mu.Lock()
		running, rport, kind := r.running, r.cfg.TunnelPort, r.cfg.Kind
		switch kind {
		case DeviceKindCamera:
			rport = r.cfg.Port
		}
		r.mu.Unlock()
		if running && rport == port {
			return true
		}
	}
	return false
}

func (s *DeviceService) maybeUnloadLoopback() {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	if !s.loopbackOwned.Load() || s.camerasActive.Load() > 0 {
		return
	}
	s.loopbackOwned.Store(false)

	s.loopbackCfgMu.Lock()
	cfg := s.loopbackCfg
	s.loopbackCfgMu.Unlock()

	if err := unloadRemoteLoopback(cfg.cameraConfig()); err != nil {
		s.loopbackOwned.Store(true)
		s.emitServiceLog(cfg.ID, "自动卸载 v4l2loopback 失败: "+err.Error())
		return
	}
	clearLoopbackMarker()
	s.emitServiceLog(cfg.ID, "已自动卸载 v4l2loopback")
}

func (s *DeviceService) emitServiceLog(id, line string) {
	if s.app != nil {
		s.app.Event.Emit("device:log", DeviceLog{ID: id, Line: line})
	}
}

func staleCleanupScript(patterns []string, link string) string {
	quoted := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		quoted = append(quoted, shellQuote(pattern))
	}
	linkBlock := ""
	if strings.TrimSpace(link) != "" {
		q := shellQuote(link)
		linkBlock = fmt.Sprintf(`if [ -L %s ]; then rm -f %s 2>/dev/null || true; echo "unlinked"; fi`, q, q)
	}
	return fmt.Sprintf(`pids=""
for pat in %s; do
  for p in $(pgrep -f "$pat" 2>/dev/null || true); do
    [ "$p" = "$PPID" ] && continue
    case " $pids " in *" $p "*) continue;; esac
    pids="$pids $p"
  done
done
victims=""
for p in $pids; do
  c=$(cat /proc/$p/comm 2>/dev/null || true)
  case "$c" in
    ffmpeg|socat|orbbec-depth) victims="$victims $p";;
  esac
done
if [ -n "$victims" ]; then
  kill -9 $victims 2>/dev/null || true
  sleep 0.2
  left=""
  for p in $victims; do kill -0 $p 2>/dev/null && left="$left $p"; done
  if [ -n "$left" ]; then echo "remaining:$left"; else echo "cleaned:$victims"; fi
fi
%s
exit 0`, strings.Join(quoted, " "), linkBlock)
}

func parseCleanupOutput(out string) (cleaned, remaining string) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "cleaned:"):
			cleaned = strings.TrimSpace(strings.TrimPrefix(line, "cleaned:"))
		case strings.HasPrefix(line, "remaining:"):
			remaining = strings.TrimSpace(strings.TrimPrefix(line, "remaining:"))
		}
	}
	return cleaned, remaining
}

// cleanupRemoteLeftovers kills orphaned ffmpeg/socat processes and stale pty
// links left on the server by an interrupted previous run.
func (r *deviceRuntime) cleanupRemoteLeftovers() {
	cfg := r.cfg
	if strings.TrimSpace(cfg.Host) == "" {
		return
	}
	var patterns []string
	link := ""
	switch cfg.Kind {
	case DeviceKindCamera:
		patterns = []string{
			fmt.Sprintf("tcp://127.0.0.1:%d", cfg.Port),
			cfg.RemoteDevice,
		}
	case DeviceKindArm:
		patterns = []string{fmt.Sprintf("TCP:127.0.0.1:%d", cfg.TunnelPort)}
		if cfg.LinkName != "" {
			patterns = append(patterns, cfg.LinkName)
		}
		link = cfg.LinkName
	default:
		return
	}
	tc := cfg.sshTunnelConfig()
	script := staleCleanupScript(patterns, link)
	extra := []string{"-o", "ConnectTimeout=4"}

	out, err := runSSHOpts(tc, "bash -lc "+shellQuote(script), extra...)
	if err != nil {
		return
	}
	r.reportCleanup(out)
	if _, remaining := parseCleanupOutput(out); remaining != "" {
		out, err = remoteSudoOpts(tc, "bash -lc "+shellQuote(script), extra...)
		if err != nil {
			return
		}
		r.reportCleanup(out)
		if _, remaining := parseCleanupOutput(out); remaining != "" {
			r.emitLog("远程残留进程无法清理: " + remaining)
		}
	}
}

func (r *deviceRuntime) reportCleanup(out string) {
	if cleaned, _ := parseCleanupOutput(out); cleaned != "" {
		r.emitLog("已清理远程残留进程: " + cleaned)
	}
	if strings.Contains(out, "unlinked") {
		r.emitLog("已清理远程残留虚拟串口链接")
	}
}

func (r *deviceRuntime) startArmLocked() error {
	cfg := r.cfg
	tc := cfg.tunnelConfig()
	port := strconv.Itoa(cfg.TunnelPort)

	local := localSocatStatus()
	if !local.OK {
		return fmt.Errorf("%s。安装: %s", local.Message, local.Hint)
	}
	r.cleanupRemoteLeftovers()
	remote := checkRemoteSocat(tc)
	if !remote.OK {
		return fmt.Errorf("%s\n安装命令: %s", remote.Message, remote.Hint)
	}

	socat, socatOut, err := startLocalSocat(cfg.SerialDevice, port)
	if err != nil {
		return err
	}
	time.Sleep(400 * time.Millisecond)
	if !portListening(cfg.TunnelPort) {
		killCmd(socat)
		return fmt.Errorf("本机 socat 未在端口 %d 监听", cfg.TunnelPort)
	}

	rcmd, sudoMode, sudoPass, err := withSudo(tc, buildRemoteSocatCmd(tc, cfg.LinkName, port))
	if err != nil {
		killCmd(socat)
		return err
	}
	ssh, sshIn, sshOut, err := startSSHTunnel(tc, port, rcmd)
	if err != nil {
		killCmd(socat)
		return err
	}
	if sudoPass != "" {
		if _, err := io.WriteString(sshIn, sudoPass+"\n"); err != nil {
			_ = sshIn.Close()
			killCmd(ssh)
			killCmd(socat)
			return fmt.Errorf("写入 sudo 密码失败: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.local, r.ssh, r.sshIn, r.cancel = socat, ssh, sshIn, cancel
	r.localPID, r.sshPID, r.port = pidOf(socat), pidOf(ssh), cfg.TunnelPort
	r.running = true
	r.gen++
	gen := r.gen
	r.activity = Activity{Running: true, Listening: true, Message: "监听中，等待远端连接"}
	r.emitStatus(true)
	r.emitActivity(r.activity)
	r.emitLog(fmt.Sprintf("本机 socat: %s", local.Path))
	r.emitLog(fmt.Sprintf("远程 socat: %s (%s)", remote.Path, sudoMode))
	r.emitLog(fmt.Sprintf("已启动: %s -> %s@%s:%s (tcp %d)", cfg.SerialDevice, cfg.User, cfg.Host, cfg.LinkName, cfg.TunnelPort))

	go r.pump(ctx, socatOut, "local-socat")
	go r.pump(ctx, sshOut, "ssh")
	go r.watch(ctx, socat, ssh, "arm", gen)
	go r.monitor(ctx, cfg.TunnelPort, DeviceKindArm, gen)
	return nil
}

func (r *deviceRuntime) startCameraLocked() error {
	if r.cfg.CameraModel == CameraModelOrbbec {
		return r.startOrbbecCameraLocked()
	}
	cfg := r.cfg
	cc := cfg.cameraConfig()

	local := localFFmpegStatus()
	if !local.OK {
		return fmt.Errorf("%s。安装: %s", local.Message, local.Hint)
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

	rcmd := buildRemoteCameraCmd(cc)
	sudoMode, sudoPass := "direct", ""
	if needsSudo {
		rcmd, sudoMode, sudoPass, err = withSudo(cc.sshConfig(), rcmd)
		if err != nil {
			killCmd(ffmpeg)
			return err
		}
	}
	ssh, sshIn, sshOut, err := startSSHTunnel(cc.sshConfig(), strconv.Itoa(cfg.Port), rcmd)
	if err != nil {
		killCmd(ffmpeg)
		return err
	}
	if sudoPass != "" {
		if _, err := io.WriteString(sshIn, sudoPass+"\n"); err != nil {
			_ = sshIn.Close()
			killCmd(ssh)
			killCmd(ffmpeg)
			return fmt.Errorf("写入 sudo 密码失败: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.local, r.ssh, r.sshIn, r.cancel = ffmpeg, ssh, sshIn, cancel
	r.localPID, r.sshPID, r.port = pidOf(ffmpeg), pidOf(ssh), cfg.Port
	r.running = true
	r.gen++
	gen := r.gen
	r.activity = Activity{Running: true, Listening: true, Message: "等待服务器拉流"}
	r.emitStatus(true)
	r.emitActivity(r.activity)
	r.emitLog(fmt.Sprintf("本机 ffmpeg: %s", local.Path))
	r.emitLog(fmt.Sprintf("远程 ffmpeg: %s (%s)", probe.ffmpegPath, sudoMode))
	r.emitLog(fmt.Sprintf("推流已启动: %s（采集 %dx%d@%d） -> %s@%s:%s (tcp %d)",
		cfg.CameraName, mode.Width, mode.Height, mode.FPS, cfg.User, cfg.Host, cfg.RemoteDevice, cfg.Port))

	go r.pump(ctx, ffOut, "ffmpeg")
	go r.pump(ctx, sshOut, "ssh")
	go r.watch(ctx, ffmpeg, ssh, "camera", gen)
	go r.monitor(ctx, cfg.Port, DeviceKindCamera, gen)
	started = true
	return nil
}

// prepareRemoteV4L2 checks the remote ffmpeg, makes sure the loopback device
// exists and is writable, and reports whether writing it needs sudo.
func (r *deviceRuntime) prepareRemoteV4L2(cfg DeviceConfig, cc CameraConfig) (cameraRemoteProbe, bool, error) {
	probe, err := probeRemoteCamera(cc)
	if err != nil {
		return probe, false, err
	}
	if probe.ffmpegPath == "" {
		return probe, false, fmt.Errorf("远程主机找不到 ffmpeg\n安装命令: sudo apt install ffmpeg")
	}

	r.svc.loopMu.Lock()
	loaded, err := prepareRemoteLoopback(cc, r.svc.camerasActive.Load() > 1)
	r.svc.loopMu.Unlock()
	if err != nil {
		return probe, false, err
	}
	if loaded {
		writeLoopbackMarker(cfg)
		r.svc.loopbackOwned.Store(true)
		r.svc.loopbackCfgMu.Lock()
		r.svc.loopbackCfg = cfg
		r.svc.loopbackCfgMu.Unlock()
		r.emitLog(fmt.Sprintf("已自动加载 v4l2loopback（%s）", cfg.RemoteDevice))
	} else if _, ok := readLoopbackMarker(); ok {
		// Previous crash left our module loaded and startup unload failed.
		r.svc.loopbackOwned.Store(true)
		r.svc.loopbackCfgMu.Lock()
		r.svc.loopbackCfg = cfg
		r.svc.loopbackCfgMu.Unlock()
	}

	probe, err = probeRemoteCamera(cc)
	if err != nil {
		return probe, false, err
	}
	if probe.ffmpegBusy != "" {
		return probe, false, fmt.Errorf("远程设备 %s 仍被 ffmpeg 进程 %s 占用；请重试或先结束该进程", cfg.RemoteDevice, probe.ffmpegBusy)
	}
	if !probe.writable {
		if err := makeRemoteDeviceAccessible(cc); err != nil {
			if errors.Is(err, ErrSudoPasswordRequired) {
				return probe, false, err
			}
			r.emitLog(err.Error())
		} else {
			probe, err = probeRemoteCamera(cc)
			if err != nil {
				return probe, false, err
			}
		}
	}
	return probe, !probe.writable, nil
}

func (r *deviceRuntime) stopLocked(emit bool) error {
	if !r.running && r.local == nil && r.ssh == nil && r.port == 0 {
		return nil
	}
	wasRunning := r.running
	r.running = false
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.sshIn != nil {
		_ = r.sshIn.Close()
		r.sshIn = nil
	}
	local, aux, ssh := r.local, r.aux, r.ssh
	localPID, auxPID, sshPID, port := r.localPID, r.auxPID, r.sshPID, r.port
	depthPort := r.cfg.DepthPort
	r.local, r.aux, r.ssh, r.sshIn = nil, nil, nil, nil
	r.localPID, r.auxPID, r.sshPID, r.port = 0, 0, 0, 0
	r.activity = Activity{Message: idleMessage(r.cfg.Kind)}
	// Kill ssh first so the remote command dies with the tunnel.
	killCmd(ssh)
	killProcessGroup(sshPID)
	killCmd(local)
	killProcessGroup(localPID)
	killCmd(aux)
	killProcessGroup(auxPID)
	killPortListeners(port)
	if r.cfg.CameraModel == CameraModelOrbbec {
		killPortListeners(depthPort)
	}
	if wasRunning && r.cfg.Kind == DeviceKindCamera {
		r.svc.camerasActive.Add(-1)
	}
	if emit {
		r.cleanupRemoteLeftovers()
		r.emitStatus(false)
		r.emitActivity(r.activity)
		r.emitLog("已停止")
	}
	return nil
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
var controlChars = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

func cleanLogLine(line string) string {
	line = ansiEscape.ReplaceAllString(line, "")
	line = strings.ReplaceAll(line, "\r", "")
	return controlChars.ReplaceAllString(line, "")
}

func (r *deviceRuntime) pump(ctx context.Context, src io.Reader, name string) {
	sc := bufio.NewScanner(src)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			line := cleanLogLine(sc.Text())
			if line == "" {
				continue
			}
			r.emitLog(fmt.Sprintf("[%s] %s", name, line))
		}
	}
}

func (r *deviceRuntime) watch(ctx context.Context, local, ssh *exec.Cmd, kind string, gen uint64) {
	done := make(chan struct{}, 2)
	wait := func(cmd *exec.Cmd) {
		_ = cmd.Wait()
		select {
		case done <- struct{}{}:
		default:
		}
	}
	go wait(local)
	go wait(ssh)
	select {
	case <-ctx.Done():
	case <-done:
		r.mu.Lock()
		still := r.running && r.gen == gen
		r.mu.Unlock()
		if still {
			if kind == DeviceKindCamera {
				r.emitLog("推流连接已断开")
			} else {
				r.emitLog("连接已断开")
			}
			_ = r.svc.StopDevice(r.cfg.ID)
		}
	}
}

func (r *deviceRuntime) monitor(ctx context.Context, port int, kind string, gen uint64) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastLinked bool
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			listening := portListening(port)
			linked := listening && portHasPeer(port)
			msg := "监听中，等待远端连接"
			if kind == DeviceKindCamera {
				msg = "等待服务器拉流"
			}
			if !listening {
				msg = "本机端口未监听"
			} else if linked {
				if kind == DeviceKindCamera {
					msg = "推流已接通"
				} else {
					msg = "socat 链路已接通"
				}
			}
			act := Activity{Running: true, Listening: listening, Linked: linked, Message: msg}
			r.mu.Lock()
			if r.gen != gen {
				r.mu.Unlock()
				return
			}
			r.activity = act
			r.mu.Unlock()
			if first || linked != lastLinked || !listening {
				r.emitActivity(act)
				if linked && !lastLinked {
					if kind == DeviceKindCamera {
						r.emitLog("远端已连上推流")
					} else {
						r.emitLog("检测到 socat 连接活动")
					}
				} else if !linked && lastLinked {
					r.emitLog("连接已断开（仍在监听）")
				}
				first, lastLinked = false, linked
			}
		}
	}
}

func (r *deviceRuntime) emitLog(line string) {
	if r.svc.app != nil {
		r.svc.app.Event.Emit("device:log", DeviceLog{ID: r.cfg.ID, Line: line})
	}
}

func (r *deviceRuntime) emitStatus(running bool) {
	if r.svc.app != nil {
		r.svc.app.Event.Emit("device:status", DeviceStatus{ID: r.cfg.ID, Running: running})
	}
}

func (r *deviceRuntime) emitActivity(activity Activity) {
	if r.svc.app != nil {
		r.svc.app.Event.Emit("device:activity", DeviceActivity{ID: r.cfg.ID, Activity: activity})
	}
}
