package asc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// These tests pin down how the generated client handles the JSON:API shapes
// the provider relies on, using documents shaped like the examples in
// Apple's documentation.

const profileDocument = `{
  "data": {
    "type": "profiles", "id": "P1",
    "attributes": {
      "name": "Example Dev", "platform": "IOS", "profileType": "IOS_APP_DEVELOPMENT",
      "profileState": "INVALID", "profileContent": "TUlJ", "uuid": "u-1",
      "createdDate": "2026-01-02T03:04:05.000+0000", "expirationDate": "2027-01-02T03:04:05.000+0000"
    },
    "relationships": {
      "bundleId": {"data": {"type": "bundleIds", "id": "B1"}, "links": {"self": "https://api.appstoreconnect.apple.com/v1/profiles/P1/relationships/bundleId"}},
      "certificates": {"data": [{"type": "certificates", "id": "C1"}, {"type": "certificates", "id": "C2"}], "meta": {"paging": {"total": 2, "limit": 10}}},
      "devices": {"links": {"related": "https://api.appstoreconnect.apple.com/v1/profiles/P1/devices"}}
    },
    "links": {"self": "https://api.appstoreconnect.apple.com/v1/profiles/P1"}
  },
  "included": [
    {"type": "bundleIds", "id": "B1", "attributes": {"identifier": "com.example.app", "platform": "IOS", "name": "Example"}},
    {"type": "certificates", "id": "C1", "attributes": {"certificateType": "IOS_DEVELOPMENT", "serialNumber": "5A1B", "activated": true}},
    {"type": "devices", "id": "D1", "attributes": {"udid": "00008030-000", "status": "ENABLED", "deviceClass": "IPHONE"}}
  ],
  "links": {"self": "https://api.appstoreconnect.apple.com/v1/profiles/P1"}
}`

