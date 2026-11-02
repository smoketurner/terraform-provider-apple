package asctest

import (
	"net/http"
	"strings"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Beta testers. A tester is created into groups or builds
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-betatesters);
// deleting one removes their access to every app
// (https://developer.apple.com/documentation/appstoreconnectapi/delete-v1-betatesters-_id_).

// testerApps returns the apps a tester can test through groups and builds.
func (s *Server) testerApps(t *betaTester) map[string]bool {
	apps := map[string]bool{}
	for id := range t.groups {
		if g, ok := s.tf.groups[id]; ok {
			apps[g.appID] = true
		}
	}
	for id := range t.builds {
		if b, ok := s.tf.builds[id]; ok {
			apps[b.appID] = true
		}
	}
	return apps
}

func anyIn[F ~string](filter *[]F, set map[string]bool) bool {
	if filter == nil {
		return true
	}
	for _, f := range *filter {
		if set[string(f)] {
			return true
		}
	}
	return false
}

func (s *Server) BetaTestersGetCollection(w http.ResponseWriter, r *http.Request, params gen.BetaTestersGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.BetaTester
	for _, t := range sortedValues(s.tf.testers) {
		a := t.Attributes
		if matches(params.FilterId, t.Id) && matches(params.FilterEmail, string(asc.Value(a.Email))) &&
			matches(params.FilterFirstName, asc.Value(a.FirstName)) && matches(params.FilterLastName, asc.Value(a.LastName)) &&
			matches(params.FilterInviteType, string(asc.Value(a.InviteType))) && anyIn(params.FilterApps, s.testerApps(t)) &&
			anyIn(params.FilterBetaGroups, t.groups) && anyIn(params.FilterBuilds, t.builds) {
			items = append(items, s.renderTester(r, t))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaTestersResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) renderTester(r *http.Request, t *betaTester) gen.BetaTester {
	out := t.BetaTester
	out.Links = resourceLinks(r, "betaTesters", t.Id)
	return out
}

func (s *Server) BetaTestersCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaTesterCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.Contains(string(in.Email), "@") {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "email is not a valid email address", "/data/attributes/email")
		return
	}
	for _, other := range s.tf.testers {
		if strings.EqualFold(string(asc.Value(other.Attributes.Email)), string(in.Email)) {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A beta tester with email "+string(in.Email)+" already exists.", "/data/attributes/email")
			return
		}
	}
	t := &betaTester{groups: map[string]bool{}, builds: map[string]bool{}, BetaTester: gen.BetaTester{Id: s.newID("E"), Type: gen.BetaTesterTypeBetaTesters}}
	if rel := req.Data.Relationships; rel != nil {
		if rel.BetaGroups != nil {
			for _, d := range asc.Value(rel.BetaGroups.Data) {
				if _, ok := s.tf.groups[d.Id]; !ok {
					conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no betaGroups with id '"+d.Id+"'", "/data/relationships/betaGroups")
					return
				}
				t.groups[d.Id] = true
			}
		}
		if rel.Builds != nil {
			for _, d := range asc.Value(rel.Builds.Data) {
				if _, ok := s.tf.builds[d.Id]; !ok {
					conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no builds with id '"+d.Id+"'", "/data/relationships/builds")
					return
				}
				t.builds[d.Id] = true
			}
		}
	}
	if len(t.groups) == 0 && len(t.builds) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "A beta tester must be assigned to a group or a build.", "/data/relationships")
		return
	}
	a := alloc(&t.Attributes)
	a.Email, a.FirstName, a.LastName = new(in.Email), in.FirstName, in.LastName
	a.InviteType = new(gen.BetaInviteTypeEMAIL)
	a.State = new(gen.BetaTesterStateINVITED)
	s.tf.testers[t.Id] = t
	writeJSON(w, http.StatusCreated, gen.BetaTesterResponse{Data: s.renderTester(r, t), Links: docLinks(r)})
}

func (s *Server) BetaTestersGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BetaTestersGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tf.testers[id]
	if !ok {
		notFound(w, "betaTesters", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaTesterResponse{Data: s.renderTester(r, t), Links: docLinks(r)})
}

