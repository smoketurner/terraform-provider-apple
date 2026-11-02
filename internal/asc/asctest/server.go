// Package asctest is an in-memory fake of the App Store Connect API for
// tests. It implements the generated gen.ServerInterface, so every operation
// the client is generated for must be implemented here, with the request and
// response types of the spec.
//
// The fake encodes the documented behavior the provider relies on, citing the
// Apple documentation for each. There are no live tests, so it must stay
// faithful to that documentation.
package asctest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// The API key the fake accepts. Its private key is generated per server; see
// Server.PrivateKey.
const (
	IssuerID = "57246542-96fe-1a63-e053-0824d011072a"
	KeyID    = "2X9R4HXF34"
)

// SeedID is the team ID the fake assigns as the seed ID of new bundle IDs.
const SeedID = "ABCDE12345"

// Server is a fake App Store Connect API backed by in-memory state.
type Server struct {
	*httptest.Server

	// PrivateKey is the PEM-encoded .p8 key that signs accepted tokens.
	PrivateKey string
	key        *ecdsa.PrivateKey

	mu     sync.Mutex
	nextID int
	now    func() time.Time

	bundleIDs    map[string]*gen.BundleId
	capabilities map[string]*capability
	certificates map[string]*certificate
	devices      map[string]*gen.Device
	profiles     map[string]*profile
	merchantIDs  map[string]*gen.MerchantId
	passTypeIDs  map[string]*gen.PassTypeId
	apps         map[string]*gen.App
	// certLimits caps the active certificates of a type.
	certLimits map[gen.CertificateType]int

	failNext map[string][]int
	requests []string
}

// NewServer starts a fake App Store Connect API. Call Close when done.
func NewServer() *Server {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	s := &Server{
		PrivateKey:   string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		key:          key,
		nextID:       1,
		now:          time.Now,
		bundleIDs:    map[string]*gen.BundleId{},
		capabilities: map[string]*capability{},
		certificates: map[string]*certificate{},
		devices:      map[string]*gen.Device{},
		profiles:     map[string]*profile{},
		merchantIDs:  map[string]*gen.MerchantId{},
		passTypeIDs:  map[string]*gen.PassTypeId{},
		apps:         map[string]*gen.App{},
		certLimits:   defaultCertLimits(),
		failNext:     map[string][]int{},
	}
	handler := gen.HandlerWithOptions(s, gen.StdHTTPServerOptions{
		BaseRouter: http.NewServeMux(),
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "PARAMETER_ERROR.INVALID", "A parameter has an invalid value", err.Error(), "")
		},
	})
	s.Server = httptest.NewServer(s.middleware(handler))
	return s
}

// SetClock replaces the clock the fake uses for creation and expiration
// dates and token checks.
func (s *Server) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// FailNext makes the next requests to method and path (e.g. "POST
// /v1/certificates") fail with the given statuses, in order.
func (s *Server) FailNext(method, path string, statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := method + " " + path
	s.failNext[key] = append(s.failNext[key], statuses...)
}

// Requests returns the "METHOD /path" of every request received.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.requests = append(s.requests, key)
		var fail int
		if queued := s.failNext[key]; len(queued) > 0 {
			fail, s.failNext[key] = queued[0], queued[1:]
		}
		now := s.now()
		s.mu.Unlock()

		if err := s.checkToken(r.Header.Get("Authorization"), now); err != nil {
			// Apple's 401 for a missing or invalid token.
			writeError(w, http.StatusUnauthorized, "NOT_AUTHORIZED", "Authentication credentials are missing or invalid.", err.Error(), "")
			return
		}
		if fail != 0 {
			if fail == http.StatusTooManyRequests {
				w.Header().Set("X-Rate-Limit", "user-hour-lim:3600;user-hour-rem:0;")
				writeError(w, fail, "RATE_LIMIT_EXCEEDED", "The request rate limit has been reached.", "", "")
				return
			}
			writeError(w, fail, "UNEXPECTED_ERROR", "An unexpected error occurred.", "injected failure", "")
			return
		}
		w.Header().Set("X-Rate-Limit", "user-hour-lim:3600;user-hour-rem:3599;")
		next.ServeHTTP(w, r)
	})
}

// checkToken verifies a bearer token as described in "Generating Tokens for
// API Requests"
// (https://developer.apple.com/documentation/appstoreconnectapi/generating-tokens-for-api-requests):
// ES256 with the key's kid, aud appstoreconnect-v1, iss for a team key or
// sub "user" for an individual key, and a lifetime of at most 20 minutes.
func (s *Server) checkToken(header string, now time.Time) error {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return errors.New("no bearer token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("malformed token")
	}
	enc := base64.RawURLEncoding
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return errors.New("malformed signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&s.key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return errors.New("bad signature")
	}
	var head struct{ Alg, Kid, Typ string }
	var claims struct {
		Iss, Sub, Aud string
		Iat, Exp      int64
	}
	for i, out := range []any{&head, &claims} {
		b, err := enc.DecodeString(parts[i])
		if err != nil {
			return errors.New("malformed token")
		}
		if err := json.Unmarshal(b, out); err != nil {
			return errors.New("malformed token")
		}
	}
	switch {
	case head.Alg != "ES256" || head.Typ != "JWT" || head.Kid != KeyID:
		return fmt.Errorf("unexpected header %+v", head)
	case claims.Aud != "appstoreconnect-v1":
		return fmt.Errorf("unexpected audience %q", claims.Aud)
	case claims.Iss != IssuerID && (claims.Iss != "" || claims.Sub != "user"):
		return errors.New("unknown issuer")
	case claims.Exp-claims.Iat > 20*60:
		return errors.New("token lifetime is longer than 20 minutes")
	case now.Unix() >= claims.Exp || now.Unix() < claims.Iat-60:
		return errors.New("token is expired or not yet valid")
	}
	return nil
}

