package asc

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func op(srv, method string, offset, length int, headers ...string) gen.UploadOperation {
	o := gen.UploadOperation{Url: new(srv + "/part"), Offset: new(offset), Length: new(length)}
	if method != "" {
		o.Method = new(method)
	}
	var hs []gen.HttpHeader
	for i := 0; i+1 < len(headers); i += 2 {
		hs = append(hs, gen.HttpHeader{Name: new(headers[i]), Value: new(headers[i+1])})
	}
	if hs != nil {
		o.RequestHeaders = &hs
	}
	return o
}

func TestChecksum(t *testing.T) {
	if got := Checksum([]byte("hello")); got != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("Checksum = %s", got)
	}
}

func TestUploadSendsParts(t *testing.T) {
	c, rec, srv := testClient(t, reply(http.StatusOK, ""), reply(http.StatusCreated, ""))
	data := []byte("0123456789")
	ops := []gen.UploadOperation{
		op(srv.URL, "PUT", 0, 6, "Content-Type", "image/png"),
		op(srv.URL, "", 6, 4),
	}
	if err := c.Upload(context.Background(), ops, data); err != nil {
		t.Fatal(err)
	}
	if got := rec.bodies; len(got) != 2 || got[0] != "012345" || got[1] != "6789" {
		t.Errorf("bodies = %q", got)
	}
	first := rec.requests[0]
	if first.Method != http.MethodPut || first.Header.Get("Content-Type") != "image/png" {
		t.Errorf("first request = %s with Content-Type %q", first.Method, first.Header.Get("Content-Type"))
	}
	if rec.requests[1].Method != http.MethodPut {
		t.Errorf("default method = %s, want PUT", rec.requests[1].Method)
	}
	// The pre-signed URLs must not receive the API token.
	for _, r := range rec.requests {
		if r.Header.Get("Authorization") != "" {
			t.Error("upload request carried an Authorization header")
		}
	}
}

func TestUploadErrors(t *testing.T) {
	data := []byte("0123456789")
	tests := []struct {
		name string
		ops  func(srv string) []gen.UploadOperation
		resp []func(http.ResponseWriter, *http.Request)
		want string
	}{
		{"no operations", func(string) []gen.UploadOperation { return nil }, nil, "no upload operations were returned"},
		{"out of range", func(srv string) []gen.UploadOperation { return []gen.UploadOperation{op(srv, "", 8, 4)} }, nil, "covers bytes 8-12 of a 10-byte file"},
		{"negative offset", func(srv string) []gen.UploadOperation { return []gen.UploadOperation{op(srv, "", -1, 2)} }, nil, "covers bytes"},
		{
			"rejected part", func(srv string) []gen.UploadOperation { return []gen.UploadOperation{op(srv, "", 0, 10)} },
			[]func(http.ResponseWriter, *http.Request){reply(http.StatusForbidden, "signature expired")}, "HTTP 403: signature expired",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, srv := testClient(t, tt.resp...)
			err := c.Upload(context.Background(), tt.ops(srv.URL), data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Upload error = %v, want %q", err, tt.want)
			}
		})
	}
}