func (s *Server) BetaTestersDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tf.testers[id]; !ok {
		notFound(w, "betaTesters", id)
		return
	}
	delete(s.tf.testers, id)
	for _, b := range s.tf.builds {
		delete(b.testers, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// linkageIDs decodes the IDs of a to-many linkage request body.
func linkageIDs(w http.ResponseWriter, r *http.Request, kind string, exists func(string) bool) ([]string, bool) {
	var req struct {
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if !decode(w, r, &req) {
		return nil, false
	}
	ids := make([]string, len(req.Data))
	for i, d := range req.Data {
		if d.Type != kind || !exists(d.ID) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+kind+" with id '"+d.ID+"'", "/data")
			return nil, false
		}
		ids[i] = d.ID
	}
	return ids, true
}

func (s *Server) groupExists(id string) bool  { _, ok := s.tf.groups[id]; return ok }
func (s *Server) testerExists(id string) bool { _, ok := s.tf.testers[id]; return ok }

func (s *Server) BetaTestersBetaGroupsCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tf.testers[id]
	if !ok {
		notFound(w, "betaTesters", id)
		return
	}
	ids, ok := linkageIDs(w, r, "betaGroups", s.groupExists)
	if !ok {
		return
	}
	for _, g := range ids {
		t.groups[g] = true
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BetaTestersBetaGroupsDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tf.testers[id]
	if !ok {
		notFound(w, "betaTesters", id)
		return
	}
	ids, ok := linkageIDs(w, r, "betaGroups", s.groupExists)
	if !ok {
		return
	}
	for _, g := range ids {
		delete(t.groups, g)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BetaTestersBetaGroupsGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.BetaTestersBetaGroupsGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tf.testers[id]
	if !ok {
		notFound(w, "betaTesters", id)
		return
	}
	var ids []string
	for _, g := range sortedValues(s.tf.groups) {
		if t.groups[g.Id] {
			ids = append(ids, g.Id)
		}
	}
	ids, links, meta, ok := page(w, r, ids, params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.BetaTesterBetaGroupsLinkagesResponse{Links: links, Meta: meta}
	for _, g := range ids {
		e := push(&resp.Data)
		e.Id, e.Type = g, "betaGroups"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}

// AppsBetaTestersDeleteToManyRelationship removes testers from every group
// and build of an app
// (https://developer.apple.com/documentation/appstoreconnectapi/delete-v1-apps-_id_-relationships-betatesters).
func (s *Server) AppsBetaTestersDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	ids, ok := linkageIDs(w, r, "betaTesters", s.testerExists)
	if !ok {
		return
	}
	for _, tid := range ids {
		t := s.tf.testers[tid]
		for g := range t.groups {
			if s.tf.groups[g].appID == id {
				delete(t.groups, g)
			}
		}
		for b := range t.builds {
			if s.tf.builds[b].appID == id {
				delete(t.builds, b)
				delete(s.tf.builds[b].testers, tid)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// BetaTesterInvitationsCreateInstance sends or resends an invitation to test
// an app the tester has access to
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-betatesterinvitations).
func (s *Server) BetaTesterInvitationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaTesterInvitationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	appID := req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := req.Data.Relationships.BetaTester
	if rel == nil || rel.Data == nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "betaTester is required", "/data/relationships/betaTester")
		return
	}
	t, ok := s.tf.testers[rel.Data.Id]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no betaTesters with id '"+rel.Data.Id+"'", "/data/relationships/betaTester")
		return
	}
	if !s.testerApps(t)[appID] {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The beta tester is not assigned to a group or build of app '"+appID+"'.", "/data/relationships/app")
		return
	}
	s.tf.invitations = append(s.tf.invitations, Invitation{AppID: appID, TesterID: t.Id})
	t.Attributes.State = new(gen.BetaTesterStateINVITED)
	writeJSON(w, http.StatusCreated, gen.BetaTesterInvitationResponse{
		Data:  gen.BetaTesterInvitation{Id: s.newID("I"), Type: gen.BetaTesterInvitationTypeBetaTesterInvitations},
		Links: docLinks(r),
	})
}
