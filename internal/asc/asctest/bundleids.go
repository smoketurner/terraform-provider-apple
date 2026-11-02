package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

type capability struct {
	gen.BundleIdCapability
	bundleID string
}

func (s *Server) BundleIdsGetCollection(w http.ResponseWriter, r *http.Request, params gen.BundleIdsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.BundleId
	for _, b := range sortedValues(s.bundleIDs) {
		a := b.Attributes
		if matches(params.FilterId, b.Id) && matches(params.FilterIdentifier, asc.Value(a.Identifier)) &&
			matches(params.FilterName, asc.Value(a.Name)) && matches(params.FilterPlatform, string(asc.Value(a.Platform))) &&
			matches(params.FilterSeedId, asc.Value(a.SeedId)) {
			items = append(items, *b)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BundleIdsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) BundleIdsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BundleIdCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	if !in.Platform.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "platform is invalid", "/data/attributes/platform")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bundleIDs {
		if asc.Value(b.Attributes.Identifier) == in.Identifier {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID",
				"An App ID with Identifier '"+in.Identifier+"' is not available. Please enter a different string.", "/data/attributes/identifier")
			return
		}
	}
	b := &gen.BundleId{Id: s.newID("B"), Type: gen.BundleIdTypeBundleIds}
	a := alloc(&b.Attributes)
	a.Identifier, a.Name, a.Platform = new(in.Identifier), new(in.Name), new(in.Platform)
	a.SeedId = new(SeedID)
	if in.SeedId != nil {
		a.SeedId = new(*in.SeedId)
	}
	b.Links = resourceLinks(r, "bundleIds", b.Id)
	s.bundleIDs[b.Id] = b
	writeJSON(w, http.StatusCreated, gen.BundleIdResponse{Data: *b, Links: docLinks(r)})
}

func (s *Server) BundleIdsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BundleIdsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bundleIDs[id]
	if !ok {
		notFound(w, "bundleIds", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.BundleIdResponse{Data: *b, Links: docLinks(r)})
}

// BundleIdsUpdateInstance changes the name, the only attribute
// BundleIdUpdateRequest allows.
func (s *Server) BundleIdsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BundleIdUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bundleIDs[id]
	if !ok {
		notFound(w, "bundleIds", id)
		return
	}
	if req.Data.Id != id {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The id in the request does not match the URL", "/data/id")
		return
	}
	if req.Data.Attributes != nil && req.Data.Attributes.Name != nil {
		b.Attributes.Name = new(*req.Data.Attributes.Name)
	}
	writeJSON(w, http.StatusOK, gen.BundleIdResponse{Data: *b, Links: docLinks(r)})
}

