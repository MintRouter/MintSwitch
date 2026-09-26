package claudedesktop

import "strings"

// policyKeyPath is the registry path (under HKLM, then HKCU) holding Claude
// Desktop's managed (MDM) configuration on Windows.
const policyKeyPath = `SOFTWARE\Policies\Claude`

// policyDetail explains why Status withholds "Applied" while a managed
// configuration owns the device.
const policyDetail = "Claude Desktop is managed by an organization policy (HKLM/HKCU\\SOFTWARE\\Policies\\Claude); " +
	"local third-party config is ignored while that policy is present, so the MintSwitch gateway does not take effect."

// appBehaviorPolicyKeys are the managed-config value names (lower-cased)
// that only tune app behavior — updates, relaunch, config recheck, egress
// proxy. Per claude.com/docs/third-party/claude-desktop/mdm, a policy key
// holding only these does not take ownership of the deployment, so the
// local configLibrary still applies.
var appBehaviorPolicyKeys = map[string]bool{
	"disableautoupdates":           true,
	"autoupdaterenforcementhours":  true,
	"updateviaupdateshost":         true,
	"relaunchenforcementhours":     true,
	"configrecheckintervalminutes": true,
	"egressproxyurl":               true,
	"egressproxypacurl":            true,
}

// policyOwnsDevice reports whether Claude Desktop's managed configuration
// takes over the deployment, given the value names found directly under
// HKLM\SOFTWARE\Policies\Claude and HKCU\SOFTWARE\Policies\Claude. HKLM wins
// whenever it holds any value (HKCU is then ignored entirely); otherwise
// HKCU decides. The chosen hive owns the device when it sets at least one
// value that is not an app-behavior key. Registry value names are
// case-insensitive, so the comparison is too.
func policyOwnsDevice(hklm, hkcu []string) bool {
	names := hkcu
	if len(hklm) > 0 {
		names = hklm
	}
	for _, n := range names {
		if !appBehaviorPolicyKeys[strings.ToLower(n)] {
			return true
		}
	}
	return false
}
