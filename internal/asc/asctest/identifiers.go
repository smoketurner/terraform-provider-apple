package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Merchant IDs and pass type IDs have the same shape: a unique identifier and
// a name that can be changed.

// certificatesFor returns the certificates whose merchant or pass type ID,
// as chosen by owner, is id.
func (s *Server) certificatesFor(id string, owner func(*certificate) string) []gen.Certificate {
	var out []gen.Certificate
	for _, c := range sortedValues(s.certificates) {
		if owner(c) == id {
			out = append(out, c.Certificate)
		}
	}
	return out
}

func merchantOf(c *certificate) string { return c.merchantID }
func passTypeOf(c *certificate) string { return c.passTypeID }

// certificateFilter applies the filters the certificate listings of merchant
// and pass type IDs accept.
func certificateFilter[F ~string](certs []gen.Certificate, ids, serials, names *[]string, types *[]F) []gen.Certificate {
	var out []gen.Certificate
	for _, c := range certs {
		a := c.Attributes
		if matches(ids, c.Id) && matches(serials, asc.Value(a.SerialNumber)) && matches(names, asc.Value(a.DisplayName)) &&
			matches(types, string(asc.Value(a.CertificateType))) {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) MerchantIdsGetCollection(w http.ResponseWriter, r *http.Request, params gen.MerchantIdsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.MerchantId
	for _, m := range sortedValues(s.merchantIDs) {
		if matches(params.FilterIdentifier, asc.Value(m.Attributes.Identifier)) && matches(params.FilterName, asc.Value(m.Attributes.Name)) {
			items = append(items, *m)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.MerchantIdsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) MerchantIdsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.MerchantIdCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.merchantIDs {
		if asc.Value(m.Attributes.Identifier) == in.Identifier {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The identifier '"+in.Identifier+"' is not available.", "/data/attributes/identifier")
			return
		}
	}
	m := &gen.MerchantId{Id: s.newID("M"), Type: gen.MerchantIdTypeMerchantIds}
	a := alloc(&m.Attributes)
	a.Identifier, a.Name = new(in.Identifier), new(in.Name)
	m.Links = resourceLinks(r, "merchantIds", m.Id)
	s.merchantIDs[m.Id] = m
	writeJSON(w, http.StatusCreated, gen.MerchantIdResponse{Data: *m, Links: docLinks(r)})
}

func (s *Server) MerchantIdsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.MerchantIdsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.merchantIDs[id]
	if !ok {
		notFound(w, "merchantIds", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.MerchantIdResponse{Data: *m, Links: docLinks(r)})
}

func (s *Server) MerchantIdsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.MerchantIdUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.merchantIDs[id]
	if !ok {
		notFound(w, "merchantIds", id)
		return
	}
	if req.Data.Attributes != nil && req.Data.Attributes.Name != nil {
		m.Attributes.Name = new(*req.Data.Attributes.Name)
	}
	writeJSON(w, http.StatusOK, gen.MerchantIdResponse{Data: *m, Links: docLinks(r)})
}

func (s *Server) MerchantIdsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.merchantIDs[id]; !ok {
		notFound(w, "merchantIds", id)
		return
	}
	delete(s.merchantIDs, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) MerchantIdsCertificatesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.MerchantIdsCertificatesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.merchantIDs[id]; !ok {
		notFound(w, "merchantIds", id)
		return
	}
	certs := certificateFilter(s.certificatesFor(id, merchantOf), params.FilterId, params.FilterSerialNumber, params.FilterDisplayName, params.FilterCertificateType)
	data, links, meta, ok := page(w, r, certs, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.CertificatesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) MerchantIdsCertificatesGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.MerchantIdsCertificatesGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.merchantIDs[id]; !ok {
		notFound(w, "merchantIds", id)
		return
	}
	certs, links, meta, ok := page(w, r, s.certificatesFor(id, merchantOf), params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.MerchantIdCertificatesLinkagesResponse{Links: links, Meta: meta}
	for _, c := range certs {
		e := push(&resp.Data)
		e.Id, e.Type = c.Id, "certificates"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) PassTypeIdsGetCollection(w http.ResponseWriter, r *http.Request, params gen.PassTypeIdsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.PassTypeId
	for _, p := range sortedValues(s.passTypeIDs) {
		if matches(params.FilterId, p.Id) && matches(params.FilterIdentifier, asc.Value(p.Attributes.Identifier)) &&
			matches(params.FilterName, asc.Value(p.Attributes.Name)) {
			items = append(items, *p)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.PassTypeIdsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) PassTypeIdsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.PassTypeIdCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.passTypeIDs {
		if asc.Value(p.Attributes.Identifier) == in.Identifier {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The identifier '"+in.Identifier+"' is not available.", "/data/attributes/identifier")
			return
		}
	}
	p := &gen.PassTypeId{Id: s.newID("T"), Type: gen.PassTypeIdTypePassTypeIds}
	a := alloc(&p.Attributes)
	a.Identifier, a.Name = new(in.Identifier), new(in.Name)
	p.Links = resourceLinks(r, "passTypeIds", p.Id)
	s.passTypeIDs[p.Id] = p
	writeJSON(w, http.StatusCreated, gen.PassTypeIdResponse{Data: *p, Links: docLinks(r)})
}

func (s *Server) PassTypeIdsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.PassTypeIdsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.passTypeIDs[id]
	if !ok {
		notFound(w, "passTypeIds", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.PassTypeIdResponse{Data: *p, Links: docLinks(r)})
}

func (s *Server) PassTypeIdsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.PassTypeIdUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.passTypeIDs[id]
	if !ok {
		notFound(w, "passTypeIds", id)
		return
	}
	if req.Data.Attributes != nil && req.Data.Attributes.Name != nil {
		p.Attributes.Name = new(*req.Data.Attributes.Name)
	}
	writeJSON(w, http.StatusOK, gen.PassTypeIdResponse{Data: *p, Links: docLinks(r)})
}

func (s *Server) PassTypeIdsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.passTypeIDs[id]; !ok {
		notFound(w, "passTypeIds", id)
		return
	}
	delete(s.passTypeIDs, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) PassTypeIdsCertificatesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.PassTypeIdsCertificatesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.passTypeIDs[id]; !ok {
		notFound(w, "passTypeIds", id)
		return
	}
	certs := certificateFilter(s.certificatesFor(id, passTypeOf), params.FilterId, params.FilterSerialNumber, params.FilterDisplayName, params.FilterCertificateType)
	data, links, meta, ok := page(w, r, certs, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.CertificatesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) PassTypeIdsCertificatesGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.PassTypeIdsCertificatesGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.passTypeIDs[id]; !ok {
		notFound(w, "passTypeIds", id)
		return
	}
	certs, links, meta, ok := page(w, r, s.certificatesFor(id, passTypeOf), params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.PassTypeIdCertificatesLinkagesResponse{Links: links, Meta: meta}
	for _, c := range certs {
		e := push(&resp.Data)
		e.Id, e.Type = c.Id, "certificates"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}
