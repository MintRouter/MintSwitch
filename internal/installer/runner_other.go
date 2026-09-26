//go:build !windows

package installer

import "os/exec"

// hideWindow is a no-op outside Windows: no console window is ever created.
func hideWindow(*exec.Cmd) {}
