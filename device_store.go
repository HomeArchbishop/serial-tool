package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Device configs are persisted so user-added devices survive restarts.
// Passwords are deliberately not written to disk; key-based auth keeps working
// and password prompts reappear when needed.
type deviceStoreFile struct {
	Version int            `json:"version"`
	Devices []DeviceConfig `json:"devices"`
}

const deviceStoreVersion = 1

// deviceStoreOverride lets tests redirect the store away from the real config.
var deviceStoreOverride string

func deviceStoreDir() string {
	if deviceStoreOverride != "" {
		return filepath.Dir(deviceStoreOverride)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "serial-tool")
}

func deviceStorePath() string {
	if deviceStoreOverride != "" {
		return deviceStoreOverride
	}
	dir := deviceStoreDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "devices.json")
}

func loopbackMarkerPath() string {
	dir := deviceStoreDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "loopback.json")
}

// writeLoopbackMarker records that this app loaded the v4l2loopback module, so
// a crash can be detected and the module unloaded on the next launch.
func writeLoopbackMarker(cfg DeviceConfig) {
	path := loopbackMarkerPath()
	if path == "" {
		return
	}
	cfg.Password = ""
	cfg.SudoPassword = ""
	data, err := json.Marshal(deviceStoreFile{Version: deviceStoreVersion, Devices: []DeviceConfig{cfg}})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

func readLoopbackMarker() (DeviceConfig, bool) {
	path := loopbackMarkerPath()
	if path == "" {
		return DeviceConfig{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DeviceConfig{}, false
	}
	var file deviceStoreFile
	if err := json.Unmarshal(data, &file); err != nil || len(file.Devices) == 0 {
		return DeviceConfig{}, false
	}
	cfg := file.Devices[0]
	if cfg.Kind != DeviceKindCamera {
		return DeviceConfig{}, false
	}
	return cfg, true
}

func clearLoopbackMarker() {
	if path := loopbackMarkerPath(); path != "" {
		_ = os.Remove(path)
	}
}

func (s *DeviceService) loadLocked() {
	path := deviceStorePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var file deviceStoreFile
	if err := json.Unmarshal(data, &file); err != nil {
		return
	}
	for _, cfg := range file.Devices {
		if cfg.ID == "" || (cfg.Kind != DeviceKindArm && cfg.Kind != DeviceKindCamera) {
			continue
		}
		if _, exists := s.devices[cfg.ID]; exists {
			continue
		}
		s.devices[cfg.ID] = &deviceRuntime{
			svc:      s,
			cfg:      cfg,
			activity: Activity{Message: idleMessage(cfg.Kind)},
		}
		s.order = append(s.order, cfg.ID)
	}
}

func (s *DeviceService) save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveLocked()
}

func (s *DeviceService) saveLocked() {
	path := deviceStorePath()
	if path == "" {
		return
	}
	file := deviceStoreFile{Version: deviceStoreVersion}
	for _, id := range s.order {
		r := s.devices[id]
		if r == nil {
			continue
		}
		r.mu.Lock()
		cfg := r.cfg
		r.mu.Unlock()
		cfg.Password = ""
		cfg.SudoPassword = ""
		file.Devices = append(file.Devices, cfg)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}
