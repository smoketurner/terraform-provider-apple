package asc

import (
	"errors"
	"net/http"
	"reflect"
)

// Err returns err when the request failed, and otherwise the API error in a
// generated response, which carries the raw response as HTTPResponse and
// Body. It replaces the two-step "if err == nil { err = Check(...) }" at
// every call site.
func Err(resp any, err error) error {
	if err != nil {
		return err
	}
	v := reflect.ValueOf(resp)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return errors.New("asc.Err: not a generated response")
	}
	hf, bf := v.Elem().FieldByName("HTTPResponse"), v.Elem().FieldByName("Body")
	if !hf.IsValid() || !bf.IsValid() {
		return errors.New("asc.Err: not a generated response")
	}
	httpResp, ok1 := hf.Interface().(*http.Response)
	body, ok2 := bf.Interface().([]byte)
	if !ok1 || !ok2 || httpResp == nil {
		return errors.New("asc.Err: not a generated response")
	}
	return Check(httpResp, body)
}
