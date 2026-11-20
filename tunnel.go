package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type TunnelService struct {
	mu         sync.Mutex
	app        *application.App
	socat      *exec.Cmd
	ssh        *exec.Cmd
	sshIn      io.WriteCloser
	socatPID   int
	sshPID     int
	cancel     context.CancelFunc
	running    bool
	tunnelPort int
	activity   Activity
}

func (t *TunnelService) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	t.app = application.Get()
	return nil
}

func (t *TunnelService) ServiceShutdown() error {
	t.Cleanup()
	return nil
}

func (t *TunnelService) ListDevices() []string       { return listSerialDevices() }
func (t *TunnelService) ListSSHHosts() []SSHHost     { return listSSHHosts() }
func (t *TunnelService) ListSSHKeys() []string       { return listSSHKeys() }
func (t *TunnelService) CheckLocalSocat() ToolStatus { return localSocatStatus() }
func (t *TunnelService) CheckRemoteSocat(cfg TunnelConfig) ToolStatus {
	return checkRemoteSocat(cfg)
}

func (t *TunnelService) IsRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

func (t *TunnelService) GetActivity() Activity {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activity
}

func (t *TunnelService) Start(cfg TunnelConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		return fmt.Errorf("already running")
	}
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}

	local := localSocatStatus()
	if !local.OK {
		return fmt.Errorf("%s。安装: %s", local.Message, local.Hint)
	}
	remote := checkRemoteSocat(cfg)
	if !remote.OK {
		return fmt.Errorf("%s\n安装命令: %s", remote.Message, remote.Hint)
	}

	port := fmt.Sprintf("%d", cfg.TunnelPort)
	link := normalizeLink(cfg.LinkName)

	socat, socatOut, err := startLocalSocat(cfg.Device, port)
	if err != nil {
		return err
	}
	time.Sleep(400 * time.Millisecond)
	if !portListening(cfg.TunnelPort) {
		killCmd(socat)
		return fmt.Errorf("本机 socat 未在端口 %d 监听", cfg.TunnelPort)
	}

	rcmd, sudoMode, sudoPass, err := withSudo(cfg, buildRemoteSocatCmd(cfg, link, port))
	if err != nil {
		killCmd(socat)
		return err
	}

	ssh, sshIn, sshOut, err := startSSHTunnel(cfg, port, rcmd)
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
	t.socat, t.ssh, t.sshIn, t.cancel = socat, ssh, sshIn, cancel
	t.socatPID, t.sshPID = pidOf(socat), pidOf(ssh)
	t.running, t.tunnelPort = true, cfg.TunnelPort
	t.activity = Activity{Running: true, Listening: true, Message: "监听中，等待远端连接"}
	t.emitStatus(true)
	t.emitActivity(t.activity)
	t.emitLog(fmt.Sprintf("本机 socat: %s", local.Path))
	t.emitLog(fmt.Sprintf("远程 socat: %s (%s)", remote.Path, sudoMode))
	t.emitLog(fmt.Sprintf("已启动: %s -> %s@%s:%s (tcp %d)", cfg.Device, cfg.User, cfg.Host, link, cfg.TunnelPort))

	go t.pump(ctx, socatOut, "local-socat")
	go t.pump(ctx, sshOut, "ssh")
	go t.watch(ctx, socat, ssh)
	go t.monitor(ctx, cfg.TunnelPort)
	return nil
}

func pidOf(cmd *exec.Cmd) int {
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Pid
	}
	return 0
}

func normalizeConfig(cfg *TunnelConfig) error {
	if cfg.Device == "" {
		return fmt.Errorf("请选择串口设备")
	}
	if cfg.Host == "" {
		return fmt.Errorf("请填写主机地址")
	}
	if cfg.TunnelPort <= 0 {
		cfg.TunnelPort = defaultTunnelPort
	}
	if !portFree(cfg.TunnelPort) {
		return fmt.Errorf("端口 %d 已被占用，请在高级设置中更换", cfg.TunnelPort)
	}
	if cfg.LinkName == "" {
		cfg.LinkName = "/dev/ttyACM0"
	}
	if cfg.SSHPort <= 0 {
		cfg.SSHPort = 22
	}
	return nil
}

