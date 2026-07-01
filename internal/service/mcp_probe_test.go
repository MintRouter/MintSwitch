package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mintswitch/internal/core"
)

func TestTestMCPConnectionNoKey(t *testing.T) {
	s := newMCPService(t, &fakeInjector{id: "claude-code"})
	res, err := s.TestMCPConnection()
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.OK || !strings.Contains(res.Meaning, "No MintRouter API key") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestTestMCPConnectionStatusMapping drives the probe against an httptest server
// and asserts the status→meaning mapping, that the request is a JSON-RPC
// initialize with the correct headers, and that the key never appears in the
// result.
func TestTestMCPConnectionStatusMapping(t *testing.T) {
	const key = "sk-super-secret"
	var gotAuth, gotAccept, gotCT, gotMethod string
	var gotBody map[string]any
	code := http.StatusOK

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotCT = r.Header.Get("Content-Type")
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(code)
	}))
	defer srv.Close()

	s := newMCPService(t, &fakeInjector{id: "claude-code"})
	if err := s.SetMCPKey(key); err != nil {
		t.Fatal(err)
	}
	stt, _ := s.store.Load()
	stt.MCPEndpoint = srv.URL
	if err := s.store.Save(stt); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		code int
		ok   bool
		want string
	}{
		{http.StatusOK, true, "Connected"},
		{http.StatusUnauthorized, false, "Unauthorized"},
		{http.StatusForbidden, false, "Forbidden"},
		{http.StatusNotFound, false, "Not found"},
		{http.StatusTooManyRequests, false, "Rate limited"},
		{http.StatusInternalServerError, false, "Unexpected response"},
	}
	for _, c := range cases {
		code = c.code
		res, err := s.TestMCPConnection()
		if err != nil {
			t.Fatalf("code %d: %v", c.code, err)
		}
		if res.OK != c.ok || res.Status != c.code || !strings.Contains(res.Meaning, c.want) {
			t.Fatalf("code %d: got %+v, want ok=%v contains %q", c.code, res, c.ok, c.want)
		}
		if strings.Contains(res.Meaning, key) {
			t.Fatalf("code %d: meaning leaked key: %q", c.code, res.Meaning)
		}
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer "+key {
		t.Fatalf("auth header not sent correctly")
	}
	if !strings.Contains(gotAccept, "text/event-stream") || !strings.Contains(gotAccept, "application/json") {
		t.Fatalf("accept header = %q", gotAccept)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Fatalf("content-type = %q", gotCT)
	}
	if gotBody["method"] != "initialize" {
		t.Fatalf("body method = %v, want initialize", gotBody["method"])
	}
	params, _ := gotBody["params"].(map[string]any)
	if params["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", params["protocolVersion"])
	}
	clientInfo, _ := params["clientInfo"].(map[string]any)
	if clientInfo["name"] != "mintswitch" {
		t.Fatalf("clientInfo.name = %v", clientInfo["name"])
	}
}

func TestProbeTransportError(t *testing.T) {
	// A server that is immediately closed forces a connection-level failure.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	res := probeMCP(context.Background(), http.DefaultClient, core.MCPServerSpec{Endpoint: url, APIKey: "k"})
	if res.OK || res.Status != 0 || !strings.Contains(res.Meaning, "Could not reach") {
		t.Fatalf("unexpected transport result: %+v", res)
	}
}
