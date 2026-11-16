package asctest

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Advanced App Clip experiences, App Clip experiences for testers, and the
// App Clip domain status of a build
// (https://developer.apple.com/documentation/appstoreconnectapi/app-clips-and-app-clip-experiences).
// An advanced experience is created with an uploaded header image and at
// least one localization, and optionally a place that Apple Maps matches. It
// can't be deleted, only removed, which deactivates it. A beta App Clip
// invocation belongs to an App Clip build bundle and has a title per locale.
// The fake requires a localization in the experience's default language, and
// keeps a place's status PENDING, because matching happens at Apple.

const advancedImage fileKind = "appClipAdvancedExperienceImages"

const (
	advancedExperiencesRoute = "appClipAdvancedExperiences"
	invocationsRoute         = "betaAppClipInvocations"
	invocationLocalizations  = "betaAppClipInvocationLocalizations"
	buildBundlesRoute        = "buildBundles"
)

type advancedExperience struct {
	gen.AppClipAdvancedExperience
	clipID, imageID string
	localizations   []gen.AppClipAdvancedExperienceLocalization
}

type invocationLocalization struct {
	id, locale, title string
}

type clipInvocation struct {
	id, bundleID, url string
	localizations     []*invocationLocalization
}

type advancedClipsState struct {
	experiences map[string]*advancedExperience
	invocations map[string]*clipInvocation
	// bundles maps a build bundle ID to the bundle.
	bundles map[string]*gen.BuildBundle
	// buildBundles maps a build ID to its bundle IDs.
	buildBundles map[string][]string
	// domains maps an App Clip build bundle ID to its associated domains.
	domains map[string][]string
}

func newAdvancedClipsState() *advancedClipsState {
	return &advancedClipsState{
		experiences: map[string]*advancedExperience{}, invocations: map[string]*clipInvocation{}, bundles: map[string]*gen.BuildBundle{},
		buildBundles: map[string][]string{}, domains: map[string][]string{},
	}
}

// AddBuildBundle adds a bundle to a build, with the associated domains of an
// App Clip bundle, and returns its ID.
func (s *Server) AddBuildBundle(buildID, bundleID string, bundleType gen.BuildBundleType, domains ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &gen.BuildBundle{Id: s.newID("BB"), Type: gen.BuildBundleResourceTypeBuildBundles}
	a := alloc(&b.Attributes)
	a.BundleId, a.BundleType = new(bundleID), new(bundleType)
	s.advanced.bundles[b.Id] = b
	s.advanced.buildBundles[buildID] = append(s.advanced.buildBundles[buildID], b.Id)
	s.advanced.domains[b.Id] = domains
	return b.Id
}

// ---- header images ----

func (s *Server) renderAdvancedImage(r *http.Request, f *uploadedFile) gen.AppClipAdvancedExperienceImage {
	out := gen.AppClipAdvancedExperienceImage{Id: f.id, Type: gen.AppClipAdvancedExperienceImageTypeAppClipAdvancedExperienceImages}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, a.SourceFileChecksum = s.fileAttributes(r, f)
	return out
}

func (s *Server) AppClipAdvancedExperienceImagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppClipAdvancedExperienceImageCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.newFile(advancedImage, "", req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.AppClipAdvancedExperienceImageResponse{Data: s.renderAdvancedImage(r, f), Links: docLinks(r)})
}

func (s *Server) AppClipAdvancedExperienceImagesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppClipAdvancedExperienceImagesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, advancedImage, id); f != nil {
		writeJSON(w, http.StatusOK, gen.AppClipAdvancedExperienceImageResponse{Data: s.renderAdvancedImage(r, f), Links: docLinks(r)})
	}
}

func (s *Server) AppClipAdvancedExperienceImagesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppClipAdvancedExperienceImageUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, advancedImage, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	commitFile(f, in.Uploaded, in.SourceFileChecksum)
	writeJSON(w, http.StatusOK, gen.AppClipAdvancedExperienceImageResponse{Data: s.renderAdvancedImage(r, f), Links: docLinks(r)})
}

