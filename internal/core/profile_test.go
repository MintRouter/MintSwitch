package core

import "testing"

func TestProfileValidate(t *testing.T) {
	valid := Profile{APIKey: "sk-123", BaseURL: "https://api.example.com/v1", Model: "gpt-x"}
	tests := []struct {
		name    string
		p       Profile
		wantErr bool
	}{
		{"valid https", valid, false},
		{"valid http", Profile{APIKey: "k", BaseURL: "http://localhost:8080", Model: "m"}, false},
		{"valid with optional fields", Profile{Label: "l", APIKey: "k", BaseURL: "https://h", Model: "m", SmallFastModel: "s"}, false},
		{"missing api key", Profile{BaseURL: "https://h", Model: "m"}, true},
		{"blank api key", Profile{APIKey: "   ", BaseURL: "https://h", Model: "m"}, true},
		{"missing model", Profile{APIKey: "k", BaseURL: "https://h"}, true},
		{"missing base url", Profile{APIKey: "k", Model: "m"}, true},
		{"non http scheme", Profile{APIKey: "k", BaseURL: "ftp://h", Model: "m"}, true},
		{"missing scheme", Profile{APIKey: "k", BaseURL: "example.com/v1", Model: "m"}, true},
		{"no host", Profile{APIKey: "k", BaseURL: "https://", Model: "m"}, true},
		{"unparseable url", Profile{APIKey: "k", BaseURL: "http://%zz", Model: "m"}, true},
		{"model is one of models", Profile{APIKey: "k", BaseURL: "https://h", Model: "m", Models: []string{"x", "m"}}, false},
		{"empty models ok", Profile{APIKey: "k", BaseURL: "https://h", Model: "m", Models: nil}, false},
		{"model not in models", Profile{APIKey: "k", BaseURL: "https://h", Model: "m", Models: []string{"x", "y"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	base := Profile{APIKey: "k", BaseURL: "https://h", Model: "m", SmallFastModel: "s"}
	if Fingerprint(base) != Fingerprint(base) {
		t.Fatal("fingerprint not stable for identical profiles")
	}
	changes := []Profile{
		{APIKey: "k2", BaseURL: "https://h", Model: "m", SmallFastModel: "s"},
		{APIKey: "k", BaseURL: "https://h2", Model: "m", SmallFastModel: "s"},
		{APIKey: "k", BaseURL: "https://h", Model: "m2", SmallFastModel: "s"},
		{APIKey: "k", BaseURL: "https://h", Model: "m", SmallFastModel: "s2"},
	}
	for i, c := range changes {
		if Fingerprint(c) == Fingerprint(base) {
			t.Fatalf("change %d did not alter fingerprint", i)
		}
	}
}

func TestNewMarker(t *testing.T) {
	p := Profile{APIKey: "k", BaseURL: "https://h", Model: "m"}
	m := NewMarker(p, "work")
	if !m.Managed || m.ProfileLabel != "work" || m.Version != MarkerVersion {
		t.Fatalf("unexpected marker: %+v", m)
	}
	if m.Fingerprint != Fingerprint(p) {
		t.Fatal("marker fingerprint mismatch")
	}
	if m.AppliedAt.IsZero() {
		t.Fatal("marker AppliedAt not set")
	}
}
