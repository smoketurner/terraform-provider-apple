package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// App Clips and their default experiences. An App Clip comes from an
// uploaded build. A default experience is attached to an App Store version
// and has an App Review detail with invocation URLs, and a localization per
// locale with a subtitle and a header image. The experiences of a released
// App Clip can't be changed or deleted: a new version takes a new experience
// (https://developer.apple.com/documentation/appstoreconnectapi/app-clips-and-app-clip-experiences).
// The fake treats an experience as released once its version can no longer
// be edited, and gives a version at most one default experience.

const headerImage fileKind = "appClipHeaderImages"

const (
	clipExperiencesRoute    = "appClipDefaultExperiences"
	clipLocalizationsRoute  = "appClipDefaultExperienceLocalizations"
	clipReviewDetailsRoute  = "appClipAppStoreReviewDetails"
	clipReleasedExplanation = "The App Clip experience belongs to a released App Clip and can't change."
)

type appClip struct {
	id, appID, bundleID string
}

type clipExperience struct {
	id, clipID, versionID string
	action                *gen.AppClipAction
	// reviewDetailID is "" until the review detail is created.
	reviewDetailID string
	invocationURLs []string
}

type clipLocalization struct {
	id, experienceID, locale string
	subtitle                 *string
}

type appClipsState struct {
	clips         map[string]*appClip
	experiences   map[string]*clipExperience
	localizations map[string]*clipLocalization
}

func newAppClipsState() *appClipsState {
	return &appClipsState{clips: map[string]*appClip{}, experiences: map[string]*clipExperience{}, localizations: map[string]*clipLocalization{}}
}

// AddAppClip adds an App Clip to an app, as uploading a build with one does,
// and returns its ID.
func (s *Server) AddAppClip(appID, bundleID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &appClip{id: s.newID("AC"), appID: appID, bundleID: bundleID}
	s.clips.clips[c.id] = c
	return c.id
}

func (s *Server) AppsAppClipsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppClipsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppClip
	for _, c := range sortedValues(s.clips.clips) {
		if c.appID != id || !matches(params.FilterBundleId, c.bundleID) {
			continue
		}
		item := gen.AppClip{Id: c.id, Type: gen.AppClipTypeAppClips}
		alloc(&item.Attributes).BundleId = new(c.bundleID)
		items = append(items, item)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppClipsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- default experiences ----

func renderClipExperience(e *clipExperience) gen.AppClipDefaultExperience {
	out := gen.AppClipDefaultExperience{Id: e.id, Type: gen.AppClipDefaultExperienceTypeAppClipDefaultExperiences}
	alloc(&out.Attributes).Action = e.action
	rel := alloc(&out.Relationships)
	c := alloc(&alloc(&rel.AppClip).Data)
	c.Id, c.Type = e.clipID, "appClips"
	if e.versionID != "" {
		v := alloc(&alloc(&rel.ReleaseWithAppStoreVersion).Data)
		v.Id, v.Type = e.versionID, "appStoreVersions"
	}
	return out
}

// clipVersion writes an error and returns false unless the version is an
// editable version of the App Clip's app without another default experience.
func (s *Server) clipVersion(w http.ResponseWriter, e *clipExperience, versionID string) bool {
	const pointer = "/data/relationships/releaseWithAppStoreVersion"
	v, ok := s.versions.versions[versionID]
	if !ok || v.appID != s.clips.clips[e.clipID].appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app has no appStoreVersions with id '"+versionID+"'", pointer)
		return false
	}
	if !v.editable() {
		stateError(w, "The version cannot be edited in its current state: "+string(*v.Attributes.AppVersionState)+".")
		return false
	}
	for _, other := range s.clips.experiences {
		if other.id != e.id && other.versionID == versionID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has a default App Clip experience.", pointer)
			return false
		}
	}
	return true
}

// unreleasedExperience returns the experience, or writes an error and
// returns nil when it doesn't exist or its version can't be edited any more.
func (s *Server) unreleasedExperience(w http.ResponseWriter, id string) *clipExperience {
	e, ok := s.clips.experiences[id]
	if !ok {
		notFound(w, clipExperiencesRoute, id)
		return nil
	}
	if v, ok := s.versions.versions[e.versionID]; ok && !v.editable() {
		stateError(w, clipReleasedExplanation)
		return nil
	}
	return e
}

func (s *Server) AppClipDefaultExperiencesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppClipDefaultExperienceCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clips.clips[rel.AppClip.Data.Id]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appClips with id '"+rel.AppClip.Data.Id+"'", "/data/relationships/appClip")
		return
	}
	e := &clipExperience{id: s.newID("CE"), clipID: rel.AppClip.Data.Id, action: asc.Value(req.Data.Attributes).Action}
	if rel.ReleaseWithAppStoreVersion != nil && rel.ReleaseWithAppStoreVersion.Data != nil {
		if !s.clipVersion(w, e, rel.ReleaseWithAppStoreVersion.Data.Id) {
			return
		}
		e.versionID = rel.ReleaseWithAppStoreVersion.Data.Id
	}
	s.clips.experiences[e.id] = e
	writeJSON(w, http.StatusCreated, gen.AppClipDefaultExperienceResponse{Data: renderClipExperience(e), Links: docLinks(r)})
}

func (s *Server) AppClipDefaultExperiencesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppClipDefaultExperiencesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.clips.experiences[id]
	if !ok {
		notFound(w, clipExperiencesRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppClipDefaultExperienceResponse{Data: renderClipExperience(e), Links: docLinks(r)})
}

func (s *Server) AppClipDefaultExperiencesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppClipDefaultExperienceUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.unreleasedExperience(w, id)
	if e == nil {
		return
	}
	if rel := req.Data.Relationships; rel != nil && rel.ReleaseWithAppStoreVersion != nil && rel.ReleaseWithAppStoreVersion.Data != nil {
		if !s.clipVersion(w, e, rel.ReleaseWithAppStoreVersion.Data.Id) {
			return
		}
		e.versionID = rel.ReleaseWithAppStoreVersion.Data.Id
	}
	if in := req.Data.Attributes; in != nil && (in.Action != nil || nulls["action"]) {
		e.action = in.Action
	}
	writeJSON(w, http.StatusOK, gen.AppClipDefaultExperienceResponse{Data: renderClipExperience(e), Links: docLinks(r)})
}

// AppClipDefaultExperiencesDeleteInstance deletes an unreleased experience
// with its localizations and their header images.
func (s *Server) AppClipDefaultExperiencesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unreleasedExperience(w, id) == nil {
		return
	}
	for _, l := range s.clipLocalizations(id) {
		s.removeClipLocalization(l.id)
	}
	delete(s.clips.experiences, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- App Review details ----

func renderClipReviewDetail(e *clipExperience) gen.AppClipAppStoreReviewDetail {
	out := gen.AppClipAppStoreReviewDetail{Id: e.reviewDetailID, Type: gen.AppClipAppStoreReviewDetailTypeAppClipAppStoreReviewDetails}
	alloc(&out.Attributes).InvocationUrls = new(nonNil(e.invocationURLs))
	d := alloc(&alloc(&alloc(&out.Relationships).AppClipDefaultExperience).Data)
	d.Id, d.Type = e.id, clipExperiencesRoute
	return out
}

func (s *Server) clipReviewDetail(id string) *clipExperience {
	for _, e := range s.clips.experiences {
		if e.reviewDetailID == id {
			return e
		}
	}
	return nil
}

func (s *Server) AppClipAppStoreReviewDetailsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppClipAppStoreReviewDetailCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	experienceID := req.Data.Relationships.AppClipDefaultExperience.Data.Id
	if _, ok := s.clips.experiences[experienceID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appClipDefaultExperiences with id '"+experienceID+"'", "/data/relationships/appClipDefaultExperience")
		return
	}
	e := s.unreleasedExperience(w, experienceID)
	if e == nil {
		return
	}
	if e.reviewDetailID != "" {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The App Clip experience already has an App Review detail.", "/data/relationships/appClipDefaultExperience")
		return
	}
	e.reviewDetailID, e.invocationURLs = s.newID("CR"), asc.Value(asc.Value(req.Data.Attributes).InvocationUrls)
	writeJSON(w, http.StatusCreated, gen.AppClipAppStoreReviewDetailResponse{Data: renderClipReviewDetail(e), Links: docLinks(r)})
}

