package main

import "errors"

var ErrSudoPasswordRequired = errors.New("SUDO_PASSWORD_REQUIRED")

type SSHHost struct {
	Alias        string `json:"alias"`
	Host         string `json:"host"`
	User         string `json:"user"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identityFile"`
}

type ToolStatus struct {
	OK      bool   `json:"ok"`
	Path    string `json:"path"`
	Hint    string `json:"hint"`
	Message string `json:"message"`
}

type TunnelConfig struct {
	Device       string `json:"device"`
	Host         string `json:"host"`
	User         string `json:"user"`
	Password     string `json:"password"`
	IdentityFile string `json:"identityFile"`
	SSHPort      int    `json:"sshPort"`
	TunnelPort   int    `json:"tunnelPort"`
	LinkName     string `json:"linkName"`
	SudoPassword string `json:"sudoPassword"`
}

type Activity struct {
	Running   bool   `json:"running"`
	Listening bool   `json:"listening"`
	Linked    bool   `json:"linked"`
	Message   string `json:"message"`
}
