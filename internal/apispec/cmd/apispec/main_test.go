package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const spec = `{"info": {"version": "9.9"}, "paths": {"/v1/apps": {"get": {"operationId": "apps_getCollection"}}}}`

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no command":       {nil, "usage"},
		"unknown command":  {[]string{"nope"}, `unknown command "nope"`},
		"fetch without o":  {[]string{"fetch"}, "-o is required"},
		"fetch bad flag":   {[]string{"fetch", "-bogus"}, "flag provided but not defined"},
		"watch no flags":   {[]string{"watch"}, "are required"},
		"watch bad flag":   {[]string{"watch", "-bogus"}, "flag provided but not defined"},
		"watch no spec":    {[]string{"watch", "-pinned", missing, "-latest", missing}, "reading spec"},
		"gencfg without o": {[]string{"gencfg"}, "-o is required"},
		"gencfg bad flag":  {[]string{"gencfg", "-bogus"}, "flag provided but not defined"},
		"gencfg no file":   {[]string{"gencfg", "-o", "x", "-manifest", missing}, "reading manifest"},
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			err := run(t.Context(), tc.args, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestFetch(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("openapi.oas.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(spec)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad.zip" {
			_, _ = w.Write([]byte("not a zip"))
			return
		}
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)

	out := filepath.Join(t.TempDir(), "spec.json")
	var stdout strings.Builder
	if err := fetch(t.Context(), []string{"-o", out}, &stdout, srv.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout.String(), "version=9.9\nsha256=") {
		t.Errorf("output = %q", stdout.String())
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != spec {
		t.Errorf("wrote %q, %v", got, err)
	}

	if err := fetch(t.Context(), []string{"-o", out}, &stdout, srv.URL+"/bad.zip"); err == nil {
		t.Error("fetching a bad ZIP succeeded")
	}
	if err := fetch(t.Context(), []string{"-o", filepath.Join(t.TempDir(), "missing", "spec.json")}, &stdout, srv.URL); err == nil {
		t.Error("writing to a missing directory succeeded")
	}
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	pinned := write(t, dir, "pinned.json", spec)
	latest := write(t, dir, "latest.json", strings.Replace(spec, `}}}}`, `}}, "/v1/users": {"get": {"operationId": "users_getCollection"}}}}`, 1))
	manifest := write(t, dir, "coverage.yaml", "operations:\n  apps_getCollection: {status: planned, issue: 1}\n")

	var out strings.Builder
	if err := run(t.Context(), []string{"watch", "-pinned", pinned, "-latest", latest, "-manifest", manifest}, &out); err != nil {
		t.Fatal(err)
	}
	var findings []struct{ Title string }
	if err := json.Unmarshal([]byte(out.String()), &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Title != "API spec: new operation users_getCollection" {
		t.Errorf("findings = %+v", findings)
	}

	out.Reset()
	if err := run(t.Context(), []string{"watch", "-pinned", pinned, "-latest", pinned, "-manifest", manifest}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("output with no changes = %q, want []", out.String())
	}

	if err := run(t.Context(), []string{"watch", "-pinned", pinned, "-latest", latest, "-manifest", filepath.Join(dir, "missing.yaml")}, &out); err == nil {
		t.Error("watch with a missing manifest succeeded")
	}
	if err := run(t.Context(), []string{"watch", "-pinned", pinned, "-latest", filepath.Join(dir, "missing.json"), "-manifest", manifest}, &out); err == nil {
		t.Error("watch with a missing latest spec succeeded")
	}
}

func TestGencfg(t *testing.T) {
	dir := t.TempDir()
	manifest := write(t, dir, "coverage.yaml", "operations:\n  apps_getCollection: {status: in_progress, issue: 1}\n")
	out := filepath.Join(dir, "config.yaml")
	if err := run(t.Context(), []string{"gencfg", "-manifest", manifest, "-o", out}, nil); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(out); err != nil || !strings.Contains(string(b), "    - apps_getCollection\n") {
		t.Errorf("wrote %q, %v", b, err)
	}

	empty := write(t, dir, "empty.yaml", "operations:\n  apps_getCollection: {status: planned, issue: 1}\n")
	if err := run(t.Context(), []string{"gencfg", "-manifest", empty, "-o", out}, nil); err == nil {
		t.Error("gencfg with no generated operations succeeded")
	}
	if err := run(t.Context(), []string{"gencfg", "-manifest", manifest, "-o", filepath.Join(dir, "missing", "c.yaml")}, nil); err == nil {
		t.Error("writing to a missing directory succeeded")
	}
}
