package asctest

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// App Store versions. A new app record comes with its first version, in
// Prepare for Submission. A version's metadata can be edited only in the
// editable states of "App and submission statuses"
// (https://developer.apple.com/help/app-store-connect/reference/app-information/app-and-submission-statuses);
// removing a submission from review makes it Developer Rejected; an approved
// version with a manual release waits in Pending Developer Release until a
// release request
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appstoreversionreleaserequests).

type version struct {
	gen.AppStoreVersion
	appID         string
	buildID       string
	localizations map[string]*gen.AppStoreVersionLocalization
	reviewDetail  *gen.AppStoreReviewDetail
	phased        *gen.AppStoreVersionPhasedRelease
}

type reviewSubmission struct {
	gen.ReviewSubmission
	appID    string
	versions []string
}

type versionsState struct {
	versions     map[string]*version
	submissions  map[string]*reviewSubmission
	declarations map[string]*encryptionDeclaration
}

type encryptionDeclaration struct {
	gen.AppEncryptionDeclaration
	appID string
}

func newVersionsState() *versionsState {
	return &versionsState{versions: map[string]*version{}, submissions: map[string]*reviewSubmission{}, declarations: map[string]*encryptionDeclaration{}}
}

var editableVersionStates = []gen.AppVersionState{
	gen.AppVersionStatePREPAREFORSUBMISSION, gen.AppVersionStateREADYFORREVIEW, gen.AppVersionStateWAITINGFORREVIEW,
	gen.AppVersionStateREJECTED, gen.AppVersionStateMETADATAREJECTED, gen.AppVersionStateDEVELOPERREJECTED, gen.AppVersionStateINVALIDBINARY,
}

func (v *version) editable() bool {
	return slices.Contains(editableVersionStates, *v.Attributes.AppVersionState)
}

// setState sets the version's state, the matching App Store state, and the
// state of the app's editable app info.
func (s *Server) setVersionState(v *version, state gen.AppVersionState) {
	v.Attributes.AppVersionState = new(state)
	store := gen.AppStoreVersionState(state)
	switch state {
	case gen.AppVersionStateREADYFORDISTRIBUTION:
		store = gen.AppStoreVersionStateREADYFORSALE
	case gen.AppVersionStatePROCESSINGFORDISTRIBUTION:
		store = gen.AppStoreVersionStatePROCESSINGFORAPPSTORE
	}
	v.Attributes.AppStoreState = new(store)
	if state == gen.AppVersionStateREADYFORDISTRIBUTION {
		s.releaseAppInfo(v.appID)
		for _, other := range s.versions.versions {
			if other != v && other.appID == v.appID && *other.Attributes.Platform == *v.Attributes.Platform &&
				*other.Attributes.AppVersionState == gen.AppVersionStateREADYFORDISTRIBUTION {
				other.Attributes.AppVersionState = new(gen.AppVersionStateREPLACEDWITHNEWVERSION)
				other.Attributes.AppStoreState = new(gen.AppStoreVersionStateREPLACEDWITHNEWVERSION)
			}
		}
		return
	}
	for _, info := range s.appInfos {
		if info.appID == v.appID && info.editable() || info.appID == v.appID && *info.Attributes.State == gen.AppInfoAttributesStatePENDINGRELEASE {
			info.Attributes.State = new(appInfoStateFor(state))
		}
	}
}

func appInfoStateFor(state gen.AppVersionState) gen.AppInfoAttributesState {
	switch state {
	case gen.AppVersionStatePENDINGDEVELOPERRELEASE:
		return gen.AppInfoAttributesStatePENDINGRELEASE
	case gen.AppVersionStateINVALIDBINARY, gen.AppVersionStateMETADATAREJECTED:
		return gen.AppInfoAttributesStateREJECTED
	}
	if slices.Contains([]gen.AppInfoAttributesState{gen.AppInfoAttributesStatePREPAREFORSUBMISSION, gen.AppInfoAttributesStateREADYFORREVIEW,
		gen.AppInfoAttributesStateWAITINGFORREVIEW, gen.AppInfoAttributesStateREJECTED, gen.AppInfoAttributesStateDEVELOPERREJECTED,
		gen.AppInfoAttributesStateINREVIEW}, gen.AppInfoAttributesState(state)) {
		return gen.AppInfoAttributesState(state)
	}
	return gen.AppInfoAttributesStatePREPAREFORSUBMISSION
}

