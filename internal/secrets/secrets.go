// Package secrets stores MintSwitch's API key in the OS keychain (macOS
// Keychain, Windows Credential Manager, Linux Secret Service) via
// zalando/go-keyring, which is cgo-free on all three platforms.
//
// The secret value must never be logged; errors returned by the underlying
// keyring never contain it. On systems without a usable keychain (e.g.
// headless Linux with no Secret Service) every call returns an error and the
// caller is expected to fall back to its previous plaintext behaviour.
package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// account is the keychain account (username) under which the API key is
// stored. The service name distinguishes production from dev builds (see
// serviceName in the build-tagged files).
const account = "api_key"

// Keyring reads and writes the API key in the OS keychain under a fixed
// service/account pair.
type Keyring struct {
	// Service is the keychain service name ("mintswitch" in production
	// builds, "mintswitch-dev" otherwise, mirroring paths.dataDirName).
	Service string
}

// New returns a Keyring using the build-appropriate service name.
func New() *Keyring { return &Keyring{Service: serviceName} }

// Get returns the stored API key. A missing entry is not an error: it
// returns ("", false, nil) so first-run callers need no special handling.
func (k *Keyring) Get() (string, bool, error) {
	v, err := keyring.Get(k.Service, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

// Set stores (or overwrites) the API key in the keychain.
func (k *Keyring) Set(value string) error {
	return keyring.Set(k.Service, account, value)
}

// Delete removes the stored API key. A missing entry is not an error.
func (k *Keyring) Delete() error {
	err := keyring.Delete(k.Service, account)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
