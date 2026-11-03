// Package asc is the App Store Connect API client. The operations and models
// in package gen are generated from Apple's OpenAPI spec; this package adds
// authentication, retries, error decoding and pagination.
package asc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// DefaultBaseURL is the App Store Connect API endpoint used when none is
// configured.
const DefaultBaseURL = "https://api.appstoreconnect.apple.com"

// Client is the generated App Store Connect client, plus what it needs to
// follow pagination links.
type Client struct {
	*gen.ClientWithResponses
	base    *url.URL
	http    *http.Client
	editors []gen.RequestEditorFn
}

// New returns a client for the API at baseURL (DefaultBaseURL when empty)
// that authorizes requests with tokens from ts.
func New(baseURL string, ts *TokenSource, version string) (*Client, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", baseURL)
	}
	userAgent := "terraform-provider-apple/" + version
	c := &Client{
		base: base,
		http: &http.Client{Timeout: 2 * time.Minute, Transport: newRetryTransport(http.DefaultTransport)},
		editors: []gen.RequestEditorFn{
			ts.Authorize,
			func(_ context.Context, req *http.Request) error {
				req.Header.Set("User-Agent", userAgent)
				return nil
			},
		},
	}
	opts := []gen.ClientOption{gen.WithHTTPClient(c.http)}
	for _, e := range c.editors {
		opts = append(opts, gen.WithRequestEditorFn(e))
	}
	c.ClientWithResponses, err = gen.NewClientWithResponses(base.String(), opts...)
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	return c, nil
}

// getJSON sends an authorized GET to a URL from a response's links and
// decodes the response into out. The URL must be on the client's API host,
// so a link can never send the token elsewhere.
func (c *Client) getJSON(ctx context.Context, link string, out any) error {
	u, err := url.Parse(link)
	if err != nil {
		return fmt.Errorf("parsing link %q: %w", link, err)
	}
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
		return fmt.Errorf("link %q is not on %s", link, c.base.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	for _, e := range c.editors {
		if err := e(ctx, req); err != nil {
			return err
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", u.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("GET %s: %w", u.Path, err)
	}
	if err := Check(resp, body); err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: decoding response: %w", u.Path, err)
	}
	return nil
}

// Follow fetches a pagination link from a response into out, for
// collections whose pages carry more than their data, such as included
// objects. All covers the common case.
func (c *Client) Follow(ctx context.Context, link string, out any) error {
	return c.getJSON(ctx, link, out)
}

// All returns the data of every page of a collection. first is the decoded
// first page, data returns a page's items and next its links.next URL (nil
// on the last page).
func All[R, T any](ctx context.Context, c *Client, first *R, data func(*R) []T, next func(*R) *string) ([]T, error) {
	var out []T
	page := first
	for {
		out = append(out, data(page)...)
		link := next(page)
		if link == nil || *link == "" {
			return out, nil
		}
		page = new(R)
		if err := c.getJSON(ctx, *link, page); err != nil {
			return nil, err
		}
	}
}

// Error is an error response from App Store Connect. See "Interpreting and
// Handling Errors"
// (https://developer.apple.com/documentation/appstoreconnectapi/interpreting-and-handling-errors).
type Error struct {
	Method     string
	Path       string
	StatusCode int
	// RateLimit is the X-Rate-Limit header, e.g.
	// "user-hour-lim:3600;user-hour-rem:0;".
	RateLimit string
	Errors    []ErrorDetail `json:"errors"`
}

// ErrorDetail is one entry of an error response.
type ErrorDetail struct {
	Status string `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Source *struct {
		Pointer   string `json:"pointer"`
		Parameter string `json:"parameter"`
	} `json:"source"`
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	for _, d := range e.Errors {
		fmt.Fprintf(&b, "\n%s: %s", d.Code, d.Title)
		if d.Detail != "" && d.Detail != d.Title {
			fmt.Fprintf(&b, ": %s", d.Detail)
		}
		if d.Source != nil {
			if d.Source.Pointer != "" {
				fmt.Fprintf(&b, " (at %s)", d.Source.Pointer)
			}
			if d.Source.Parameter != "" {
				fmt.Fprintf(&b, " (parameter %s)", d.Source.Parameter)
			}
		}
	}
	if e.StatusCode == http.StatusTooManyRequests && e.RateLimit != "" {
		fmt.Fprintf(&b, "\nRate limit: %s", e.RateLimit)
	}
	return b.String()
}

// Check returns an *Error for a non-2xx response with its body, and nil
// otherwise. Generated responses carry both as HTTPResponse and Body.
func Check(resp *http.Response, body []byte) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	e := &Error{StatusCode: resp.StatusCode, RateLimit: resp.Header.Get("X-Rate-Limit")}
	if resp.Request != nil {
		e.Method, e.Path = resp.Request.Method, resp.Request.URL.Path
	}
	// A body that is not an error document still yields the status code.
	_ = json.Unmarshal(body, e)
	return e
}

// IsNotFound reports whether err is a 404 from App Store Connect.
func IsNotFound(err error) bool {
	return hasStatus(err, http.StatusNotFound)
}

// IsConflict reports whether err is a 409 from App Store Connect, which it
// returns for invalid or conflicting entities such as a certificate over its
// type's limit.
func IsConflict(err error) bool {
	return hasStatus(err, http.StatusConflict)
}

// HasCode reports whether err is an App Store Connect error with code, e.g.
// "STATE_ERROR".
func HasCode(err error, code string) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	for _, d := range e.Errors {
		if d.Code == code {
			return true
		}
	}
	return false
}

func hasStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == status
}

// Retry policy. App Store Connect's rate limit is a rolling hour (see
// "Identifying Rate Limits",
// https://developer.apple.com/documentation/appstoreconnectapi/identifying-rate-limits)
// and a 429 carries no Retry-After, so a few short retries cover bursts and
// anything longer fails with the rate limit in the error.
const (
	maxAttempts  = 4
	firstBackoff = time.Second
	maxBackoff   = 8 * time.Second
)

// retryTransport retries 429 responses, which Apple rejects before acting on
// them, and 5xx responses and network errors for idempotent methods. A POST
// is never retried after a 5xx, which could create a second certificate or
// device.
type retryTransport struct {
	base  http.RoundTripper
	sleep func(context.Context, time.Duration) error
}

func newRetryTransport(base http.RoundTripper) *retryTransport {
	return &retryTransport{base: base, sleep: sleepContext}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	backoff := firstBackoff
	for attempt := 1; ; attempt++ {
		attemptReq := req
		if attempt > 1 && req.Body != nil {
			if req.GetBody == nil {
				return nil, errors.New("cannot retry a request whose body cannot be re-read")
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("re-reading request body: %w", err)
			}
			attemptReq = req.Clone(ctx)
			attemptReq.Body = body
		}
		resp, err := t.base.RoundTrip(attemptReq)
		if resp != nil {
			tflog.Debug(ctx, "App Store Connect API response", map[string]any{
				"method": req.Method, "path": req.URL.Path, "status": resp.StatusCode,
				"rate_limit": resp.Header.Get("X-Rate-Limit"), "attempt": attempt,
			})
		}
		if attempt == maxAttempts || !retryable(req.Method, resp, err) {
			return resp, err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		if err := t.sleep(ctx, backoff); err != nil {
			return nil, err
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

func retryable(method string, resp *http.Response, err error) bool {
	idempotent := method != http.MethodPost
	if err != nil {
		return idempotent
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true
	case resp.StatusCode >= 500:
		return idempotent
	}
	return false
}
