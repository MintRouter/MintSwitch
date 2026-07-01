package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"mintswitch/internal/core"
)

// mcpProbeTimeout bounds the Test-Connection probe so a hung endpoint cannot
// block the UI indefinitely.
const mcpProbeTimeout = 10 * time.Second

// MCPToolView is the per-tool MCP status returned by [Service.GetMCPState].
type MCPToolView struct {
	ID          string   `json:"id"`
	Installed   bool     `json:"installed"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail"`
	ConfigPaths []string `json:"config_paths"`
}

// MCPState is the redacted MCP state returned to the frontend. It carries a
// HasKey flag (never the key itself) and the per-tool status list, plus the
// non-secret endpoint that would be injected.
type MCPState struct {
	HasKey   bool          `json:"has_key"`
	Endpoint string        `json:"endpoint"`
	Tools    []MCPToolView `json:"tools"`
}

// MCPTestResult is the structured outcome of [Service.TestMCPConnection]. It
// carries only display-safe fields and never includes the key or Authorization
// header.
type MCPTestResult struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Meaning string `json:"meaning"`
}

// mcpSpec loads the persisted MCP server spec (name, endpoint, saved key) and
// reports whether a key is present. The endpoint falls back to the default
// constant when no override is saved.
func (s *Service) mcpSpec() (core.MCPServerSpec, bool, error) {
	st, err := s.store.Load()
	if err != nil {
		return core.MCPServerSpec{}, false, err
	}
	spec := core.MCPServerSpec{
		Name:     core.DefaultMCPServerName,
		Endpoint: strings.TrimSpace(st.MCPEndpoint),
		APIKey:   st.MCPKey,
	}.Normalized()
	return spec, strings.TrimSpace(st.MCPKey) != "", nil
}

// SetMCPKey persists the MintRouter API key used for MCP injection. The key is
// trimmed; an empty value clears the stored key. The key is never logged.
func (s *Service) SetMCPKey(key string) error {
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	st.MCPKey = strings.TrimSpace(key)
	return s.store.Save(st)
}

// GetMCPState returns the redacted MCP state: whether a key is saved, the
// endpoint that would be injected, and each injector's current status. It never
// returns the raw key.
func (s *Service) GetMCPState() (MCPState, error) {
	spec, hasKey, err := s.mcpSpec()
	if err != nil {
		return MCPState{}, err
	}
	tools := make([]MCPToolView, 0, len(s.mcp))
	for _, inj := range s.mcp {
		status, detail, serr := inj.MCPStatus(spec)
		if serr != nil {
			detail = serr.Error()
		}
		tools = append(tools, MCPToolView{
			ID:          inj.ID(),
			Installed:   inj.Detect(),
			Status:      status.String(),
			Detail:      detail,
			ConfigPaths: inj.MCPConfigPaths(),
		})
	}
	return MCPState{HasKey: hasKey, Endpoint: spec.Endpoint, Tools: tools}, nil
}

// InjectMCPOne injects the MintRouter MCP server into the single tool identified
// by toolID. It fails when no key is saved or the tool is unknown.
func (s *Service) InjectMCPOne(toolID string) (core.MCPResult, error) {
	inj, ok := s.mcpInjector(toolID)
	if !ok {
		return core.MCPResult{}, fmt.Errorf("service: unknown MCP tool %q", toolID)
	}
	spec, hasKey, err := s.mcpSpec()
	if err != nil {
		return core.MCPResult{}, err
	}
	if !hasKey {
		return core.MCPResult{}, fmt.Errorf("service: no MintRouter API key saved; set one before injecting")
	}
	return inj.InjectMCP(spec)
}

// RemoveMCPOne removes the MintRouter MCP server from the single tool identified
// by toolID. It is a safe no-op when nothing was injected.
func (s *Service) RemoveMCPOne(toolID string) (core.MCPResult, error) {
	inj, ok := s.mcpInjector(toolID)
	if !ok {
		return core.MCPResult{}, fmt.Errorf("service: unknown MCP tool %q", toolID)
	}
	return inj.RemoveMCP()
}

// TestMCPConnection probes the MintRouter MCP endpoint with the saved key via a
// JSON-RPC initialize request and maps the HTTP status to a user-facing meaning.
// The key/Authorization header are never logged or included in the result.
func (s *Service) TestMCPConnection() (MCPTestResult, error) {
	spec, hasKey, err := s.mcpSpec()
	if err != nil {
		return MCPTestResult{}, err
	}
	if !hasKey {
		return MCPTestResult{OK: false, Meaning: "No MintRouter API key saved; set one before testing."}, nil
	}
	client := s.mcpClient
	if client == nil {
		client = &http.Client{Timeout: mcpProbeTimeout}
	}
	return probeMCP(context.Background(), client, spec), nil
}

// mcpInjector returns the registered injector for toolID and whether it exists.
func (s *Service) mcpInjector(toolID string) (core.MCPInjector, bool) {
	for _, inj := range s.mcp {
		if inj.ID() == toolID {
			return inj, true
		}
	}
	return nil, false
}
