package asctest

import (
	"net/http"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// In-app purchases. A purchase holds its product ID, type and pricing; its
// localized metadata and promotional image belong to a version, which is
// edited while Prepare for Submission, goes through App Review, and is
// read-only after
// (https://developer.apple.com/documentation/appstoreconnectapi/working-with-in-app-purchase-versions).
// The fake's new version starts with copies of the latest version's
// localizations and image, as app info does when a new app version is
// created. The App Review screenshot belongs to the purchase itself.

type iap struct {
	gen.InAppPurchaseV2
	appID    string
	versions []string
}

type iapVersion struct {
	gen.InAppPurchaseVersion
	iapID         string
	localizations map[string]*gen.InAppPurchaseLocalizationV2
}

const (
	iapImage            fileKind = "inAppPurchaseImages"
	iapReviewScreenshot fileKind = "inAppPurchaseAppStoreReviewScreenshots"
)

type iapState struct {
	iaps     map[string]*iap
	versions map[string]*iapVersion
}

func newIAPState() *iapState {
	return &iapState{iaps: map[string]*iap{}, versions: map[string]*iapVersion{}}
}

var iapVersionsInReview = []gen.InAppPurchaseVersionAttributesState{
	gen.InAppPurchaseVersionAttributesStateREADYFORREVIEW, gen.InAppPurchaseVersionAttributesStateWAITINGFORREVIEW,
	gen.InAppPurchaseVersionAttributesStateINREVIEW,
}

func (v *iapVersion) state() gen.InAppPurchaseVersionAttributesState { return *v.Attributes.State }

func (s *Server) latestIAPVersion(p *iap) *iapVersion {
	if len(p.versions) == 0 {
		return nil
	}
	return s.iap.versions[p.versions[len(p.versions)-1]]
}

// refreshIAPState derives a purchase's state from its versions.
func (s *Server) refreshIAPState(p *iap) {
	state := gen.InAppPurchaseStateMISSINGMETADATA
	for _, id := range p.versions {
		v := s.iap.versions[id]
		switch {
		case v.state() == gen.InAppPurchaseVersionAttributesStateAPPROVED:
			state = gen.InAppPurchaseStateAPPROVED
		case slices.Contains(iapVersionsInReview, v.state()):
			state = gen.InAppPurchaseStateWAITINGFORREVIEW
		case v.state() == gen.InAppPurchaseVersionAttributesStatePREPAREFORSUBMISSION && len(v.localizations) > 0 && state != gen.InAppPurchaseStateAPPROVED:
			state = gen.InAppPurchaseStateREADYTOSUBMIT
		}
	}
	p.Attributes.State = new(state)
}

func (s *Server) renderIAP(p *iap) gen.InAppPurchaseV2 {
	out := p.InAppPurchaseV2
	attrs := *p.Attributes
	out.Attributes = &attrs
	return out
}

func (s *Server) InAppPurchasesV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.InAppPurchaseV2CreateRequest
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
	if !in.InAppPurchaseType.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "inAppPurchaseType is invalid.", "/data/attributes/inAppPurchaseType")
		return
	}
	for _, p := range s.iap.iaps {
		if p.appID == appID && *p.Attributes.ProductId == in.ProductId {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The product ID "+in.ProductId+" is already used.", "/data/attributes/productId")
			return
		}
	}
	p := &iap{appID: appID, InAppPurchaseV2: gen.InAppPurchaseV2{Id: s.newID("P"), Type: gen.InAppPurchaseV2TypeInAppPurchases}}
	a := alloc(&p.Attributes)
	a.Name, a.ProductId, a.InAppPurchaseType = new(in.Name), new(in.ProductId), new(in.InAppPurchaseType)
	a.ReviewNote, a.FamilySharable, a.ContentHosting = in.ReviewNote, new(asc.Value(in.FamilySharable)), new(false)
	s.iap.iaps[p.Id] = p
	s.refreshIAPState(p)
	writeJSON(w, http.StatusCreated, gen.InAppPurchaseV2Response{Data: s.renderIAP(p), Links: docLinks(r)})
}

