package omp

import (
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Mapping-node helpers. yaml.v3 stores a mapping as a flat Content slice of
// alternating key/value nodes; these keep the key order and comments intact.

// isKey reports whether n is the scalar key named key.
func isKey(n *yaml.Node, key string) bool {
	return n.Kind == yaml.ScalarNode && n.Value == key
}

// mapGet returns the value node for key in mapping m, or nil when absent or
// when m is not a mapping.
func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isKey(m.Content[i], key) {
			return m.Content[i+1]
		}
	}
	return nil
}

// mapSet replaces the value for key in mapping m, keeping the key's position
// and carrying over the old value's trailing comment, or appends a new pair.
// An empty flow-style mapping ("{}") switches to block style once it gains a
// pair so the inserted block renders as normal indented YAML.
func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isKey(m.Content[i], key) {
			if v.LineComment == "" {
				v.LineComment = m.Content[i+1].LineComment
			}
			m.Content[i+1] = v
			return
		}
	}
	if len(m.Content) == 0 {
		m.Style &^= yaml.FlowStyle
	}
	m.Content = append(m.Content, strNode(key), v)
}

// mapDelete removes key (and its value) from mapping m and reports whether it
// was present (false for a nil or non-mapping m).
func mapDelete(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isKey(m.Content[i], key) {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// ensureMapping returns the mapping stored under key in m, creating it when
// the key is absent or null. Any other value shape is an error naming the
// file, since MintSwitch cannot merge into it without destroying user data.
func ensureMapping(m *yaml.Node, key, path string) (*yaml.Node, error) {
	v := mapGet(m, key)
	switch {
	case v == nil:
		v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapSet(m, key, v)
	case v.Kind == yaml.ScalarNode && v.Tag == "!!null":
		nv := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: v.HeadComment}
		mapSet(m, key, nv)
		v = nv
	case v.Kind != yaml.MappingNode:
		return nil, fmt.Errorf("%s: %q is not a mapping", filepath.Base(path), key)
	}
	return v, nil
}

// scalarString returns the string value of a scalar node, or "" for nil and
// non-scalar nodes.
func scalarString(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// strNode builds a plain string scalar node; the encoder quotes it only when
// YAML would otherwise misread the value (e.g. "yes", "0123").
func strNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// encodeNode encodes v (a struct or map) into a fresh mapping node.
func encodeNode(v any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return &n, nil
}
