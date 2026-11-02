package asctest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func newServer(t *testing.T) (*Server, *asc.Client) {
	t.Helper()
	s := NewServer()
	t.Cleanup(s.Close)
	return s, s.Client()
}

// must fails the test unless the request succeeded.
func must(t *testing.T, body []byte, httpResp *http.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if err := asc.Check(httpResp, body); err != nil {
		t.Fatal(err)
	}
}

func csr(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func createBundleID(t *testing.T, c *asc.Client, identifier string) string {
	t.Helper()
	var body gen.BundleIdCreateRequest
	body.Data.Type = gen.BundleIdCreateRequestDataTypeBundleIds
	body.Data.Attributes.Identifier, body.Data.Attributes.Name = identifier, "App"
	body.Data.Attributes.Platform = gen.BundleIdPlatformIOS
	resp, err := c.BundleIdsCreateInstanceWithResponse(t.Context(), body)
	must(t, resp.Body, resp.HTTPResponse, err)
	return resp.JSON201.Data.Id
}

func createCertificate(t *testing.T, c *asc.Client, typ gen.CertificateType) (*gen.CertificatesCreateInstanceResponse, error) {
	t.Helper()
	var body gen.CertificateCreateRequest
	body.Data.Type = gen.CertificateCreateRequestDataTypeCertificates
	body.Data.Attributes.CertificateType = typ
	body.Data.Attributes.CsrContent = csr(t, "Example")
	return c.CertificatesCreateInstanceWithResponse(t.Context(), body)
}

func createDevice(t *testing.T, c *asc.Client, udid string) (*gen.DevicesCreateInstanceResponse, error) {
	t.Helper()
	var body gen.DeviceCreateRequest
	body.Data.Type = gen.DeviceCreateRequestDataTypeDevices
	body.Data.Attributes.Name, body.Data.Attributes.Udid = "Phone", udid
	body.Data.Attributes.Platform = gen.BundleIdPlatformIOS
	return c.DevicesCreateInstanceWithResponse(t.Context(), body)
}

func createProfile(t *testing.T, c *asc.Client, bundleID, certID, deviceID string) string {
	t.Helper()
	var body gen.ProfileCreateRequest
	body.Data.Type = gen.ProfileCreateRequestDataTypeProfiles
	body.Data.Attributes.Name = "Dev"
	body.Data.Attributes.ProfileType = gen.ProfileCreateRequestDataAttributesProfileTypeIOSAPPDEVELOPMENT
	body.Data.Relationships.BundleId.Data.Id = bundleID
	body.Data.Relationships.BundleId.Data.Type = "bundleIds"
	e := push(&body.Data.Relationships.Certificates.Data)
	e.Id, e.Type = certID, "certificates"
	if deviceID != "" {
		devices := alloc(&body.Data.Relationships.Devices)
		d := asc.Append(&devices.Data)
		d.Id, d.Type = deviceID, "devices"
	}
	resp, err := c.ProfilesCreateInstanceWithResponse(t.Context(), body)
	must(t, resp.Body, resp.HTTPResponse, err)
	return resp.JSON201.Data.Id
}

func profileState(t *testing.T, c *asc.Client, id string) gen.ProfileAttributesProfileState {
	t.Helper()
	resp, err := c.ProfilesGetInstanceWithResponse(t.Context(), id, nil)
	must(t, resp.Body, resp.HTTPResponse, err)
	return *resp.JSON200.Data.Attributes.ProfileState
}

func TestAuthentication(t *testing.T) {
	s, _ := newServer(t)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, creds := range map[string]asc.Credentials{
		"wrong key":    {IssuerID: IssuerID, KeyID: KeyID, PrivateKey: other},
		"wrong key ID": {IssuerID: IssuerID, KeyID: "OTHER", PrivateKey: s.key},
		"wrong issuer": {IssuerID: "someone-else", KeyID: KeyID, PrivateKey: s.key},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := asc.New(s.URL, asc.NewTokenSource(creds), "test")
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.BundleIdsGetCollectionWithResponse(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := asc.Check(resp.HTTPResponse, resp.Body); err == nil || !strings.Contains(err.Error(), "NOT_AUTHORIZED") {
				t.Errorf("got %v, want NOT_AUTHORIZED", err)
			}
		})
	}

	individual, err := asc.New(s.URL, asc.NewTokenSource(asc.Credentials{KeyID: KeyID, PrivateKey: s.key}), "test")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := individual.BundleIdsGetCollectionWithResponse(t.Context(), nil)
	must(t, resp.Body, resp.HTTPResponse, err)

	for name, header := range map[string]string{
		"no token":  "",
		"malformed": "Bearer abc",
		"bad sig":   "Bearer a.b.c",
	} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL+"/v1/bundleIds", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, r.StatusCode)
		}
	}
}