func (s *Server) InAppPurchasesV2GetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchasesV2GetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.iap.iaps[id]
	if !ok {
		notFound(w, "inAppPurchases", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseV2Response{Data: s.renderIAP(p), Links: docLinks(r)})
}

func (s *Server) AppsInAppPurchasesV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsInAppPurchasesV2GetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.InAppPurchaseV2
	for _, p := range sortedValues(s.iap.iaps) {
		a := p.Attributes
		if p.appID == id && matches(params.FilterProductId, *a.ProductId) && matches(params.FilterName, *a.Name) &&
			matches(params.FilterState, string(*a.State)) && matches(params.FilterInAppPurchaseType, string(*a.InAppPurchaseType)) {
			items = append(items, s.renderIAP(p))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchasesV2Response{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) InAppPurchasesV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.InAppPurchaseV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.iap.iaps[id]
	if !ok {
		notFound(w, "inAppPurchases", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		setString(&p.Attributes.Name, in.Name)
		setString(&p.Attributes.ReviewNote, in.ReviewNote)
		if in.FamilySharable != nil {
			// Family Sharing can't be turned off once on ("Turn on Family
			// Sharing for in-app purchases",
			// https://developer.apple.com/help/app-store-connect/manage-in-app-purchases/turn-on-family-sharing-for-in-app-purchases).
			if *p.Attributes.FamilySharable && !*in.FamilySharable {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Family Sharing can't be turned off once it's on.", "/data/attributes/familySharable")
				return
			}
			p.Attributes.FamilySharable = new(*in.FamilySharable)
		}
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseV2Response{Data: s.renderIAP(p), Links: docLinks(r)})
}

// InAppPurchasesV2DeleteInstance deletes a purchase that was never
// approved; an approved purchase can only be removed from sale.
func (s *Server) InAppPurchasesV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.iap.iaps[id]
	if !ok {
		notFound(w, "inAppPurchases", id)
		return
	}
	for _, vid := range p.versions {
		if s.iap.versions[vid].state() == gen.InAppPurchaseVersionAttributesStateAPPROVED || s.iap.versions[vid].state() == gen.InAppPurchaseVersionAttributesStateREPLACEDWITHNEWVERSION {
			stateError(w, "An approved in-app purchase can't be deleted.")
			return
		}
	}
	for _, vid := range p.versions {
		delete(s.iap.versions, vid)
	}
	for _, f := range s.files {
		if f.kind == iapReviewScreenshot && f.parentID == id {
			s.removeFile(f)
		}
	}
	delete(s.iap.iaps, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- versions ----

func renderIAPVersion(v *iapVersion) gen.InAppPurchaseVersion {
	out := v.InAppPurchaseVersion
	attrs := *v.Attributes
	out.Attributes = &attrs
	return out
}

func (s *Server) InAppPurchasesV2VersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.InAppPurchasesV2VersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.iap.iaps[id]
	if !ok {
		notFound(w, "inAppPurchases", id)
		return
	}
	var items []gen.InAppPurchaseVersion
	for _, vid := range p.versions {
		v := s.iap.versions[vid]
		if matches(params.FilterState, string(v.state())) {
			items = append(items, renderIAPVersion(v))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// InAppPurchaseVersionsCreateInstance starts a purchase's next version. A
// purchase can't have two open versions.
func (s *Server) InAppPurchaseVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.InAppPurchaseVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	iapID := req.Data.Relationships.InAppPurchase.Data.Id
	p, ok := s.iap.iaps[iapID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no inAppPurchases with id '"+iapID+"'", "/data/relationships/inAppPurchase")
		return
	}
	previous := s.latestIAPVersion(p)
	if previous != nil && (previous.state() == gen.InAppPurchaseVersionAttributesStatePREPAREFORSUBMISSION || slices.Contains(iapVersionsInReview, previous.state())) {
		stateError(w, "The in-app purchase already has an open version.")
		return
	}
	v := &iapVersion{iapID: iapID, localizations: map[string]*gen.InAppPurchaseLocalizationV2{},
		InAppPurchaseVersion: gen.InAppPurchaseVersion{Id: s.newID("V"), Type: gen.InAppPurchaseVersionTypeInAppPurchaseVersions}}
	a := alloc(&v.Attributes)
	a.State, a.Version = new(gen.InAppPurchaseVersionAttributesStatePREPAREFORSUBMISSION), new(len(p.versions)+1)
	if previous != nil {
		for _, l := range previous.localizations {
			c := &gen.InAppPurchaseLocalizationV2{Id: s.newID("L"), Type: gen.InAppPurchaseLocalizationV2TypeInAppPurchaseLocalizations}
			attrs := *l.Attributes
			c.Attributes = &attrs
			v.localizations[c.Id] = c
		}
		for _, f := range s.files {
			if f.kind == iapImage && f.parentID == previous.Id {
				c := s.newFile(iapImage, v.Id, f.fileName, f.size)
				c.state, c.checksum = f.state, f.checksum
				break
			}
		}
	}
	s.iap.versions[v.Id] = v
	p.versions = append(p.versions, v.Id)
	s.refreshIAPState(p)
	writeJSON(w, http.StatusCreated, gen.InAppPurchaseVersionResponse{Data: renderIAPVersion(v), Links: docLinks(r)})
}

func (s *Server) editableIAPVersion(w http.ResponseWriter, id string) *iapVersion {
	v, ok := s.iap.versions[id]
	if !ok {
		notFound(w, "inAppPurchaseVersions", id)
		return nil
	}
	if v.state() != gen.InAppPurchaseVersionAttributesStatePREPAREFORSUBMISSION {
		stateError(w, "The in-app purchase version is read-only in its current state: "+string(v.state())+".")
		return nil
	}
	return v
}

// ---- localizations ----

func (s *Server) InAppPurchaseVersionsLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.InAppPurchaseVersionsLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.iap.versions[id]
	if !ok {
		notFound(w, "inAppPurchaseVersions", id)
		return
	}
	var items []gen.InAppPurchaseLocalizationV2
	for _, l := range sortedValues(v.localizations) {
		items = append(items, *l)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseLocalizationsV2Response{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) findIAPLocalization(w http.ResponseWriter, id string) (*gen.InAppPurchaseLocalizationV2, *iapVersion) {
	for _, v := range s.iap.versions {
		if l, ok := v.localizations[id]; ok {
			if s.editableIAPVersion(w, v.Id) == nil {
				return nil, nil
			}
			return l, v
		}
	}
	notFound(w, "inAppPurchaseLocalizations", id)
	return nil, nil
}

func (s *Server) InAppPurchaseLocalizationsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.InAppPurchaseLocalizationV2CreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableIAPVersion(w, req.Data.Relationships.Version.Data.Id)
	if v == nil {
		return
	}
	for _, l := range v.localizations {
		if *l.Attributes.Locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A localization for locale "+in.Locale+" already exists.", "/data/attributes/locale")
			return
		}
	}
	if len(in.Name) < 2 || len(in.Name) > 35 {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The display name must be 2 to 35 characters.", "/data/attributes/name")
		return
	}
	l := &gen.InAppPurchaseLocalizationV2{Id: s.newID("L"), Type: gen.InAppPurchaseLocalizationV2TypeInAppPurchaseLocalizations}
	a := alloc(&l.Attributes)
	a.Locale, a.Name, a.Description = new(in.Locale), new(in.Name), in.Description
	v.localizations[l.Id] = l
	s.refreshIAPState(s.iap.iaps[v.iapID])
	writeJSON(w, http.StatusCreated, gen.InAppPurchaseLocalizationV2Response{Data: *l, Links: docLinks(r)})
}

func (s *Server) InAppPurchaseLocalizationsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.InAppPurchaseLocalizationV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, _ := s.findIAPLocalization(w, id)
	if l == nil {
		return
	}
	if in := req.Data.Attributes; in != nil {
		setString(&l.Attributes.Name, in.Name)
		setString(&l.Attributes.Description, in.Description)
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseLocalizationV2Response{Data: *l, Links: docLinks(r)})
}

func (s *Server) InAppPurchaseLocalizationsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, v := s.findIAPLocalization(w, id)
	if l == nil {
		return
	}
	delete(v.localizations, id)
	s.refreshIAPState(s.iap.iaps[v.iapID])
	w.WriteHeader(http.StatusNoContent)
}

// ---- promotional image (one per version) ----

func (s *Server) renderIAPImage(r *http.Request, f *uploadedFile) gen.InAppPurchaseImageV2 {
	out := gen.InAppPurchaseImageV2{Id: f.id, Type: gen.InAppPurchaseImageV2TypeInAppPurchaseImages}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, _ = s.fileAttributes(r, f)
	return out
}

func (s *Server) InAppPurchaseImagesV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.InAppPurchaseImageV2CreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableIAPVersion(w, req.Data.Relationships.Version.Data.Id)
	if v == nil {
		return
	}
	for _, f := range s.files {
		if f.kind == iapImage && f.parentID == v.Id {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has an image.", "/data/relationships/version")
			return
		}
	}
	f := s.newFile(iapImage, v.Id, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.InAppPurchaseImageV2Response{Data: s.renderIAPImage(r, f), Links: docLinks(r)})
}

