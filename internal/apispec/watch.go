package apispec

import (
	"fmt"
	"slices"
	"strings"
)

// Finding is a spec change that needs an issue. Title identifies the change,
// so the watcher workflow updates an existing issue instead of opening a
// duplicate.
type Finding struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Watch compares the latest spec with the pinned spec and the manifest. It
// reports operations the manifest does not map, and request fields or query
// parameters added since the pinned spec to operations the provider covers or
// is building.
func Watch(pinned, latest *Spec, m *Manifest) []Finding {
	source := fmt.Sprintf("App Store Connect API spec %s (%s)", latest.Version, SpecURL)
	var out []Finding
	for id, op := range latest.Operations {
		if _, ok := m.Operations[id]; ok {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Apple's OpenAPI spec has a new operation, `%s`:\n\n", id)
		fmt.Fprintf(&b, "- `%s %s`\n", op.Method, op.Path)
		if op.Summary != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", op.Summary)
		}
		if len(op.Tags) > 0 {
			fmt.Fprintf(&b, "- Tags: %s\n", strings.Join(backticked(op.Tags), ", "))
		}
		fmt.Fprintf(&b, "\nSource: %s\n\n", source)
		b.WriteString("Map it in `coverage.yaml` as `covered`, `in_progress`, `planned` or `out_of_scope`.\n")
		out = append(out, Finding{Title: "API spec: new operation " + id, Body: b.String()})
	}

	for _, id := range m.Generated() {
		before, ok1 := pinned.Operations[id]
		after, ok2 := latest.Operations[id]
		if !ok1 || !ok2 {
			continue
		}
		known := pinned.RequestFields(before)
		for _, field := range latest.RequestFields(after) {
			if slices.Contains(known, field) {
				continue
			}
			kind, name := "request field", field
			if q, ok := strings.CutPrefix(field, "?"); ok {
				kind, name = "query parameter", q
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Apple's OpenAPI spec added the %s `%s` to `%s` (`%s %s`).\n\n", kind, name, id, after.Method, after.Path)
			if by := m.Operations[id].By; len(by) > 0 {
				fmt.Fprintf(&b, "Used by: %s\n\n", strings.Join(backticked(by), ", "))
			} else {
				fmt.Fprintf(&b, "In progress under #%d.\n\n", m.Operations[id].Issue)
			}
			fmt.Fprintf(&b, "Source: %s\n\n", source)
			b.WriteString("Check Apple's documentation, then support the field or record why not. The watcher reports it until the vendored spec in `api/openapi.oas.json` includes it.\n")
			out = append(out, Finding{Title: fmt.Sprintf("API spec: new %s %s on %s", kind, name, id), Body: b.String()})
		}
	}
	slices.SortFunc(out, func(a, b Finding) int { return strings.Compare(a.Title, b.Title) })
	return out
}

func backticked(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "`" + v + "`"
	}
	return out
}
