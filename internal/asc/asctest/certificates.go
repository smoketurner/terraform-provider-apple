package asctest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

type certificate struct {
	gen.Certificate
	merchantID string
	passTypeID string
}

// defaultCertLimits caps distribution certificates at one per type: "only
// one type of each distribution certificate (with the exception of Developer
// ID certificates) is allowed per team" ("Certificates overview",
// https://developer.apple.com/help/account/certificates/certificates-overview/).
// Other types are not capped.
func defaultCertLimits() map[gen.CertificateType]int {
	return map[gen.CertificateType]int{
		gen.CertificateTypeDISTRIBUTION:             1,
		gen.CertificateTypeIOSDISTRIBUTION:          1,
		gen.CertificateTypeMACAPPDISTRIBUTION:       1,
		gen.CertificateTypeMACINSTALLERDISTRIBUTION: 1,
	}
}

// SetCertificateLimit caps the active certificates of a type; 0 removes the
// cap.
func (s *Server) SetCertificateLimit(t gen.CertificateType, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == 0 {
		delete(s.certLimits, t)
		return
	}
	s.certLimits[t] = n
}

// certificateValidity is how long issued certificates are valid.
const certificateValidity = 365 * 24 * time.Hour

var (
	caOnce sync.Once
	caKey  *ecdsa.PrivateKey
	caCert *x509.Certificate
)

// ca returns the fake certificate authority that signs issued certificates.
func ca() (*ecdsa.PrivateKey, *x509.Certificate) {
	caOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "Fake Apple Worldwide Developer Relations Certification Authority"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(20 * 365 * 24 * time.Hour),
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			panic(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			panic(err)
		}
		caKey, caCert = key, cert
	})
	return caKey, caCert
}

// parseCSR accepts a CSR as PEM or as its base64 body.
func parseCSR(content string) (*x509.CertificateRequest, error) {
	der := []byte(nil)
	if block, _ := pem.Decode([]byte(content)); block != nil {
		der = block.Bytes
	} else {
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(content), ""))
		if err != nil {
			return nil, errors.New("csrContent is not a PEM or base64 certificate signing request")
		}
		der = b
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, fmt.Errorf("parsing csrContent: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("csrContent signature: %w", err)
	}
	return csr, nil
}

func certificatePlatform(t gen.CertificateType) *gen.BundleIdPlatform {
	switch {
	case strings.HasPrefix(string(t), "IOS_"):
		return new(gen.BundleIdPlatformIOS)
	case strings.HasPrefix(string(t), "MAC_"), strings.HasPrefix(string(t), "DEVELOPER_ID_"):
		return new(gen.BundleIdPlatformMACOS)
	}
	return nil
}