func (s *Server) InAppPurchaseImagesV2GetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchaseImagesV2GetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, iapImage, id); f != nil {
		writeJSON(w, http.StatusOK, gen.InAppPurchaseImageV2Response{Data: s.renderIAPImage(r, f), Links: docLinks(r)})
	}
}

func (s *Server) InAppPurchaseVersionsImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchaseVersionsImageGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.iap.versions[id]; !ok {
		notFound(w, "inAppPurchaseVersions", id)
		return
	}
	for _, f := range s.files {
		if f.kind == iapImage && f.parentID == id {
			writeJSON(w, http.StatusOK, gen.InAppPurchaseImageV2Response{Data: s.renderIAPImage(r, f), Links: docLinks(r)})
			return
		}
	}
	notFound(w, "inAppPurchaseImages", id)
}

// InAppPurchaseImagesV2UpdateInstance commits an image. The v2 request has
// no checksum, so the fake can only check that every byte arrived.
func (s *Server) InAppPurchaseImagesV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.InAppPurchaseImageV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, iapImage, id)
	if f == nil {
		return
	}
	if in := asc.Value(req.Data.Attributes); asc.Value(in.Uploaded) && f.state == "AWAITING_UPLOAD" {
		var data []byte
		for i := 0; i*uploadPartSize < f.size; i++ {
			data = append(data, f.parts[i]...)
		}
		f.commit(asc.Checksum(data))
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseImageV2Response{Data: s.renderIAPImage(r, f), Links: docLinks(r)})
}