// releaseAppInfo makes the app's editable app info the live one.
func (s *Server) releaseAppInfo(appID string) {
	for id, info := range s.appInfos {
		if info.appID != appID {
			continue
		}
		switch {
		case *info.Attributes.State == gen.AppInfoAttributesStateREADYFORDISTRIBUTION:
			delete(s.appInfos, id)
		case info.editable() || *info.Attributes.State == gen.AppInfoAttributesStatePENDINGRELEASE:
			info.Attributes.State = new(gen.AppInfoAttributesStateREADYFORDISTRIBUTION)
		}
	}
}

// newVersion adds a version in Prepare for Submission, with copies of the
// platform's latest version's localizations, and an editable app info.
func (s *Server) newVersion(appID string, platform gen.Platform, versionString string) *version {
	v := &version{appID: appID, localizations: map[string]*gen.AppStoreVersionLocalization{},
		AppStoreVersion: gen.AppStoreVersion{Id: s.newID("R"), Type: gen.AppStoreVersionTypeAppStoreVersions}}
	a := alloc(&v.Attributes)
	a.Platform, a.VersionString = new(platform), new(versionString)
	a.ReleaseType = new(gen.AppStoreVersionAttributesReleaseTypeAFTERAPPROVAL)
	a.ReviewType = new(gen.AppStoreVersionAttributesReviewTypeAPPSTORE)
	a.CreatedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	a.Downloadable = new(true)
	var previous *version
	for _, other := range sortedValues(s.versions.versions) {
		if other.appID == appID && *other.Attributes.Platform == platform {
			previous = other
		}
	}
	if previous != nil {
		for _, l := range previous.localizations {
			c := &gen.AppStoreVersionLocalization{Id: s.newID("O"), Type: gen.AppStoreVersionLocalizationTypeAppStoreVersionLocalizations}
			attrs := *l.Attributes
			attrs.WhatsNew = nil
			c.Attributes = &attrs
			v.localizations[c.Id] = c
		}
	} else {
		l := &gen.AppStoreVersionLocalization{Id: s.newID("O"), Type: gen.AppStoreVersionLocalizationTypeAppStoreVersionLocalizations}
		alloc(&l.Attributes).Locale = new("en-US")
		v.localizations[l.Id] = l
	}
	s.versions.versions[v.Id] = v
	s.setVersionState(v, gen.AppVersionStatePREPAREFORSUBMISSION)
	hasEditable := false
	for _, info := range s.appInfos {
		hasEditable = hasEditable || info.appID == appID && info.editable()
	}
	if !hasEditable {
		s.prepareAppInfo(appID)
	}
	return v
}

// renderVersion renders a version, with its app relationship when
// withApp, as "include=app" asks.
func (s *Server) renderVersion(r *http.Request, v *version, withApp bool) gen.AppStoreVersion {
	out := v.AppStoreVersion
	attrs := *v.Attributes
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "appStoreVersions", v.Id)
	out.Relationships = nil
	if withApp {
		rel := alloc(&out.Relationships)
		app := alloc(&rel.App)
		data := alloc(&app.Data)
		data.Id, data.Type = v.appID, "apps"
	}
	return out
}

