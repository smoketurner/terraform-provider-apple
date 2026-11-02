package asctest

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// App info: the app metadata that applies across versions. "If the app has
// both a 'Ready for Sale' version and a version you're preparing for release,
// it will have two app infos"; the one for the next version is the one to
// edit (https://developer.apple.com/documentation/appstoreconnectapi/get-v1-apps-_id_-appinfos).
// Metadata can be edited only while the version's status is editable ("App
// and submission statuses",
// https://developer.apple.com/help/app-store-connect/reference/app-information/app-and-submission-statuses).

type appInfo struct {
	gen.AppInfo
	appID         string
	categories    map[string]string // relationship name to category ID
	localizations map[string]*gen.AppInfoLocalization
	ageRating     *gen.AgeRatingDeclaration
}

// editableStates are the app info states whose metadata can be changed.
var editableStates = []gen.AppInfoAttributesState{
	gen.AppInfoAttributesStatePREPAREFORSUBMISSION, gen.AppInfoAttributesStateREADYFORREVIEW,
	gen.AppInfoAttributesStateWAITINGFORREVIEW, gen.AppInfoAttributesStateREJECTED, gen.AppInfoAttributesStateDEVELOPERREJECTED,
}

func (i *appInfo) editable() bool {
	return slices.Contains(editableStates, *i.Attributes.State)
}

// categoryRelationships are the app info's category relationships.
var categoryRelationships = []string{
	"primaryCategory", "primarySubcategoryOne", "primarySubcategoryTwo",
	"secondaryCategory", "secondarySubcategoryOne", "secondarySubcategoryTwo",
}

// appCategories is the fake's category catalog: categories and their
// subcategories.
var appCategories = map[string][]string{
	"BUSINESS":     nil,
	"GAMES":        {"GAMES_ACTION", "GAMES_PUZZLE"},
	"PRODUCTIVITY": nil,
	"UTILITIES":    nil,
}

func categoryParent(id string) (string, bool) {
	for parent, subs := range appCategories {
		if id == parent {
			return "", true
		}
		if slices.Contains(subs, id) {
			return parent, true
		}
	}
	return "", false
}

// newAppInfo creates an app info in state, with a default age rating
// declaration.
func (s *Server) newAppInfo(appID string, state gen.AppInfoAttributesState) *appInfo {
	info := &appInfo{appID: appID, categories: map[string]string{}, localizations: map[string]*gen.AppInfoLocalization{},
		AppInfo: gen.AppInfo{Id: s.newID("F"), Type: gen.AppInfoTypeAppInfos}}
	alloc(&info.Attributes).State = new(state)
	info.ageRating = &gen.AgeRatingDeclaration{Id: s.newID("Q"), Type: gen.AgeRatingDeclarationTypeAgeRatingDeclarations}
	alloc(&info.ageRating.Attributes)
	s.appInfos[info.Id] = info
	return info
}

// ReleaseApp makes the app's editable app info the live one, as releasing a
// version does.
func (s *Server) ReleaseApp(appID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, info := range s.appInfos {
		if info.appID != appID {
			continue
		}
		switch {
		case *info.Attributes.State == gen.AppInfoAttributesStateREADYFORDISTRIBUTION:
			delete(s.appInfos, id)
		case info.editable():
			info.Attributes.State = new(gen.AppInfoAttributesStateREADYFORDISTRIBUTION)
		}
	}
}

// PrepareVersion creates an editable app info for the app's next version,
// copied from the live one, as creating a version does. Its localizations
// and age rating declaration get new IDs.
func (s *Server) PrepareVersion(appID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var live *appInfo
	for _, info := range s.appInfos {
		if info.appID != appID {
			continue
		}
		if info.editable() {
			return
		}
		live = info
	}
	next := s.newAppInfo(appID, gen.AppInfoAttributesStatePREPAREFORSUBMISSION)
	if live == nil {
		return
	}
	for k, v := range live.categories {
		next.categories[k] = v
	}
	attrs := *live.ageRating.Attributes
	next.ageRating.Attributes = &attrs
	for _, l := range live.localizations {
		c := &gen.AppInfoLocalization{Id: s.newID("H"), Type: gen.AppInfoLocalizationTypeAppInfoLocalizations}
		a := *l.Attributes
		c.Attributes = &a
		next.localizations[c.Id] = c
	}
}