func (s *Server) InAppPurchaseImagesV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, iapImage, id)
	if f == nil || s.editableIAPVersion(w, f.parentID) == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

// ---- App Review screenshot (one per purchase) ----

func (s *Server) renderIAPReviewScreenshot(r *http.Request, f *uploadedFile) gen.InAppPurchaseAppStoreReviewScreenshot {
	out := gen.InAppPurchaseAppStoreReviewScreenshot{Id: f.id, Type: gen.InAppPurchaseAppStoreReviewScreenshotTypeInAppPurchaseAppStoreReviewScreenshots}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, a.SourceFileChecksum = s.fileAttributes(r, f)
	return out
}

func (s *Server) InAppPurchaseAppStoreReviewScreenshotsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.InAppPurchaseAppStoreReviewScreenshotCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	iapID := req.Data.Relationships.InAppPurchaseV2.Data.Id
	if _, ok := s.iap.iaps[iapID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no inAppPurchases with id '"+iapID+"'", "/data/relationships/inAppPurchaseV2")
		return
	}
	for _, f := range s.files {
		if f.kind == iapReviewScreenshot && f.parentID == iapID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The in-app purchase already has a review screenshot.", "/data/relationships/inAppPurchaseV2")
			return
		}
	}
	f := s.newFile(iapReviewScreenshot, iapID, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.InAppPurchaseAppStoreReviewScreenshotResponse{Data: s.renderIAPReviewScreenshot(r, f), Links: docLinks(r)})
}