func (s *Server) AppsAppStoreVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppStoreVersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppStoreVersion
	for _, v := range sortedValues(s.versions.versions) {
		a := v.Attributes
		if v.appID == id && matches(params.FilterId, v.Id) && matches(params.FilterPlatform, string(*a.Platform)) &&
			matches(params.FilterVersionString, *a.VersionString) && matches(params.FilterAppVersionState, string(*a.AppVersionState)) &&
			matches(params.FilterAppStoreState, string(*a.AppStoreState)) {
			items = append(items, s.renderVersion(r, v, false))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// AppStoreVersionsCreateInstance adds a version
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appstoreversions).
// A platform can have one version in preparation at a time.
func (s *Server) AppStoreVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	appID := req.Data.Relationships.App.Data.Id
	if !in.Platform.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "platform is invalid", "/data/attributes/platform")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	for _, v := range s.versions.versions {
		if v.appID != appID || *v.Attributes.Platform != in.Platform {
			continue
		}
		if v.editable() || !slices.Contains([]gen.AppVersionState{gen.AppVersionStateREADYFORDISTRIBUTION, gen.AppVersionStateREPLACEDWITHNEWVERSION}, *v.Attributes.AppVersionState) {
			stateError(w, "You cannot create a new version of the app in the current state.")
			return
		}
		if *v.Attributes.VersionString == in.VersionString {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The version number has been previously used.", "/data/attributes/versionString")
			return
		}
	}
	v := s.newVersion(appID, in.Platform, in.VersionString)
	a := v.Attributes
	a.Copyright, a.EarliestReleaseDate = in.Copyright, in.EarliestReleaseDate
	if in.ReleaseType != nil {
		a.ReleaseType = new(gen.AppStoreVersionAttributesReleaseType(*in.ReleaseType))
	}
	if in.ReviewType != nil {
		a.ReviewType = new(gen.AppStoreVersionAttributesReviewType(*in.ReviewType))
	}
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionResponse{Data: s.renderVersion(r, v, false), Links: docLinks(r)})
}

func (s *Server) AppStoreVersionsGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions.versions[id]
	if !ok {
		notFound(w, "appStoreVersions", id)
		return
	}
	withApp := params.Include != nil && slices.Contains(*params.Include, gen.AppStoreVersionsGetInstanceParamsIncludeApp)
	writeJSON(w, http.StatusOK, gen.AppStoreVersionResponse{Data: s.renderVersion(r, v, withApp), Links: docLinks(r)})
}

func (s *Server) editableVersion(w http.ResponseWriter, id string) *version {
	v, ok := s.versions.versions[id]
	if !ok {
		notFound(w, "appStoreVersions", id)
		return nil
	}
	if !v.editable() {
		stateError(w, "The version cannot be edited in its current state: "+string(*v.Attributes.AppVersionState)+".")
		return nil
	}
	return v
}

// versionUsed reports whether another version of v's app and platform has
// versionString.
func (s *Server) versionUsed(v *version, versionString string) bool {
	for _, other := range s.versions.versions {
		if other != v && other.appID == v.appID && *other.Attributes.Platform == *v.Attributes.Platform && *other.Attributes.VersionString == versionString {
			return true
		}
	}
	return false
}

func (s *Server) AppStoreVersionsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreVersionUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableVersion(w, id)
	if v == nil {
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.VersionString != nil && s.versionUsed(v, *in.VersionString) {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The version number has been previously used.", "/data/attributes/versionString")
			return
		}
		a := v.Attributes
		setString(&a.Copyright, in.Copyright)
		if in.VersionString != nil {
			a.VersionString = new(*in.VersionString)
		}
		if in.EarliestReleaseDate != nil {
			a.EarliestReleaseDate = new(*in.EarliestReleaseDate)
		}
		if in.ReleaseType != nil {
			a.ReleaseType = new(gen.AppStoreVersionAttributesReleaseType(*in.ReleaseType))
		}
		if in.ReviewType != nil {
			a.ReviewType = new(gen.AppStoreVersionAttributesReviewType(*in.ReviewType))
		}
		if in.Downloadable != nil {
			a.Downloadable = new(*in.Downloadable)
		}
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionResponse{Data: s.renderVersion(r, v, false), Links: docLinks(r)})
}

// AppStoreVersionsDeleteInstance deletes a version that was never submitted.
func (s *Server) AppStoreVersionsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions.versions[id]
	if !ok {
		notFound(w, "appStoreVersions", id)
		return
	}
	if *v.Attributes.AppVersionState != gen.AppVersionStatePREPAREFORSUBMISSION {
		stateError(w, "Only a version in Prepare for Submission can be deleted.")
		return
	}
	delete(s.versions.versions, id)
	w.WriteHeader(http.StatusNoContent)
}

