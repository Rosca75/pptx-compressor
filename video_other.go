//go:build !windows

// video_other.go — non-Windows counterparts of the helpers in
// video_windows.go. See that file for why they exist.

package main

import "os/exec"

// exeSuffix is appended to executable names looked up on disk (empty on
// Unix-like systems; ".exe" on Windows).
const exeSuffix = ""

// hideConsoleWindow is a no-op outside Windows — there is no console window
// to hide when spawning a child process.
func hideConsoleWindow(cmd *exec.Cmd) {}
