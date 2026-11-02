package asctest

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Beta groups. Rules from "Invite external testers"
// (https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers/):
// an app needs an internal group before it can have an external one, public
// links are created for external groups, and builds uploaded as TestFlight
// Internal Only can only be added to internal groups.

func includes[T ~string](include *[]T, name string) bool {
	return include != nil && slices.Contains(*include, T(name))
}

func (s *Server) renderGroup(r *http.Request, g *betaGroup, withApp bool) gen.BetaGroup {
	out := g.BetaGroup
	attrs := *g.Attributes
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "betaGroups", g.Id)
	out.Relationships = nil
	if withApp {
		d := alloc(&alloc(&alloc(&out.Relationships).App).Data)
		d.Id, d.Type = g.appID, "apps"
	}
	return out
}

func boolString(b *bool) string {
	return strconv.FormatBool(asc.Value(b))
}

func (s *Server) BetaGroupsGetCollection(w http.ResponseWriter, r *http.Request, params gen.BetaGroupsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.BetaGroup
	for _, g := range sortedValues(s.tf.groups) {
		a := g.Attributes
		inBuild := params.FilterBuilds == nil
		if !inBuild {
			for _, id := range *params.FilterBuilds {
				if b, ok := s.tf.builds[id]; ok && b.groups[g.Id] {
					inBuild = true
				}
			}
		}
		if inBuild && matches(params.FilterId, g.Id) && matches(params.FilterApp, g.appID) && matches(params.FilterName, asc.Value(a.Name)) &&
			matches(params.FilterIsInternalGroup, boolString(a.IsInternalGroup)) &&
			matches(params.FilterPublicLinkEnabled, boolString(a.PublicLinkEnabled)) &&
			matches(params.FilterPublicLink, asc.Value(a.PublicLink)) {
			items = append(items, s.renderGroup(r, g, includes(params.Include, "app")))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaGroupsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// setPublicLink keeps the public link attributes consistent with
// publicLinkEnabled.
func (s *Server) setPublicLink(g *betaGroup) {
	a := g.Attributes
	if !asc.Value(a.PublicLinkEnabled) {
		a.PublicLink, a.PublicLinkId = nil, nil
		return
	}
	if a.PublicLinkId == nil {
		id := "pl" + g.Id
		a.PublicLinkId = &id
		a.PublicLink = new("https://testflight.apple.com/join/" + id)
	}
}

func (s *Server) BetaGroupsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaGroupCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	appID := req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	internal := asc.Value(in.IsInternalGroup)
	if !internal {
		hasInternal := false
		for _, g := range s.tf.groups {
			hasInternal = hasInternal || g.appID == appID && *g.Attributes.IsInternalGroup
		}
		if !hasInternal {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "To create an external group, first create an internal group.", "/data/attributes/isInternalGroup")
			return
		}
	}
	if internal && (asc.Value(in.PublicLinkEnabled) || in.PublicLinkLimit != nil || asc.Value(in.PublicLinkLimitEnabled)) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Public links are only available for external groups.", "/data/attributes/publicLinkEnabled")
		return
	}
	if !internal && in.HasAccessToAllBuilds != nil {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "hasAccessToAllBuilds is only available for internal groups.", "/data/attributes/hasAccessToAllBuilds")
		return
	}
	g := &betaGroup{appID: appID, BetaGroup: gen.BetaGroup{Id: s.newID("G"), Type: gen.BetaGroupTypeBetaGroups}}
	a := alloc(&g.Attributes)
	a.Name, a.IsInternalGroup = new(in.Name), new(internal)
	a.CreatedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	a.FeedbackEnabled = new(true)
	if in.FeedbackEnabled != nil {
		a.FeedbackEnabled = new(*in.FeedbackEnabled)
	}
	if internal {
		a.HasAccessToAllBuilds = new(asc.Value(in.HasAccessToAllBuilds))
	}
	a.PublicLinkEnabled = new(asc.Value(in.PublicLinkEnabled))
	a.PublicLinkLimitEnabled = new(asc.Value(in.PublicLinkLimitEnabled))
	a.PublicLinkLimit = in.PublicLinkLimit
	a.IosBuildsAvailableForAppleSiliconMac, a.IosBuildsAvailableForAppleVision = new(false), new(false)
	s.setPublicLink(g)
	s.tf.groups[g.Id] = g
	writeJSON(w, http.StatusCreated, gen.BetaGroupResponse{Data: s.renderGroup(r, g, false), Links: docLinks(r)})
}

func (s *Server) BetaGroupsGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.BetaGroupsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.tf.groups[id]
	if !ok {
		notFound(w, "betaGroups", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaGroupResponse{Data: s.renderGroup(r, g, includes(params.Include, "app")), Links: docLinks(r)})
}

