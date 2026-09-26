//go:build !windows

package claudedesktop

// registryPolicyManaged always reports false off Windows: the registry
// policy override exists only there.
func registryPolicyManaged() bool { return false }
