package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func sshDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func listSSHHosts() []SSHHost {
	f, err := os.Open(filepath.Join(sshDir(), "config"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var hosts []SSHHost
	var cur *SSHHost
	flush := func() {
		if cur == nil {
			return
		}
		if cur.Host == "" {
			cur.Host = cur.Alias
		}
		if cur.Port == 0 {
			cur.Port = 22
		}
		hosts = append(hosts, *cur)
		cur = nil
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key, val := strings.ToLower(fields[0]), strings.Join(fields[1:], " ")
		switch key {
		case "host":
			flush()
			alias := fields[1]
			if strings.ContainsAny(alias, "*?") {
				cur = nil
				continue
			}
			cur = &SSHHost{Alias: alias}
		case "hostname":
			if cur != nil {
				cur.Host = val
			}
		case "user":
			if cur != nil {
				cur.User = val
			}
		case "port":
			if cur != nil {
				if p, err := strconv.Atoi(val); err == nil {
					cur.Port = p
				}
			}
		case "identityfile":
			if cur != nil {
				cur.IdentityFile = expandHome(val)
			}
		}
	}
	flush()
	return hosts
}

func listSSHKeys() []string {
	entries, err := os.ReadDir(sshDir())
	if err != nil {
		return nil
	}
	skip := map[string]bool{
		"config": true, "known_hosts": true, "known_hosts.old": true, "authorized_keys": true,
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".pub") || skip[name] || strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasPrefix(name, "id_") || strings.HasSuffix(name, ".pem") ||
			strings.Contains(name, "key") || strings.HasPrefix(name, "ssh_") {
			out = append(out, filepath.Join(sshDir(), name))
		}
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildSSHArgs(cfg TunnelConfig, remoteCmd string, extraSSH ...string) ([]string, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, fmt.Errorf("请填写主机")
	}
	target := host
	if u := strings.TrimSpace(cfg.User); u != "" {
		target = u + "@" + host
	}
	port := cfg.SSHPort
	if port <= 0 {
		port = 22
	}

	var args []string
	if cfg.Password != "" {
		if _, err := exec.LookPath("sshpass"); err != nil {
			hint := "brew install hudochenkov/sshpass/sshpass"
			switch runtime.GOOS {
			case "linux":
				hint = "sudo apt install sshpass"
			case "windows":
				hint = "请安装 sshpass，或改用密钥"
			}
			return nil, fmt.Errorf("密码登录需要 sshpass（%s）", hint)
		}
		args = append(args, "sshpass", "-p", cfg.Password)
	}

	batch := "yes"
	if cfg.Password != "" {
		batch = "no"
	}
	args = append(args, "ssh",
		"-o", "BatchMode="+batch,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=8",
		"-p", strconv.Itoa(port),
	)
	if cfg.IdentityFile != "" {
		args = append(args, "-i", expandHome(cfg.IdentityFile))
	}
	if cfg.Password != "" {
		args = append(args, "-o", "PreferredAuthentications=password", "-o", "PubkeyAuthentication=no")
	}
	args = append(args, extraSSH...)
	args = append(args, target)
	if remoteCmd != "" {
		args = append(args, remoteCmd)
	}
	return args, nil
}

func runSSH(cfg TunnelConfig, remoteCmd string) (string, error) {
	return runSSHOpts(cfg, remoteCmd)
}

func runSSHOpts(cfg TunnelConfig, remoteCmd string, extraSSH ...string) (string, error) {
	args, err := buildSSHArgs(cfg, remoteCmd, extraSSH...)
	if err != nil {
		return "", err
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// remoteSudo runs a remote command through sudo. The password, when needed, is
// fed on stdin so it never appears in the local process arguments.
func remoteSudo(cfg TunnelConfig, remoteCmd string) (string, error) {
	return remoteSudoOpts(cfg, remoteCmd)
}

func remoteSudoOpts(cfg TunnelConfig, remoteCmd string, extraSSH ...string) (string, error) {
	pass := cfg.SudoPassword
	if pass == "" {
		pass = cfg.Password
	}
	if pass != "" {
		args, err := buildSSHArgs(cfg, "sudo -S -p '' "+remoteCmd, extraSSH...)
		if err != nil {
			return "", err
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(pass + "\n")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	if out, err := runSSHOpts(cfg, "sudo -n true", extraSSH...); err != nil {
		return out, ErrSudoPasswordRequired
	}
	return runSSHOpts(cfg, "sudo -n "+remoteCmd, extraSSH...)
}
