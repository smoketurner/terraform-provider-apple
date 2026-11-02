package apispec

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixtureSpec is a small JSON:API spec in the shape of Apple's.
const fixtureSpec = `{
  "info": {"version": "1.0.0"},
  "paths": {
    "/v1/bundleIds": {
      "get": {"operationId": "bundleIds_getCollection", "tags": ["BundleIds"], "summary": "List bundle IDs",
        "parameters": [{"name": "filter[identifier]", "in": "query"}, {"name": "limit", "in": "query"}]},
      "post": {"operationId": "bundleIds_createInstance", "tags": ["BundleIds"], "requestBody": {"content": {
        "application/json": {"schema": {"$ref": "#/components/schemas/BundleIdCreateRequest"}}}}}
    },
    "/v1/bundleIds/{id}": {
      "parameters": [{"name": "id", "in": "path"}],
      "get": {"operationId": "bundleIds_getInstance", "tags": ["BundleIds"]},
      "patch": {"operationId": "bundleIds_updateInstance", "tags": ["BundleIds"], "requestBody": {"content": {
        "application/json": {"schema": {"$ref": "#/components/schemas/BundleIdUpdateRequest"}}}}},
      "delete": {"operationId": "bundleIds_deleteInstance", "tags": ["BundleIds"]}
    },
    "/v1/apps/{id}/appInfos": {"get": {"operationId": "apps_appInfos_getToManyRelated", "tags": ["Apps"]}},
    "/v1/salesReports": {"get": {"operationId": "salesReports_getCollection", "tags": ["SalesReports"],
      "requestBody": {"content": {"text/plain": {"schema": {"type": "string"}}}}}}
  },
  "components": {"schemas": {
    "BundleIdCreateRequest": {"type": "object", "properties": {"data": {"type": "object", "properties": {
      "type": {"type": "string", "enum": ["bundleIds"]},
      "attributes": {"$ref": "#/components/schemas/BundleIdAttributes"},
      "relationships": {"type": "object", "properties": {"app": {"type": "object", "properties": {
        "data": {"type": "object", "properties": {"id": {"type": "string"}, "type": {"type": "string"}}}}}}}
    }}}},
    "BundleIdAttributes": {"allOf": [
      {"type": "object", "properties": {"name": {"type": "string"}}},
      {"oneOf": [{"type": "object", "properties": {"identifier": {"type": "string"}}}]},
      {"anyOf": [{"$ref": "#/components/schemas/Platform"}]}
    ]},
    "Platform": {"type": "object", "properties": {"platform": {"type": "string"}}},
    "BundleIdUpdateRequest": {"type": "object", "properties": {"data": {"type": "object", "properties": {
      "id": {"type": "string"},
      "attributes": {"type": "object", "properties": {"name": {"type": "string"}, "deep": {"$ref": "#/components/schemas/Deep"}}}
    }}}},
    "Deep": {"type": "object", "properties": {"a": {"type": "object", "properties": {"b": {"type": "object", "properties": {"c": {"type": "string"}}}}}}},
    "Loop": {"$ref": "#/components/schemas/Loop"}
  }}
}`

func fixture(t *testing.T) *Spec {
	t.Helper()
	s, err := ParseSpec([]byte(fixtureSpec))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseSpec(t *testing.T) {
	s := fixture(t)
	if s.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", s.Version)
	}
	if len(s.Operations) != 7 {
		t.Fatalf("got %d operations, want 7", len(s.Operations))
	}
	op := s.Operations["bundleIds_updateInstance"]
	if op.Method != "PATCH" || op.Path != "/v1/bundleIds/{id}" || !slices.Equal(op.Tags, []string{"BundleIds"}) {
		t.Errorf("bundleIds_updateInstance = %s %s %v", op.Method, op.Path, op.Tags)
	}
}

func TestParseSpecErrors(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"invalid JSON":    {`{`, "parsing spec"},
		"no paths":        {`{"paths": {}}`, "no paths"},
		"bad operation":   {`{"paths": {"/a": {"get": []}}}`, "parsing GET /a"},
		"no operationId":  {`{"paths": {"/a": {"get": {}}}}`, "GET /a has no operationId"},
		"duplicate id":    {`{"paths": {"/a": {"get": {"operationId": "x"}}, "/b": {"get": {"operationId": "x"}}}}`, "operationId x is used by"},
		"bad schema type": {`{"paths": {"/a": {"get": {"operationId": "x"}}}, "components": {"schemas": []}}`, "parsing spec"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSpec([]byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	if _, err := LoadSpec(path); err == nil || !strings.Contains(err.Error(), "reading spec") {
		t.Errorf("got %v, want a reading error", err)
	}
	if err := os.WriteFile(path, []byte(fixtureSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(path); err != nil {
		t.Error(err)
	}
}

func TestRequestFields(t *testing.T) {
	s := fixture(t)
	for id, want := range map[string][]string{
		"bundleIds_getCollection": {"?filter[identifier]", "?limit"},
		"bundleIds_createInstance": {
			"data.attributes.identifier", "data.attributes.name", "data.attributes.platform",
			"data.relationships.app.data.id", "data.relationships.app.data.type", "data.type",
		},
		// Fields deeper than maxFieldDepth are reported at that depth.
		"bundleIds_updateInstance": {"data.attributes.deep.a.b", "data.attributes.name", "data.id"},
		"bundleIds_deleteInstance": {},
		// Only JSON bodies are read.
		"salesReports_getCollection": {},
	} {
		if got := s.RequestFields(s.Operations[id]); !slices.Equal(got, want) {
			t.Errorf("RequestFields(%s) = %v, want %v", id, got, want)
		}
	}
}

func TestResolveLoop(t *testing.T) {
	s := fixture(t)
	if got := s.properties(s.Schemas["Loop"]); len(got) != 0 {
		t.Errorf("properties(Loop) = %v, want none", got)
	}
	if got := s.properties(&Schema{Ref: "#/components/schemas/Missing"}); len(got) != 0 {
		t.Errorf("properties(Missing) = %v, want none", got)
	}
}