func (s *Server) renderAppInfo(r *http.Request, info *appInfo, withRelationships bool) gen.AppInfo {
	out := info.AppInfo
	attrs := *info.Attributes
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "appInfos", info.Id)
	out.Relationships = nil
	if !withRelationships {
		return out
	}
	rel := alloc(&out.Relationships)
	// The relationship fields have distinct generated types, so they are
	// filled through JSON.
	raw := map[string]any{}
	for _, name := range categoryRelationships {
		if id, ok := info.categories[name]; ok {
			raw[name] = map[string]any{"data": map[string]string{"type": "appCategories", "id": id}}
		} else {
			raw[name] = map[string]any{"data": nil}
		}
	}
	raw["ageRatingDeclaration"] = map[string]any{"data": map[string]string{"type": "ageRatingDeclarations", "id": info.ageRating.Id}}
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, rel)
	return out
}

func (s *Server) AppsAppInfosGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppInfosGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppInfo
	for _, info := range sortedValues(s.appInfos) {
		if info.appID == id {
			items = append(items, s.renderAppInfo(r, info, params.Include != nil))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppInfosResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func stateError(w http.ResponseWriter, detail string) {
	writeError(w, http.StatusConflict, "STATE_ERROR", "The request cannot be fulfilled because of the state of another resource.", detail, "")
}

// AppInfosUpdateInstance sets the app's categories
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-appinfos-_id_).
// A subcategory must belong to its category, and needs one.
func (s *Server) AppInfosUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Data struct {
			ID            string `json:"id"`
			Relationships map[string]struct {
				Data *struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"relationships"`
		} `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.appInfos[id]
	if !ok {
		notFound(w, "appInfos", id)
		return
	}
	if !info.editable() {
		stateError(w, "The app info cannot be edited in its current state: "+string(*info.Attributes.State)+".")
		return
	}
	next := map[string]string{}
	for k, v := range info.categories {
		next[k] = v
	}
	for name, rel := range req.Data.Relationships {
		if !slices.Contains(categoryRelationships, name) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "Unknown relationship "+name, "/data/relationships/"+name)
			return
		}
		if rel.Data == nil {
			delete(next, name)
			continue
		}
		next[name] = rel.Data.ID
	}
	for _, group := range [][3]string{{"primaryCategory", "primarySubcategoryOne", "primarySubcategoryTwo"}, {"secondaryCategory", "secondarySubcategoryOne", "secondarySubcategoryTwo"}} {
		category := next[group[0]]
		if parent, ok := categoryParent(category); category != "" && (!ok || parent != "") {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "'"+category+"' is not a category.", "/data/relationships/"+group[0])
			return
		}
		for _, sub := range group[1:] {
			if next[sub] == "" {
				continue
			}
			if parent, ok := categoryParent(next[sub]); !ok || parent == "" || parent != category {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "'"+next[sub]+"' is not a subcategory of '"+category+"'.", "/data/relationships/"+sub)
				return
			}
		}
	}
	info.categories = next
	writeJSON(w, http.StatusOK, gen.AppInfoResponse{Data: s.renderAppInfo(r, info, true), Links: docLinks(r)})
}

