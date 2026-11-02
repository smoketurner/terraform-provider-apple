package asctest

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// App-level metadata other than app info: app settings, the custom license
// agreement, accessibility declarations, Android app mappings and tags.

type eula struct {
	gen.EndUserLicenseAgreement
	appID       string
	territories []string
}

type accessibility struct {
	gen.AccessibilityDeclaration
	appID string
}

type androidMapping struct {
	gen.AndroidToIosAppMappingDetail
	appID string
}

type appTag struct {
	gen.AppTag
	appID string
}

type appMetaState struct {
	eulas         map[string]*eula
	accessibility map[string]*accessibility
	mappings      map[string]*androidMapping
	tags          map[string]*appTag
}

func newAppMetaState() *appMetaState {
	return &appMetaState{
		eulas:         map[string]*eula{},
		accessibility: map[string]*accessibility{},
		mappings:      map[string]*androidMapping{},
		tags:          map[string]*appTag{},
	}
}

// AppsUpdateInstance changes app settings
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-apps-_id_).
func (s *Server) AppsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[id]
	if !ok {
		notFound(w, "apps", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.ContentRightsDeclaration != nil && !in.ContentRightsDeclaration.Valid() {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "contentRightsDeclaration is invalid", "/data/attributes/contentRightsDeclaration")
			return
		}
		a := app.Attributes
		setString(&a.PrimaryLocale, in.PrimaryLocale)
		setString(&a.BundleId, in.BundleId)
		setString(&a.AccessibilityUrl, in.AccessibilityUrl)
		setString(&a.SubscriptionStatusUrl, in.SubscriptionStatusUrl)
		setString(&a.SubscriptionStatusUrlForSandbox, in.SubscriptionStatusUrlForSandbox)
		if in.ContentRightsDeclaration != nil {
			a.ContentRightsDeclaration = new(gen.AppAttributesContentRightsDeclaration(*in.ContentRightsDeclaration))
		}
		if in.SubscriptionStatusUrlVersion != nil {
			a.SubscriptionStatusUrlVersion = new(*in.SubscriptionStatusUrlVersion)
		}
		if in.SubscriptionStatusUrlVersionForSandbox != nil {
			a.SubscriptionStatusUrlVersionForSandbox = new(*in.SubscriptionStatusUrlVersionForSandbox)
		}
		if in.StreamlinedPurchasingEnabled != nil {
			a.StreamlinedPurchasingEnabled = new(*in.StreamlinedPurchasingEnabled)
		}
	}
	out := *app
	out.Links = resourceLinks(r, "apps", id)
	writeJSON(w, http.StatusOK, gen.AppResponse{Data: out, Links: docLinks(r)})
}

// territoryRegexp matches territory IDs, which are ISO 3166-1 alpha-3 codes.
var territoryRegexp = regexp.MustCompile(`^[A-Z]{3}$`)

func territoryLinkages(w http.ResponseWriter, ids []string) bool {
	if len(ids) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "A license agreement needs at least one territory.", "/data/relationships/territories")
		return false
	}
	for _, t := range ids {
		if !territoryRegexp.MatchString(t) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no territories with id '"+t+"'", "/data/relationships/territories")
			return false
		}
	}
	return true
}

func (s *Server) renderEULA(r *http.Request, e *eula) gen.EndUserLicenseAgreement {
	out := e.EndUserLicenseAgreement
	out.Links = resourceLinks(r, "endUserLicenseAgreements", e.Id)
	out.Relationships = nil
	return out
}

// EndUserLicenseAgreementsCreateInstance adds a custom license agreement. An
// app has at most one.
func (s *Server) EndUserLicenseAgreementsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.EndUserLicenseAgreementCreateRequest
	if !decode(w, r, &req) {
		return
	}
	appID := req.Data.Relationships.App.Data.Id
	var territories []string
	for _, t := range req.Data.Relationships.Territories.Data {
		territories = append(territories, t.Id)
	}
	if !territoryLinkages(w, territories) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	for _, e := range s.appMeta.eulas {
		if e.appID == appID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app already has a license agreement.", "/data/relationships/app")
			return
		}
	}
	slices.Sort(territories)
	e := &eula{appID: appID, territories: territories, EndUserLicenseAgreement: gen.EndUserLicenseAgreement{Id: s.newID("Y"), Type: gen.EndUserLicenseAgreementTypeEndUserLicenseAgreements}}
	alloc(&e.Attributes).AgreementText = new(req.Data.Attributes.AgreementText)
	s.appMeta.eulas[e.Id] = e
	writeJSON(w, http.StatusCreated, gen.EndUserLicenseAgreementResponse{Data: s.renderEULA(r, e), Links: docLinks(r)})
}

