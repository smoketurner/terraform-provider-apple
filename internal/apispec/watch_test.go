package apispec

import (
	"strings"
	"testing"
)

func TestWatch(t *testing.T) {
	pinned := fixture(t)
	latestDoc := strings.NewReplacer(
		// A new operation.
		`"/v1/salesReports": {`, `"/v1/devices": {"get": {"operationId": "devices_getCollection", "tags": ["Devices"], "summary": "List devices"}}, "/v1/salesReports": {`,
		// A new query parameter on an in_progress operation.
		`{"name": "limit", "in": "query"}`, `{"name": "limit", "in": "query"}, {"name": "filter[seedId]", "in": "query"}`,
		// A new body field on a covered operation.
		`"properties": {"name": {"type": "string"}, "deep"`, `"properties": {"name": {"type": "string"}, "seedId": {"type": "string"}, "deep"`,
		// A new field on a planned operation is not reported.
		`"/v1/apps/{id}/appInfos": {"get": {"operationId": "apps_appInfos_getToManyRelated"`, `"/v1/apps/{id}/appInfos": {"get": {"parameters": [{"name": "limit", "in": "query"}], "operationId": "apps_appInfos_getToManyRelated"`,
	).Replace(fixtureSpec)
	latest, err := ParseSpec([]byte(latestDoc))
	if err != nil {
		t.Fatal(err)
	}

	findings := Watch(pinned, latest, fixtureManifestParsed(t))
	var titles []string
	for _, f := range findings {
		titles = append(titles, f.Title)
	}
	want := []string{
		"API spec: new operation devices_getCollection",
		"API spec: new query parameter filter[seedId] on bundleIds_getCollection",
		"API spec: new request field data.attributes.seedId on bundleIds_updateInstance",
	}
	if strings.Join(titles, "\n") != strings.Join(want, "\n") {
		t.Fatalf("titles =\n%s\nwant\n%s", strings.Join(titles, "\n"), strings.Join(want, "\n"))
	}

	for _, s := range []string{"`GET /v1/devices`", "Summary: List devices", "Tags: `Devices`", "spec 1.0.0"} {
		if !strings.Contains(findings[0].Body, s) {
			t.Errorf("new operation body does not contain %q:\n%s", s, findings[0].Body)
		}
	}
	if !strings.Contains(findings[1].Body, "In progress under #2.") {
		t.Errorf("new parameter body does not name the issue:\n%s", findings[1].Body)
	}
	if !strings.Contains(findings[2].Body, "Used by: `apple_bundle_id`") {
		t.Errorf("new field body does not name the resources:\n%s", findings[2].Body)
	}
}

func TestWatchNoChanges(t *testing.T) {
	s := fixture(t)
	if got := Watch(s, s, fixtureManifestParsed(t)); len(got) != 0 {
		t.Errorf("Watch() = %v, want no findings", got)
	}
}
