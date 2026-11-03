package asc

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // App Store Connect identifies uploaded files by their MD5 checksum.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Uploading a file to App Store Connect takes three steps
// (https://developer.apple.com/documentation/appstoreconnectapi/uploading-assets-to-app-store-connect):
// reserve it with its name and size, which returns upload operations; send
// each part of the file as an operation says; then commit it with the file's
// MD5 checksum. This file implements the middle step.

// Checksum returns the MD5 checksum App Store Connect expects for data, as
// lowercase hex.
func Checksum(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // See the import.
	return hex.EncodeToString(sum[:])
}

// Upload sends data as ops describe: each operation sends the part of data
// at its offset and length to its URL, with its method and headers. The
// URLs are pre-signed, so the requests carry no API token.
func (c *Client) Upload(ctx context.Context, ops []gen.UploadOperation, data []byte) error {
	if len(ops) == 0 {
		return errors.New("no upload operations were returned")
	}
	for i, op := range ops {
		offset, length := Value(op.Offset), Value(op.Length)
		if offset < 0 || length < 0 || offset+length > len(data) {
			return fmt.Errorf("upload operation %d covers bytes %d-%d of a %d-byte file", i, offset, offset+length, len(data))
		}
		method := Value(op.Method)
		if method == "" {
			method = http.MethodPut
		}
		req, err := http.NewRequestWithContext(ctx, method, Value(op.Url), bytes.NewReader(data[offset:offset+length]))
		if err != nil {
			return fmt.Errorf("upload operation %d: %w", i, err)
		}
		req.ContentLength = int64(length)
		for _, h := range Value(op.RequestHeaders) {
			req.Header.Set(Value(h.Name), Value(h.Value))
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("upload operation %d: %w", i, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("upload operation %d: HTTP %d: %s", i, resp.StatusCode, bytes.TrimSpace(body))
		}
	}
	return nil
}

// DeliveryOperations converts the upload operations of delivery files, such
// as background asset archives, for Upload. The files are uploaded the same
// way; their operations number their parts, which Upload doesn't need.
func DeliveryOperations(ops []gen.DeliveryFileUploadOperation) []gen.UploadOperation {
	out := make([]gen.UploadOperation, len(ops))
	for i, op := range ops {
		out[i] = gen.UploadOperation{Method: op.Method, Url: op.Url, RequestHeaders: op.RequestHeaders}
		if op.Offset != nil {
			out[i].Offset = new(int(*op.Offset))
		}
		if op.Length != nil {
			out[i].Length = new(int(*op.Length))
		}
	}
	return out
}