// AppStoreVersionsBuildUpdateToOneRelationship chooses the version's build
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-appstoreversions-_id_-relationships-build),
// which must be a valid, unexpired build of the app for the platform.
func (s *Server) AppStoreVersionsBuildUpdateToOneRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreVersionBuildLinkageRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableVersion(w, id)
	if v == nil {
		return
	}
	b, ok := s.tf.builds[req.Data.Id]
	if !ok || b.appID != v.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no build '"+req.Data.Id+"' of this app.", "/data")
		return
	}
	if *b.Attributes.ProcessingState != gen.BuildAttributesProcessingStateVALID || *b.Attributes.Expired {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The build is not valid for App Store submission.", "/data")
		return
	}
	if p := s.tf.preReleaseVersions[b.preReleaseID]; *p.Attributes.Platform != *v.Attributes.Platform {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The build is for another platform.", "/data")
		return
	}
	v.buildID = b.Id
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppStoreVersionsBuildGetToOneRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions.versions[id]
	if !ok {
		notFound(w, "appStoreVersions", id)
		return
	}
	if v.buildID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": nil, "links": docLinks(r)})
		return
	}
	resp := gen.AppStoreVersionBuildLinkageResponse{Links: docLinks(r)}
	resp.Data.Id, resp.Data.Type = v.buildID, "builds"
	writeJSON(w, http.StatusOK, resp)
}

// Version localizations: description, keywords, URLs and release notes.

func (s *Server) AppStoreVersionsAppStoreVersionLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionsAppStoreVersionLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions.versions[id]
	if !ok {
		notFound(w, "appStoreVersions", id)
		return
	}
	var items []gen.AppStoreVersionLocalization
	for _, l := range sortedValues(v.localizations) {
		if matches(params.FilterLocale, *l.Attributes.Locale) {
			items = append(items, *l)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionLocalizationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) findVersionLocalization(id string) (*gen.AppStoreVersionLocalization, *version) {
	for _, v := range s.versions.versions {
		if l, ok := v.localizations[id]; ok {
			return l, v
		}
	}
	return nil, nil
}

func (s *Server) AppStoreVersionLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableVersion(w, req.Data.Relationships.AppStoreVersion.Data.Id)
	if v == nil {
		return
	}
	for _, l := range v.localizations {
		if *l.Attributes.Locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A localization for locale "+in.Locale+" already exists.", "/data/attributes/locale")
			return
		}
	}
	l := &gen.AppStoreVersionLocalization{Id: s.newID("O"), Type: gen.AppStoreVersionLocalizationTypeAppStoreVersionLocalizations}
	a := alloc(&l.Attributes)
	a.Locale, a.Description, a.Keywords, a.MarketingUrl = new(in.Locale), in.Description, in.Keywords, in.MarketingUrl
	a.PromotionalText, a.SupportUrl, a.WhatsNew = in.PromotionalText, in.SupportUrl, in.WhatsNew
	v.localizations[l.Id] = l
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionLocalizationResponse{Data: *l, Links: docLinks(r)})
}

func (s *Server) AppStoreVersionLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreVersionLocalizationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, v := s.findVersionLocalization(id)
	if l == nil {
		notFound(w, "appStoreVersionLocalizations", id)
		return
	}
	if !v.editable() {
		stateError(w, "The version cannot be edited in its current state: "+string(*v.Attributes.AppVersionState)+".")
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := l.Attributes
		setString(&a.Description, in.Description)
		setString(&a.Keywords, in.Keywords)
		setString(&a.MarketingUrl, in.MarketingUrl)
		setString(&a.PromotionalText, in.PromotionalText)
		setString(&a.SupportUrl, in.SupportUrl)
		setString(&a.WhatsNew, in.WhatsNew)
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionLocalizationResponse{Data: *l, Links: docLinks(r)})
}

func (s *Server) AppStoreVersionLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, v := s.findVersionLocalization(id)
	if l == nil {
		notFound(w, "appStoreVersionLocalizations", id)
		return
	}
	if !v.editable() {
		stateError(w, "The version cannot be edited in its current state: "+string(*v.Attributes.AppVersionState)+".")
		return
	}
	delete(v.localizations, id)
	w.WriteHeader(http.StatusNoContent)
}