func (s *Server) AppsEndUserLicenseAgreementGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsEndUserLicenseAgreementGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.appMeta.eulas {
		if e.appID == id {
			writeJSON(w, http.StatusOK, gen.EndUserLicenseAgreementWithoutIncludesResponse{Data: s.renderEULA(r, e), Links: docLinks(r)})
			return
		}
	}
	notFound(w, "endUserLicenseAgreements", id)
}

func (s *Server) EndUserLicenseAgreementsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.EndUserLicenseAgreementUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.appMeta.eulas[id]
	if !ok {
		notFound(w, "endUserLicenseAgreements", id)
		return
	}
	if rel := req.Data.Relationships; rel != nil && rel.Territories != nil && rel.Territories.Data != nil {
		var territories []string
		for _, t := range *rel.Territories.Data {
			territories = append(territories, t.Id)
		}
		if !territoryLinkages(w, territories) {
			return
		}
		slices.Sort(territories)
		e.territories = territories
	}
	if in := req.Data.Attributes; in != nil && in.AgreementText != nil {
		e.Attributes.AgreementText = new(*in.AgreementText)
	}
	writeJSON(w, http.StatusOK, gen.EndUserLicenseAgreementResponse{Data: s.renderEULA(r, e), Links: docLinks(r)})
}

func (s *Server) EndUserLicenseAgreementsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.appMeta.eulas[id]; !ok {
		notFound(w, "endUserLicenseAgreements", id)
		return
	}
	delete(s.appMeta.eulas, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) EndUserLicenseAgreementsTerritoriesGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.EndUserLicenseAgreementsTerritoriesGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.appMeta.eulas[id]
	if !ok {
		notFound(w, "endUserLicenseAgreements", id)
		return
	}
	ids, links, meta, ok := page(w, r, e.territories, params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.EndUserLicenseAgreementTerritoriesLinkagesResponse{Links: links, Meta: meta}
	for _, t := range ids {
		d := push(&resp.Data)
		d.Id, d.Type = t, "territories"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}

// Accessibility declarations, one draft per device family. Publishing a
// draft replaces the family's published declaration.

func (s *Server) AccessibilityDeclarationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AccessibilityDeclarationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	appID := req.Data.Relationships.App.Data.Id
	if !in.DeviceFamily.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "deviceFamily is invalid", "/data/attributes/deviceFamily")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	for _, d := range s.appMeta.accessibility {
		if d.appID == appID && *d.Attributes.DeviceFamily == in.DeviceFamily && *d.Attributes.State == gen.AccessibilityDeclarationAttributesStateDRAFT {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The app already has a draft declaration for "+string(in.DeviceFamily)+".", "/data/attributes/deviceFamily")
			return
		}
	}
	d := &accessibility{appID: appID, AccessibilityDeclaration: gen.AccessibilityDeclaration{Id: s.newID("X"), Type: gen.AccessibilityDeclarationTypeAccessibilityDeclarations}}
	// The create and model attributes share their JSON names.
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, alloc(&d.Attributes))
	d.Attributes.State = new(gen.AccessibilityDeclarationAttributesStateDRAFT)
	s.appMeta.accessibility[d.Id] = d
	writeJSON(w, http.StatusCreated, gen.AccessibilityDeclarationResponse{Data: d.AccessibilityDeclaration, Links: docLinks(r)})
}

func (s *Server) AccessibilityDeclarationsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AccessibilityDeclarationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.appMeta.accessibility[id]
	if !ok {
		notFound(w, "accessibilityDeclarations", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AccessibilityDeclarationResponse{Data: d.AccessibilityDeclaration, Links: docLinks(r)})
}

