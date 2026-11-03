package asc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// recorder is a test API that answers each request with the next queued
// response and records what it received.
type recorder struct {
	mu        sync.Mutex
	responses []func(w http.ResponseWriter, r *http.Request)
	requests  []*http.Request
	bodies    []string
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	rec.requests = append(rec.requests, r)
	rec.bodies = append(rec.bodies, string(body))
	var respond func(http.ResponseWriter, *http.Request)
	if len(rec.responses) > 0 {
		respond, rec.responses = rec.responses[0], rec.responses[1:]
	}
	rec.mu.Unlock()
	if respond == nil {
		http.Error(w, "no response queued", http.StatusTeapot)
		return
	}
	respond(w, r)
}

func reply(status int, body string, headers ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// testClient returns a client for a recorder whose retries do not sleep.
func testClient(t *testing.T, responses ...func(http.ResponseWriter, *http.Request)) (*Client, *recorder, *httptest.Server) {
	t.Helper()
	rec := &recorder{responses: responses}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	key, _ := testKey(t)
	c, err := New(srv.URL, NewTokenSource(Credentials{IssuerID: "i", KeyID: "k", PrivateKey: key}), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := c.http.Transport.(*retryTransport)
	if !ok {
		t.Fatalf("transport is %T", c.http.Transport)
	}
	transport.sleep = func(context.Context, time.Duration) error { return nil }
	return c, rec, srv
}

const emptyPage = `{"data": [], "links": {"self": "x"}}`

func TestRequestHeadersAndQuery(t *testing.T) {
	c, rec, _ := testClient(t, reply(http.StatusOK, emptyPage))
	platforms := []gen.BundleIdsGetCollectionParamsFilterPlatform{"IOS", "MAC_OS"}
	resp, err := c.BundleIdsGetCollectionWithResponse(t.Context(), &gen.BundleIdsGetCollectionParams{FilterPlatform: &platforms})
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(resp.HTTPResponse, resp.Body); err != nil {
		t.Fatal(err)
	}
	r := rec.requests[0]
	if r.URL.Path != "/v1/bundleIds" || r.URL.Query().Get("filter[platform]") != "IOS,MAC_OS" {
		t.Errorf("request = %s", r.URL)
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("User-Agent") != "terraform-provider-apple/1.2.3" {
		t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
	}
	if resp.JSON200 == nil {
		t.Error("response was not decoded")
	}
}

func TestNewErrors(t *testing.T) {
	key, _ := testKey(t)
	ts := NewTokenSource(Credentials{KeyID: "k", PrivateKey: key})
	for _, u := range []string{"api.example.com", "://x", "https://"} {
		if _, err := New(u, ts, "v"); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
	c, err := New("", ts, "v")
	if err != nil {
		t.Fatal(err)
	}
	if c.base.String() != DefaultBaseURL {
		t.Errorf("base = %s, want %s", c.base, DefaultBaseURL)
	}
}

func createBundleID(t *testing.T, c *Client) (*gen.BundleIdsCreateInstanceResponse, error) {
	t.Helper()
	var body gen.BundleIdCreateRequest
	body.Data.Type = "bundleIds"
	body.Data.Attributes.Identifier = "com.example.app"
	body.Data.Attributes.Name = "Example"
	body.Data.Attributes.Platform = gen.BundleIdPlatformIOS
	return c.BundleIdsCreateInstanceWithResponse(t.Context(), body)
}

func TestRetry429ResendsBody(t *testing.T) {
	limited := reply(http.StatusTooManyRequests, `{"errors": [{"status": "429", "code": "RATE_LIMIT_EXCEEDED", "title": "The request rate limit has been reached."}]}`)
	c, rec, _ := testClient(t, limited, limited, reply(http.StatusCreated, `{"data": {"id": "B1", "type": "bundleIds"}, "links": {"self": "x"}}`))
	resp, err := createBundleID(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil || resp.JSON201.Data.Id != "B1" {
		t.Fatalf("response = %d %s", resp.StatusCode(), resp.Body)
	}
	if len(rec.bodies) != 3 {
		t.Fatalf("sent %d requests, want 3", len(rec.bodies))
	}
	if rec.bodies[0] == "" || rec.bodies[2] != rec.bodies[0] {
		t.Errorf("retried body %q differs from %q", rec.bodies[2], rec.bodies[0])
	}
}

func TestRetry429GivesUp(t *testing.T) {
	limited := reply(http.StatusTooManyRequests,
		`{"errors": [{"status": "429", "code": "RATE_LIMIT_EXCEEDED", "title": "The request rate limit has been reached."}]}`,
		"X-Rate-Limit", "user-hour-lim:3600;user-hour-rem:0;")
	responses := make([]func(http.ResponseWriter, *http.Request), maxAttempts+1)
	for i := range responses {
		responses[i] = limited
	}
	c, rec, _ := testClient(t, responses...)
	resp, err := c.BundleIdsGetInstanceWithResponse(t.Context(), "B1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.requests) != maxAttempts {
		t.Errorf("sent %d requests, want %d", len(rec.requests), maxAttempts)
	}
	err = Check(resp.HTTPResponse, resp.Body)
	for _, s := range []string{"GET /v1/bundleIds/B1: HTTP 429", "RATE_LIMIT_EXCEEDED", "Rate limit: user-hour-lim:3600;user-hour-rem:0;"} {
		if err == nil || !strings.Contains(err.Error(), s) {
			t.Errorf("error %v does not contain %q", err, s)
		}
	}
}

func TestRetry5xxOnlyWhenIdempotent(t *testing.T) {
	failed := reply(http.StatusInternalServerError, `{"errors": [{"status": "500", "code": "UNEXPECTED_ERROR", "title": "An unexpected error occurred."}]}`)
	c, rec, _ := testClient(t, failed, reply(http.StatusOK, `{"data": {"id": "B1", "type": "bundleIds"}, "links": {"self": "x"}}`))
	resp, err := c.BundleIdsGetInstanceWithResponse(t.Context(), "B1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != http.StatusOK || len(rec.requests) != 2 {
		t.Errorf("GET: status %d after %d requests, want 200 after 2", resp.StatusCode(), len(rec.requests))
	}

	c, rec, _ = testClient(t, failed, failed)
	created, err := createBundleID(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if created.StatusCode() != http.StatusInternalServerError || len(rec.requests) != 1 {
		t.Errorf("POST: status %d after %d requests, want 500 after 1", created.StatusCode(), len(rec.requests))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// roundTrip sends req through rt and closes any response body.
func roundTrip(rt http.RoundTripper, req *http.Request) error {
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

func TestRetryNetworkErrors(t *testing.T) {
	calls := 0
	failing := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection reset")
	})
	rt := &retryTransport{base: failing, sleep: func(context.Context, time.Duration) error { return nil }}
	for method, want := range map[string]int{http.MethodGet: maxAttempts, http.MethodPost: 1} {
		calls = 0
		req, err := http.NewRequestWithContext(t.Context(), method, "https://example.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := roundTrip(rt, req); err == nil {
			t.Errorf("%s succeeded", method)
		}
		if calls != want {
			t.Errorf("%s: %d attempts, want %d", method, calls, want)
		}
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	limited := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	rt := &retryTransport{base: limited, sleep: sleepContext}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := roundTrip(rt, req); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestRetryUnrereadableBody(t *testing.T) {
	limited := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	rt := &retryTransport{base: limited, sleep: func(context.Context, time.Duration) error { return nil }}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com", io.NopCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatal(err)
	}
	if err := roundTrip(rt, req); err == nil || !strings.Contains(err.Error(), "cannot retry") {
		t.Errorf("got %v, want a cannot retry error", err)
	}
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("gone") }
	if err := roundTrip(rt, req); err == nil || !strings.Contains(err.Error(), "re-reading request body") {
		t.Errorf("got %v, want a re-reading error", err)
	}
}

func TestSleepContext(t *testing.T) {
	if err := sleepContext(t.Context(), time.Millisecond); err != nil {
		t.Error(err)
	}
}

func TestCheck(t *testing.T) {
	c, _, _ := testClient(t,
		reply(http.StatusConflict, `{"errors": [{"id": "e1", "status": "409", "code": "ENTITY_ERROR.ATTRIBUTE.INVALID",
			"title": "An attribute value is invalid.", "detail": "The CSR is invalid.",
			"source": {"pointer": "/data/attributes/csrContent"}}]}`),
		reply(http.StatusNotFound, `{"errors": [{"status": "404", "code": "NOT_FOUND", "title": "The specified resource does not exist.",
			"detail": "There is no resource of type 'bundleIds' with id 'X'", "source": {"parameter": "id"}}]}`),
		reply(http.StatusBadGateway, `<html>bad gateway</html>`),
	)

	resp, err := createBundleID(t, c)
	if err != nil {
		t.Fatal(err)
	}
	err = Check(resp.HTTPResponse, resp.Body)
	if !IsConflict(err) || IsNotFound(err) {
		t.Errorf("IsConflict = %v, IsNotFound = %v for %v", IsConflict(err), IsNotFound(err), err)
	}
	if !HasCode(err, "ENTITY_ERROR.ATTRIBUTE.INVALID") || HasCode(err, "STATE_ERROR") || HasCode(errors.New("plain"), "STATE_ERROR") {
		t.Errorf("HasCode is wrong for %v", err)
	}
	want := "POST /v1/bundleIds: HTTP 409\nENTITY_ERROR.ATTRIBUTE.INVALID: An attribute value is invalid.: The CSR is invalid. (at /data/attributes/csrContent)"
	if err == nil || err.Error() != want {
		t.Errorf("error =\n%v\nwant\n%s", err, want)
	}

	got, err := c.BundleIdsGetInstanceWithResponse(t.Context(), "X", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = Check(got.HTTPResponse, got.Body)
	if !IsNotFound(err) || !strings.Contains(err.Error(), "(parameter id)") {
		t.Errorf("not found error = %v", err)
	}

	// A 5xx response is retried; the queue then runs dry with a 418, whose
	// non-JSON body still yields the status.
	got, err = c.BundleIdsGetInstanceWithResponse(t.Context(), "X", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(got.HTTPResponse, got.Body); err == nil || !strings.Contains(err.Error(), "HTTP 418") {
		t.Errorf("error = %v, want HTTP 418", err)
	}

	if IsNotFound(errors.New("other")) || IsNotFound(nil) {
		t.Error("IsNotFound matched a non-API error")
	}
	if err := Check(&http.Response{StatusCode: http.StatusNoContent}, nil); err != nil {
		t.Errorf("Check(204) = %v", err)
	}
}

func bundleIDsPage(next, ids string) string {
	link := ""
	if next != "" {
		link = `, "next": "` + next + `"`
	}
	return `{"data": [` + ids + `], "links": {"self": "x"` + link + `}}`
}

func listBundleIDs(t *testing.T, c *Client) ([]string, error) {
	t.Helper()
	resp, err := c.BundleIdsGetCollectionWithResponse(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(resp.HTTPResponse, resp.Body); err != nil {
		t.Fatal(err)
	}
	items, err := All(t.Context(), c, resp.JSON200,
		func(p *gen.BundleIdsResponse) []gen.BundleId { return p.Data },
		func(p *gen.BundleIdsResponse) *string { return p.Links.Next })
	var ids []string
	for _, b := range items {
		ids = append(ids, b.Id)
	}
	return ids, err
}

func TestAll(t *testing.T) {
	var srvURL string
	page := func(next, ids string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			n := next
			if n != "" {
				n = srvURL + n
			}
			reply(http.StatusOK, bundleIDsPage(n, ids))(w, r)
		}
	}
	c, rec, srv := testClient(t,
		page("/v1/bundleIds?cursor=Mg&limit=2", `{"id": "a", "type": "bundleIds"}, {"id": "b", "type": "bundleIds"}`),
		page("/v1/bundleIds?cursor=NA&limit=2", `{"id": "c", "type": "bundleIds"}`),
		page("", `{"id": "d", "type": "bundleIds"}`),
	)
	srvURL = srv.URL
	ids, err := listBundleIDs(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "a,b,c,d" {
		t.Errorf("ids = %v", ids)
	}
	if got := rec.requests[2].URL.RawQuery; got != "cursor=NA&limit=2" {
		t.Errorf("third request query = %q", got)
	}
	if !strings.HasPrefix(rec.requests[2].Header.Get("Authorization"), "Bearer ") {
		t.Error("next-page request is not authorized")
	}
}

func TestAllErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		next   string
		second func(http.ResponseWriter, *http.Request)
		want   string
	}{
		"foreign host": {"https://evil.example.com/v1/bundleIds?cursor=x", nil, "is not on"},
		"bad link":     {"%zz", nil, "parsing link"},
		"error page":   {"/v1/bundleIds?cursor=x", reply(http.StatusForbidden, `{"errors": [{"code": "FORBIDDEN_ERROR"}]}`), "FORBIDDEN_ERROR"},
		"undecodable":  {"/v1/bundleIds?cursor=x", reply(http.StatusOK, `{"data": 1}`), "decoding response"},
		"unreadable body": {"/v1/bundleIds?cursor=x", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusOK)
		}, "GET /v1/bundleIds"},
	} {
		t.Run(name, func(t *testing.T) {
			var srvURL string
			first := func(w http.ResponseWriter, r *http.Request) {
				next := tc.next
				if strings.HasPrefix(next, "/") {
					next = srvURL + next
				}
				reply(http.StatusOK, bundleIDsPage(next, `{"id": "a", "type": "bundleIds"}`))(w, r)
			}
			c, rec, srv := testClient(t, first, tc.second)
			srvURL = srv.URL
			if _, err := listBundleIDs(t, c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
			if tc.second == nil && len(rec.requests) != 1 {
				t.Errorf("followed a rejected link: %d requests", len(rec.requests))
			}
		})
	}
}
