package omp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"mintswitch/internal/core"
)

// document is a comment-preserving view of one of omp's YAML config files:
// the root mapping of the (single) YAML document kept as a yaml.v3 node tree,
// so Apply and Restore rewrite only the keys they own and the user's comments,
// key order and scalar styles survive every round trip.
type document struct {
	doc  *yaml.Node
	root *yaml.Node
}

// readDocument parses path as a YAML mapping. A missing, empty or null
// document yields an empty mapping so callers can merge without
// special-casing first run (the file is created on write). Malformed YAML, a
// multi-document stream, or a top-level sequence/scalar is an error, so Apply
// fails before touching anything rather than clobbering a file it does not
// understand.
func readDocument(path string) (*document, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return parseDocument(path, data)
}

func parseDocument(path string, data []byte) (*document, error) {
	name := filepath.Base(path)
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if err := dec.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: multi-document YAML is not supported", name)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return newDocument(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}), nil
	}
	root := doc.Content[0]
	switch {
	case root.Kind == yaml.MappingNode:
	case root.Kind == yaml.ScalarNode && root.Tag == "!!null":
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: root.HeadComment}
		doc.Content[0] = root
	default:
		return nil, fmt.Errorf("parse %s: top-level YAML value is not a mapping", name)
	}
	return &document{doc: &doc, root: root}, nil
}

// newDocument wraps a root mapping node in a document node.
func newDocument(root *yaml.Node) *document {
	return &document{doc: &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, root: root}
}

// readWithLegacy loads the YAML file at yamlPath. When it does not exist but
// omp's legacy JSON file at jsonPath does, the returned in-memory document is
// seeded from the JSON contents instead (nothing is written until the caller
// writes): omp migrates the legacy file only while the .yml is still absent,
// so creating models.yml/config.yml from scratch would silently orphan the
// user's existing custom providers/settings. An unreadable or non-object
// legacy file is ignored, as omp itself does.
func readWithLegacy(yamlPath, jsonPath string) (*document, error) {
	if _, err := os.Stat(yamlPath); err == nil {
		return readDocument(yamlPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	legacy, err := core.ReadJSONObject(jsonPath)
	if err != nil || len(legacy) == 0 {
		return readDocument(yamlPath)
	}
	root, err := encodeNode(normalizeJSON(legacy))
	if err != nil {
		return nil, err
	}
	return newDocument(root), nil
}

// normalizeJSON converts JSON-decoded whole-number floats back to integers so
// a seeded legacy contextWindow renders as 200000 rather than 2e+05.
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeJSON(e)
		}
	case []any:
		for i, e := range t {
			t[i] = normalizeJSON(e)
		}
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
	}
	return v
}

// flowList is a string list that encodes in YAML flow style — "[text, image]",
// the spelling omp's docs use for models[].input; omp parses block style just
// as well. Each item is a plain string scalar, so the encoder quotes it only
// when YAML would otherwise misread the value.
type flowList []string

// MarshalYAML implements yaml.Marshaler.
func (l flowList) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, v := range l {
		n.Content = append(n.Content, strNode(v))
	}
	return n, nil
}

// marshal renders the document with omp's two-space indentation.
func (d *document) marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// write atomically writes the document to path. The file carries 0600 perms
// since models.yml holds the API key.
func (d *document) write(path string) error {
	data, err := d.marshal()
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(path, data, 0o600)
}

// writeDocument is the package-level form of [document.write], the default
// for the Adapter's injectable config writer.
func writeDocument(path string, d *document) error { return d.write(path) }