func (s *Server) AppClipAppStoreReviewDetailsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppClipAppStoreReviewDetailsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.clipReviewDetail(id)
	if e == nil {
		notFound(w, clipReviewDetailsRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppClipAppStoreReviewDetailResponse{Data: renderClipReviewDetail(e), Links: docLinks(r)})
}

func (s *Server) AppClipAppStoreReviewDetailsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppClipAppStoreReviewDetailUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.clipReviewDetail(id)
	if e == nil {
		notFound(w, clipReviewDetailsRoute, id)
		return
	}
	if s.unreleasedExperience(w, e.id) == nil {
		return
	}
	if in := req.Data.Attributes; in != nil && in.InvocationUrls != nil {
		e.invocationURLs = *in.InvocationUrls
	}
	writeJSON(w, http.StatusOK, gen.AppClipAppStoreReviewDetailResponse{Data: renderClipReviewDetail(e), Links: docLinks(r)})
}

func (s *Server) AppClipDefaultExperiencesAppClipAppStoreReviewDetailGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppClipDefaultExperiencesAppClipAppStoreReviewDetailGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.clips.experiences[id]
	if !ok || e.reviewDetailID == "" {
		notFound(w, clipExperiencesRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppClipAppStoreReviewDetailResponse{Data: renderClipReviewDetail(e), Links: docLinks(r)})
}

// ---- localizations ----

func (s *Server) clipLocalizations(experienceID string) []*clipLocalization {
	var out []*clipLocalization
	for _, l := range sortedValues(s.clips.localizations) {
		if l.experienceID == experienceID {
			out = append(out, l)
		}
	}
	return out
}

// removeClipLocalization deletes a localization with its header image.
func (s *Server) removeClipLocalization(id string) {
	if f := s.clipHeaderImage(id); f != nil {
		s.removeFile(f)
	}
	delete(s.clips.localizations, id)
}

func renderClipLocalization(l *clipLocalization) gen.AppClipDefaultExperienceLocalization {
	out := gen.AppClipDefaultExperienceLocalization{Id: l.id, Type: gen.AppClipDefaultExperienceLocalizationTypeAppClipDefaultExperienceLocalizations}
	a := alloc(&out.Attributes)
	a.Locale, a.Subtitle = new(l.locale), l.subtitle
	e := alloc(&alloc(&alloc(&out.Relationships).AppClipDefaultExperience).Data)
	e.Id, e.Type = l.experienceID, clipExperiencesRoute
	return out
}

func (s *Server) AppClipDefaultExperienceLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppClipDefaultExperienceLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, experienceID := req.Data.Attributes, req.Data.Relationships.AppClipDefaultExperience.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clips.experiences[experienceID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appClipDefaultExperiences with id '"+experienceID+"'", "/data/relationships/appClipDefaultExperience")
		return
	}
	if s.unreleasedExperience(w, experienceID) == nil {
		return
	}
	for _, l := range s.clipLocalizations(experienceID) {
		if l.locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The App Clip experience already has a localization for "+in.Locale+".", "/data/attributes/locale")
			return
		}
	}
	l := &clipLocalization{id: s.newID("CL"), experienceID: experienceID, locale: in.Locale, subtitle: in.Subtitle}
	s.clips.localizations[l.id] = l
	writeJSON(w, http.StatusCreated, gen.AppClipDefaultExperienceLocalizationResponse{Data: renderClipLocalization(l), Links: docLinks(r)})
}

// unreleasedClipLocalization returns the localization, or writes an error
// and returns nil when it doesn't exist or its experience is released.
func (s *Server) unreleasedClipLocalization(w http.ResponseWriter, id string) *clipLocalization {
	l, ok := s.clips.localizations[id]
	if !ok {
		notFound(w, clipLocalizationsRoute, id)
		return nil
	}
	if s.unreleasedExperience(w, l.experienceID) == nil {
		return nil
	}
	return l
}