func (s *Server) newID(prefix string) string {
	id := fmt.Sprintf("%s%08d", prefix, s.nextID)
	s.nextID++
	return id
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an error document as described in "Interpreting and
// Handling Errors"
// (https://developer.apple.com/documentation/appstoreconnectapi/interpreting-and-handling-errors).
func writeError(w http.ResponseWriter, status int, code, title, detail, pointer string) {
	e := map[string]any{"status": strconv.Itoa(status), "code": code, "title": title, "detail": detail}
	if pointer != "" {
		e["source"] = map[string]string{"pointer": pointer}
	}
	writeJSON(w, status, map[string]any{"errors": []any{e}})
}

func notFound(w http.ResponseWriter, kind, id string) {
	writeError(w, http.StatusNotFound, "NOT_FOUND", "The specified resource does not exist",
		fmt.Sprintf("There is no resource of type '%s' with id '%s'", kind, id), "")
}

func conflict(w http.ResponseWriter, code, detail, pointer string) {
	writeError(w, http.StatusConflict, code, "The provided entity includes an attribute with an invalid value", detail, pointer)
}

// decode reads a JSON request body into v, writing a 422 on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ENTITY_UNPROCESSABLE", "The request entity is not valid JSON", err.Error(), "")
		return false
	}
	return true
}

// selfURL is the absolute URL of the request, for "links".
func selfURL(r *http.Request) string {
	return "http://" + r.Host + r.URL.RequestURI()
}

func docLinks(r *http.Request) gen.DocumentLinks {
	return gen.DocumentLinks{Self: selfURL(r)}
}

// Collections default to 20 items and allow up to 200 per page; relationship
// linkages allow up to 1000. The cursor is an opaque offset.
const (
	defaultLimit = 20
	maxLimit     = 200
)

// page returns one page of items, the links for the page and the paging
// metadata. It writes a 400 and returns ok false for an invalid limit or
// cursor.
func page[T any](w http.ResponseWriter, r *http.Request, items []T, limit *int, maxPage int) (out []T, links gen.PagedDocumentLinks, meta *gen.PagingInformation, ok bool) {
	n := defaultLimit
	if limit != nil {
		n = *limit
	}
	if n < 1 || n > maxPage {
		writeError(w, http.StatusBadRequest, "PARAMETER_ERROR.INVALID", "A parameter has an invalid value",
			fmt.Sprintf("limit must be between 1 and %d", maxPage), "")
		return nil, links, nil, false
	}
	offset := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err == nil {
			offset, err = strconv.Atoi(string(b))
		}
		if err != nil || offset < 0 || offset > len(items) {
			writeError(w, http.StatusBadRequest, "PARAMETER_ERROR.INVALID", "A parameter has an invalid value", "invalid cursor", "")
			return nil, links, nil, false
		}
	}
	end := min(offset+n, len(items))
	out = items[offset:end]
	links.Self = selfURL(r)
	meta = &gen.PagingInformation{}
	meta.Paging.Limit = n
	meta.Paging.Total = new(len(items))
	if end < len(items) {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
		q := r.URL.Query()
		q.Set("cursor", cursor)
		q.Set("limit", strconv.Itoa(n))
		next := url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawQuery: q.Encode()}
		links.Next = new(next.String())
		meta.Paging.NextCursor = new(cursor)
	}
	return out, links, meta, true
}

// sortedValues returns the map's values ordered by ID.
func sortedValues[T any](m map[string]T) []T {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]T, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

// matches reports whether value passes a filter[...] parameter, which is
// unset or lists acceptable values.
func matches[F ~string](filter *[]F, value string) bool {
	if filter == nil {
		return true
	}
	for _, f := range *filter {
		if string(f) == value {
			return true
		}
	}
	return false
}

// alloc is shorthand for asc.Alloc.
func alloc[T any](p **T) *T { return asc.Alloc(p) }

// push is shorthand for asc.Push.
func push[E any](s *[]E) *E { return asc.Push(s) }

func resourceLinks(r *http.Request, collection, id string) *gen.ResourceLinks {
	return &gen.ResourceLinks{Self: new("http://" + r.Host + "/v1/" + collection + "/" + id)}
}

// Client returns a client authorized with the fake's API key, for changing
// state outside Terraform in tests.
func (s *Server) Client() *asc.Client {
	key, err := asc.ParsePrivateKey(s.PrivateKey)
	if err != nil {
		panic(err)
	}
	c, err := asc.New(s.URL, asc.NewTokenSource(asc.Credentials{IssuerID: IssuerID, KeyID: KeyID, PrivateKey: key}), "test")
	if err != nil {
		panic(err)
	}
	return c
}
