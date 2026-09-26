//go:build windows

package installer

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the Win32 CREATE_NO_WINDOW process-creation flag.
const createNoWindow = 0x08000000

// hideWindow starts cmd without a console window. MintSwitch is a GUI
// (-H windowsgui) app, so running npm.cmd would otherwise pop up a visible cmd
// console, and closing it would kill the install.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
