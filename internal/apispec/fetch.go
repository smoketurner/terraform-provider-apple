package apispec

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// SpecURL is where Apple publishes the latest spec. The ZIP is not versioned,
// so the pinned spec is vendored rather than downloaded.
const SpecURL = "https://developer.apple.com/sample-code/app-store-connect/app-store-connect-openapi-specification.zip"

// specFile is the name of the spec inside the ZIP.
const specFile = "openapi.oas.json"

// maxSpecSize bounds both the download and the unzipped spec. Spec 4.5.1 is
// 7.5 MB uncompressed.
const maxSpecSize = 64 << 20

// FetchSpec downloads the spec ZIP from url and returns the unzipped spec.
func FetchSpec(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSpecSize+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if len(b) > maxSpecSize {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, maxSpecSize)
	}
	return Unzip(b)
}

// Unzip returns the spec file at the top level of a spec ZIP. Other entries,
// such as the macOS resource fork __MACOSX/._openapi.oas.json, are ignored.
func Unzip(b []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("reading spec ZIP: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != specFile {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening %s: %w", f.Name, err)
		}
		defer func() { _ = rc.Close() }()
		spec, err := io.ReadAll(io.LimitReader(rc, maxSpecSize+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.Name, err)
		}
		if len(spec) > maxSpecSize {
			return nil, fmt.Errorf("%s is larger than %d bytes", f.Name, maxSpecSize)
		}
		return spec, nil
	}
	return nil, errors.New("spec ZIP has no " + specFile)
}

// VerifySHA256 checks b against a hex SHA-256 checksum.
func VerifySHA256(b []byte, want string) error {
	if got := SHA256(b); got != want {
		return fmt.Errorf("spec checksum is %s, want %s", got, want)
	}
	return nil
}

// SHA256 returns the hex SHA-256 checksum of b.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