// App Store review details: contact and demo account information for App
// Review, one per version.

func (s *Server) AppStoreVersionsAppStoreReviewDetailGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionsAppStoreReviewDetailGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions.versions[id]
	if !ok {
		notFound(w, "appStoreVersions", id)
		return
	}
	if v.reviewDetail == nil {
		notFound(w, "appStoreReviewDetails", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppStoreReviewDetailResponse{Data: *v.reviewDetail, Links: docLinks(r)})
}

func (s *Server) AppStoreReviewDetailsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreReviewDetailCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableVersion(w, req.Data.Relationships.AppStoreVersion.Data.Id)
	if v == nil {
		return
	}
	if v.reviewDetail != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has review details.", "/data/relationships/appStoreVersion")
		return
	}
	d := &gen.AppStoreReviewDetail{Id: s.newID("W"), Type: gen.AppStoreReviewDetailTypeAppStoreReviewDetails}
	a := alloc(&d.Attributes)
	if in := req.Data.Attributes; in != nil {
		a.ContactEmail, a.ContactFirstName, a.ContactLastName, a.ContactPhone = in.ContactEmail, in.ContactFirstName, in.ContactLastName, in.ContactPhone
		a.DemoAccountName, a.DemoAccountPassword, a.DemoAccountRequired, a.Notes = in.DemoAccountName, in.DemoAccountPassword, in.DemoAccountRequired, in.Notes
	}
	v.reviewDetail = d
	writeJSON(w, http.StatusCreated, gen.AppStoreReviewDetailResponse{Data: *d, Links: docLinks(r)})
}

func (s *Server) AppStoreReviewDetailsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreReviewDetailUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var v *version
	for _, candidate := range s.versions.versions {
		if candidate.reviewDetail != nil && candidate.reviewDetail.Id == id {
			v = candidate
		}
	}
	if v == nil {
		notFound(w, "appStoreReviewDetails", id)
		return
	}
	if !v.editable() {
		stateError(w, "The version cannot be edited in its current state: "+string(*v.Attributes.AppVersionState)+".")
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := v.reviewDetail.Attributes
		setString(&a.ContactEmail, in.ContactEmail)
		setString(&a.ContactFirstName, in.ContactFirstName)
		setString(&a.ContactLastName, in.ContactLastName)
		setString(&a.ContactPhone, in.ContactPhone)
		setString(&a.DemoAccountName, in.DemoAccountName)
		setString(&a.DemoAccountPassword, in.DemoAccountPassword)
		setString(&a.Notes, in.Notes)
		if in.DemoAccountRequired != nil {
			a.DemoAccountRequired = new(*in.DemoAccountRequired)
		}
	}
	writeJSON(w, http.StatusOK, gen.AppStoreReviewDetailResponse{Data: *v.reviewDetail, Links: docLinks(r)})
}

// Phased releases spread an automatic update over seven days. A phased
// release is created inactive before the version is released, starts when it
// is, and can then be paused, resumed or completed
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appstoreversionphasedreleases).

func (s *Server) AppStoreVersionsAppStoreVersionPhasedReleaseGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionsAppStoreVersionPhasedReleaseGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions.versions[id]
	if !ok {
		notFound(w, "appStoreVersions", id)
		return
	}
	if v.phased == nil {
		notFound(w, "appStoreVersionPhasedReleases", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionPhasedReleaseWithoutIncludesResponse{Data: *v.phased, Links: docLinks(r)})
}

func (s *Server) findPhased(id string) *version {
	for _, v := range s.versions.versions {
		if v.phased != nil && v.phased.Id == id {
			return v
		}
	}
	return nil
}

func (s *Server) AppStoreVersionPhasedReleasesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionPhasedReleaseCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableVersion(w, req.Data.Relationships.AppStoreVersion.Data.Id)
	if v == nil {
		return
	}
	if v.phased != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has a phased release.", "/data/relationships/appStoreVersion")
		return
	}
	p := &gen.AppStoreVersionPhasedRelease{Id: s.newID("P"), Type: gen.AppStoreVersionPhasedReleaseTypeAppStoreVersionPhasedReleases}
	alloc(&p.Attributes).PhasedReleaseState = new(gen.PhasedReleaseStateINACTIVE)
	v.phased = p
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionPhasedReleaseResponse{Data: *p, Links: docLinks(r)})
}