func (s *Server) AccessibilityDeclarationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AccessibilityDeclarationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.appMeta.accessibility[id]
	if !ok {
		notFound(w, "accessibilityDeclarations", id)
		return
	}
	in := asc.Value(req.Data.Attributes)
	publish := asc.Value(in.Publish)
	in.Publish = nil
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, d.Attributes)
	if publish && *d.Attributes.State != gen.AccessibilityDeclarationAttributesStatePUBLISHED {
		for _, other := range s.appMeta.accessibility {
			if other.appID == d.appID && *other.Attributes.DeviceFamily == *d.Attributes.DeviceFamily &&
				*other.Attributes.State == gen.AccessibilityDeclarationAttributesStatePUBLISHED {
				other.Attributes.State = new(gen.AccessibilityDeclarationAttributesStateREPLACED)
			}
		}
		d.Attributes.State = new(gen.AccessibilityDeclarationAttributesStatePUBLISHED)
	}
	writeJSON(w, http.StatusOK, gen.AccessibilityDeclarationResponse{Data: d.AccessibilityDeclaration, Links: docLinks(r)})
}

func (s *Server) AccessibilityDeclarationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.appMeta.accessibility[id]; !ok {
		notFound(w, "accessibilityDeclarations", id)
		return
	}
	delete(s.appMeta.accessibility, id)
	w.WriteHeader(http.StatusNoContent)
}

// Android to iOS app mappings, which let the App Store match an app's
// Android version.

func (s *Server) AndroidToIosAppMappingDetailsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AndroidToIosAppMappingDetailCreateRequest
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
	m := &androidMapping{appID: appID, AndroidToIosAppMappingDetail: gen.AndroidToIosAppMappingDetail{Id: s.newID("Z"), Type: gen.AndroidToIosAppMappingDetailTypeAndroidToIosAppMappingDetails}}
	a := alloc(&m.Attributes)
	a.PackageName, a.AppSigningKeyPublicCertificateSha256Fingerprints = new(in.PackageName), new(slices.Clone(in.AppSigningKeyPublicCertificateSha256Fingerprints))
	s.appMeta.mappings[m.Id] = m
	writeJSON(w, http.StatusCreated, gen.AndroidToIosAppMappingDetailResponse{Data: m.AndroidToIosAppMappingDetail, Links: docLinks(r)})
}

func (s *Server) AndroidToIosAppMappingDetailsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AndroidToIosAppMappingDetailsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.appMeta.mappings[id]
	if !ok {
		notFound(w, "androidToIosAppMappingDetails", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AndroidToIosAppMappingDetailResponse{Data: m.AndroidToIosAppMappingDetail, Links: docLinks(r)})
}

func (s *Server) AndroidToIosAppMappingDetailsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AndroidToIosAppMappingDetailUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.appMeta.mappings[id]
	if !ok {
		notFound(w, "androidToIosAppMappingDetails", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.PackageName != nil {
			m.Attributes.PackageName = new(*in.PackageName)
		}
		if in.AppSigningKeyPublicCertificateSha256Fingerprints != nil {
			m.Attributes.AppSigningKeyPublicCertificateSha256Fingerprints = new(slices.Clone(*in.AppSigningKeyPublicCertificateSha256Fingerprints))
		}
	}
	writeJSON(w, http.StatusOK, gen.AndroidToIosAppMappingDetailResponse{Data: m.AndroidToIosAppMappingDetail, Links: docLinks(r)})
}

func (s *Server) AndroidToIosAppMappingDetailsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.appMeta.mappings[id]; !ok {
		notFound(w, "androidToIosAppMappingDetails", id)
		return
	}
	delete(s.appMeta.mappings, id)
	w.WriteHeader(http.StatusNoContent)
}

// App tags are assigned by Apple; apps can only choose whether each is
// visible on the App Store.

// AddAppTag adds a tag to an app, as Apple does, and returns its ID.
func (s *Server) AddAppTag(appID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &appTag{appID: appID, AppTag: gen.AppTag{Id: s.newID("K"), Type: gen.AppTagTypeAppTags}}
	a := alloc(&t.Attributes)
	a.Name, a.VisibleInAppStore = new(name), new(true)
	s.appMeta.tags[t.Id] = t
	return t.Id
}

func (s *Server) AppsAppTagsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppTagsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppTag
	for _, t := range sortedValues(s.appMeta.tags) {
		if t.appID == id && matches(params.FilterVisibleInAppStore, boolString(t.Attributes.VisibleInAppStore)) {
			items = append(items, t.AppTag)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppTagsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AppTagsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppTagUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.appMeta.tags[id]
	if !ok {
		notFound(w, "appTags", id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.VisibleInAppStore != nil {
		t.Attributes.VisibleInAppStore = new(*in.VisibleInAppStore)
	}
	writeJSON(w, http.StatusOK, gen.AppTagResponse{Data: t.AppTag, Links: docLinks(r)})
}