func TestExpiredToken(t *testing.T) {
	s, c := newServer(t)
	s.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	resp, err := c.BundleIdsGetCollectionWithResponse(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode())
	}
}

func TestFailNext(t *testing.T) {
	s, c := newServer(t)
	s.FailNext(http.MethodGet, "/v1/bundleIds", http.StatusTooManyRequests, http.StatusInternalServerError)
	resp, err := c.BundleIdsGetCollectionWithResponse(t.Context(), nil)
	must(t, resp.Body, resp.HTTPResponse, err)
	got := s.Requests()
	if len(got) != 3 {
		t.Errorf("requests = %v, want 2 failures and a retry", got)
	}
}

func TestPagination(t *testing.T) {
	_, c := newServer(t)
	for i := range 5 {
		createBundleID(t, c, fmt.Sprintf("com.example.app%d", i))
	}
	resp, err := c.BundleIdsGetCollectionWithResponse(t.Context(), &gen.BundleIdsGetCollectionParams{Limit: new(2)})
	must(t, resp.Body, resp.HTTPResponse, err)
	all, err := asc.All(t.Context(), c, resp.JSON200,
		func(p *gen.BundleIdsResponse) []gen.BundleId { return p.Data },
		func(p *gen.BundleIdsResponse) *string { return p.Links.Next })
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 || *all[4].Attributes.Identifier != "com.example.app4" {
		t.Errorf("got %d bundle IDs", len(all))
	}
	bad, err := c.BundleIdsGetCollectionWithResponse(t.Context(), &gen.BundleIdsGetCollectionParams{Limit: new(201)})
	if err != nil {
		t.Fatal(err)
	}
	if bad.StatusCode() != http.StatusBadRequest {
		t.Errorf("limit 201: status %d", bad.StatusCode())
	}
}

func TestFilters(t *testing.T) {
	_, c := newServer(t)
	createBundleID(t, c, "com.example.a")
	createBundleID(t, c, "com.example.b")
	resp, err := c.BundleIdsGetCollectionWithResponse(t.Context(), &gen.BundleIdsGetCollectionParams{FilterIdentifier: &[]string{"com.example.b"}})
	must(t, resp.Body, resp.HTTPResponse, err)
	if len(resp.JSON200.Data) != 1 || *resp.JSON200.Data[0].Attributes.Identifier != "com.example.b" {
		t.Errorf("filtered = %s", resp.Body)
	}
}

func TestDuplicates(t *testing.T) {
	_, c := newServer(t)
	createBundleID(t, c, "com.example.a")
	var body gen.BundleIdCreateRequest
	body.Data.Attributes.Identifier, body.Data.Attributes.Name, body.Data.Attributes.Platform = "com.example.a", "x", gen.BundleIdPlatformIOS
	dup, err := c.BundleIdsCreateInstanceWithResponse(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	if err := asc.Check(dup.HTTPResponse, dup.Body); !asc.IsConflict(err) {
		t.Errorf("duplicate bundle ID: %v", err)
	}

	d, err := createDevice(t, c, "00008030-0001")
	must(t, d.Body, d.HTTPResponse, err)
	d, err = createDevice(t, c, "00008030-0001")
	if err != nil {
		t.Fatal(err)
	}
	if err := asc.Check(d.HTTPResponse, d.Body); !asc.IsConflict(err) || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate device: %v", err)
	}
}

func TestDeviceLimit(t *testing.T) {
	_, c := newServer(t)
	for i := range DeviceLimit {
		d, err := createDevice(t, c, fmt.Sprintf("udid-%03d", i))
		must(t, d.Body, d.HTTPResponse, err)
	}
	d, err := createDevice(t, c, "one-too-many")
	if err != nil {
		t.Fatal(err)
	}
	if err := asc.Check(d.HTTPResponse, d.Body); !asc.IsConflict(err) || !strings.Contains(err.Error(), "maximum number of IPHONE") {
		t.Errorf("over the limit: %v", err)
	}
}