// ---- advanced experiences ----

// remarshal copies a value into another type with the same JSON shape, such
// as a request's place into a response's.
func remarshal(from, to any) {
	b, err := json.Marshal(from)
	if err == nil {
		err = json.Unmarshal(b, to)
	}
	if err != nil {
		panic(err)
	}
}

// advancedImageReady writes an error and returns false unless id is an
// uploaded advanced experience image.
func (s *Server) advancedImageReady(w http.ResponseWriter, id string) bool {
	f, ok := s.files[id]
	if !ok || f.kind != advancedImage || f.state == "AWAITING_UPLOAD" {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The header image '"+id+"' doesn't exist or isn't uploaded.", "/data/relationships/headerImage")
		return false
	}
	return true
}

// inlineAdvancedLocalizations resolves the localizations a request lists,
// which must include one in language.
func (s *Server) inlineAdvancedLocalizations(w http.ResponseWriter, refs []string, included []gen.AppClipAdvancedExperienceLocalizationInlineCreate, language gen.AppClipAdvancedExperienceLanguage) ([]gen.AppClipAdvancedExperienceLocalization, bool) {
	const pointer = "/data/relationships/localizations"
	inline := map[string]gen.AppClipAdvancedExperienceLocalizationInlineCreate{}
	for _, l := range included {
		inline[asc.Value(l.Id)] = l
	}
	var out []gen.AppClipAdvancedExperienceLocalization
	hasDefault := false
	for _, ref := range refs {
		l, ok := inline[ref]
		if !ok || l.Attributes == nil || l.Attributes.Language == nil {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The localization '"+ref+"' is not included, or has no language.", pointer)
			return nil, false
		}
		for _, other := range out {
			if *other.Attributes.Language == *l.Attributes.Language {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The experience has two localizations for "+string(*l.Attributes.Language)+".", pointer)
				return nil, false
			}
		}
		hasDefault = hasDefault || *l.Attributes.Language == language
		item := gen.AppClipAdvancedExperienceLocalization{Id: s.newID("AL"), Type: gen.AppClipAdvancedExperienceLocalizationTypeAppClipAdvancedExperienceLocalizations}
		a := alloc(&item.Attributes)
		a.Language, a.Title, a.Subtitle = l.Attributes.Language, l.Attributes.Title, l.Attributes.Subtitle
		out = append(out, item)
	}
	if !hasDefault {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The experience needs a localization in its default language, "+string(language)+".", pointer)
		return nil, false
	}
	return out, true
}

func (s *Server) renderAdvanced(e *advancedExperience, withLocalizations bool) gen.AppClipAdvancedExperienceResponse {
	out := e.AppClipAdvancedExperience
	rel := alloc(&out.Relationships)
	c := alloc(&alloc(&rel.AppClip).Data)
	c.Id, c.Type = e.clipID, "appClips"
	i := alloc(&alloc(&rel.HeaderImage).Data)
	i.Id, i.Type = e.imageID, "appClipAdvancedExperienceImages"
	resp := gen.AppClipAdvancedExperienceResponse{Data: out}
	if withLocalizations {
		objects := make([]any, len(e.localizations))
		for i, l := range e.localizations {
			objects[i] = l
		}
		resp.Included = includedItems(objects, func(item *gen.AppClipAdvancedExperienceResponse_Included_Item, o any) error {
			l, _ := o.(gen.AppClipAdvancedExperienceLocalization)
			return item.FromAppClipAdvancedExperienceLocalization(l)
		})
	}
	return resp
}

func (s *Server) AppClipAdvancedExperiencesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppClipAdvancedExperienceCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, rel := req.Data.Attributes, req.Data.Relationships
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clips.clips[rel.AppClip.Data.Id]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appClips with id '"+rel.AppClip.Data.Id+"'", "/data/relationships/appClip")
		return
	}
	if !s.advancedImageReady(w, rel.HeaderImage.Data.Id) {
		return
	}
	var refs []string
	for _, d := range rel.Localizations.Data {
		refs = append(refs, d.Id)
	}
	localizations, ok := s.inlineAdvancedLocalizations(w, refs, asc.Value(req.Included), in.DefaultLanguage)
	if !ok {
		return
	}
	e := &advancedExperience{clipID: rel.AppClip.Data.Id, imageID: rel.HeaderImage.Data.Id, localizations: localizations}
	e.Id, e.Type = s.newID("AE"), gen.AppClipAdvancedExperienceTypeAppClipAdvancedExperiences
	a := alloc(&e.Attributes)
	a.Link, a.Action, a.IsPoweredBy, a.DefaultLanguage = new(in.Link), in.Action, new(in.IsPoweredBy), new(in.DefaultLanguage)
	a.Status, a.Version = new(gen.AppClipAdvancedExperienceAttributesStatusRECEIVED), new(1)
	if in.BusinessCategory != nil {
		a.BusinessCategory = new(gen.AppClipAdvancedExperienceAttributesBusinessCategory(*in.BusinessCategory))
	}
	if in.Place != nil {
		remarshal(in.Place, &a.Place)
		a.PlaceStatus = new(gen.AppClipAdvancedExperienceAttributesPlaceStatusPENDING)
	}
	s.advanced.experiences[e.Id] = e
	out := s.renderAdvanced(e, false)
	out.Links = docLinks(r)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) AppClipAdvancedExperiencesGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.AppClipAdvancedExperiencesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.advanced.experiences[id]
	if !ok {
		notFound(w, advancedExperiencesRoute, id)
		return
	}
	out := s.renderAdvanced(e, includes(params.Include, "localizations"))
	out.Links = docLinks(r)
	writeJSON(w, http.StatusOK, out)
}

// AppClipAdvancedExperiencesUpdateInstance changes an active experience, or
// removes it, which deactivates it for good.
func (s *Server) AppClipAdvancedExperiencesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppClipAdvancedExperienceUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.advanced.experiences[id]
	if !ok {
		notFound(w, advancedExperiencesRoute, id)
		return
	}
	a := e.Attributes
	if *a.Status == gen.AppClipAdvancedExperienceAttributesStatusDEACTIVATED {
		stateError(w, "A removed App Clip experience can't change.")
		return
	}
	in, rel := asc.Value(req.Data.Attributes), asc.Value(req.Data.Relationships)
	language := *a.DefaultLanguage
	if in.DefaultLanguage != nil {
		language = *in.DefaultLanguage
	}
	localizations := e.localizations
	if rel.Localizations != nil && rel.Localizations.Data != nil {
		var refs []string
		for _, d := range *rel.Localizations.Data {
			refs = append(refs, d.Id)
		}
		if localizations, ok = s.inlineAdvancedLocalizations(w, refs, asc.Value(req.Included), language); !ok {
			return
		}
	} else if !slices.ContainsFunc(localizations, func(l gen.AppClipAdvancedExperienceLocalization) bool { return *l.Attributes.Language == language }) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The experience has no localization in "+string(language)+".", "/data/attributes/defaultLanguage")
		return
	}
	if rel.HeaderImage != nil && rel.HeaderImage.Data != nil {
		if !s.advancedImageReady(w, rel.HeaderImage.Data.Id) {
			return
		}
		e.imageID = rel.HeaderImage.Data.Id
	}
	e.localizations, a.DefaultLanguage = localizations, new(language)
	if in.Action != nil || nulls["action"] {
		a.Action = in.Action
	}
	if in.IsPoweredBy != nil {
		a.IsPoweredBy = new(*in.IsPoweredBy)
	}
	switch {
	case in.BusinessCategory != nil:
		a.BusinessCategory = new(gen.AppClipAdvancedExperienceAttributesBusinessCategory(*in.BusinessCategory))
	case nulls["businessCategory"]:
		a.BusinessCategory = nil
	}
	switch {
	case in.Place != nil:
		remarshal(in.Place, &a.Place)
		a.PlaceStatus = new(gen.AppClipAdvancedExperienceAttributesPlaceStatusPENDING)
	case nulls["place"]:
		a.Place, a.PlaceStatus = nil, nil
	}
	if asc.Value(in.Removed) {
		a.Status = new(gen.AppClipAdvancedExperienceAttributesStatusDEACTIVATED)
	}
	out := s.renderAdvanced(e, false)
	out.Links = docLinks(r)
	writeJSON(w, http.StatusOK, out)
}