func (s *Server) AppStoreVersionPhasedReleasesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreVersionPhasedReleaseUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.findPhased(id)
	if v == nil {
		notFound(w, "appStoreVersionPhasedReleases", id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.PhasedReleaseState != nil {
		state := *in.PhasedReleaseState
		released := *v.Attributes.AppVersionState == gen.AppVersionStateREADYFORDISTRIBUTION
		if state != gen.PhasedReleaseStateINACTIVE && !released || state == gen.PhasedReleaseStateINACTIVE && released {
			stateError(w, "The phased release cannot become "+string(state)+" before or after the version's release.")
			return
		}
		v.phased.Attributes.PhasedReleaseState = new(state)
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionPhasedReleaseResponse{Data: *v.phased, Links: docLinks(r)})
}

func (s *Server) AppStoreVersionPhasedReleasesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.findPhased(id)
	if v == nil {
		notFound(w, "appStoreVersionPhasedReleases", id)
		return
	}
	if *v.phased.Attributes.PhasedReleaseState != gen.PhasedReleaseStateINACTIVE {
		stateError(w, "A phased release that has started cannot be removed.")
		return
	}
	v.phased = nil
	w.WriteHeader(http.StatusNoContent)
}

// Review submissions: a platform has one open submission, which collects
// items such as a version, and is then submitted.

var openSubmissionStates = []gen.ReviewSubmissionAttributesState{
	gen.ReviewSubmissionAttributesStateREADYFORREVIEW, gen.ReviewSubmissionAttributesStateWAITINGFORREVIEW,
	gen.ReviewSubmissionAttributesStateINREVIEW, gen.ReviewSubmissionAttributesStateUNRESOLVEDISSUES,
}

func (s *Server) ReviewSubmissionsGetCollection(w http.ResponseWriter, r *http.Request, params gen.ReviewSubmissionsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.ReviewSubmission
	for _, sub := range sortedValues(s.versions.submissions) {
		a := sub.Attributes
		if matches(&params.FilterApp, sub.appID) && matches(params.FilterPlatform, string(*a.Platform)) && matches(params.FilterState, string(*a.State)) {
			items = append(items, sub.ReviewSubmission)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.ReviewSubmissionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) ReviewSubmissionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.ReviewSubmissionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	appID := req.Data.Relationships.App.Data.Id
	platform := gen.PlatformIOS
	if req.Data.Attributes != nil && req.Data.Attributes.Platform != nil {
		platform = *req.Data.Attributes.Platform
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	for _, sub := range s.versions.submissions {
		if sub.appID == appID && *sub.Attributes.Platform == platform && slices.Contains(openSubmissionStates, *sub.Attributes.State) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app already has an open review submission for "+string(platform)+".", "/data/relationships/app")
			return
		}
	}
	sub := &reviewSubmission{appID: appID, ReviewSubmission: gen.ReviewSubmission{Id: s.newID("S"), Type: gen.ReviewSubmissionTypeReviewSubmissions}}
	a := alloc(&sub.Attributes)
	a.Platform, a.State = new(platform), new(gen.ReviewSubmissionAttributesStateREADYFORREVIEW)
	s.versions.submissions[sub.Id] = sub
	writeJSON(w, http.StatusCreated, gen.ReviewSubmissionResponse{Data: sub.ReviewSubmission, Links: docLinks(r)})
}

// ReviewSubmissionItemsCreateInstance adds a version to a submission. The
// version needs a build.
func (s *Server) ReviewSubmissionItemsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.ReviewSubmissionItemCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.versions.submissions[rel.ReviewSubmission.Data.Id]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no reviewSubmissions with id '"+rel.ReviewSubmission.Data.Id+"'", "/data/relationships/reviewSubmission")
		return
	}
	if rel.AppStoreVersion == nil || rel.AppStoreVersion.Data == nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The fake supports only App Store version items.", "/data/relationships")
		return
	}
	v := s.editableVersion(w, rel.AppStoreVersion.Data.Id)
	if v == nil {
		return
	}
	if v.appID != sub.appID || *v.Attributes.Platform != *sub.Attributes.Platform {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version belongs to another app or platform.", "/data/relationships/appStoreVersion")
		return
	}
	if v.buildID == "" {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version has no build.", "/data/relationships/appStoreVersion")
		return
	}
	sub.versions = append(sub.versions, v.Id)
	s.setVersionState(v, gen.AppVersionStateREADYFORREVIEW)
	item := map[string]any{"type": "reviewSubmissionItems", "id": s.newID("T"), "attributes": map[string]any{"state": "READY_FOR_REVIEW"}}
	writeJSON(w, http.StatusCreated, map[string]any{"data": item, "links": docLinks(r)})
}

