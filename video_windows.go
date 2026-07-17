//go:build windows

// video_windows.go — Windows-only helper for running ffmpeg invisibly.
//
// On Windows, launching a console program (like ffmpeg.exe) from a GUI app
// briefly flashes a black console window unless the process is created with
// the HideWindow attribute. This file provides that; the non-Windows build
// uses the no-op in video_other.go.

package main

import (
	"os/exec"
	"syscall"
)

// exeSuffix is appended to executable names looked up on disk (".exe" here).
const exeSuffix = ".exe"

// hideConsoleWindow marks cmd so Windows creates its process without a
// visible console window.
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