// BundleIdsDeleteInstance deletes the bundle ID with its capabilities.
func (s *Server) BundleIdsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bundleIDs[id]; !ok {
		notFound(w, "bundleIds", id)
		return
	}
	delete(s.bundleIDs, id)
	for cid, c := range s.capabilities {
		if c.bundleID == id {
			delete(s.capabilities, cid)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) bundleCapabilities(id string) []gen.BundleIdCapability {
	var out []gen.BundleIdCapability
	for _, c := range sortedValues(s.capabilities) {
		if c.bundleID == id {
			out = append(out, c.BundleIdCapability)
		}
	}
	return out
}

func (s *Server) BundleIdsBundleIdCapabilitiesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.BundleIdsBundleIdCapabilitiesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bundleIDs[id]; !ok {
		notFound(w, "bundleIds", id)
		return
	}
	data, links, meta, ok := page(w, r, s.bundleCapabilities(id), params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BundleIdCapabilitiesWithoutIncludesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) BundleIdsBundleIdCapabilitiesGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.BundleIdsBundleIdCapabilitiesGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bundleIDs[id]; !ok {
		notFound(w, "bundleIds", id)
		return
	}
	caps, links, meta, ok := page(w, r, s.bundleCapabilities(id), params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.BundleIdBundleIdCapabilitiesLinkagesResponse{Links: links, Meta: meta}
	for _, c := range caps {
		e := push(&resp.Data)
		e.Id, e.Type = c.Id, "bundleIdCapabilities"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) BundleIdsProfilesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.BundleIdsProfilesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bundleIDs[id]; !ok {
		notFound(w, "bundleIds", id)
		return
	}
	var items []gen.Profile
	for _, p := range sortedValues(s.profiles) {
		if p.bundleID == id {
			items = append(items, s.renderProfile(r, p))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.ProfilesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) BundleIdsProfilesGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.BundleIdsProfilesGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bundleIDs[id]; !ok {
		notFound(w, "bundleIds", id)
		return
	}
	var ids []string
	for _, p := range sortedValues(s.profiles) {
		if p.bundleID == id {
			ids = append(ids, p.Id)
		}
	}
	ids, links, meta, ok := page(w, r, ids, params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.BundleIdProfilesLinkagesResponse{Links: links, Meta: meta}
	for _, pid := range ids {
		e := push(&resp.Data)
		e.Id, e.Type = pid, "profiles"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}

// BundleIdCapabilitiesCreateInstance enables a capability. Changing a bundle
// ID's capabilities invalidates the provisioning profiles that use it
// (Technical Q&A QA1878, "Resolving the Provisioning Profile Invalid Status",
// https://developer.apple.com/library/archive/qa/qa1878/_index.html).
func (s *Server) BundleIdCapabilitiesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BundleIdCapabilityCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	if !in.CapabilityType.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "capabilityType is invalid", "/data/attributes/capabilityType")
		return
	}
	bundleID := req.Data.Relationships.BundleId.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bundleIDs[bundleID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no bundleIds with id '"+bundleID+"'", "/data/relationships/bundleId")
		return
	}
	id := bundleID + "_" + string(in.CapabilityType)
	if _, ok := s.capabilities[id]; ok {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The capability "+string(in.CapabilityType)+" is already enabled", "/data/attributes/capabilityType")
		return
	}
	c := &capability{bundleID: bundleID, BundleIdCapability: gen.BundleIdCapability{Id: id, Type: gen.BundleIdCapabilityTypeBundleIdCapabilities}}
	a := alloc(&c.Attributes)
	a.CapabilityType = new(in.CapabilityType)
	a.Settings = in.Settings
	c.Links = resourceLinks(r, "bundleIdCapabilities", id)
	s.capabilities[id] = c
	s.invalidateProfiles(func(p *profile) bool { return p.bundleID == bundleID })
	writeJSON(w, http.StatusCreated, gen.BundleIdCapabilityResponse{Data: c.BundleIdCapability, Links: docLinks(r)})
}

func (s *Server) BundleIdCapabilitiesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BundleIdCapabilityUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.capabilities[id]
	if !ok {
		notFound(w, "bundleIdCapabilities", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.CapabilityType != nil && *in.CapabilityType != *c.Attributes.CapabilityType {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "capabilityType cannot be changed", "/data/attributes/capabilityType")
			return
		}
		if in.Settings != nil {
			c.Attributes.Settings = in.Settings
		}
	}
	s.invalidateProfiles(func(p *profile) bool { return p.bundleID == c.bundleID })
	writeJSON(w, http.StatusOK, gen.BundleIdCapabilityResponse{Data: c.BundleIdCapability, Links: docLinks(r)})
}

func (s *Server) BundleIdCapabilitiesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.capabilities[id]
	if !ok {
		notFound(w, "bundleIdCapabilities", id)
		return
	}
	delete(s.capabilities, id)
	s.invalidateProfiles(func(p *profile) bool { return p.bundleID == c.bundleID })
	w.WriteHeader(http.StatusNoContent)
}

// nonNil returns s, or an empty slice for nil, so "data" encodes as [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

const maxLinkageLimit = 1000
