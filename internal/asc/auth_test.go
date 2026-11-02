package asc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// decodeToken verifies a token's ES256 signature with pub and returns its
// header and claims.
func decodeToken(t *testing.T, token string, pub *ecdsa.PublicKey) (header, claims map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	enc := base64.RawURLEncoding
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature is %d bytes (%v), want 64", len(sig), err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("signature does not verify")
	}
	for i, out := range []*map[string]any{&header, &claims} {
		b, err := enc.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return header, claims
}

// claimTime returns a numeric date claim.
func claimTime(t *testing.T, claims map[string]any, name string) int64 {
	t.Helper()
	v, ok := claims[name].(float64)
	if !ok {
		t.Fatalf("claim %s = %v is not a number", name, claims[name])
	}
	return int64(v)
}

func TestParsePrivateKey(t *testing.T) {
	key, p8 := testKey(t)
	got, err := ParsePrivateKey(p8)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(key) {
		t.Error("parsed key differs")
	}
}

func TestParsePrivateKeyErrors(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384DER, err := x509.MarshalPKCS8PrivateKey(p384)
	if err != nil {
		t.Fatal(err)
	}
	pemOf := func(der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	}
	for name, tc := range map[string]struct{ p8, want string }{
		"empty":       {"", "not PEM-encoded"},
		"not PEM":     {"MIGTAgEAMBMGByqGSM49", "not PEM-encoded"},
		"not PKCS #8": {pemOf([]byte("garbage")), "parsing private key"},
		"RSA":         {pemOf(rsaDER), "not a P-256"},
		"P-384":       {pemOf(p384DER), "not a P-256"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePrivateKey(tc.p8); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestTokenTeamKey(t *testing.T) {
	key, _ := testKey(t)
	now := time.Unix(1_800_000_000, 0)
	ts := NewTokenSource(Credentials{IssuerID: "57246542-96fe-1a63-e053-0824d011072a", KeyID: "2X9R4HXF34", PrivateKey: key})
	ts.now = func() time.Time { return now }
	token, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	header, claims := decodeToken(t, token, &key.PublicKey)
	if header["alg"] != "ES256" || header["kid"] != "2X9R4HXF34" || header["typ"] != "JWT" || len(header) != 3 {
		t.Errorf("header = %v", header)
	}
	iat, exp := claimTime(t, claims, "iat"), claimTime(t, claims, "exp")
	if claims["iss"] != "57246542-96fe-1a63-e053-0824d011072a" || claims["aud"] != "appstoreconnect-v1" {
		t.Errorf("claims = %v", claims)
	}
	if _, ok := claims["sub"]; ok {
		t.Errorf("team key token has sub: %v", claims)
	}
	if exp-iat > 20*60 {
		t.Errorf("lifetime is %v seconds; Apple rejects more than 20 minutes", exp-iat)
	}
	if iat > now.Unix() || exp <= now.Unix() {
		t.Errorf("token is not valid now: iat %v, exp %v, now %v", iat, exp, now.Unix())
	}
}

func TestTokenIndividualKey(t *testing.T) {
	key, _ := testKey(t)
	ts := NewTokenSource(Credentials{KeyID: "K", PrivateKey: key})
	token, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	_, claims := decodeToken(t, token, &key.PublicKey)
	if claims["sub"] != "user" {
		t.Errorf("sub = %v, want user", claims["sub"])
	}
	if _, ok := claims["iss"]; ok {
		t.Errorf("individual key token has iss: %v", claims)
	}
}

func TestTokenCachingAndRenewal(t *testing.T) {
	key, _ := testKey(t)
	now := time.Unix(1_800_000_000, 0)
	ts := NewTokenSource(Credentials{IssuerID: "i", KeyID: "k", PrivateKey: key})
	ts.now = func() time.Time { return now }
	first, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	_, claims := decodeToken(t, first, &key.PublicKey)
	exp := time.Unix(claimTime(t, claims, "exp"), 0)

	now = exp.Add(-tokenRenewBefore - time.Second)
	if again, _ := ts.Token(); again != first {
		t.Error("token was re-signed before it neared expiry")
	}
	now = exp.Add(-tokenRenewBefore)
	renewed, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if renewed == first {
		t.Fatal("token was not re-signed near expiry")
	}
	_, claims = decodeToken(t, renewed, &key.PublicKey)
	if claimTime(t, claims, "exp") <= now.Unix() {
		t.Error("renewed token is already expired")
	}
}

func TestAuthorize(t *testing.T) {
	key, _ := testKey(t)
	ts := NewTokenSource(Credentials{IssuerID: "i", KeyID: "k", PrivateKey: key})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Authorize(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok {
		t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
	}
	decodeToken(t, token, &key.PublicKey)
}
