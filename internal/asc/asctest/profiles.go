package asctest

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

type profile struct {
	gen.Profile
	bundleID       string
	certificateIDs []string
	deviceIDs      []string
}

// profileValidity is how long a new profile is valid, capped by its
// certificates' expiration.
const profileValidity = 365 * 24 * time.Hour

// invalidateProfiles marks the profiles that match pred INVALID.
func (s *Server) invalidateProfiles(pred func(*profile) bool) {
	for _, p := range s.profiles {
		if pred(p) {
			p.Attributes.ProfileState = new(gen.ProfileAttributesProfileStateINVALID)
		}
	}
}

// InvalidateProfile marks a profile INVALID, as App Store Connect does when
// one of its certificates expires.
func (s *Server) InvalidateProfile(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateProfiles(func(p *profile) bool { return p.Id == id })
}

// renderProfile returns the profile as the API shows it. A profile past its
// expiration date is INVALID.
func (s *Server) renderProfile(r *http.Request, p *profile) gen.Profile {
	out := p.Profile
	attrs := *p.Attributes
	if s.now().After(attrs.ExpirationDate.Time) {
		attrs.ProfileState = new(gen.ProfileAttributesProfileStateINVALID)
	}
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "profiles", p.Id)
	return out
}

func (s *Server) ProfilesGetCollection(w http.ResponseWriter, r *http.Request, params gen.ProfilesGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.Profile
	for _, p := range sortedValues(s.profiles) {
		rp := s.renderProfile(r, p)
		a := rp.Attributes
		if matches(params.FilterId, p.Id) && matches(params.FilterName, asc.Value(a.Name)) &&
			matches(params.FilterProfileType, string(asc.Value(a.ProfileType))) &&
			matches(params.FilterProfileState, string(asc.Value(a.ProfileState))) {
			items = append(items, rp)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.ProfilesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ProfilesCreateInstance creates a profile for a bundle ID, certificates and
// (for development and ad hoc profiles) devices.
func (s *Server) ProfilesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.ProfileCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	if !in.ProfileType.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "profileType is invalid", "/data/attributes/profileType")
		return
	}
	rel := req.Data.Relationships
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &profile{bundleID: rel.BundleId.Data.Id}
	if _, ok := s.bundleIDs[p.bundleID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no bundleIds with id '"+p.bundleID+"'", "/data/relationships/bundleId")
		return
	}
	if len(rel.Certificates.Data) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "A profile needs at least one certificate", "/data/relationships/certificates")
		return
	}
	expires := s.now().Add(profileValidity)
	for _, c := range rel.Certificates.Data {
		cert, ok := s.certificates[c.Id]
		if !ok {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no certificates with id '"+c.Id+"'", "/data/relationships/certificates")
			return
		}
		if exp := cert.Attributes.ExpirationDate.Time; exp.Before(expires) {
			expires = exp
		}
		p.certificateIDs = append(p.certificateIDs, c.Id)
	}
	if rel.Devices != nil && rel.Devices.Data != nil {
		for _, d := range *rel.Devices.Data {
			if _, ok := s.devices[d.Id]; !ok {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no devices with id '"+d.Id+"'", "/data/relationships/devices")
				return
			}
			p.deviceIDs = append(p.deviceIDs, d.Id)
		}
	}
	slices.Sort(p.certificateIDs)
	slices.Sort(p.deviceIDs)

	bundle := s.bundleIDs[p.bundleID]
	content := make([]byte, 64)
	_, _ = rand.Read(content)
	p.Profile = gen.Profile{Id: s.newID("P"), Type: gen.ProfileTypeProfiles}
	a := alloc(&p.Attributes)
	a.Name = new(in.Name)
	a.ProfileType = new(gen.ProfileAttributesProfileType(in.ProfileType))
	a.Platform = bundle.Attributes.Platform
	a.ProfileState = new(gen.ProfileAttributesProfileStateACTIVE)
	a.ProfileContent = new(base64.StdEncoding.EncodeToString(content))
	a.Uuid = new(fmt.Sprintf("%x-%x-%x-%x-%x", content[0:4], content[4:6], content[6:8], content[8:10], content[10:16]))
	a.CreatedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	a.ExpirationDate = new(gen.DateTime{Time: expires.UTC().Truncate(time.Second)})
	s.profiles[p.Id] = p
	writeJSON(w, http.StatusCreated, gen.ProfileResponse{Data: s.renderProfile(r, p), Links: docLinks(r)})
}

func (s *Server) ProfilesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.ProfilesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		notFound(w, "profiles", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.ProfileResponse{Data: s.renderProfile(r, p), Links: docLinks(r)})
}

// ProfilesDeleteInstance deletes the profile
// (https://developer.apple.com/documentation/appstoreconnectapi/delete-v1-profiles-_id_).
func (s *Server) ProfilesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[id]; !ok {
		notFound(w, "profiles", id)
		return
	}
	delete(s.profiles, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ProfilesBundleIdGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.ProfilesBundleIdGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		notFound(w, "profiles", id)
		return
	}
	b, ok := s.bundleIDs[p.bundleID]
	if !ok {
		notFound(w, "bundleIds", p.bundleID)
		return
	}
	writeJSON(w, http.StatusOK, gen.BundleIdResponse{Data: *b, Links: docLinks(r)})
}

func (s *Server) ProfilesBundleIdGetToOneRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		notFound(w, "profiles", id)
		return
	}
	resp := gen.ProfileBundleIdLinkageResponse{Links: docLinks(r)}
	resp.Data.Id, resp.Data.Type = p.bundleID, "bundleIds"
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) ProfilesCertificatesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.ProfilesCertificatesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		notFound(w, "profiles", id)
		return
	}
	var items []gen.Certificate
	for _, cid := range p.certificateIDs {
		if c, ok := s.certificates[cid]; ok {
			items = append(items, c.Certificate)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.CertificatesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ProfilesCertificatesGetToManyRelationship lists the profile's certificate
// IDs, including revoked ones, which is what makes the profile INVALID.
func (s *Server) ProfilesCertificatesGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.ProfilesCertificatesGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		notFound(w, "profiles", id)
		return
	}
	ids, links, meta, ok := page(w, r, p.certificateIDs, params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.ProfileCertificatesLinkagesResponse{Links: links, Meta: meta}
	for _, cid := range ids {
		e := push(&resp.Data)
		e.Id, e.Type = cid, "certificates"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) ProfilesDevicesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.ProfilesDevicesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		notFound(w, "profiles", id)
		return
	}
	var items []gen.Device
	for _, did := range p.deviceIDs {
		items = append(items, *s.devices[did])
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.DevicesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) ProfilesDevicesGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.ProfilesDevicesGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		notFound(w, "profiles", id)
		return
	}
	ids, links, meta, ok := page(w, r, p.deviceIDs, params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.ProfileDevicesLinkagesResponse{Links: links, Meta: meta}
	for _, did := range ids {
		e := push(&resp.Data)
		e.Id, e.Type = did, "devices"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}
