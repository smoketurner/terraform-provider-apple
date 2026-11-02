package asc

import (
	"context"
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
	"net/http"
	"sync"
	"time"
)

// Token lifetimes, from "Generating Tokens for API Requests"
// (https://developer.apple.com/documentation/appstoreconnectapi/generating-tokens-for-api-requests):
// App Store Connect rejects most requests whose token has exp - iat greater
// than 20 minutes.
const (
	tokenLifetime = 19 * time.Minute
	// tokenBackdate sets iat slightly in the past, so a clock running ahead
	// of Apple's does not produce a token issued in the future.
	tokenBackdate = time.Minute
	// tokenRenewBefore re-signs a cached token this long before it expires,
	// so a token never expires in flight.
	tokenRenewBefore = 2 * time.Minute
	audience         = "appstoreconnect-v1"
)

// Credentials identify an App Store Connect API key. IssuerID is empty for an
// individual API key, which signs tokens with "sub": "user" instead of "iss".
type Credentials struct {
	IssuerID   string
	KeyID      string
	PrivateKey *ecdsa.PrivateKey
}

// ParsePrivateKey parses the contents of a .p8 file downloaded from App Store
// Connect: a PEM-encoded PKCS #8 P-256 private key.
func ParsePrivateKey(p8 string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(p8))
	if block == nil {
		return nil, errors.New("private key is not PEM-encoded; pass the contents of the .p8 file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("private key is not a P-256 (ES256) key")
	}
	return ec, nil
}

// TokenSource signs and caches the JWTs that authorize API requests.
type TokenSource struct {
	creds Credentials
	now   func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewTokenSource returns a TokenSource for the given credentials.
func NewTokenSource(creds Credentials) *TokenSource {
	return &TokenSource{creds: creds, now: time.Now}
}

// Token returns a signed token, reusing the cached one until it nears expiry.
func (ts *TokenSource) Token() (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := ts.now()
	if ts.token != "" && now.Before(ts.expires.Add(-tokenRenewBefore)) {
		return ts.token, nil
	}
	iat := now.Add(-tokenBackdate)
	exp := iat.Add(tokenLifetime)
	token, err := ts.sign(iat, exp)
	if err != nil {
		return "", err
	}
	ts.token, ts.expires = token, exp
	return token, nil
}

// Authorize sets the request's bearer token. Its signature matches the
// generated client's RequestEditorFn.
func (ts *TokenSource) Authorize(_ context.Context, req *http.Request) error {
	token, err := ts.Token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Iss string `json:"iss,omitempty"`
	Sub string `json:"sub,omitempty"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Aud string `json:"aud"`
}

func (ts *TokenSource) sign(iat, exp time.Time) (string, error) {
	claims := jwtClaims{Iat: iat.Unix(), Exp: exp.Unix(), Aud: audience}
	if ts.creds.IssuerID != "" {
		claims.Iss = ts.creds.IssuerID
	} else {
		claims.Sub = "user"
	}
	header, err := json.Marshal(jwtHeader{Alg: "ES256", Kid: ts.creds.KeyID, Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("encoding token header: %w", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encoding token claims: %w", err)
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, ts.creds.PrivateKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	// JWS (RFC 7518, section 3.4) encodes an ES256 signature as R and S, each
	// a 32-byte big-endian integer, rather than as ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + enc.EncodeToString(sig), nil
}
