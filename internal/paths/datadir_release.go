//go:build production

package paths

// dataDirName is the directory name (under os.UserConfigDir()) holding
// MintSwitch's own data in production builds (-tags production, set by the
// wails3 build/package tasks).
const dataDirName = "mintswitch"