func startLocalSocat(device, port string) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.Command("socat",
		fmt.Sprintf("TCP-LISTEN:%s,bind=127.0.0.1,reuseaddr,fork,nodelay", port),
		fmt.Sprintf("%s,raw,echo=0", device),
	)
	prepCmd(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("启动本机 socat 失败: %w", err)
	}
	return cmd, out, nil
}

func startSSHTunnel(cfg TunnelConfig, port, rcmd string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	args, err := buildSSHArgs(cfg, "",
		"-tt",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-R", fmt.Sprintf("127.0.0.1:%s:127.0.0.1:%s", port, port),
	)
	if err != nil {
		return nil, nil, nil, err
	}
	// buildSSHArgs ends with target; append remote command.
	args = append(args, rcmd)
	cmd := exec.Command(args[0], args[1:]...)
	prepCmd(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return nil, nil, nil, err
	}
	return cmd, in, out, nil
}

func (t *TunnelService) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopLocked(true)
}

func (t *TunnelService) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.stopLocked(false)
}

func (t *TunnelService) stopLocked(emit bool) error {
	if !t.running && t.ssh == nil && t.socat == nil && t.tunnelPort == 0 {
		return nil
	}
	t.running = false
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.sshIn != nil {
		_ = t.sshIn.Close()
		t.sshIn = nil
	}
	ssh, socat := t.ssh, t.socat
	sshPID, socatPID, port := t.sshPID, t.socatPID, t.tunnelPort
	t.ssh, t.socat, t.sshIn = nil, nil, nil
	t.sshPID, t.socatPID, t.tunnelPort = 0, 0, 0
	t.activity = Activity{Running: false, Message: "未连接"}
	// Kill ssh first so remote trap/HUP can reap remote socat.
	killCmd(ssh)
	killProcessGroup(sshPID)
	killCmd(socat)
	killProcessGroup(socatPID)
	killPortListeners(port)
	if emit {
		t.emitStatus(false)
		t.emitActivity(t.activity)
		t.emitLog("已停止")
	}
	return nil
}

func (t *TunnelService) pump(ctx context.Context, r io.Reader, name string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			t.emitLog(fmt.Sprintf("[%s] %s", name, sc.Text()))
		}
	}
}

func (t *TunnelService) watch(ctx context.Context, socat, ssh *exec.Cmd) {
	done := make(chan struct{}, 2)
	wait := func(cmd *exec.Cmd) {
		_ = cmd.Wait()
		select {
		case done <- struct{}{}:
		default:
		}
	}
	go wait(socat)
	go wait(ssh)
	select {
	case <-ctx.Done():
	case <-done:
		t.mu.Lock()
		still := t.running
		t.mu.Unlock()
		if still {
			t.emitLog("连接已断开")
			_ = t.Stop()
		}
	}
}

func (t *TunnelService) monitor(ctx context.Context, port int) {
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
			if !listening {
				msg = "本机端口未监听"
			} else if linked {
				msg = "socat 链路已接通"
			}
			act := Activity{Running: true, Listening: listening, Linked: linked, Message: msg}
			t.mu.Lock()
			t.activity = act
			t.mu.Unlock()
			if first || linked != lastLinked || !listening {
				t.emitActivity(act)
				if linked && !lastLinked {
					t.emitLog("检测到 socat 连接活动")
				} else if !linked && lastLinked {
					t.emitLog("socat 连接已断开（仍在监听）")
				}
				first, lastLinked = false, linked
			}
		}
	}
}

func (t *TunnelService) emitLog(msg string) {
	if t.app != nil {
		t.app.Event.Emit("tunnel:log", msg)
	}
}
func (t *TunnelService) emitStatus(running bool) {
	if t.app != nil {
		t.app.Event.Emit("tunnel:status", running)
	}
}
func (t *TunnelService) emitActivity(a Activity) {
	if t.app != nil {
		t.app.Event.Emit("tunnel:activity", a)
	}
}
