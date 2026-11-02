package apispec

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// specZip is laid out like Apple's ZIP, with a macOS resource fork.
func specZip(t *testing.T) []byte {
	t.Helper()
	return zipOf(t, map[string]string{"__MACOSX/._openapi.oas.json": "fork", "openapi.oas.json": fixtureSpec})
}

func TestFetchSpec(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/spec.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(specZip(t))
	}))
	t.Cleanup(srv.Close)

	b, err := FetchSpec(t.Context(), srv.Client(), srv.URL+"/spec.zip")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(b, SHA256([]byte(fixtureSpec))); err != nil {
		t.Error(err)
	}
	if err := VerifySHA256(b, strings.Repeat("0", 64)); err == nil {
		t.Error("checksum mismatch was not reported")
	}

	if _, err := FetchSpec(t.Context(), srv.Client(), srv.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("got %v, want an HTTP 404 error", err)
	}
	if _, err := FetchSpec(t.Context(), srv.Client(), "://bad"); err == nil || !strings.Contains(err.Error(), "building request") {
		t.Errorf("got %v, want a request error", err)
	}
	srv.Close()
	if _, err := FetchSpec(t.Context(), srv.Client(), srv.URL+"/spec.zip"); err == nil {
		t.Error("request to a closed server succeeded")
	}
}

func TestFetchSpecTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxSpecSize+1))
	}))
	t.Cleanup(srv.Close)
	if _, err := FetchSpec(t.Context(), srv.Client(), srv.URL); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("got %v, want a size error", err)
	}
}

func TestUnzipErrors(t *testing.T) {
	if _, err := Unzip([]byte("not a zip")); err == nil || !strings.Contains(err.Error(), "reading spec ZIP") {
		t.Errorf("got %v, want a ZIP error", err)
	}
	// Only the top-level file counts.
	nested := zipOf(t, map[string]string{"spec/openapi.oas.json": fixtureSpec, "__MACOSX/._openapi.oas.json": "fork"})
	if _, err := Unzip(nested); err == nil || !strings.Contains(err.Error(), "has no openapi.oas.json") {
		t.Errorf("got %v, want a missing file error", err)
	}
	big := zipOf(t, map[string]string{"openapi.oas.json": strings.Repeat(" ", maxSpecSize+1)})
	if _, err := Unzip(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("got %v, want a size error", err)
	}
}
