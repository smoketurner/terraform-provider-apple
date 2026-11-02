package apispec

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fixtureManifest = `operations:
  apps_appInfos_getToManyRelated: {status: planned, issue: 5}
  bundleIds_createInstance: {status: covered, by: [apple_bundle_id]}
  bundleIds_deleteInstance: {status: covered, by: [apple_bundle_id]}
  bundleIds_getCollection: {status: in_progress, issue: 2}
  bundleIds_getInstance: {status: covered, by: [apple_bundle_id, data.apple_bundle_id]}
  bundleIds_updateInstance: {status: covered, by: [apple_bundle_id]}
  salesReports_getCollection: {status: out_of_scope, reason: "test"}
`

func fixtureManifestParsed(t *testing.T) *Manifest {
	t.Helper()
	m, err := ParseManifest([]byte(fixtureManifest))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestCheck(t *testing.T) {
	if err := fixtureManifestParsed(t).Check(fixture(t)); err != nil {
		t.Error(err)
	}
}

func TestManifestCoveredAndGenerated(t *testing.T) {
	m := fixtureManifestParsed(t)
	covered := []string{"bundleIds_createInstance", "bundleIds_deleteInstance", "bundleIds_getInstance", "bundleIds_updateInstance"}
	if got := m.Covered(); !slices.Equal(got, covered) {
		t.Errorf("Covered() = %v, want %v", got, covered)
	}
	generated := slices.Sorted(slices.Values(append(slices.Clone(covered), "bundleIds_getCollection")))
	if got := m.Generated(); !slices.Equal(got, generated) {
		t.Errorf("Generated() = %v, want %v", got, generated)
	}
}

func TestManifestCheckErrors(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, want string }{
		"unmapped operation": {
			"  salesReports_getCollection: {status: out_of_scope, reason: \"test\"}\n", "",
			"salesReports_getCollection (GET /v1/salesReports) is not in the manifest",
		},
		"operation not in spec": {
			"  salesReports_getCollection:", "  salesReports_x: {status: planned, issue: 1}\n  salesReports_getCollection:",
			"salesReports_x is in the manifest but not in the spec",
		},
		"covered without by":        {"{status: covered, by: [apple_bundle_id]}", "{status: covered}", "need by"},
		"covered with issue":        {"by: [apple_bundle_id]}", "by: [apple_bundle_id], issue: 3}", "take only by"},
		"planned without issue":     {"{status: planned, issue: 5}", "{status: planned}", "planned operations need issue"},
		"planned with reason":       {"issue: 5}", "issue: 5, reason: x}", "planned operations take only issue"},
		"in_progress without issue": {"{status: in_progress, issue: 2}", "{status: in_progress}", "in_progress operations need issue"},
		"in_progress with by":       {"issue: 2}", "issue: 2, by: [x]}", "in_progress operations take only issue"},
		"out_of_scope w/o reason":   {`reason: "test"`, `issue: 1`, "need reason"},
		"out_of_scope with by":      {`reason: "test"}`, `reason: "test", by: [x]}`, "take only reason"},
		"unknown status":            {"{status: planned, issue: 5}", "{status: done}", `unknown status "done"`},
		"operations out of order":   {"  apps_appInfos_getToManyRelated:", "  zzz_last: {status: planned, issue: 1}\n  apps_appInfos_getToManyRelated:", "sorted"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := strings.Replace(fixtureManifest, tc.old, tc.new, 1)
			if doc == fixtureManifest {
				t.Fatal("replacement did not apply")
			}
			m, err := ParseManifest([]byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			err = m.Check(fixture(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestParseManifestErrors(t *testing.T) {
	for name, doc := range map[string]string{
		"invalid YAML":  "operations: [",
		"unknown field": "operations:\n  x: {status: covered, by: [x], note: y}\n",
		"wrong type":    "operations:\n  x: {status: planned, issue: soon}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(doc)); err == nil {
				t.Error("parsing succeeded")
			}
		})
	}
}

func TestLoadManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.yaml")
	if _, err := LoadManifest(path); err == nil {
		t.Error("loading a missing file succeeded")
	}
	if err := os.WriteFile(path, []byte(fixtureManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err != nil {
		t.Error(err)
	}
}

func TestGeneratorConfig(t *testing.T) {
	b, err := GeneratorConfig(fixtureManifestParsed(t))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, s := range []string{
		"DO NOT EDIT", "std-http-server: true", "path: overlay.yaml",
		"    - bundleIds_createInstance\n", "    - bundleIds_getCollection\n",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("config does not contain %q:\n%s", s, got)
		}
	}
	for _, s := range []string{"apps_appInfos_getToManyRelated", "salesReports_getCollection"} {
		if strings.Contains(got, s) {
			t.Errorf("config includes %s, which is not generated", s)
		}
	}
}

func TestGeneratorConfigEmpty(t *testing.T) {
	m, err := ParseManifest([]byte("operations:\n  x: {status: planned, issue: 1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GeneratorConfig(m); err == nil || !strings.Contains(err.Error(), "no operation") {
		t.Errorf("got %v, want an error for an empty operation list", err)
	}
}
