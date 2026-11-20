package main

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const defaultTunnelPort = 47329

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func portListening(port int) bool {
	if runtime.GOOS == "linux" {
		out, err := exec.Command("ss", "-H", "-tln", fmt.Sprintf("sport = :%d", port)).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return true
		}
	}
	out, err := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return true
	}
	if runtime.GOOS == "windows" {
		return windowsNetstatHas(port, "LISTEN")
	}
	return false
}

func portHasPeer(port int) bool {
	if runtime.GOOS == "linux" {
		out, err := exec.Command("ss", "-H", "-tn", "state", "established",
			fmt.Sprintf("( sport = :%d or dport = :%d )", port, port)).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return true
		}
	}
	out, err := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:ESTABLISHED").Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return true
	}
	if runtime.GOOS == "windows" {
		return windowsNetstatHas(port, "ESTABLISHED")
	}
	return false
}

func windowsNetstatHas(port int, state string) bool {
	out, err := exec.Command("cmd", "/C", "netstat", "-an").Output()
	if err != nil {
		return false
	}
	needle := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(string(out), "\n") {
		u := strings.ToUpper(line)
		if strings.Contains(u, state) && strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func localSocatStatus() ToolStatus {
	path, err := exec.LookPath("socat")
	if err != nil {
		return ToolStatus{
			OK:      false,
			Message: "本机 PATH 中找不到 socat，隧道无法启动",
			Hint:    localSocatHint(),
		}
	}
	return ToolStatus{OK: true, Path: path, Message: "本机 socat 可用: " + path}
}

func localSocatHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install socat"
	case "linux":
		return "sudo apt install socat  # 或: sudo yum install socat"
	case "windows":
		return "scoop install socat  # 或: choco install socat"
	default:
		return "请安装 socat 并确保在 PATH 中"
	}
}

func checkRemoteSocat(cfg TunnelConfig) ToolStatus {
	out, err := runSSH(cfg, "command -v socat")
	if err != nil || out == "" || strings.Contains(out, "not found") {
		return ToolStatus{
			OK:      false,
			Message: "远程主机找不到 socat，无法创建虚拟串口",
			Hint:    "sudo apt install socat",
		}
	}
	return ToolStatus{OK: true, Path: out, Message: "远程 socat 可用: " + out}
}

func normalizeLink(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "/dev/ttyACM0"
	}
	return name
}

func buildRemoteSocatCmd(cfg TunnelConfig, link, port string) string {
	owner := strings.TrimSpace(cfg.User)
	pty := fmt.Sprintf("pty,link=%s,raw,echo=0,unlink-close=1,mode=0666", link)
	if owner != "" {
		pty += ",user=" + owner
	}
	socatLine := fmt.Sprintf("%s TCP:127.0.0.1:%s,nodelay", pty, port)
	fix := fmt.Sprintf("chmod 666 %s 2>/dev/null || true", link)
	if owner != "" {
		fix += fmt.Sprintf("; chown %s %s 2>/dev/null || true", owner, link)
	}
	script := fmt.Sprintf(
		"socat %s & spid=$!; trap 'kill -9 $spid 2>/dev/null' EXIT HUP INT TERM; "+
			"for i in $(seq 1 30); do if [ -e %s ] || [ -L %s ]; then %s; break; fi; sleep 0.1; done; wait $spid",
		socatLine, link, link, fix,
	)
	return "bash -lc " + shellQuote(script)
}

func withSudo(cfg TunnelConfig, cmd string) (remoteCmd, mode, sudoPass string, err error) {
	pass := cfg.SudoPassword
	if pass == "" {
		pass = cfg.Password
	}
	if pass != "" {
		return "sudo -S -p '' " + cmd, "sudo -S", pass, nil
	}
	if _, err := runSSH(cfg, "sudo -n true"); err != nil {
		return "", "", "", ErrSudoPasswordRequired
	}
	return "sudo -n " + cmd, "sudo -n", "", nil
}
