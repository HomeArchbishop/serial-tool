package main

import (
	"fmt"
	"io"
	"os/exec"
	"time"
)

func pidOf(cmd *exec.Cmd) int {
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Pid
	}
	return 0
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
	return startSSHTunnelMulti(cfg, []string{port}, rcmd)
}

// startSSHTunnelMulti forwards several local ports over one SSH connection so
// a single device only needs one ssh process.
func startSSHTunnelMulti(cfg TunnelConfig, ports []string, rcmd string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	extra := []string{
		"-tt",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
	}
	for _, port := range ports {
		extra = append(extra, "-R", fmt.Sprintf("127.0.0.1:%s:127.0.0.1:%s", port, port))
	}
	args, err := buildSSHArgs(cfg, "", extra...)
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

func startSSHForward(cfg TunnelConfig, port string) (*exec.Cmd, io.ReadCloser, error) {
	args, err := buildSSHArgs(cfg, "",
		"-N",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-R", fmt.Sprintf("127.0.0.1:%s:127.0.0.1:%s", port, port),
	)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(args[0], args[1:]...)
	prepCmd(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, out, nil
}

func waitForListener(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if portListening(port) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}