func TestDecodeProfileWithIncluded(t *testing.T) {
	var doc gen.ProfileResponse
	if err := json.Unmarshal([]byte(profileDocument), &doc); err != nil {
		t.Fatal(err)
	}
	attrs := doc.Data.Attributes
	if *attrs.ProfileState != gen.ProfileAttributesProfileStateINVALID || *attrs.Platform != gen.BundleIdPlatformIOS {
		t.Errorf("attributes = %+v", attrs)
	}
	if attrs.ExpirationDate == nil || attrs.ExpirationDate.Year() != 2027 {
		t.Errorf("expirationDate = %v; Apple's +0000 offsets must parse", attrs.ExpirationDate)
	}

	rel := doc.Data.Relationships
	if rel.BundleId.Data.Id != "B1" || rel.BundleId.Links == nil {
		t.Errorf("to-one relationship = %+v", rel.BundleId)
	}
	if certs := *rel.Certificates.Data; len(certs) != 2 || certs[1].Id != "C2" || rel.Certificates.Meta.Paging.Total == nil {
		t.Errorf("to-many relationship = %+v", rel.Certificates)
	}
	if rel.Devices.Data != nil {
		t.Errorf("relationship without data decoded data %+v", rel.Devices.Data)
	}

	got := map[string]string{}
	for _, item := range *doc.Included {
		kind, err := item.Discriminator()
		if err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "bundleIds":
			b, err := item.AsBundleId()
			if err != nil {
				t.Fatal(err)
			}
			got[kind] = *b.Attributes.Identifier
		case "certificates":
			c, err := item.AsCertificate()
			if err != nil {
				t.Fatal(err)
			}
			if *c.Attributes.CertificateType != gen.CertificateTypeIOSDEVELOPMENT {
				t.Errorf("certificateType = %v", *c.Attributes.CertificateType)
			}
			got[kind] = *c.Attributes.SerialNumber
		case "devices":
			d, err := item.AsDevice()
			if err != nil {
				t.Fatal(err)
			}
			got[kind] = *d.Attributes.Udid
		default:
			t.Errorf("unexpected included type %q", kind)
		}
	}
	want := map[string]string{"bundleIds": "com.example.app", "certificates": "5A1B", "devices": "00008030-000"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("included %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestIncludedValueByDiscriminator(t *testing.T) {
	var doc gen.ProfileResponse
	if err := json.Unmarshal([]byte(profileDocument), &doc); err != nil {
		t.Fatal(err)
	}
	v, err := (*doc.Included)[2].ValueByDiscriminator()
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := v.(gen.Device); !ok || d.Id != "D1" {
		t.Errorf("ValueByDiscriminator() = %#v", v)
	}
}

func TestDecodeCapabilitySettings(t *testing.T) {
	const doc = `{"data": {"type": "bundleIdCapabilities", "id": "B1_ICLOUD",
	  "attributes": {"capabilityType": "ICLOUD", "settings": [{"key": "ICLOUD_VERSION",
	    "options": [{"key": "XCODE_6", "enabled": false}, {"key": "XCODE_5", "enabled": true}]}]}},
	  "links": {"self": "x"}}`
	var resp gen.BundleIdCapabilityResponse
	if err := json.Unmarshal([]byte(doc), &resp); err != nil {
		t.Fatal(err)
	}
	a := resp.Data.Attributes
	if *a.CapabilityType != gen.CapabilityTypeICLOUD {
		t.Errorf("capabilityType = %v", *a.CapabilityType)
	}
	s := (*a.Settings)[0]
	if *s.Key != gen.CapabilitySettingKeyICLOUDVERSION || len(*s.Options) != 2 || !*(*s.Options)[1].Enabled {
		t.Errorf("settings = %+v", s)
	}
}

func TestEncodeRequests(t *testing.T) {
	var create gen.CertificateCreateRequest
	create.Data.Type = gen.CertificateCreateRequestDataTypeCertificates
	create.Data.Attributes.CertificateType = gen.CertificateTypeDISTRIBUTION
	create.Data.Attributes.CsrContent = "CSR"
	b, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	// Optional relationships are left out rather than sent as null.
	if want := `{"data":{"attributes":{"certificateType":"DISTRIBUTION","csrContent":"CSR"},"type":"certificates"}}`; string(b) != want {
		t.Errorf("create = %s, want %s", b, want)
	}

	// Only the attributes being changed are sent.
	var update gen.BundleIdUpdateRequest
	update.Data.Type = gen.BundleIdUpdateRequestDataTypeBundleIds
	update.Data.Id = "B1"
	name := "Renamed"
	update.Data.Attributes = &struct {
		Name *string `json:"name,omitempty"`
	}{Name: &name}
	b, err = json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"data":{"attributes":{"name":"Renamed"},"id":"B1","type":"bundleIds"}}`; string(b) != want {
		t.Errorf("update = %s, want %s", b, want)
	}
}

func TestDecodeErrorSource(t *testing.T) {
	const doc = `{"errors": [
	  {"status": "409", "code": "ENTITY_ERROR", "title": "t", "detail": "d", "source": {"pointer": "/data/attributes/name"}},
	  {"status": "400", "code": "PARAMETER_ERROR.INVALID", "title": "t", "detail": "d", "source": {"parameter": "filter[platform]"}}]}`
	var resp gen.ErrorResponse
	if err := json.Unmarshal([]byte(doc), &resp); err != nil {
		t.Fatal(err)
	}
	errs := *resp.Errors
	p, err := errs[0].Source.AsErrorSourcePointer()
	if err != nil || p.Pointer != "/data/attributes/name" {
		t.Errorf("pointer = %+v, %v", p, err)
	}
	q, err := errs[1].Source.AsErrorSourceParameter()
	if err != nil || q.Parameter != "filter[platform]" {
		t.Errorf("parameter = %+v, %v", q, err)
	}
}

// profilesOnly is a fake that implements one operation of the generated
// server interface; the embedded nil interface panics on any other.
type profilesOnly struct {
	gen.ServerInterface
}

func (profilesOnly) ProfilesGetInstance(w http.ResponseWriter, _ *http.Request, id string, params gen.ProfilesGetInstanceParams) {
	if id != "P1" || params.Include == nil || len(*params.Include) != 2 {
		http.Error(w, "unexpected request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(profileDocument))
}

// TestGeneratedServer checks that the generated std-http server, which the
// fake API builds on, routes and parses a request from the generated client.
func TestGeneratedServer(t *testing.T) {
	srv := httptest.NewServer(gen.HandlerFromMux(profilesOnly{}, http.NewServeMux()))
	t.Cleanup(srv.Close)
	key, _ := testKey(t)
	c, err := New(srv.URL, NewTokenSource(Credentials{KeyID: "k", PrivateKey: key}), "test")
	if err != nil {
		t.Fatal(err)
	}
	include := []gen.ProfilesGetInstanceParamsInclude{gen.ProfilesGetInstanceParamsIncludeBundleId, gen.ProfilesGetInstanceParamsIncludeCertificates}
	resp, err := c.ProfilesGetInstanceWithResponse(t.Context(), "P1", &gen.ProfilesGetInstanceParams{Include: &include})
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(resp.HTTPResponse, resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.JSON200 == nil || resp.JSON200.Data.Id != "P1" || len(*resp.JSON200.Included) != 3 {
		t.Errorf("response = %s", resp.Body)
	}

	bad, err := c.ProfilesGetInstanceWithResponse(t.Context(), "P2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bad.StatusCode() != http.StatusBadRequest || !strings.Contains(string(bad.Body), "unexpected request") {
		t.Errorf("response = %d %s", bad.StatusCode(), bad.Body)
	}
}