// ---- build bundles and domain status ----

func (s *Server) appClipBundle(w http.ResponseWriter, id, pointer string) *gen.BuildBundle {
	b, ok := s.advanced.bundles[id]
	if !ok {
		if pointer == "" {
			notFound(w, buildBundlesRoute, id)
		} else {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no buildBundles with id '"+id+"'", pointer)
		}
		return nil
	}
	if *b.Attributes.BundleType != gen.BuildBundleTypeAPPCLIP {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The build bundle '"+id+"' is not an App Clip.", pointer)
		return nil
	}
	return b
}

// domainStatus writes a bundle's domain status, in which every associated
// domain is valid.
func (s *Server) domainStatus(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appClipBundle(w, id, "") == nil {
		return
	}
	now := gen.DateTime{Time: s.now().UTC().Truncate(time.Second)}
	out := gen.AppClipDomainStatus{Id: id, Type: gen.AppClipDomainStatusTypeAppClipDomainStatuses}
	a := alloc(&out.Attributes)
	a.LastUpdatedDate = new(now)
	a.Domains = alloc(&a.Domains)
	for _, domain := range s.advanced.domains[id] {
		d := push(a.Domains)
		d.Domain, d.IsValid, d.LastUpdatedDate = new(domain), new(true), new(now)
	}
	writeJSON(w, http.StatusOK, gen.AppClipDomainStatusResponse{Data: out, Links: docLinks(r)})
}

func (s *Server) BuildBundlesAppClipDomainCacheStatusGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.BuildBundlesAppClipDomainCacheStatusGetToOneRelatedParams) {
	s.domainStatus(w, r, id)
}

func (s *Server) BuildBundlesAppClipDomainDebugStatusGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.BuildBundlesAppClipDomainDebugStatusGetToOneRelatedParams) {
	s.domainStatus(w, r, id)
}

// ---- beta App Clip invocations ----

func renderInvocation(i *clipInvocation) gen.BetaAppClipInvocationResponse {
	out := gen.BetaAppClipInvocation{Id: i.id, Type: gen.BetaAppClipInvocationTypeBetaAppClipInvocations}
	alloc(&out.Attributes).Url = new(i.url)
	included := []gen.BetaAppClipInvocationLocalization{}
	for _, l := range i.localizations {
		item := gen.BetaAppClipInvocationLocalization{Id: l.id, Type: gen.BetaAppClipInvocationLocalizationTypeBetaAppClipInvocationLocalizations}
		a := alloc(&item.Attributes)
		a.Locale, a.Title = new(l.locale), new(l.title)
		included = append(included, item)
	}
	return gen.BetaAppClipInvocationResponse{Data: out, Included: &included}
}

func (s *Server) BetaAppClipInvocationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaAppClipInvocationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appClipBundle(w, rel.BuildBundle.Data.Id, "/data/relationships/buildBundle") == nil {
		return
	}
	inline := map[string]gen.BetaAppClipInvocationLocalizationInlineCreate{}
	for _, l := range asc.Value(req.Included) {
		inline[asc.Value(l.Id)] = l
	}
	i := &clipInvocation{id: s.newID("BI"), bundleID: rel.BuildBundle.Data.Id, url: req.Data.Attributes.Url}
	for _, ref := range rel.BetaAppClipInvocationLocalizations.Data {
		l, ok := inline[ref.Id]
		if !ok || slices.ContainsFunc(i.localizations, func(o *invocationLocalization) bool { return o.locale == l.Attributes.Locale }) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The localization '"+ref.Id+"' is not included, or repeats a locale.", "/data/relationships/betaAppClipInvocationLocalizations")
			return
		}
		i.localizations = append(i.localizations, &invocationLocalization{id: s.newID("IL"), locale: l.Attributes.Locale, title: l.Attributes.Title})
	}
	if len(i.localizations) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "An invocation needs a localization.", "/data/relationships/betaAppClipInvocationLocalizations")
		return
	}
	s.advanced.invocations[i.id] = i
	out := renderInvocation(i)
	out.Links = docLinks(r)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) findInvocation(w http.ResponseWriter, id string) *clipInvocation {
	i, ok := s.advanced.invocations[id]
	if !ok {
		notFound(w, invocationsRoute, id)
	}
	return i
}

func (s *Server) BetaAppClipInvocationsGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.BetaAppClipInvocationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findInvocation(w, id)
	if i == nil {
		return
	}
	out := renderInvocation(i)
	if !includes(params.Include, "betaAppClipInvocationLocalizations") {
		out.Included = nil
	}
	out.Links = docLinks(r)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) BetaAppClipInvocationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaAppClipInvocationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findInvocation(w, id)
	if i == nil {
		return
	}
	if in := req.Data.Attributes; in != nil && in.Url != nil {
		i.url = *in.Url
	}
	out := renderInvocation(i)
	out.Included, out.Links = nil, docLinks(r)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) BetaAppClipInvocationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findInvocation(w, id) == nil {
		return
	}
	delete(s.advanced.invocations, id)
	w.WriteHeader(http.StatusNoContent)
}

// findInvocationLocalization returns a localization and its invocation.
func (s *Server) findInvocationLocalization(w http.ResponseWriter, id string) (*clipInvocation, *invocationLocalization) {
	for _, i := range s.advanced.invocations {
		for _, l := range i.localizations {
			if l.id == id {
				return i, l
			}
		}
	}
	notFound(w, invocationLocalizations, id)
	return nil, nil
}

func renderInvocationLocalization(l *invocationLocalization) gen.BetaAppClipInvocationLocalization {
	out := gen.BetaAppClipInvocationLocalization{Id: l.id, Type: gen.BetaAppClipInvocationLocalizationTypeBetaAppClipInvocationLocalizations}
	a := alloc(&out.Attributes)
	a.Locale, a.Title = new(l.locale), new(l.title)
	return out
}

func (s *Server) BetaAppClipInvocationLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaAppClipInvocationLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, invocationID := req.Data.Attributes, req.Data.Relationships.BetaAppClipInvocation.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.advanced.invocations[invocationID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no betaAppClipInvocations with id '"+invocationID+"'", "/data/relationships/betaAppClipInvocation")
		return
	}
	if slices.ContainsFunc(i.localizations, func(o *invocationLocalization) bool { return o.locale == in.Locale }) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The invocation already has a localization for "+in.Locale+".", "/data/attributes/locale")
		return
	}
	l := &invocationLocalization{id: s.newID("IL"), locale: in.Locale, title: in.Title}
	i.localizations = append(i.localizations, l)
	writeJSON(w, http.StatusCreated, gen.BetaAppClipInvocationLocalizationResponse{Data: renderInvocationLocalization(l), Links: docLinks(r)})
}

func (s *Server) BetaAppClipInvocationLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaAppClipInvocationLocalizationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, l := s.findInvocationLocalization(w, id)
	if l == nil {
		return
	}
	if in := req.Data.Attributes; in != nil && in.Title != nil {
		l.title = *in.Title
	}
	writeJSON(w, http.StatusOK, gen.BetaAppClipInvocationLocalizationResponse{Data: renderInvocationLocalization(l), Links: docLinks(r)})
}

func (s *Server) BetaAppClipInvocationLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, l := s.findInvocationLocalization(w, id)
	if l == nil {
		return
	}
	i.localizations = slices.DeleteFunc(i.localizations, func(o *invocationLocalization) bool { return o.id == id })
	w.WriteHeader(http.StatusNoContent)
}
