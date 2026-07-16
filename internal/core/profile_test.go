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

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		want         string
		wantUpgraded bool
	}{
		{"public http upgrades", "http://api.example.com/v1", "https://api.example.com/v1", true},
		{"public http with port upgrades", "http://api.example.com:8443/v1", "https://api.example.com:8443/v1", true},
		{"https untouched", "https://api.example.com/v1", "https://api.example.com/v1", false},
		{"trailing slash trimmed", "http://api.example.com/v1/", "https://api.example.com/v1", true},
		{"multiple trailing slashes trimmed", "https://api.example.com/v1///", "https://api.example.com/v1", false},
		{"root slash trimmed to empty", "https://api.example.com/", "https://api.example.com", false},
		{"whitespace trimmed", "  http://api.example.com/v1  ", "https://api.example.com/v1", true},
		{"localhost stays http", "http://localhost:1234/v1", "http://localhost:1234/v1", false},
		{"loopback 127 stays http", "http://127.0.0.1:8080/v1", "http://127.0.0.1:8080/v1", false},
		{"loopback 127 other stays http", "http://127.1.2.3/v1", "http://127.1.2.3/v1", false},
		{"ipv6 loopback stays http", "http://[::1]:8080/v1", "http://[::1]:8080/v1", false},
		{"private 192.168 stays http", "http://192.168.1.10:1234/v1", "http://192.168.1.10:1234/v1", false},
		{"private 10 stays http", "http://10.0.0.5/v1", "http://10.0.0.5/v1", false},
		{"private 172.16 stays http", "http://172.16.5.4/v1", "http://172.16.5.4/v1", false},
		{"private 172.31 stays http", "http://172.31.255.1/v1", "http://172.31.255.1/v1", false},
		{"link-local 169.254 stays http", "http://169.254.1.1/v1", "http://169.254.1.1/v1", false},
		{"link-local fe80 stays http", "http://[fe80::1]:8080/v1", "http://[fe80::1]:8080/v1", false},
		{"dot local stays http", "http://foo.local/v1", "http://foo.local/v1", false},
		{"dot localhost stays http", "http://foo.localhost/v1", "http://foo.localhost/v1", false},
		{"public ip 172.32 upgrades", "http://172.32.0.1/v1", "https://172.32.0.1/v1", true},
		{"public ip 8.8.8.8 upgrades", "http://8.8.8.8/v1", "https://8.8.8.8/v1", true},
		{"empty unchanged", "", "", false},
		{"bare host unchanged", "api.example.com/v1", "api.example.com/v1", false},
		{"invalid unchanged", "http://%zz", "http://%zz", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, upgraded := NormalizeBaseURL(tt.in)
			if got != tt.want || upgraded != tt.wantUpgraded {
				t.Fatalf("NormalizeBaseURL(%q) = (%q, %v), want (%q, %v)", tt.in, got, upgraded, tt.want, tt.wantUpgraded)
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
		{APIKey: "k", BaseURL: "https://h", Model: "m", SmallFastModel: "s", OpusModel: "o"},
		{APIKey: "k", BaseURL: "https://h", Model: "m", SmallFastModel: "s", SonnetModel: "sn"},
		{APIKey: "k", BaseURL: "https://h", Model: "m", SmallFastModel: "s", HaikuModel: "hk"},
	}
	for i, c := range changes {
		if Fingerprint(c) == Fingerprint(base) {
			t.Fatalf("change %d did not alter fingerprint", i)
		}
	}
}

// validProvider is a fully-populated provider for Provider tests.
func validProvider() Provider {
	return Provider{
		ID: "p1", Name: "OpenAI", Note: "team key",
		APIKey: "sk-1", BaseURL: "https://api.example.com/v1",
		Models: []string{"m1", "m2"}, Model: "m1", SmallFastModel: "s",
	}
}

func TestProviderValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Provider)
		wantErr bool
	}{
		{"valid", func(*Provider) {}, false},
		{"empty note ok", func(p *Provider) { p.Note = "" }, false},
		{"missing id", func(p *Provider) { p.ID = "" }, true},
		{"missing name", func(p *Provider) { p.Name = "  " }, true},
		{"missing key", func(p *Provider) { p.APIKey = "" }, true},
		{"missing model", func(p *Provider) { p.Model = ""; p.Models = nil }, true},
		{"missing base url", func(p *Provider) { p.BaseURL = "" }, true},
		{"model not in models", func(p *Provider) { p.Model = "nope" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validProvider()
			tt.mutate(&p)
			err := p.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// TestProviderProfile proves the resolved Profile carries every endpoint
// field and is labeled with the provider name.
func TestProviderProfile(t *testing.T) {
	pr := validProvider()
	pr.OpusModel, pr.SonnetModel, pr.HaikuModel = "o", "sn", "hk"
	p := pr.Profile()
	if p.Label != "OpenAI" || p.APIKey != "sk-1" || p.BaseURL != pr.BaseURL ||
		p.Model != "m1" || p.SmallFastModel != "s" || len(p.Models) != 2 ||
		p.OpusModel != "o" || p.SonnetModel != "sn" || p.HaikuModel != "hk" {
		t.Fatalf("resolved profile wrong: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("resolved profile invalid: %v", err)
	}
}

func TestProviderHasModel(t *testing.T) {
	pr := validProvider()
	if !pr.HasModel("m2") || pr.HasModel("nope") {
		t.Fatal("HasModel membership wrong")
	}
	// Empty Models (pre-Models provider) offers exactly the default Model.
	pr.Models = nil
	if !pr.HasModel("m1") || pr.HasModel("m2") || pr.HasModel("") {
		t.Fatal("HasModel empty-list fallback wrong")
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