func (s *Server) CertificatesGetCollection(w http.ResponseWriter, r *http.Request, params gen.CertificatesGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.Certificate
	for _, c := range sortedValues(s.certificates) {
		a := c.Attributes
		if matches(params.FilterId, c.Id) && matches(params.FilterSerialNumber, asc.Value(a.SerialNumber)) &&
			matches(params.FilterDisplayName, asc.Value(a.DisplayName)) &&
			matches(params.FilterCertificateType, string(asc.Value(a.CertificateType))) {
			items = append(items, c.Certificate)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.CertificatesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// CertificatesCreateInstance issues a certificate for a CSR
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-certificates).
func (s *Server) CertificatesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.CertificateCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	if !in.CertificateType.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "certificateType is invalid", "/data/attributes/certificateType")
		return
	}
	csr, err := parseCSR(in.CsrContent)
	if err != nil {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", err.Error(), "/data/attributes/csrContent")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	c := &certificate{}
	if rel := req.Data.Relationships; rel != nil {
		if rel.MerchantId != nil && rel.MerchantId.Data != nil {
			c.merchantID = rel.MerchantId.Data.Id
			if _, ok := s.merchantIDs[c.merchantID]; !ok {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no merchantIds with id '"+c.merchantID+"'", "/data/relationships/merchantId")
				return
			}
		}
		if rel.PassTypeId != nil && rel.PassTypeId.Data != nil {
			c.passTypeID = rel.PassTypeId.Data.Id
			if _, ok := s.passTypeIDs[c.passTypeID]; !ok {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no passTypeIds with id '"+c.passTypeID+"'", "/data/relationships/passTypeId")
				return
			}
		}
	}
	if limit, ok := s.certLimits[in.CertificateType]; ok {
		n := 0
		for _, other := range s.certificates {
			if *other.Attributes.CertificateType == in.CertificateType {
				n++
			}
		}
		if n >= limit {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID",
				"There is a problem with the request entity: You already have a current "+string(in.CertificateType)+
					" certificate or a pending certificate request.", "/data/attributes/certificateType")
			return
		}
	}

	now := s.now()
	key, issuer := ca()
	serial := big.NewInt(int64(0x5A000000 + s.nextID))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: string(in.CertificateType) + ": " + csr.Subject.CommonName, OrganizationalUnit: []string{SeedID}},
		NotBefore:    now,
		NotAfter:     now.Add(certificateValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, csr.PublicKey, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UNEXPECTED_ERROR", "An unexpected error occurred.", err.Error(), "")
		return
	}

	c.Certificate = gen.Certificate{Id: s.newID("C"), Type: gen.CertificateResourceTypeCertificates}
	a := alloc(&c.Attributes)
	a.CertificateType = new(in.CertificateType)
	a.CertificateContent = new(base64.StdEncoding.EncodeToString(der))
	a.SerialNumber = new(strings.ToUpper(serial.Text(16)))
	a.Name = new(string(in.CertificateType) + ": " + csr.Subject.CommonName)
	a.DisplayName = new(csr.Subject.CommonName)
	a.Platform = certificatePlatform(in.CertificateType)
	a.ExpirationDate = new(gen.DateTime{Time: tmpl.NotAfter.UTC().Truncate(time.Second)})
	a.Activated = new(true)
	c.Links = resourceLinks(r, "certificates", c.Id)
	s.certificates[c.Id] = c
	writeJSON(w, http.StatusCreated, gen.CertificateResponse{Data: c.Certificate, Links: docLinks(r)})
}

func (s *Server) CertificatesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.CertificatesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.certificates[id]
	if !ok {
		notFound(w, "certificates", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.CertificateResponse{Data: c.Certificate, Links: docLinks(r)})
}

// CertificatesUpdateInstance changes activated, the only attribute
// CertificateUpdateRequest allows.
func (s *Server) CertificatesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.CertificateUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.certificates[id]
	if !ok {
		notFound(w, "certificates", id)
		return
	}
	if req.Data.Attributes != nil && req.Data.Attributes.Activated != nil {
		c.Attributes.Activated = new(*req.Data.Attributes.Activated)
	}
	writeJSON(w, http.StatusOK, gen.CertificateResponse{Data: c.Certificate, Links: docLinks(r)})
}

// CertificatesDeleteInstance revokes the certificate
// (https://developer.apple.com/documentation/appstoreconnectapi/delete-v1-certificates-_id_).
// "Provisioning profiles that contain a revoked certificate become invalid"
// ("Revoke a certificate",
// https://developer.apple.com/help/account/create-certificates/revoke-a-certificate).
func (s *Server) CertificatesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.certificates[id]; !ok {
		notFound(w, "certificates", id)
		return
	}
	delete(s.certificates, id)
	s.invalidateProfiles(func(p *profile) bool { return slices.Contains(p.certificateIDs, id) })
	w.WriteHeader(http.StatusNoContent)
}

// CertificatesPassTypeIdGetToOneRelationship returns the linkage, with null
// data for a certificate that has no pass type ID.
func (s *Server) CertificatesPassTypeIdGetToOneRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.certificates[id]
	if !ok {
		notFound(w, "certificates", id)
		return
	}
	if c.passTypeID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": nil, "links": docLinks(r)})
		return
	}
	resp := gen.CertificatePassTypeIdLinkageResponse{Links: docLinks(r)}
	resp.Data.Id, resp.Data.Type = c.passTypeID, "passTypeIds"
	writeJSON(w, http.StatusOK, resp)
}