func (s *Server) BetaGroupsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaGroupUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.tf.groups[id]
	if !ok {
		notFound(w, "betaGroups", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if *g.Attributes.IsInternalGroup && (asc.Value(in.PublicLinkEnabled) || in.PublicLinkLimit != nil || asc.Value(in.PublicLinkLimitEnabled)) {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Public links are only available for external groups.", "/data/attributes/publicLinkEnabled")
			return
		}
		a := g.Attributes
		for dst, src := range map[**bool]*bool{
			&a.FeedbackEnabled: in.FeedbackEnabled, &a.PublicLinkEnabled: in.PublicLinkEnabled,
			&a.PublicLinkLimitEnabled: in.PublicLinkLimitEnabled, &a.IosBuildsAvailableForAppleSiliconMac: in.IosBuildsAvailableForAppleSiliconMac,
			&a.IosBuildsAvailableForAppleVision: in.IosBuildsAvailableForAppleVision,
		} {
			if src != nil {
				*dst = new(*src)
			}
		}
		if in.Name != nil {
			a.Name = new(*in.Name)
		}
		if in.PublicLinkLimit != nil {
			a.PublicLinkLimit = new(*in.PublicLinkLimit)
		}
		s.setPublicLink(g)
	}
	writeJSON(w, http.StatusOK, gen.BetaGroupResponse{Data: s.renderGroup(r, g, false), Links: docLinks(r)})
}

// BetaGroupsDeleteInstance deletes the group. Its testers stay, without it.
func (s *Server) BetaGroupsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tf.groups[id]; !ok {
		notFound(w, "betaGroups", id)
		return
	}
	delete(s.tf.groups, id)
	for _, t := range s.tf.testers {
		delete(t.groups, id)
	}
	for _, b := range s.tf.builds {
		delete(b.groups, id)
	}
	for cid, c := range s.tf.criteria {
		if c.groupID == id {
			delete(s.tf.criteria, cid)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// Recruitment criteria filter who can join an external group's public link.

func (s *Server) BetaRecruitmentCriteriaCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaRecruitmentCriterionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	groupID := req.Data.Relationships.BetaGroup.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.tf.groups[groupID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no betaGroups with id '"+groupID+"'", "/data/relationships/betaGroup")
		return
	}
	if *g.Attributes.IsInternalGroup {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "Tester criteria apply only to the public links of external groups.", "/data/relationships/betaGroup")
		return
	}
	for _, c := range s.tf.criteria {
		if c.groupID == groupID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The group already has recruitment criteria.", "/data/relationships/betaGroup")
			return
		}
	}
	c := &criterion{groupID: groupID, BetaRecruitmentCriterion: gen.BetaRecruitmentCriterion{Id: s.newID("R"), Type: gen.BetaRecruitmentCriterionTypeBetaRecruitmentCriteria}}
	a := alloc(&c.Attributes)
	a.DeviceFamilyOsVersionFilters = new(req.Data.Attributes.DeviceFamilyOsVersionFilters)
	a.LastModifiedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	s.tf.criteria[c.Id] = c
	writeJSON(w, http.StatusCreated, gen.BetaRecruitmentCriterionResponse{Data: c.BetaRecruitmentCriterion, Links: docLinks(r)})
}

func (s *Server) BetaRecruitmentCriteriaUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaRecruitmentCriterionUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.tf.criteria[id]
	if !ok {
		notFound(w, "betaRecruitmentCriteria", id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.DeviceFamilyOsVersionFilters != nil {
		c.Attributes.DeviceFamilyOsVersionFilters = new(*in.DeviceFamilyOsVersionFilters)
		c.Attributes.LastModifiedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	}
	writeJSON(w, http.StatusOK, gen.BetaRecruitmentCriterionResponse{Data: c.BetaRecruitmentCriterion, Links: docLinks(r)})
}

func (s *Server) BetaRecruitmentCriteriaDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tf.criteria[id]; !ok {
		notFound(w, "betaRecruitmentCriteria", id)
		return
	}
	delete(s.tf.criteria, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BetaGroupsBetaRecruitmentCriteriaGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.BetaGroupsBetaRecruitmentCriteriaGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tf.groups[id]; !ok {
		notFound(w, "betaGroups", id)
		return
	}
	for _, c := range s.tf.criteria {
		if c.groupID == id {
			writeJSON(w, http.StatusOK, gen.BetaRecruitmentCriterionResponse{Data: c.BetaRecruitmentCriterion, Links: docLinks(r)})
			return
		}
	}
	notFound(w, "betaRecruitmentCriteria", id)
}

// recruitmentOptions are the device families and OS versions criteria can
// name.
var recruitmentOptions = map[gen.DeviceFamily][]string{
	gen.DeviceFamilyIPHONE: {"17.0", "18.0", "26.0"},
	gen.DeviceFamilyIPAD:   {"17.0", "18.0", "26.0"},
	gen.DeviceFamilyMAC:    {"14.0", "15.0", "26.0"},
}

func (s *Server) BetaRecruitmentCriterionOptionsGetCollection(w http.ResponseWriter, r *http.Request, params gen.BetaRecruitmentCriterionOptionsGetCollectionParams) {
	o := gen.BetaRecruitmentCriterionOption{Id: "options", Type: gen.BetaRecruitmentCriterionOptionTypeBetaRecruitmentCriterionOptions}
	a := alloc(&o.Attributes)
	for _, family := range []gen.DeviceFamily{gen.DeviceFamilyIPAD, gen.DeviceFamilyIPHONE, gen.DeviceFamilyMAC} {
		e := asc.Append(&a.DeviceFamilyOsVersions)
		e.DeviceFamily, e.OsVersions = new(family), new(recruitmentOptions[family])
	}
	data, links, meta, ok := page(w, r, []gen.BetaRecruitmentCriterionOption{o}, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaRecruitmentCriterionOptionsResponse{Data: data, Links: links, Meta: meta})
}
