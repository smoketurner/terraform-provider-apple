package asctest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Featuring nominations tell Apple's editorial team about an app launch,
// update or new content (https://developer.apple.com/documentation/appstoreconnectapi/nominations).
// A nomination is a draft until submitted, and archived when withdrawn.

type nomination struct {
	gen.Nomination
	appIDs      []string
	territories []string
}

// App keywords are the search keywords App Store Connect offers for an
// app's platform and locale; a version localization links the ones it uses.
type appKeyword struct {
	appID, platform, locale string
}

type nominationsState struct {
	nominations map[string]*nomination
	keywords    map[string]appKeyword
	// links holds each version localization's keyword IDs.
	links map[string][]string
}

func newNominationsState() *nominationsState {
	return &nominationsState{nominations: map[string]*nomination{}, keywords: map[string]appKeyword{}, links: map[string][]string{}}
}

// readBody reads a request body and the attributes it sets to null.
func readBody(w http.ResponseWriter, r *http.Request, v any) (nulls map[string]bool, ok bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ENTITY_UNPROCESSABLE", "The request entity is not valid JSON", err.Error(), "")
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !decode(w, r, v) {
		return nil, false
	}
	var raw struct {
		Data struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &raw)
	nulls = map[string]bool{}
	for k, v := range raw.Data.Attributes {
		if string(v) == "null" {
			nulls[k] = true
		}
	}
	return nulls, true
}

func (s *Server) renderNomination(n *nomination) gen.Nomination {
	out := n.Nomination
	attrs := *n.Attributes
	out.Attributes = &attrs
	rel := alloc(&out.Relationships)
	apps := alloc(&rel.RelatedApps)
	for _, id := range n.appIDs {
		e := push(alloc(&apps.Data))
		e.Id, e.Type = id, "apps"
	}
	terr := alloc(&rel.SupportedTerritories)
	data := alloc(&terr.Data)
	for _, id := range n.territories {
		e := push(data)
		e.Id, e.Type = id, "territories"
	}
	return out
}

func (s *Server) checkNominationApps(w http.ResponseWriter, ids []string) bool {
	if len(ids) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "A nomination needs at least one related app.", "/data/relationships/relatedApps")
		return false
	}
	for _, id := range ids {
		if _, ok := s.apps[id]; !ok {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+id+"'", "/data/relationships/relatedApps")
			return false
		}
	}
	return true
}

func checkPublishDates(w http.ResponseWriter, start *gen.DateTime, end *gen.DateTime) bool {
	if start != nil && end != nil && !end.After(start.Time) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "publishEndDate must be after publishStartDate.", "/data/attributes/publishEndDate")
		return false
	}
	return true
}

