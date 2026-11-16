package asctest

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Custom product pages. An app has up to 70, each with a unique URL, and a
// page can be disabled or deleted at any time. A page's content belongs to
// versions: the first is created with the page, in PREPARE_FOR_SUBMISSION,
// and an approved page is edited by creating a new version, which replaces
// the current one once App Review approves it
// (https://developer.apple.com/help/app-store-connect/create-custom-product-pages/configure-multiple-product-page-versions/,
// https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appcustomproductpages).
// A version has a localization per locale, whose promotional text can
// change at any time
// (https://developer.apple.com/documentation/appstoreconnectapi/app-custom-product-page-localizations).
// The fake starts a new version with copies of the latest version's
// localizations, as the versions of in-app purchases do.

const (
	maxProductPages        = 70
	productPagesCollection = "appCustomProductPages"
	pageVersionsCollection = "appCustomProductPageVersions"
	pageLocalizationsRoute = "appCustomProductPageLocalizations"
	pageVersionDraft       = gen.AppCustomProductPageVersionAttributesStatePREPAREFORSUBMISSION
	pageVersionReady       = gen.AppCustomProductPageVersionAttributesStateREADYFORREVIEW
	pageVersionWaiting     = gen.AppCustomProductPageVersionAttributesStateWAITINGFORREVIEW
	pageVersionInReview    = gen.AppCustomProductPageVersionAttributesStateINREVIEW
	pageVersionApproved    = gen.AppCustomProductPageVersionAttributesStateAPPROVED
	pageVersionReplaced    = gen.AppCustomProductPageVersionAttributesStateREPLACEDWITHNEWVERSION
)

var pageVersionsUnderReview = []gen.AppCustomProductPageVersionAttributesState{pageVersionReady, pageVersionWaiting, pageVersionInReview}

type productPage struct {
	id, appID, name string
	visible         bool
}

type pageVersion struct {
	id, pageID string
	number     int
	state      gen.AppCustomProductPageVersionAttributesState
	deepLink   *string
}

type pageLocalization struct {
	id, versionID, locale string
	promotionalText       *string
}

type productPagesState struct {
	pages         map[string]*productPage
	versions      map[string]*pageVersion
	localizations map[string]*pageLocalization
}

func newProductPagesState() *productPagesState {
	return &productPagesState{pages: map[string]*productPage{}, versions: map[string]*pageVersion{}, localizations: map[string]*pageLocalization{}}
}

// ---- pages ----

func renderProductPage(p *productPage) gen.AppCustomProductPage {
	out := gen.AppCustomProductPage{Id: p.id, Type: gen.AppCustomProductPageTypeAppCustomProductPages}
	a := alloc(&out.Attributes)
	a.Name, a.Visible = new(p.name), new(p.visible)
	a.Url = new("https://apps.apple.com/us/app/id" + p.appID + "?ppid=" + p.id)
	app := alloc(&alloc(&alloc(&out.Relationships).App).Data)
	app.Id, app.Type = p.appID, "apps"
	return out
}

