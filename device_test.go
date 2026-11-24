package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestDeviceService(t *testing.T) *DeviceService {
	t.Helper()
	deviceStoreOverride = filepath.Join(t.TempDir(), "devices.json")
	t.Cleanup(func() { deviceStoreOverride = "" })
	return &DeviceService{devices: map[string]*deviceRuntime{}}
}

func TestAddDevicesAssignsUniqueResources(t *testing.T) {
	s := newTestDeviceService(t)

	arm, err := s.AddDevice(DeviceConfig{Kind: DeviceKindArm})
	if err != nil {
		t.Fatal(err)
	}
	cam1, err := s.AddDevice(DeviceConfig{Kind: DeviceKindCamera})
	if err != nil {
		t.Fatal(err)
	}
	cam2, err := s.AddDevice(DeviceConfig{Kind: DeviceKindCamera})
	if err != nil {
		t.Fatal(err)
	}

	if arm.Config.Label != "机械臂 1" {
		t.Fatalf("arm label = %q", arm.Config.Label)
	}
	if cam1.Config.Label != "相机 1" || cam2.Config.Label != "相机 2" {
		t.Fatalf("camera labels = %q, %q", cam1.Config.Label, cam2.Config.Label)
	}
	if cam1.Config.RemoteDevice == cam2.Config.RemoteDevice {
		t.Fatalf("camera remote devices collide: %q", cam1.Config.RemoteDevice)
	}
	if cam1.Config.Port == cam2.Config.Port {
		t.Fatalf("camera ports collide: %d", cam1.Config.Port)
	}
	if len(s.ListDevices()) != 3 {
		t.Fatalf("devices = %d, want 3", len(s.ListDevices()))
	}

	if err := s.RemoveDevice(cam1.Config.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ListDevices()) != 2 {
		t.Fatalf("devices after remove = %d, want 2", len(s.ListDevices()))
	}
}

func TestStartDeviceValidatesConfig(t *testing.T) {
	s := newTestDeviceService(t)
	state, err := s.AddDevice(DeviceConfig{Kind: DeviceKindArm})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartDevice(state.Config); err == nil {
		t.Fatal("expected missing host to fail")
	}
}

func TestDeviceStoreRoundTripWithoutSecrets(t *testing.T) {
	s := newTestDeviceService(t)
	state, err := s.AddDevice(DeviceConfig{
		Kind:     DeviceKindCamera,
		Host:     "example",
		Password: "ssh-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Config.ID == "" {
		t.Fatal("missing id")
	}

	data, err := os.ReadFile(deviceStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ssh-secret") {
		t.Fatal("password written to store")
	}

	loaded := &DeviceService{devices: map[string]*deviceRuntime{}}
	loaded.mu.Lock()
	loaded.loadLocked()
	loaded.mu.Unlock()

	list := loaded.ListDevices()
	if len(list) != 1 || list[0].Config.ID != state.Config.ID {
		t.Fatalf("loaded devices = %+v", list)
	}
	if list[0].Config.Password != "" {
		t.Fatal("password restored from store")
	}
}

func TestCleanLogLineStripsANSI(t *testing.T) {
	in := "\x1b[48;5;0m\x1b[38;5;207m[mpegts @ 0x1] \x1b[0mStream #0\r"
	got := cleanLogLine(in)
	if want := "[mpegts @ 0x1] Stream #0"; got != want {
		t.Fatalf("cleanLogLine = %q, want %q", got, want)
	}
}

func TestLoopbackMarkerRoundTrip(t *testing.T) {
	s := newTestDeviceService(t)
	_ = s
	cfg := DeviceConfig{ID: "x", Kind: DeviceKindCamera, Host: "h", Password: "secret", SudoPassword: "sudo-secret"}
	writeLoopbackMarker(cfg)
	got, ok := readLoopbackMarker()
	if !ok || got.ID != "x" {
		t.Fatalf("marker = %+v, %v", got, ok)
	}
	if got.Password != "" || got.SudoPassword != "" {
		t.Fatal("marker contains secrets")
	}
	clearLoopbackMarker()
	if _, ok := readLoopbackMarker(); ok {
		t.Fatal("marker not cleared")
	}
}

func TestParseCleanupOutput(t *testing.T) {
	cleaned, remaining := parseCleanupOutput("hello\ncleaned: 12 13\nremaining: 14\n")
	if cleaned != "12 13" || remaining != "14" {
		t.Fatalf("cleaned=%q remaining=%q", cleaned, remaining)
	}
}

func TestAddOrbbecCameraDefaults(t *testing.T) {
	s := newTestDeviceService(t)
	state, err := s.AddDevice(DeviceConfig{Kind: DeviceKindCamera, CameraModel: CameraModelOrbbec, DepthSerial: "sn"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := state.Config
	if cfg.Label != "相机 1" {
		t.Fatalf("label = %q", cfg.Label)
	}
	if cfg.DepthWidth != defaultDepthWidth || cfg.DepthHeight != defaultDepthHeight || cfg.DepthFPS != defaultDepthFPS {
		t.Fatalf("profile = %dx%d@%d", cfg.DepthWidth, cfg.DepthHeight, cfg.DepthFPS)
	}
	if cfg.DepthPort <= 0 || cfg.Port <= 0 {
		t.Fatalf("ports: color=%d depth=%d", cfg.Port, cfg.DepthPort)
	}
}

func TestMigrateDepthDeviceToOrbbecCamera(t *testing.T) {
	s := newTestDeviceService(t)
	state, err := s.AddDevice(DeviceConfig{Kind: DeviceKindDepth, DepthSerial: "sn", DepthPort: 9300})
	if err != nil {
		t.Fatal(err)
	}
	cfg := state.Config
	if cfg.Kind != DeviceKindCamera || cfg.CameraModel != CameraModelOrbbec {
		t.Fatalf("migration produced kind=%s model=%s", cfg.Kind, cfg.CameraModel)
	}
	if cfg.DepthPort != 9300 {
		t.Fatalf("depth port = %d", cfg.DepthPort)
	}
}

func TestResolveDevicePortMovesPastOccupiedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	occupied := ln.Addr().(*net.TCPAddr).Port

	s := newTestDeviceService(t)
	cfg := DeviceConfig{Kind: DeviceKindCamera, Port: occupied}
	msg := s.resolveDevicePort(&cfg)
	if msg == "" {
		t.Fatal("expected a port change message")
	}
	if cfg.Port == occupied || cfg.Port <= 0 {
		t.Fatalf("port = %d, want a free port", cfg.Port)
	}
	if !portFree(cfg.Port) {
		t.Fatalf("chosen port %d is not free", cfg.Port)
	}
}

func TestParseDepthDeviceListWithLogNoise(t *testing.T) {
	raw := []byte("[sdk][warning] New openni device matched.\n{\"devices\":[{\"id\":\"uid\",\"name\":\"DaBai DC1\"}]}\n")
	list := parseDepthDeviceList(raw)
	if len(list) != 1 || list[0].ID != "uid" || list[0].Name != "DaBai DC1" {
		t.Fatalf("devices = %+v", list)
	}
}

func TestMigrateLegacyPorts(t *testing.T) {
	if got := migrateLegacyPort(9100, 9100); got != 47100 {
		t.Fatalf("color 9100 -> %d", got)
	}
	if got := migrateLegacyPort(9201, 9200); got != 47201 {
		t.Fatalf("depth 9201 -> %d", got)
	}
	if got := migrateLegacyPort(47100, 9100); got != 47100 {
		t.Fatalf("new port changed: %d", got)
	}
}
