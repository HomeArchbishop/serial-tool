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

type CameraDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CameraConfig struct {
	CameraID     string `json:"cameraId"`
	CameraName   string `json:"cameraName"`
	RemoteDevice string `json:"remoteDevice"`
	Port         int    `json:"port"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FPS          int    `json:"fps"`
	Host         string `json:"host"`
	User         string `json:"user"`
	Password     string `json:"password"`
	IdentityFile string `json:"identityFile"`
	SSHPort      int    `json:"sshPort"`
	SudoPassword string `json:"sudoPassword"`
}

const (
	DeviceKindArm    = "arm"
	DeviceKindCamera = "camera"
	// DeviceKindDepth is kept only to migrate configs saved before depth was
	// folded into the camera device.
	DeviceKindDepth = "depth"
)

const (
	CameraModelRGB    = "rgb"
	CameraModelOrbbec = "orbbec"
)

type DepthListResult struct {
	Devices []DepthCamera `json:"devices"`
	Error   string        `json:"error"`
}

type DepthCamera struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Serial     string `json:"serial"`
	UID        string `json:"uid"`
	PID        int    `json:"pid"`
	VID        int    `json:"vid"`
	Connection string `json:"connection"`
}

// DeviceConfig is the persisted config of one user-added device. Kind decides
// which pipeline runs; both kinds share the SSH fields.
type DeviceConfig struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	Host         string `json:"host"`
	User         string `json:"user"`
	Password     string `json:"password"`
	IdentityFile string `json:"identityFile"`
	SSHPort      int    `json:"sshPort"`
	SudoPassword string `json:"sudoPassword"`

	// Arm (serial tunnel).
	SerialDevice string `json:"serialDevice"`
	LinkName     string `json:"linkName"`
	TunnelPort   int    `json:"tunnelPort"`

	// Camera (push stream).
	CameraModel  string `json:"cameraModel"`
	CameraID     string `json:"cameraId"`
	CameraName   string `json:"cameraName"`
	RemoteDevice string `json:"remoteDevice"`
	Port         int    `json:"port"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FPS          int    `json:"fps"`

	// Depth (Orbbec SDK over TCP).
	DepthSerial string `json:"depthSerial"`
	DepthName   string `json:"depthName"`
	DepthWidth  int    `json:"depthWidth"`
	DepthHeight int    `json:"depthHeight"`
	DepthFPS    int    `json:"depthFps"`
	DepthPort   int    `json:"depthPort"`
	DepthAlign  string `json:"depthAlign"`
}

type DeviceState struct {
	Config   DeviceConfig `json:"config"`
	Running  bool         `json:"running"`
	Activity Activity     `json:"activity"`
}

type DeviceLog struct {
	ID   string `json:"id"`
	Line string `json:"line"`
}

type DeviceStatus struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
}

type DeviceActivity struct {
	ID       string   `json:"id"`
	Activity Activity `json:"activity"`
}
