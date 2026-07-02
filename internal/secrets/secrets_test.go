package secrets

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestKeyringRoundTrip exercises Set/Get/Delete against go-keyring's
// in-memory mock so the test never touches the real OS keychain.
func TestKeyringRoundTrip(t *testing.T) {
	keyring.MockInit()
	k := New()
	if k.Service != serviceName {
		t.Fatalf("Service = %q, want %q", k.Service, serviceName)
	}

	if _, found, err := k.Get(); err != nil || found {
		t.Fatalf("Get before Set = (found=%v, err=%v), want not found, nil", found, err)
	}

	if err := k.Set("sk-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, found, err := k.Get()
	if err != nil || !found || v != "sk-secret" {
		t.Fatalf("Get after Set = (%q, %v, %v), want (sk-secret, true, nil)", v, found, err)
	}

	if err := k.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, err := k.Get(); err != nil || found {
		t.Fatalf("Get after Delete = (found=%v, err=%v), want not found, nil", found, err)
	}
	// Deleting a missing entry is not an error.
	if err := k.Delete(); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

// TestKeyringUnavailable proves keychain errors surface to the caller (who
// falls back to plaintext) rather than being swallowed as "not found".
func TestKeyringUnavailable(t *testing.T) {
	mockErr := errors.New("no secret service")
	keyring.MockInitWithError(mockErr)
	k := New()
	if _, _, err := k.Get(); !errors.Is(err, mockErr) {
		t.Fatalf("Get err = %v, want %v", err, mockErr)
	}
	if err := k.Set("v"); !errors.Is(err, mockErr) {
		t.Fatalf("Set err = %v, want %v", err, mockErr)
	}
}