func (s *Server) NominationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.NominationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	var appIDs []string
	for _, a := range req.Data.Relationships.RelatedApps.Data {
		appIDs = append(appIDs, a.Id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checkNominationApps(w, appIDs) || !checkPublishDates(w, &in.PublishStartDate, in.PublishEndDate) {
		return
	}
	n := &nomination{appIDs: appIDs, Nomination: gen.Nomination{Id: s.newID("N"), Type: gen.NominationTypeNominations}}
	if t := req.Data.Relationships.SupportedTerritories; t != nil && t.Data != nil {
		for _, e := range *t.Data {
			n.territories = append(n.territories, e.Id)
		}
	}
	a := alloc(&n.Attributes)
	now := gen.DateTime{Time: s.now().UTC().Truncate(time.Second)}
	a.Name, a.Description, a.Type = new(in.Name), new(in.Description), new(gen.NominationAttributesType(in.Type))
	a.PublishStartDate, a.PublishEndDate = new(in.PublishStartDate), in.PublishEndDate
	a.DeviceFamilies, a.Locales, a.SupplementalMaterialsUris = in.DeviceFamilies, in.Locales, in.SupplementalMaterialsUris
	a.HasInAppEvents, a.LaunchInSelectMarketsFirst, a.Notes, a.PreOrderEnabled = in.HasInAppEvents, in.LaunchInSelectMarketsFirst, in.Notes, in.PreOrderEnabled
	a.CreatedDate, a.LastModifiedDate = new(now), new(now)
	a.State = new(gen.NominationAttributesStateDRAFT)
	if in.Submitted {
		a.State, a.SubmittedDate = new(gen.NominationAttributesStateSUBMITTED), new(now)
	}
	s.nominations.nominations[n.Id] = n
	writeJSON(w, http.StatusCreated, gen.NominationResponse{Data: s.renderNomination(n), Links: docLinks(r)})
}

func (s *Server) NominationsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.NominationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nominations.nominations[id]
	if !ok {
		notFound(w, "nominations", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.NominationResponse{Data: s.renderNomination(n), Links: docLinks(r)})
}

func (s *Server) NominationsGetCollection(w http.ResponseWriter, r *http.Request, params gen.NominationsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.Nomination
	for _, n := range sortedValues(s.nominations.nominations) {
		a := n.Attributes
		related := params.FilterRelatedApps == nil || slices.ContainsFunc(n.appIDs, func(id string) bool { return slices.Contains(*params.FilterRelatedApps, id) })
		if matches(&params.FilterState, string(*a.State)) && matches(params.FilterType, string(*a.Type)) && related {
			items = append(items, s.renderNomination(n))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.NominationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// NominationsUpdateInstance edits, submits or archives a nomination. An
// archived nomination can't change, and a submitted one can't return to
// draft.
func (s *Server) NominationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.NominationUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nominations.nominations[id]
	if !ok {
		notFound(w, "nominations", id)
		return
	}
	if *n.Attributes.State == gen.NominationAttributesStateARCHIVED {
		stateError(w, "An archived nomination can't be changed.")
		return
	}
	next := *n.Attributes
	appIDs, territories := n.appIDs, n.territories
	if rel := req.Data.Relationships; rel != nil {
		if rel.RelatedApps != nil && rel.RelatedApps.Data != nil {
			appIDs = nil
			for _, a := range *rel.RelatedApps.Data {
				appIDs = append(appIDs, a.Id)
			}
			if !s.checkNominationApps(w, appIDs) {
				return
			}
		}
		if rel.SupportedTerritories != nil && rel.SupportedTerritories.Data != nil {
			territories = nil
			for _, t := range *rel.SupportedTerritories.Data {
				territories = append(territories, t.Id)
			}
		}
	}
	if in := req.Data.Attributes; in != nil {
		setString(&next.Name, in.Name)
		setString(&next.Description, in.Description)
		if in.Type != nil {
			next.Type = new(gen.NominationAttributesType(*in.Type))
		}
		if in.PublishStartDate != nil {
			next.PublishStartDate = in.PublishStartDate
		}
		if in.PublishEndDate != nil || nulls["publishEndDate"] {
			next.PublishEndDate = in.PublishEndDate
		}
		if in.DeviceFamilies != nil || nulls["deviceFamilies"] {
			next.DeviceFamilies = in.DeviceFamilies
		}
		if in.Locales != nil || nulls["locales"] {
			next.Locales = in.Locales
		}
		if in.SupplementalMaterialsUris != nil || nulls["supplementalMaterialsUris"] {
			next.SupplementalMaterialsUris = in.SupplementalMaterialsUris
		}
		if in.Notes != nil || nulls["notes"] {
			next.Notes = in.Notes
		}
		for _, b := range []struct {
			dst **bool
			src *bool
		}{{&next.HasInAppEvents, in.HasInAppEvents}, {&next.LaunchInSelectMarketsFirst, in.LaunchInSelectMarketsFirst}, {&next.PreOrderEnabled, in.PreOrderEnabled}} {
			if b.src != nil {
				*b.dst = new(*b.src)
			}
		}
		now := gen.DateTime{Time: s.now().UTC().Truncate(time.Second)}
		switch {
		case in.Archived != nil && *in.Archived:
			next.State = new(gen.NominationAttributesStateARCHIVED)
		case in.Submitted != nil && *in.Submitted && *next.State == gen.NominationAttributesStateDRAFT:
			next.State, next.SubmittedDate = new(gen.NominationAttributesStateSUBMITTED), new(now)
		case in.Submitted != nil && !*in.Submitted && *next.State == gen.NominationAttributesStateSUBMITTED:
			stateError(w, "A submitted nomination can't return to draft.")
			return
		}
		next.LastModifiedDate = new(now)
	}
	if !checkPublishDates(w, next.PublishStartDate, next.PublishEndDate) {
		return
	}
	*n.Attributes = next
	n.appIDs, n.territories = appIDs, territories
	writeJSON(w, http.StatusOK, gen.NominationResponse{Data: s.renderNomination(n), Links: docLinks(r)})
}

func (s *Server) NominationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nominations.nominations[id]; !ok {
		notFound(w, "nominations", id)
		return
	}
	delete(s.nominations.nominations, id)
	w.WriteHeader(http.StatusNoContent)
}

// AddSearchKeyword adds a search keyword App Store Connect offers for an
// app's platform and locale, and returns its ID.
func (s *Server) AddSearchKeyword(appID string, platform gen.Platform, locale string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID("K")
	s.nominations.keywords[id] = appKeyword{appID: appID, platform: string(platform), locale: locale}
	return id
}

func keywordsResponse(w http.ResponseWriter, r *http.Request, ids []string, limit *int) {
	var items []gen.AppKeyword
	for _, id := range ids {
		items = append(items, gen.AppKeyword{Id: id, Type: gen.AppKeywordTypeAppKeywords})
	}
	data, links, meta, ok := page(w, r, items, limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppKeywordsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AppsSearchKeywordsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsSearchKeywordsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var ids []string
	for _, kid := range sortedKeys(s.nominations.keywords) {
		k := s.nominations.keywords[kid]
		if k.appID == id && matches(params.FilterPlatform, k.platform) && matches(params.FilterLocale, k.locale) {
			ids = append(ids, kid)
		}
	}
	keywordsResponse(w, r, ids, params.Limit)
}

func (s *Server) AppStoreVersionLocalizationsSearchKeywordsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionLocalizationsSearchKeywordsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, _ := s.findVersionLocalization(id); l == nil {
		notFound(w, "appStoreVersionLocalizations", id)
		return
	}
	var ids []string
	for _, kid := range s.nominations.links[id] {
		k := s.nominations.keywords[kid]
		if matches(params.FilterPlatform, k.platform) && matches(params.FilterLocale, k.locale) {
			ids = append(ids, kid)
		}
	}
	keywordsResponse(w, r, ids, params.Limit)
}

// keywordLinks checks a linkage request against a localization: the
// keywords must be offered for its app, platform and locale.
func (s *Server) keywordLinks(w http.ResponseWriter, r *http.Request, id string) ([]string, bool) {
	var req gen.AppStoreVersionLocalizationSearchKeywordsLinkagesRequest
	if !decode(w, r, &req) {
		return nil, false
	}
	l, v := s.findVersionLocalization(id)
	if l == nil {
		notFound(w, "appStoreVersionLocalizations", id)
		return nil, false
	}
	if !v.editable() {
		stateError(w, "The version cannot be edited in its current state: "+string(*v.Attributes.AppVersionState)+".")
		return nil, false
	}
	var ids []string
	for _, e := range req.Data {
		k, ok := s.nominations.keywords[e.Id]
		if !ok || k.appID != v.appID || k.platform != string(*v.Attributes.Platform) || k.locale != *l.Attributes.Locale {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The keyword '"+e.Id+"' is not offered for this app, platform and locale.", "/data")
			return nil, false
		}
		ids = append(ids, e.Id)
	}
	return ids, true
}

func (s *Server) AppStoreVersionLocalizationsSearchKeywordsCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, ok := s.keywordLinks(w, r, id)
	if !ok {
		return
	}
	for _, kid := range ids {
		if !slices.Contains(s.nominations.links[id], kid) {
			s.nominations.links[id] = append(s.nominations.links[id], kid)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppStoreVersionLocalizationsSearchKeywordsDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, ok := s.keywordLinks(w, r, id)
	if !ok {
		return
	}
	s.nominations.links[id] = slices.DeleteFunc(s.nominations.links[id], func(k string) bool { return slices.Contains(ids, k) })
	w.WriteHeader(http.StatusNoContent)
}
