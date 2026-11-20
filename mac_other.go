//go:build !darwin

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func setDockVisible(visible bool) {}

func syncTrafficLights(win *application.WebviewWindow, focused bool) {}