func TestCertificateLimit(t *testing.T) {
	s, c := newServer(t)
	first, err := createCertificate(t, c, gen.CertificateTypeIOSDISTRIBUTION)
	must(t, first.Body, first.HTTPResponse, err)
	content := *first.JSON201.Data.Attributes.CertificateContent
	if _, err := x509.ParseCertificate(mustBase64(t, content)); err != nil {
		t.Errorf("certificateContent is not a DER certificate: %v", err)
	}
	second, err := createCertificate(t, c, gen.CertificateTypeIOSDISTRIBUTION)
	if err != nil {
		t.Fatal(err)
	}
	if err := asc.Check(second.HTTPResponse, second.Body); !asc.IsConflict(err) || !strings.Contains(err.Error(), "already have a current") {
		t.Errorf("second distribution certificate: %v", err)
	}
	s.SetCertificateLimit(gen.CertificateTypeIOSDISTRIBUTION, 2)
	second, err = createCertificate(t, c, gen.CertificateTypeIOSDISTRIBUTION)
	must(t, second.Body, second.HTTPResponse, err)
	s.SetCertificateLimit(gen.CertificateTypeIOSDISTRIBUTION, 0)

	// Development certificates are not capped.
	for range 3 {
		dev, err := createCertificate(t, c, gen.CertificateTypeDEVELOPMENT)
		must(t, dev.Body, dev.HTTPResponse, err)
	}
}

func TestProfileInvalidation(t *testing.T) {
	ctx := context.Background()
	_, c := newServer(t)
	bundle := createBundleID(t, c, "com.example.app")
	cert, err := createCertificate(t, c, gen.CertificateTypeDEVELOPMENT)
	must(t, cert.Body, cert.HTTPResponse, err)
	device, err := createDevice(t, c, "udid-1")
	must(t, device.Body, device.HTTPResponse, err)
	certID, deviceID := cert.JSON201.Data.Id, device.JSON201.Data.Id

	// Revoking a certificate.
	p := createProfile(t, c, bundle, certID, "")
	if got := profileState(t, c, p); got != gen.ProfileAttributesProfileStateACTIVE {
		t.Fatalf("new profile is %s", got)
	}
	del, err := c.CertificatesDeleteInstanceWithResponse(ctx, certID)
	must(t, del.Body, del.HTTPResponse, err)
	if got := profileState(t, c, p); got != gen.ProfileAttributesProfileStateINVALID {
		t.Errorf("after revoking its certificate, profile is %s", got)
	}

	// Disabling a device.
	cert, err = createCertificate(t, c, gen.CertificateTypeDEVELOPMENT)
	must(t, cert.Body, cert.HTTPResponse, err)
	p = createProfile(t, c, bundle, cert.JSON201.Data.Id, deviceID)
	var update gen.DeviceUpdateRequest
	update.Data.Id, update.Data.Type = deviceID, gen.DeviceUpdateRequestDataTypeDevices
	alloc(&update.Data.Attributes).Status = new(gen.DeviceUpdateRequestDataAttributesStatusDISABLED)
	upd, err := c.DevicesUpdateInstanceWithResponse(ctx, deviceID, update)
	must(t, upd.Body, upd.HTTPResponse, err)
	if got := profileState(t, c, p); got != gen.ProfileAttributesProfileStateINVALID {
		t.Errorf("after disabling its device, profile is %s", got)
	}

	// Changing the bundle ID's capabilities.
	p = createProfile(t, c, bundle, cert.JSON201.Data.Id, "")
	var capReq gen.BundleIdCapabilityCreateRequest
	capReq.Data.Type = gen.BundleIdCapabilityCreateRequestDataTypeBundleIdCapabilities
	capReq.Data.Attributes.CapabilityType = gen.CapabilityTypePUSHNOTIFICATIONS
	capReq.Data.Relationships.BundleId.Data.Id = bundle
	capReq.Data.Relationships.BundleId.Data.Type = "bundleIds"
	cr, err := c.BundleIdCapabilitiesCreateInstanceWithResponse(ctx, capReq)
	must(t, cr.Body, cr.HTTPResponse, err)
	if got := profileState(t, c, p); got != gen.ProfileAttributesProfileStateINVALID {
		t.Errorf("after enabling a capability, profile is %s", got)
	}
}

func TestProfileExpiry(t *testing.T) {
	s, c := newServer(t)
	bundle := createBundleID(t, c, "com.example.app")
	cert, err := createCertificate(t, c, gen.CertificateTypeDEVELOPMENT)
	must(t, cert.Body, cert.HTTPResponse, err)
	p := createProfile(t, c, bundle, cert.JSON201.Data.Id, "")
	// Moving the clock also expires the client's tokens, so render the
	// profile directly.
	s.SetClock(func() time.Time { return time.Now().Add(400 * 24 * time.Hour) })
	s.mu.Lock()
	got := s.renderProfile(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/profiles/"+p, nil), s.profiles[p])
	s.mu.Unlock()
	if *got.Attributes.ProfileState != gen.ProfileAttributesProfileStateINVALID {
		t.Errorf("expired profile is %s", *got.Attributes.ProfileState)
	}
}

func mustBase64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