// pageCreateRequest is the shape the fake reads a page's inline version and
// localizations in.
type pageCreateRequest struct {
	Data struct {
		Relationships struct {
			Versions *struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"appCustomProductPageVersions"`
		} `json:"relationships"`
	} `json:"data"`
	Included []struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		Attributes struct {
			DeepLink        *string `json:"deepLink"`
			Locale          string  `json:"locale"`
			PromotionalText *string `json:"promotionalText"`
		} `json:"attributes"`
	} `json:"included"`
}

// AppCustomProductPagesCreateInstance creates a visible page, with a first
// version and its localizations when the request includes them inline.
func (s *Server) AppCustomProductPagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppCustomProductPageCreateRequest
	var raw pageCreateRequest
	body, err := io.ReadAll(r.Body)
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err == nil {
		err = json.Unmarshal(body, &raw)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ENTITY_UNPROCESSABLE", "The request entity is not valid JSON", err.Error(), "")
		return
	}
	appID := req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	n := 0
	for _, p := range s.pages.pages {
		if p.appID == appID {
			n++
		}
	}
	if n >= maxProductPages {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app already has 70 custom product pages.", "/data/relationships/app")
		return
	}
	p := &productPage{id: s.newID("CPP"), appID: appID, name: req.Data.Attributes.Name, visible: true}
	s.pages.pages[p.id] = p
	if raw.Data.Relationships.Versions != nil && len(raw.Data.Relationships.Versions.Data) > 0 {
		v := &pageVersion{id: s.newID("CPV"), pageID: p.id, number: 1, state: pageVersionDraft}
		s.pages.versions[v.id] = v
		for _, inc := range raw.Included {
			switch inc.Type {
			case pageVersionsCollection:
				v.deepLink = inc.Attributes.DeepLink
			case pageLocalizationsRoute:
				l := &pageLocalization{id: s.newID("CPL"), versionID: v.id, locale: inc.Attributes.Locale, promotionalText: inc.Attributes.PromotionalText}
				s.pages.localizations[l.id] = l
			}
		}
	}
	writeJSON(w, http.StatusCreated, gen.AppCustomProductPageResponse{Data: renderProductPage(p), Links: docLinks(r)})
}

func (s *Server) findProductPage(w http.ResponseWriter, id string) *productPage {
	p, ok := s.pages.pages[id]
	if !ok {
		notFound(w, productPagesCollection, id)
	}
	return p
}

func (s *Server) AppCustomProductPagesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppCustomProductPagesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.findProductPage(w, id); p != nil {
		writeJSON(w, http.StatusOK, gen.AppCustomProductPageResponse{Data: renderProductPage(p), Links: docLinks(r)})
	}
}

func (s *Server) AppCustomProductPagesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppCustomProductPageUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.findProductPage(w, id)
	if p == nil {
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.Name != nil {
			p.name = *in.Name
		}
		if in.Visible != nil {
			p.visible = *in.Visible
		}
	}
	writeJSON(w, http.StatusOK, gen.AppCustomProductPageResponse{Data: renderProductPage(p), Links: docLinks(r)})
}

// AppCustomProductPagesDeleteInstance deletes a page with its versions and
// their localizations.
func (s *Server) AppCustomProductPagesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findProductPage(w, id) == nil {
		return
	}
	for vid, v := range s.pages.versions {
		if v.pageID != id {
			continue
		}
		for _, l := range s.pageLocalizations(vid) {
			s.removePageLocalization(l.id)
		}
		delete(s.pages.versions, vid)
	}
	delete(s.pages.pages, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsAppCustomProductPagesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppCustomProductPagesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppCustomProductPage
	for _, p := range sortedValues(s.pages.pages) {
		if p.appID == id && (params.FilterVisible == nil || slices.Contains(*params.FilterVisible, strconv.FormatBool(p.visible))) {
			items = append(items, renderProductPage(p))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppCustomProductPagesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- versions ----

func renderPageVersion(v *pageVersion) gen.AppCustomProductPageVersion {
	out := gen.AppCustomProductPageVersion{Id: v.id, Type: gen.AppCustomProductPageVersionTypeAppCustomProductPageVersions}
	a := alloc(&out.Attributes)
	a.Version, a.State, a.DeepLink = new(strconv.Itoa(v.number)), new(v.state), v.deepLink
	p := alloc(&alloc(&alloc(&out.Relationships).AppCustomProductPage).Data)
	p.Id, p.Type = v.pageID, productPagesCollection
	return out
}

// pageVersions returns a page's versions, oldest first.
func (s *Server) pageVersions(pageID string) []*pageVersion {
	var out []*pageVersion
	for _, v := range s.pages.versions {
		if v.pageID == pageID {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b *pageVersion) int { return a.number - b.number })
	return out
}

func (s *Server) AppCustomProductPagesAppCustomProductPageVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppCustomProductPagesAppCustomProductPageVersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findProductPage(w, id) == nil {
		return
	}
	var items []gen.AppCustomProductPageVersion
	for _, v := range s.pageVersions(id) {
		if matches(params.FilterState, string(v.state)) {
			items = append(items, renderPageVersion(v))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppCustomProductPageVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// AppCustomProductPageVersionsCreateInstance starts a page's next version,
// unless one is being prepared or reviewed.
func (s *Server) AppCustomProductPageVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppCustomProductPageVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	pageID := req.Data.Relationships.AppCustomProductPage.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pages.pages[pageID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appCustomProductPages with id '"+pageID+"'", "/data/relationships/appCustomProductPage")
		return
	}
	versions := s.pageVersions(pageID)
	v := &pageVersion{id: s.newID("CPV"), pageID: pageID, number: 1, state: pageVersionDraft}
	if len(versions) > 0 {
		latest := versions[len(versions)-1]
		if latest.state == pageVersionDraft || slices.Contains(pageVersionsUnderReview, latest.state) {
			stateError(w, "The custom product page already has a version being prepared or reviewed.")
			return
		}
		v.number, v.deepLink = latest.number+1, latest.deepLink
		for _, l := range s.pageLocalizations(latest.id) {
			c := &pageLocalization{id: s.newID("CPL"), versionID: v.id, locale: l.locale, promotionalText: l.promotionalText}
			s.pages.localizations[c.id] = c
		}
	}
	if req.Data.Attributes != nil && req.Data.Attributes.DeepLink != nil {
		v.deepLink = req.Data.Attributes.DeepLink
	}
	s.pages.versions[v.id] = v
	writeJSON(w, http.StatusCreated, gen.AppCustomProductPageVersionResponse{Data: renderPageVersion(v), Links: docLinks(r)})
}

// AppCustomProductPageVersionsUpdateInstance sets or clears the deep link
// of a version being prepared.
func (s *Server) AppCustomProductPageVersionsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppCustomProductPageVersionUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.pages.versions[id]
	if !ok {
		notFound(w, pageVersionsCollection, id)
		return
	}
	if v.state != pageVersionDraft {
		stateError(w, "The custom product page version can't be edited in its current state: "+string(v.state)+".")
		return
	}
	if in := req.Data.Attributes; in != nil && (in.DeepLink != nil || nulls["deepLink"]) {
		v.deepLink = in.DeepLink
	}
	writeJSON(w, http.StatusOK, gen.AppCustomProductPageVersionResponse{Data: renderPageVersion(v), Links: docLinks(r)})
}

// addPageVersionItem adds a version being prepared to a review submission
// of its app. The version needs a localization.
func (s *Server) addPageVersionItem(w http.ResponseWriter, sub *reviewSubmission, versionID string) bool {
	v, ok := s.pages.versions[versionID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appCustomProductPageVersions with id '"+versionID+"'", "/data/relationships/appCustomProductPageVersion")
		return false
	}
	if s.pages.pages[v.pageID].appID != sub.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The custom product page belongs to another app.", "/data/relationships/appCustomProductPageVersion")
		return false
	}
	if v.state != pageVersionDraft {
		stateError(w, "The custom product page version is already in review or approved.")
		return false
	}
	if len(s.pageLocalizations(versionID)) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The custom product page version has no localizations.", "/data/relationships/appCustomProductPageVersion")
		return false
	}
	v.state = pageVersionReady
	sub.pageVersions = append(sub.pageVersions, versionID)
	return true
}

func (s *Server) setPageVersionStates(sub *reviewSubmission, state gen.AppCustomProductPageVersionAttributesState) {
	for _, id := range sub.pageVersions {
		if v, ok := s.pages.versions[id]; ok {
			v.state = state
		}
	}
}

// ApproveCustomProductPage approves a page's latest version, as App Review
// does, replacing the version approved before.
func (s *Server) ApproveCustomProductPage(pageID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.pageVersions(pageID)
	for _, v := range versions {
		if v.state == pageVersionApproved {
			v.state = pageVersionReplaced
		}
	}
	versions[len(versions)-1].state = pageVersionApproved
}

// CustomProductPageVersionStates returns the states of a page's versions,
// oldest first.
func (s *Server) CustomProductPageVersionStates(pageID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, v := range s.pageVersions(pageID) {
		out = append(out, string(v.state))
	}
	return out
}

// ---- localizations ----

func (s *Server) pageLocalizations(versionID string) []*pageLocalization {
	var out []*pageLocalization
	for _, l := range sortedValues(s.pages.localizations) {
		if l.versionID == versionID {
			out = append(out, l)
		}
	}
	return out
}

// removePageLocalization deletes a localization with its keyword links and
// placements.
func (s *Server) removePageLocalization(id string) {
	for pid, p := range s.lib.placements {
		if p.targetKind == productPageTarget && p.targetID == id {
			key := placementKey(p.targetID, p.group)
			s.lib.order[key] = slices.DeleteFunc(s.lib.order[key], func(other string) bool { return other == pid })
			delete(s.lib.placements, pid)
		}
	}
	delete(s.nominations.links, id)
	delete(s.pages.localizations, id)
}

func renderPageLocalization(l *pageLocalization) gen.AppCustomProductPageLocalization {
	out := gen.AppCustomProductPageLocalization{Id: l.id, Type: gen.AppCustomProductPageLocalizationTypeAppCustomProductPageLocalizations}
	a := alloc(&out.Attributes)
	a.Locale, a.PromotionalText = new(l.locale), l.promotionalText
	v := alloc(&alloc(&alloc(&out.Relationships).AppCustomProductPageVersion).Data)
	v.Id, v.Type = l.versionID, pageVersionsCollection
	return out
}

// draftPageVersion writes an error and returns false unless the version is
// being prepared.
func (s *Server) draftPageVersion(w http.ResponseWriter, versionID string) bool {
	if v := s.pages.versions[versionID]; v.state != pageVersionDraft {
		stateError(w, "The custom product page version can't be edited in its current state: "+string(v.state)+".")
		return false
	}
	return true
}

func (s *Server) AppCustomProductPageLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppCustomProductPageLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, versionID := req.Data.Attributes, req.Data.Relationships.AppCustomProductPageVersion.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pages.versions[versionID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appCustomProductPageVersions with id '"+versionID+"'", "/data/relationships/appCustomProductPageVersion")
		return
	}
	if !s.draftPageVersion(w, versionID) {
		return
	}
	for _, l := range s.pageLocalizations(versionID) {
		if l.locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The version already has a localization for "+in.Locale+".", "/data/attributes/locale")
			return
		}
	}
	l := &pageLocalization{id: s.newID("CPL"), versionID: versionID, locale: in.Locale, promotionalText: in.PromotionalText}
	s.pages.localizations[l.id] = l
	writeJSON(w, http.StatusCreated, gen.AppCustomProductPageLocalizationResponse{Data: renderPageLocalization(l), Links: docLinks(r)})
}

// AppCustomProductPageLocalizationsUpdateInstance changes the promotional
// text, which any version allows; an empty string or null clears it.
func (s *Server) AppCustomProductPageLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppCustomProductPageLocalizationUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.pages.localizations[id]
	if !ok {
		notFound(w, pageLocalizationsRoute, id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		switch {
		case nulls["promotionalText"], in.PromotionalText != nil && *in.PromotionalText == "":
			l.promotionalText = nil
		case in.PromotionalText != nil:
			l.promotionalText = in.PromotionalText
		}
	}
	writeJSON(w, http.StatusOK, gen.AppCustomProductPageLocalizationResponse{Data: renderPageLocalization(l), Links: docLinks(r)})
}

func (s *Server) AppCustomProductPageLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.pages.localizations[id]
	if !ok {
		notFound(w, pageLocalizationsRoute, id)
		return
	}
	if !s.draftPageVersion(w, l.versionID) {
		return
	}
	s.removePageLocalization(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppCustomProductPageVersionsAppCustomProductPageLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppCustomProductPageVersionsAppCustomProductPageLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pages.versions[id]; !ok {
		notFound(w, pageVersionsCollection, id)
		return
	}
	var items []gen.AppCustomProductPageLocalization
	for _, l := range s.pageLocalizations(id) {
		if matches(params.FilterLocale, l.locale) {
			items = append(items, renderPageLocalization(l))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppCustomProductPageLocalizationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- search keywords ----

func (s *Server) AppCustomProductPageLocalizationsSearchKeywordsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppCustomProductPageLocalizationsSearchKeywordsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pages.localizations[id]; !ok {
		notFound(w, pageLocalizationsRoute, id)
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

// pageKeywordLinks checks a linkage request against a page localization:
// the keywords must be offered for its app and locale.
func (s *Server) pageKeywordLinks(w http.ResponseWriter, r *http.Request, id string) ([]string, bool) {
	var req gen.AppCustomProductPageLocalizationSearchKeywordsLinkagesRequest
	if !decode(w, r, &req) {
		return nil, false
	}
	l, ok := s.pages.localizations[id]
	if !ok {
		notFound(w, pageLocalizationsRoute, id)
		return nil, false
	}
	appID := s.pages.pages[s.pages.versions[l.versionID].pageID].appID
	var ids []string
	for _, e := range req.Data {
		k, ok := s.nominations.keywords[e.Id]
		if !ok || k.appID != appID || k.locale != l.locale {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The keyword '"+e.Id+"' is not offered for this app and locale.", "/data")
			return nil, false
		}
		ids = append(ids, e.Id)
	}
	return ids, true
}

func (s *Server) AppCustomProductPageLocalizationsSearchKeywordsCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, ok := s.pageKeywordLinks(w, r, id)
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

func (s *Server) AppCustomProductPageLocalizationsSearchKeywordsDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, ok := s.pageKeywordLinks(w, r, id)
	if !ok {
		return
	}
	s.nominations.links[id] = slices.DeleteFunc(s.nominations.links[id], func(k string) bool { return slices.Contains(ids, k) })
	w.WriteHeader(http.StatusNoContent)
}