func (s *Server) InAppPurchaseAppStoreReviewScreenshotsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchaseAppStoreReviewScreenshotsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, iapReviewScreenshot, id); f != nil {
		writeJSON(w, http.StatusOK, gen.InAppPurchaseAppStoreReviewScreenshotResponse{Data: s.renderIAPReviewScreenshot(r, f), Links: docLinks(r)})
	}
}

func (s *Server) InAppPurchasesV2AppStoreReviewScreenshotGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchasesV2AppStoreReviewScreenshotGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.iap.iaps[id]; !ok {
		notFound(w, "inAppPurchases", id)
		return
	}
	for _, f := range s.files {
		if f.kind == iapReviewScreenshot && f.parentID == id {
			writeJSON(w, http.StatusOK, gen.InAppPurchaseAppStoreReviewScreenshotResponse{Data: s.renderIAPReviewScreenshot(r, f), Links: docLinks(r)})
			return
		}
	}
	notFound(w, "inAppPurchaseAppStoreReviewScreenshots", id)
}

func (s *Server) InAppPurchaseAppStoreReviewScreenshotsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.InAppPurchaseAppStoreReviewScreenshotUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, iapReviewScreenshot, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	commitFile(f, in.Uploaded, in.SourceFileChecksum)
	writeJSON(w, http.StatusOK, gen.InAppPurchaseAppStoreReviewScreenshotResponse{Data: s.renderIAPReviewScreenshot(r, f), Links: docLinks(r)})
}

func (s *Server) InAppPurchaseAppStoreReviewScreenshotsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, iapReviewScreenshot, id)
	if f == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

// ---- review ----

// submitIAPVersion adds a purchase version to a review submission.
func (s *Server) addIAPVersionItem(w http.ResponseWriter, sub *reviewSubmission, versionID string) bool {
	v := s.editableIAPVersion(w, versionID)
	if v == nil {
		return false
	}
	if p := s.iap.iaps[v.iapID]; p.appID != sub.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The in-app purchase belongs to another app.", "/data/relationships/inAppPurchaseVersion")
		return false
	}
	if len(v.localizations) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The in-app purchase version has no localizations.", "/data/relationships/inAppPurchaseVersion")
		return false
	}
	v.Attributes.State = new(gen.InAppPurchaseVersionAttributesStateREADYFORREVIEW)
	sub.iapVersions = append(sub.iapVersions, v.Id)
	s.refreshIAPState(s.iap.iaps[v.iapID])
	return true
}

// setIAPVersionStates moves a submission's purchase versions to state.
func (s *Server) setIAPVersionStates(sub *reviewSubmission, state gen.InAppPurchaseVersionAttributesState) {
	for _, id := range sub.iapVersions {
		v := s.iap.versions[id]
		v.Attributes.State = new(state)
		s.refreshIAPState(s.iap.iaps[v.iapID])
	}
}

// ApproveInAppPurchase approves a purchase's version in review, as App
// Review does; the previously approved version is replaced.
func (s *Server) ApproveInAppPurchase(iapID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.iap.iaps[iapID]
	latest := s.latestIAPVersion(p)
	for _, id := range p.versions {
		if v := s.iap.versions[id]; v != latest && v.state() == gen.InAppPurchaseVersionAttributesStateAPPROVED {
			v.Attributes.State = new(gen.InAppPurchaseVersionAttributesStateREPLACEDWITHNEWVERSION)
		}
	}
	latest.Attributes.State = new(gen.InAppPurchaseVersionAttributesStateAPPROVED)
	s.refreshIAPState(p)
}

// IAPVersionStates returns the states of a purchase's versions, oldest first.
func (s *Server) IAPVersionStates(iapID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range s.iap.iaps[iapID].versions {
		out = append(out, string(s.iap.versions[id].state()))
	}
	return out
}
