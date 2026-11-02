// Package apispec measures the provider's App Store Connect API coverage
// against Apple's OpenAPI spec, which is vendored in api/openapi.oas.json. It
// also turns the coverage manifest into the configuration that generates the
// API client in internal/asc/gen.
package apispec

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
)

// Spec is the subset of an OpenAPI 3.0 document the coverage checks read.
type Spec struct {
	Version    string
	Operations map[string]*Operation
	Schemas    map[string]*Schema
}

// Operation is one method on one path.
type Operation struct {
	ID          string       `json:"operationId"`
	Summary     string       `json:"summary"`
	Tags        []string     `json:"tags"`
	Parameters  []Parameter  `json:"parameters"`
	RequestBody *RequestBody `json:"requestBody"`
	Method      string       `json:"-"`
	Path        string       `json:"-"`
}

// Parameter is a path or query parameter.
type Parameter struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

// RequestBody is an operation's request body by content type.
type RequestBody struct {
	Content map[string]struct {
		Schema *Schema `json:"schema"`
	} `json:"content"`
}

// Schema is the subset of a JSON Schema the checks need.
type Schema struct {
	Ref        string             `json:"$ref"`
	Properties map[string]*Schema `json:"properties"`
	OneOf      []*Schema          `json:"oneOf"`
	AnyOf      []*Schema          `json:"anyOf"`
	AllOf      []*Schema          `json:"allOf"`
}

var httpMethods = map[string]string{
	"get":    http.MethodGet,
	"put":    http.MethodPut,
	"post":   http.MethodPost,
	"patch":  http.MethodPatch,
	"delete": http.MethodDelete,
}

// LoadSpec reads an OpenAPI document from a file.
func LoadSpec(path string) (*Spec, error) {
	b, err := os.ReadFile(path) //nolint:gosec // The caller chooses the spec file.
	if err != nil {
		return nil, fmt.Errorf("reading spec: %w", err)
	}
	return ParseSpec(b)
}

// ParseSpec parses an OpenAPI document.
func ParseSpec(b []byte) (*Spec, error) {
	var doc struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]*Schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	if len(doc.Paths) == 0 {
		return nil, errors.New("parsing spec: no paths")
	}
	s := &Spec{Version: doc.Info.Version, Operations: map[string]*Operation{}, Schemas: doc.Components.Schemas}
	for path, item := range doc.Paths {
		for key, raw := range item {
			method, ok := httpMethods[key]
			if !ok {
				continue
			}
			var op Operation
			if err := json.Unmarshal(raw, &op); err != nil {
				return nil, fmt.Errorf("parsing %s %s: %w", method, path, err)
			}
			if op.ID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", method, path)
			}
			if prev, dup := s.Operations[op.ID]; dup {
				return nil, fmt.Errorf("operationId %s is used by %s %s and %s %s", op.ID, prev.Method, prev.Path, method, path)
			}
			op.Method, op.Path = method, path
			s.Operations[op.ID] = &op
		}
	}
	return s, nil
}

// resolve follows $ref chains to a component schema. Unknown references
// resolve to nil.
func (s *Spec) resolve(sc *Schema) *Schema {
	for seen := 0; sc != nil && sc.Ref != ""; seen++ {
		if seen > 32 {
			return nil
		}
		sc = s.Schemas[strings.TrimPrefix(sc.Ref, "#/components/schemas/")]
	}
	return sc
}

// maxFieldDepth bounds how deep RequestFields descends into a body. JSON:API
// bodies put attributes at data.attributes.<name> and relationships at
// data.relationships.<name>.data.<id|type>.
const maxFieldDepth = 5

// RequestFields lists an operation's query parameters (prefixed with "?")
// and the leaf fields of its JSON request body as dotted paths, for example
// "data.attributes.name", sorted.
func (s *Spec) RequestFields(op *Operation) []string {
	set := map[string]bool{}
	for _, p := range op.Parameters {
		if p.In == "query" {
			set["?"+p.Name] = true
		}
	}
	if op.RequestBody != nil {
		if c, ok := op.RequestBody.Content["application/json"]; ok {
			s.collectFields(c.Schema, "", set, 0)
		}
	}
	return slices.Sorted(maps.Keys(set))
}

func (s *Spec) collectFields(sc *Schema, prefix string, set map[string]bool, depth int) {
	props := s.properties(sc)
	if len(props) == 0 || depth == maxFieldDepth {
		if prefix != "" {
			set[prefix] = true
		}
		return
	}
	for name, p := range props {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		s.collectFields(p, path, set, depth+1)
	}
}

// properties returns the object properties a schema allows, merged across
// references and unions.
func (s *Spec) properties(sc *Schema) map[string]*Schema {
	props := map[string]*Schema{}
	var walk func(sc *Schema, depth int)
	walk = func(sc *Schema, depth int) {
		sc = s.resolve(sc)
		if sc == nil || depth > 32 {
			return
		}
		for name, p := range sc.Properties {
			if _, ok := props[name]; !ok {
				props[name] = p
			}
		}
		for _, group := range [][]*Schema{sc.OneOf, sc.AnyOf, sc.AllOf} {
			for _, sub := range group {
				walk(sub, depth+1)
			}
		}
	}
	walk(sc, 0)
	return props
}
