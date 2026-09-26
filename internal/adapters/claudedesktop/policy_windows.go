//go:build windows

package claudedesktop

import "golang.org/x/sys/windows/registry"

// registryPolicyManaged reports whether a Claude Desktop managed
// configuration under HKLM/HKCU\SOFTWARE\Policies\Claude owns the device
// (see policyOwnsDevice).
func registryPolicyManaged() bool {
	return policyOwnsDevice(
		policyValueNames(registry.LOCAL_MACHINE),
		policyValueNames(registry.CURRENT_USER),
	)
}

// policyValueNames returns the value names directly under policyKeyPath in
// root, or nil when the key is absent or unreadable.
func policyValueNames(root registry.Key) []string {
	k, err := registry.OpenKey(root, policyKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil
	}
	return names
}
