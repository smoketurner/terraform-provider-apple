package asc

import (
	"errors"
	"net/http"
	"testing"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func TestErr(t *testing.T) {
	sent := errors.New("network")
	if got := Err((*gen.BundleIdsGetInstanceResponse)(nil), sent); !errors.Is(got, sent) {
		t.Errorf("Err with a request error = %v", got)
	}
	ok := &gen.BundleIdsGetInstanceResponse{HTTPResponse: &http.Response{StatusCode: http.StatusOK}}
	if err := Err(ok, nil); err != nil {
		t.Errorf("Err(200) = %v", err)
	}
	missing := &gen.BundleIdsGetInstanceResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusNotFound},
		Body:         []byte(`{"errors": [{"code": "NOT_FOUND"}]}`),
	}
	if err := Err(missing, nil); !IsNotFound(err) {
		t.Errorf("Err(404) = %v", err)
	}
	for _, bad := range []any{nil, 1, &struct{}{}, (*gen.BundleIdsGetInstanceResponse)(nil), &gen.BundleIdsGetInstanceResponse{}} {
		if err := Err(bad, nil); err == nil {
			t.Errorf("Err(%#v) = nil", bad)
		}
	}
}