// ReviewSubmissionsUpdateInstance submits or cancels a submission. Removing
// a submission from review makes its versions Developer Rejected.
func (s *Server) ReviewSubmissionsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.ReviewSubmissionUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.versions.submissions[id]
	if !ok {
		notFound(w, "reviewSubmissions", id)
		return
	}
	in := asc.Value(req.Data.Attributes)
	switch {
	case asc.Value(in.Submitted):
		if *sub.Attributes.State != gen.ReviewSubmissionAttributesStateREADYFORREVIEW || len(sub.versions) == 0 {
			stateError(w, "Only a submission with items, ready for review, can be submitted.")
			return
		}
		sub.Attributes.State = new(gen.ReviewSubmissionAttributesStateWAITINGFORREVIEW)
		sub.Attributes.SubmittedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
		for _, vid := range sub.versions {
			s.setVersionState(s.versions.versions[vid], gen.AppVersionStateWAITINGFORREVIEW)
		}
	case asc.Value(in.Canceled):
		if !slices.Contains(openSubmissionStates, *sub.Attributes.State) {
			stateError(w, "The submission is not open.")
			return
		}
		sub.Attributes.State = new(gen.ReviewSubmissionAttributesStateCOMPLETE)
		for _, vid := range sub.versions {
			s.setVersionState(s.versions.versions[vid], gen.AppVersionStateDEVELOPERREJECTED)
		}
	}
	writeJSON(w, http.StatusOK, gen.ReviewSubmissionResponse{Data: sub.ReviewSubmission, Links: docLinks(r)})
}

// ApproveVersion approves a version in review, as App Review does. A version
// with a manual release waits for a release request; otherwise it is
// released.
func (s *Server) ApproveVersion(versionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.versions.versions[versionID]
	for _, sub := range s.versions.submissions {
		if slices.Contains(sub.versions, versionID) {
			sub.Attributes.State = new(gen.ReviewSubmissionAttributesStateCOMPLETE)
		}
	}
	if *v.Attributes.ReleaseType == gen.AppStoreVersionAttributesReleaseTypeMANUAL {
		s.setVersionState(v, gen.AppVersionStatePENDINGDEVELOPERRELEASE)
		return
	}
	s.release(v)
}

func (s *Server) release(v *version) {
	s.setVersionState(v, gen.AppVersionStateREADYFORDISTRIBUTION)
	if v.phased != nil && *v.phased.Attributes.PhasedReleaseState == gen.PhasedReleaseStateINACTIVE {
		v.phased.Attributes.PhasedReleaseState = new(gen.PhasedReleaseStateACTIVE)
	}
}

func (s *Server) AppStoreVersionReleaseRequestsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionReleaseRequestCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions.versions[req.Data.Relationships.AppStoreVersion.Data.Id]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersions with id '"+req.Data.Relationships.AppStoreVersion.Data.Id+"'", "/data/relationships/appStoreVersion")
		return
	}
	if *v.Attributes.AppVersionState != gen.AppVersionStatePENDINGDEVELOPERRELEASE {
		stateError(w, "Only a version in Pending Developer Release can be released.")
		return
	}
	s.release(v)
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionReleaseRequestResponse{
		Data:  gen.AppStoreVersionReleaseRequest{Id: s.newID("Q"), Type: gen.AppStoreVersionReleaseRequestTypeAppStoreVersionReleaseRequests},
		Links: docLinks(r),
	})
}