func (s *Server) AppCategoriesGetCollection(w http.ResponseWriter, r *http.Request, params gen.AppCategoriesGetCollectionParams) {
	var items []gen.AppCategory
	for _, parent := range sortedKeys(appCategories) {
		ids := append([]string{parent}, appCategories[parent]...)
		for _, id := range ids {
			isParent := id == parent
			if params.ExistsParent != nil && *params.ExistsParent == isParent {
				continue
			}
			c := gen.AppCategory{Id: id, Type: gen.AppCategoryTypeAppCategories}
			alloc(&c.Attributes).Platforms = &[]gen.Platform{gen.PlatformIOS, gen.PlatformMACOS}
			rel := alloc(&c.Relationships)
			if !isParent {
				d := alloc(&alloc(&rel.Parent).Data)
				d.Id, d.Type = parent, "appCategories"
			}
			for _, sub := range appCategories[parent] {
				if isParent {
					e := asc.Append(&alloc(&rel.Subcategories).Data)
					e.Id, e.Type = sub, "appCategories"
				}
			}
			items = append(items, c)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppCategoriesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// App info localizations: name, subtitle and privacy information per locale.

func (s *Server) AppInfosAppInfoLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppInfosAppInfoLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.appInfos[id]
	if !ok {
		notFound(w, "appInfos", id)
		return
	}
	var items []gen.AppInfoLocalization
	for _, l := range sortedValues(info.localizations) {
		if matches(params.FilterLocale, asc.Value(l.Attributes.Locale)) {
			item := *l
			item.Links = resourceLinks(r, "appInfoLocalizations", l.Id)
			items = append(items, item)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppInfoLocalizationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// findLocalization returns an app info localization and its app info.
func (s *Server) findLocalization(id string) (*gen.AppInfoLocalization, *appInfo) {
	for _, info := range s.appInfos {
		if l, ok := info.localizations[id]; ok {
			return l, info
		}
	}
	return nil, nil
}

func (s *Server) AppInfoLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppInfoLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	infoID := req.Data.Relationships.AppInfo.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.appInfos[infoID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appInfos with id '"+infoID+"'", "/data/relationships/appInfo")
		return
	}
	if !info.editable() {
		stateError(w, "The app info cannot be edited in its current state: "+string(*info.Attributes.State)+".")
		return
	}
	for _, l := range info.localizations {
		if *l.Attributes.Locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A localization for locale "+in.Locale+" already exists.", "/data/attributes/locale")
			return
		}
	}
	l := &gen.AppInfoLocalization{Id: s.newID("H"), Type: gen.AppInfoLocalizationTypeAppInfoLocalizations}
	a := alloc(&l.Attributes)
	a.Locale, a.Name, a.Subtitle = new(in.Locale), new(in.Name), in.Subtitle
	a.PrivacyPolicyUrl, a.PrivacyChoicesUrl, a.PrivacyPolicyText = in.PrivacyPolicyUrl, in.PrivacyChoicesUrl, in.PrivacyPolicyText
	info.localizations[l.Id] = l
	writeJSON(w, http.StatusCreated, gen.AppInfoLocalizationResponse{Data: *l, Links: docLinks(r)})
}

func (s *Server) AppInfoLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppInfoLocalizationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, info := s.findLocalization(id)
	if l == nil {
		notFound(w, "appInfoLocalizations", id)
		return
	}
	if !info.editable() {
		stateError(w, "The app info cannot be edited in its current state: "+string(*info.Attributes.State)+".")
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := l.Attributes
		if in.Name != nil {
			a.Name = new(*in.Name)
		}
		setString(&a.Subtitle, in.Subtitle)
		setString(&a.PrivacyPolicyUrl, in.PrivacyPolicyUrl)
		setString(&a.PrivacyChoicesUrl, in.PrivacyChoicesUrl)
		setString(&a.PrivacyPolicyText, in.PrivacyPolicyText)
	}
	writeJSON(w, http.StatusOK, gen.AppInfoLocalizationResponse{Data: *l, Links: docLinks(r)})
}

func (s *Server) AppInfoLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, info := s.findLocalization(id)
	if l == nil {
		notFound(w, "appInfoLocalizations", id)
		return
	}
	if !info.editable() {
		stateError(w, "The app info cannot be edited in its current state: "+string(*info.Attributes.State)+".")
		return
	}
	delete(info.localizations, id)
	w.WriteHeader(http.StatusNoContent)
}

// Age rating declarations: "Every App Store version has an age rating
// declaration"; requests include only the attributes being changed
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-ageratingdeclarations-_id_).

func (s *Server) AppInfosAgeRatingDeclarationGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppInfosAgeRatingDeclarationGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.appInfos[id]
	if !ok {
		notFound(w, "appInfos", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AgeRatingDeclarationResponse{Data: *info.ageRating, Links: docLinks(r)})
}

func (s *Server) AgeRatingDeclarationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Data struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var info *appInfo
	for _, i := range s.appInfos {
		if i.ageRating.Id == id {
			info = i
		}
	}
	if info == nil {
		notFound(w, "ageRatingDeclarations", id)
		return
	}
	if !info.editable() {
		stateError(w, "The age rating cannot be edited in its current state: "+string(*info.Attributes.State)+".")
		return
	}
	// Merge the given attributes into the stored ones, validating them
	// against the generated type.
	current, _ := json.Marshal(info.ageRating.Attributes)
	merged := map[string]json.RawMessage{}
	_ = json.Unmarshal(current, &merged)
	for k, v := range req.Data.Attributes {
		merged[k] = v
	}
	b, _ := json.Marshal(merged)
	next := *info.ageRating
	next.Attributes = nil
	if err := json.Unmarshal([]byte(`{"attributes":`+string(b)+`}`), &next); err != nil {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", err.Error(), "/data/attributes")
		return
	}
	*info.ageRating = next
	writeJSON(w, http.StatusOK, gen.AgeRatingDeclarationResponse{Data: next, Links: docLinks(r)})
}