func (s *Server) AppClipDefaultExperienceLocalizationsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppClipDefaultExperienceLocalizationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.clips.localizations[id]
	if !ok {
		notFound(w, clipLocalizationsRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppClipDefaultExperienceLocalizationResponse{Data: renderClipLocalization(l), Links: docLinks(r)})
}

func (s *Server) AppClipDefaultExperienceLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppClipDefaultExperienceLocalizationUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.unreleasedClipLocalization(w, id)
	if l == nil {
		return
	}
	if in := req.Data.Attributes; in != nil && (in.Subtitle != nil || nulls["subtitle"]) {
		l.subtitle = in.Subtitle
	}
	writeJSON(w, http.StatusOK, gen.AppClipDefaultExperienceLocalizationResponse{Data: renderClipLocalization(l), Links: docLinks(r)})
}

func (s *Server) AppClipDefaultExperienceLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unreleasedClipLocalization(w, id) == nil {
		return
	}
	s.removeClipLocalization(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppClipDefaultExperiencesAppClipDefaultExperienceLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppClipDefaultExperiencesAppClipDefaultExperienceLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clips.experiences[id]; !ok {
		notFound(w, clipExperiencesRoute, id)
		return
	}
	var items []gen.AppClipDefaultExperienceLocalization
	for _, l := range s.clipLocalizations(id) {
		if matches(params.FilterLocale, l.locale) {
			items = append(items, renderClipLocalization(l))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppClipDefaultExperienceLocalizationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- header images ----

// clipHeaderImage returns a localization's header image, or nil.
func (s *Server) clipHeaderImage(localizationID string) *uploadedFile {
	for _, f := range s.files {
		if f.kind == headerImage && f.parentID == localizationID {
			return f
		}
	}
	return nil
}

func (s *Server) renderHeaderImage(r *http.Request, f *uploadedFile) gen.AppClipHeaderImage {
	out := gen.AppClipHeaderImage{Id: f.id, Type: gen.AppClipHeaderImageTypeAppClipHeaderImages}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, a.SourceFileChecksum = s.fileAttributes(r, f)
	d := alloc(&alloc(&alloc(&out.Relationships).AppClipDefaultExperienceLocalization).Data)
	d.Id, d.Type = f.parentID, clipLocalizationsRoute
	return out
}

// AppClipHeaderImagesCreateInstance reserves a localization's header image;
// a localization has one.
func (s *Server) AppClipHeaderImagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppClipHeaderImageCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	localizationID := req.Data.Relationships.AppClipDefaultExperienceLocalization.Data.Id
	if _, ok := s.clips.localizations[localizationID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appClipDefaultExperienceLocalizations with id '"+localizationID+"'", "/data/relationships/appClipDefaultExperienceLocalization")
		return
	}
	if s.unreleasedClipLocalization(w, localizationID) == nil {
		return
	}
	if s.clipHeaderImage(localizationID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The localization already has a header image.", "/data/relationships/appClipDefaultExperienceLocalization")
		return
	}
	f := s.newFile(headerImage, localizationID, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.AppClipHeaderImageResponse{Data: s.renderHeaderImage(r, f), Links: docLinks(r)})
}

func (s *Server) AppClipHeaderImagesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppClipHeaderImagesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, headerImage, id); f != nil {
		writeJSON(w, http.StatusOK, gen.AppClipHeaderImageResponse{Data: s.renderHeaderImage(r, f), Links: docLinks(r)})
	}
}

func (s *Server) AppClipDefaultExperienceLocalizationsAppClipHeaderImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppClipDefaultExperienceLocalizationsAppClipHeaderImageGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.clipHeaderImage(id)
	if f == nil {
		notFound(w, clipLocalizationsRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppClipHeaderImageResponse{Data: s.renderHeaderImage(r, f), Links: docLinks(r)})
}

func (s *Server) AppClipHeaderImagesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppClipHeaderImageUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, headerImage, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	commitFile(f, in.Uploaded, in.SourceFileChecksum)
	writeJSON(w, http.StatusOK, gen.AppClipHeaderImageResponse{Data: s.renderHeaderImage(r, f), Links: docLinks(r)})
}

func (s *Server) AppClipHeaderImagesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, headerImage, id)
	if f == nil || s.unreleasedClipLocalization(w, f.parentID) == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}