// Encryption declarations answer export compliance for an app's builds
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appencryptiondeclarations).

func (s *Server) AppEncryptionDeclarationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppEncryptionDeclarationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	appID := req.Data.Relationships.App.Data.Id //nolint:staticcheck // The spec deprecates the relationship but still requires it.
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	if strings.TrimSpace(in.AppDescription) == "" {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "appDescription is required.", "/data/attributes/appDescription")
		return
	}
	d := &encryptionDeclaration{appID: appID, AppEncryptionDeclaration: gen.AppEncryptionDeclaration{Id: s.newID("E"), Type: gen.AppEncryptionDeclarationTypeAppEncryptionDeclarations}}
	a := alloc(&d.Attributes)
	a.AppDescription, a.AvailableOnFrenchStore = new(in.AppDescription), new(in.AvailableOnFrenchStore)
	a.ContainsProprietaryCryptography, a.ContainsThirdPartyCryptography = new(in.ContainsProprietaryCryptography), new(in.ContainsThirdPartyCryptography)
	a.Exempt = new(!in.ContainsProprietaryCryptography && !in.ContainsThirdPartyCryptography)
	a.AppEncryptionDeclarationState = new(gen.AppEncryptionDeclarationStateCREATED)
	a.CreatedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	s.versions.declarations[d.Id] = d
	writeJSON(w, http.StatusCreated, gen.AppEncryptionDeclarationResponse{Data: d.AppEncryptionDeclaration, Links: docLinks(r)})
}

func (s *Server) AppEncryptionDeclarationsGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.AppEncryptionDeclarationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.versions.declarations[id]
	if !ok {
		notFound(w, "appEncryptionDeclarations", id)
		return
	}
	out := d.AppEncryptionDeclaration
	if params.Include != nil && slices.Contains(*params.Include, gen.AppEncryptionDeclarationsGetInstanceParamsIncludeApp) {
		data := alloc(&alloc(&alloc(&out.Relationships).App).Data) //nolint:staticcheck // The spec deprecates the relationship, but the API returns it.
		data.Id, data.Type = d.appID, "apps"
	}
	writeJSON(w, http.StatusOK, gen.AppEncryptionDeclarationResponse{Data: out, Links: docLinks(r)})
}

// BuildsAppEncryptionDeclarationUpdateToOneRelationship assigns a
// declaration to a build of the same app
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-builds-_id_-relationships-appencryptiondeclaration),
// which answers its export compliance question.
func (s *Server) BuildsAppEncryptionDeclarationUpdateToOneRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BuildAppEncryptionDeclarationLinkageRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "builds", id)
		return
	}
	d, ok := s.versions.declarations[req.Data.Id]
	if !ok || d.appID != b.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no encryption declaration '"+req.Data.Id+"' of this app.", "/data")
		return
	}
	b.Attributes.UsesNonExemptEncryption = new(!*d.Attributes.Exempt)
	s.refreshBuildStates(b)
	w.WriteHeader(http.StatusNoContent)
}

// ReleaseApp releases the app's versions in preparation, as if App Review
// approved them.
func (s *Server) ReleaseApp(appID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range sortedValues(s.versions.versions) {
		if v.appID == appID && (v.editable() || *v.Attributes.AppVersionState == gen.AppVersionStatePENDINGDEVELOPERRELEASE) {
			s.release(v)
		}
	}
}

// PrepareVersion adds the app's next iOS version, as a developer would in
// App Store Connect.
func (s *Server) PrepareVersion(appID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.versions.versions {
		if v.appID == appID {
			n++
		}
	}
	return s.newVersion(appID, gen.PlatformIOS, fmt.Sprintf("1.%d", n)).Id
}

// FirstVersion returns the ID of the version an app was created with.
func (s *Server) FirstVersion(appID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range sortedValues(s.versions.versions) {
		if v.appID == appID {
			return v.Id
		}
	}
	return ""
}

// VersionBuild returns the ID of a version's build.
func (s *Server) VersionBuild(versionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.versions.versions[versionID].buildID
}

// VersionState returns a version's state.
func (s *Server) VersionState(versionID string) gen.AppVersionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.versions.versions[versionID].Attributes.AppVersionState
}
