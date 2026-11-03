package asctest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// The App Asset Library
// (https://developer.apple.com/documentation/appstoreconnectapi/placing-assets-on-your-app-store-surfaces).
// Each app has one library of images and videos, uploaded like screenshots
// but committed without a checksum. A placement shows an asset on a
// localization: an App Store version's, a custom product page's, an in-app
// event's or a product page optimization treatment's. Placements display
// in order within a placement group of a localization, which an ordering
// request sets. The fake checks that images are PNG or JPEG, and keeps
// localizations other than App Store version localizations as bare IDs,
// added with AddPlacementTarget.

type libraryAsset struct {
	*mediaItem
	video                bool
	libraryID            string
	category             string
	referenceName        string
	previewFrameTimeCode string
	created, modified    time.Time
}

// Placement target kinds, named by their relationships.
const (
	versionLocalizationTarget   = "appStoreVersionLocalization"
	productPageTarget           = "appCustomProductPageLocalization"
	eventTarget                 = "appEventLocalization"
	treatmentLocalizationTarget = "appStoreVersionExperimentTreatmentLocalization"
)

type placement struct {
	id, assetID          string
	video                bool
	targetKind, targetID string
	placementType, group string
	created              time.Time
}

type assetLibraryState struct {
	// libraries maps an app ID to its library's ID.
	libraries map[string]string
	assets    map[string]*libraryAsset
	// targets maps the ID of a localization other than a version
	// localization to its kind.
	targets    map[string]string
	placements map[string]*placement
	// order lists the placement IDs of a localization's placement group in
	// display order, keyed by placementKey.
	order map[string][]string
}

func newAssetLibraryState() *assetLibraryState {
	return &assetLibraryState{
		libraries: map[string]string{}, assets: map[string]*libraryAsset{}, targets: map[string]string{},
		placements: map[string]*placement{}, order: map[string][]string{},
	}
}

func placementKey(targetID, group string) string { return targetID + "|" + group }

// AddPlacementTarget adds a localization of a kind the fake doesn't model,
// such as "appEventLocalization", and returns its ID.
func (s *Server) AddPlacementTarget(kind string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID("PT")
	s.lib.targets[id] = kind
	return id
}

func (s *Server) libraryOf(appID string) string {
	if id, ok := s.lib.libraries[appID]; ok {
		return id
	}
	id := s.newID("AL")
	s.lib.libraries[appID] = id
	return id
}

func (s *Server) AppsAssetLibraryGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsAssetLibraryGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.appExists(id) {
		notFound(w, "apps", id)
		return
	}
	lib := gen.AppAssetLibrary{Id: s.libraryOf(id), Type: gen.AppAssetLibraryTypeAppAssetLibraries}
	writeJSON(w, http.StatusOK, gen.AppAssetLibraryResponse{Data: lib, Links: docLinks(r)})
}

func (s *Server) libraryExists(id string) bool {
	return slices.Contains(sortedValues(s.lib.libraries), id)
}

// ---- images and videos ----

func (s *Server) assetState(r *http.Request, a *libraryAsset) (state gen.AppAssetLibraryAssetState, ops *[]gen.UploadOperation, details *[]gen.StateDetail) {
	state = gen.AppAssetLibraryAssetState(a.state)
	if a.state == "AWAITING_UPLOAD" {
		ops = new(s.uploadOperations(r, a.mediaItem))
	}
	for _, e := range a.errors {
		*alloc(&details) = append(asc.Value(details), gen.StateDetail(e))
	}
	return state, ops, details
}

func (s *Server) renderLibraryImage(r *http.Request, a *libraryAsset) gen.AppAssetLibraryImage {
	out := gen.AppAssetLibraryImage{Id: a.id, Type: gen.AppAssetLibraryImageTypeAppAssetLibraryImages}
	v := gen.AppAssetLibraryImageAwaitingUploadAttributes{
		Category: new(gen.AppAssetLibraryAssetCategory(a.category)), FileName: new(a.fileName), FileSize: new(int64(a.size)),
		CreatedDate: new(gen.DateTime{Time: a.created}), LastModifiedDate: new(gen.DateTime{Time: a.modified}),
	}
	if a.referenceName != "" {
		v.ReferenceName = new(a.referenceName)
	}
	v.State, v.UploadOperations, v.StateDetails = s.assetState(r, a)
	if a.state == "COMPLETE" {
		v.SpecId = new("IMAGE_SPEC")
	}
	if err := alloc(&out.Attributes).FromAppAssetLibraryImageAwaitingUploadAttributes(v); err != nil {
		panic(err)
	}
	return out
}

func (s *Server) renderLibraryVideo(r *http.Request, a *libraryAsset) gen.AppAssetLibraryVideo {
	out := gen.AppAssetLibraryVideo{Id: a.id, Type: gen.AppAssetLibraryVideoTypeAppAssetLibraryVideos}
	v := gen.AppAssetLibraryVideoAwaitingUploadAttributes{
		Category: new(gen.AppAssetLibraryAssetCategory(a.category)), FileName: new(a.fileName), FileSize: new(int64(a.size)),
		CreatedDate: new(gen.DateTime{Time: a.created}), LastModifiedDate: new(gen.DateTime{Time: a.modified}),
	}
	if a.referenceName != "" {
		v.ReferenceName = new(a.referenceName)
	}
	if a.previewFrameTimeCode != "" {
		v.PreviewFrameTimeCode = new(a.previewFrameTimeCode)
	}
	v.State, v.UploadOperations, v.StateDetails = s.assetState(r, a)
	if a.state == "COMPLETE" {
		v.SpecId = new("VIDEO_SPEC")
	}
	if err := alloc(&out.Attributes).FromAppAssetLibraryVideoAwaitingUploadAttributes(v); err != nil {
		panic(err)
	}
	return out
}

var assetCategories = []string{string(gen.AppAssetLibraryAssetCategoryCREATIVEASSETS), string(gen.AppAssetLibraryAssetCategoryAPPSCREENSHOTSANDPREVIEWS)}

// reserveAsset validates and reserves an image or video, writing an error
// and returning nil when it can't.
func (s *Server) reserveAsset(w http.ResponseWriter, video bool, libraryID, category, fileName string, fileSize int64, referenceName *string) *libraryAsset {
	switch {
	case !s.libraryExists(libraryID):
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appAssetLibraries with id '"+libraryID+"'", "/data/relationships/assetLibrary")
		return nil
	case !slices.Contains(assetCategories, category):
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "category is invalid.", "/data/attributes/category")
		return nil
	case fileSize < 1 || fileName == "":
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "fileName and fileSize are required.", "/data/attributes/fileSize")
		return nil
	}
	now := s.now().UTC().Truncate(time.Second)
	a := &libraryAsset{
		mediaItem: &mediaItem{id: s.newID("LA"), fileName: fileName, size: int(fileSize), parts: map[int][]byte{}, state: "AWAITING_UPLOAD"},
		video:     video, libraryID: libraryID, category: category, referenceName: asc.Value(referenceName), created: now, modified: now,
	}
	s.media.items[a.id] = a.mediaItem
	s.lib.assets[a.id] = a
	return a
}

func (s *Server) findAsset(w http.ResponseWriter, video bool, id string) *libraryAsset {
	a, ok := s.lib.assets[id]
	if !ok || a.video != video {
		kind := "appAssetLibraryImages"
		if video {
			kind = "appAssetLibraryVideos"
		}
		notFound(w, kind, id)
		return nil
	}
	return a
}

// commitUpload commits an upload, which takes no checksum: the parts must
// add up to the file, and images must be PNG or JPEG.
func (a *libraryAsset) commitUpload() {
	data := a.data()
	a.commit(asc.Checksum(data))
	if a.state == "COMPLETE" && !a.video && !bytes.HasPrefix(data, []byte("\x89PNG")) && !bytes.HasPrefix(data, []byte("\xff\xd8\xff")) {
		a.state = "FAILED"
		a.errors = []gen.AppMediaStateError{{Code: new("IMAGE_INCORRECT_FORMAT"), Description: new("The image isn't a PNG or JPEG file.")}}
	}
}

func (s *Server) deleteAsset(w http.ResponseWriter, video bool, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findAsset(w, video, id)
	if a == nil {
		return
	}
	for _, p := range s.lib.placements {
		if p.assetID == id {
			stateError(w, "The asset has placements; delete them first.")
			return
		}
	}
	delete(s.lib.assets, id)
	delete(s.media.items, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppAssetLibraryImagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppAssetLibraryImageCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := req.Data.Attributes
	a := s.reserveAsset(w, false, req.Data.Relationships.AssetLibrary.Data.Id, string(in.Category), in.FileName, in.FileSize, in.ReferenceName)
	if a != nil {
		writeJSON(w, http.StatusCreated, gen.AppAssetLibraryImageResponse{Data: s.renderLibraryImage(r, a), Links: docLinks(r)})
	}
}

func (s *Server) AppAssetLibraryImagesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppAssetLibraryImagesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.findAsset(w, false, id); a != nil {
		writeJSON(w, http.StatusOK, gen.AppAssetLibraryImageResponse{Data: s.renderLibraryImage(r, a), Links: docLinks(r)})
	}
}

func (s *Server) AppAssetLibraryImagesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppAssetLibraryImageUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findAsset(w, false, id)
	if a == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	if in.ReferenceName != nil {
		a.referenceName = *in.ReferenceName
	}
	if asc.Value(in.Uploaded) && a.state == "AWAITING_UPLOAD" {
		a.commitUpload()
	}
	a.modified = s.now().UTC().Truncate(time.Second)
	writeJSON(w, http.StatusOK, gen.AppAssetLibraryImageResponse{Data: s.renderLibraryImage(r, a), Links: docLinks(r)})
}

func (s *Server) AppAssetLibraryImagesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.deleteAsset(w, false, id)
}

func (s *Server) AppAssetLibraryVideosCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppAssetLibraryVideoCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := req.Data.Attributes
	a := s.reserveAsset(w, true, req.Data.Relationships.AssetLibrary.Data.Id, string(in.Category), in.FileName, in.FileSize, in.ReferenceName)
	if a == nil {
		return
	}
	a.previewFrameTimeCode = asc.Value(in.PreviewFrameTimeCode)
	writeJSON(w, http.StatusCreated, gen.AppAssetLibraryVideoResponse{Data: s.renderLibraryVideo(r, a), Links: docLinks(r)})
}

func (s *Server) AppAssetLibraryVideosGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppAssetLibraryVideosGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.findAsset(w, true, id); a != nil {
		writeJSON(w, http.StatusOK, gen.AppAssetLibraryVideoResponse{Data: s.renderLibraryVideo(r, a), Links: docLinks(r)})
	}
}

func (s *Server) AppAssetLibraryVideosUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppAssetLibraryVideoUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findAsset(w, true, id)
	if a == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	if in.ReferenceName != nil {
		a.referenceName = *in.ReferenceName
	}
	if in.PreviewFrameTimeCode != nil {
		a.previewFrameTimeCode = *in.PreviewFrameTimeCode
	}
	if asc.Value(in.Uploaded) && a.state == "AWAITING_UPLOAD" {
		a.commitUpload()
	}
	a.modified = s.now().UTC().Truncate(time.Second)
	writeJSON(w, http.StatusOK, gen.AppAssetLibraryVideoResponse{Data: s.renderLibraryVideo(r, a), Links: docLinks(r)})
}

func (s *Server) AppAssetLibraryVideosDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.deleteAsset(w, true, id)
}

// libraryAssets returns a library's images or videos that pass keep, or
// false after writing a 404 for an unknown library.
func (s *Server) libraryAssets(w http.ResponseWriter, libraryID string, video bool, keep func(*libraryAsset) bool) ([]*libraryAsset, bool) {
	if !s.libraryExists(libraryID) {
		notFound(w, "appAssetLibraries", libraryID)
		return nil, false
	}
	var out []*libraryAsset
	for _, a := range sortedValues(s.lib.assets) {
		if a.libraryID == libraryID && a.video == video && keep(a) {
			out = append(out, a)
		}
	}
	return out, true
}

func (s *Server) AppAssetLibrariesImagesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppAssetLibrariesImagesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	assets, ok := s.libraryAssets(w, id, false, func(a *libraryAsset) bool {
		return matches(params.FilterCategory, a.category) && matches(params.FilterState, a.state) && matches(params.FilterReferenceName, a.referenceName)
	})
	if !ok {
		return
	}
	items := make([]gen.AppAssetLibraryImage, len(assets))
	for i, a := range assets {
		items[i] = s.renderLibraryImage(r, a)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if ok {
		writeJSON(w, http.StatusOK, gen.AppAssetLibraryImagesResponse{Data: nonNil(data), Links: links, Meta: meta})
	}
}

func (s *Server) AppAssetLibrariesVideosGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppAssetLibrariesVideosGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	assets, ok := s.libraryAssets(w, id, true, func(a *libraryAsset) bool {
		return matches(params.FilterCategory, a.category) && matches(params.FilterState, a.state) && matches(params.FilterReferenceName, a.referenceName)
	})
	if !ok {
		return
	}
	items := make([]gen.AppAssetLibraryVideo, len(assets))
	for i, a := range assets {
		items[i] = s.renderLibraryVideo(r, a)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if ok {
		writeJSON(w, http.StatusOK, gen.AppAssetLibraryVideosResponse{Data: nonNil(data), Links: links, Meta: meta})
	}
}

// ---- placements ----

func (s *Server) placementState(p *placement) gen.AppAssetLibraryPlacementState {
	switch s.lib.assets[p.assetID].state {
	case "COMPLETE":
		return gen.AppAssetLibraryPlacementStatePARENTPREPAREFORSUBMISSION
	case "FAILED":
		return gen.AppAssetLibraryPlacementStateFAILED
	}
	return gen.AppAssetLibraryPlacementStateASSETPROCESSING
}

func (s *Server) renderPlacement(r *http.Request, p *placement) gen.AppAssetLibraryPlacement {
	out := gen.AppAssetLibraryPlacement{Id: p.id, Type: gen.AppAssetLibraryPlacementResourceTypeAppAssetLibraryPlacements, Links: resourceLinks(r, "appAssetLibraryPlacements", p.id)}
	a := alloc(&out.Attributes)
	a.PlacementType, a.PlacementGroup = new(gen.AppAssetLibraryPlacementType(p.placementType)), new(p.group)
	a.CreatedDate, a.LastModifiedDate = new(gen.DateTime{Time: p.created}), new(gen.DateTime{Time: p.created})
	a.State = new(s.placementState(p))
	// Both relationship variants share the localization relationships, so
	// one marshals the image and the localization and the other the video.
	var rel map[string]any
	target := map[string]any{"data": map[string]string{"type": p.targetKind + "s", "id": p.targetID}}
	if p.video {
		a.MediaType = new(gen.AppAssetLibraryMediaTypeVIDEO)
		rel = map[string]any{"video": map[string]any{"data": map[string]string{"type": "appAssetLibraryVideos", "id": p.assetID}}, p.targetKind: target}
	} else {
		a.MediaType = new(gen.AppAssetLibraryMediaTypeIMAGE)
		rel = map[string]any{"image": map[string]any{"data": map[string]string{"type": "appAssetLibraryImages", "id": p.assetID}}, p.targetKind: target}
	}
	b, err := json.Marshal(rel)
	if err != nil {
		panic(err)
	}
	var relationships gen.AppAssetLibraryPlacement_Relationships
	if err := relationships.UnmarshalJSON(b); err != nil {
		panic(err)
	}
	out.Relationships = &relationships
	return out
}

// targetOf returns a placement request's target localization, or ok false
// unless there is exactly one.
func targetOf(ids map[string]string) (kind, id string, ok bool) {
	for k, v := range ids {
		if v == "" {
			continue
		}
		if id != "" {
			return "", "", false
		}
		kind, id = k, v
	}
	return kind, id, id != ""
}

// checkTarget writes an error and returns false unless the target exists and
// can be changed.
func (s *Server) checkTarget(w http.ResponseWriter, kind, id string) bool {
	if kind == versionLocalizationTarget {
		return s.editableLocalization(w, id)
	}
	if s.lib.targets[id] != kind {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+kind+"s with id '"+id+"'", "/data/relationships/"+kind)
		return false
	}
	return true
}

// placementLimits caps the placements of a type in a localization's group.
var placementLimits = map[string]int{"APP_SCREENSHOT": 10, "IMESSAGE_APP_SCREENSHOT": 10, "APP_PREVIEW": 3}

func (s *Server) AppAssetLibraryPlacementsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppAssetLibraryPlacementCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in, rel := asc.Value(req.Data.Attributes), asc.Value(req.Data.Relationships)
	var image, video string
	if rel.Image != nil && rel.Image.Data != nil {
		image = rel.Image.Data.Id
	}
	if rel.Video != nil && rel.Video.Data != nil {
		video = rel.Video.Data.Id
	}
	targets := map[string]string{}
	if rel.AppStoreVersionLocalization != nil && rel.AppStoreVersionLocalization.Data != nil {
		targets[versionLocalizationTarget] = rel.AppStoreVersionLocalization.Data.Id
	}
	if rel.AppCustomProductPageLocalization != nil && rel.AppCustomProductPageLocalization.Data != nil {
		targets[productPageTarget] = rel.AppCustomProductPageLocalization.Data.Id
	}
	if rel.AppEventLocalization != nil && rel.AppEventLocalization.Data != nil {
		targets[eventTarget] = rel.AppEventLocalization.Data.Id
	}
	if rel.AppStoreVersionExperimentTreatmentLocalization != nil && rel.AppStoreVersionExperimentTreatmentLocalization.Data != nil {
		targets[treatmentLocalizationTarget] = rel.AppStoreVersionExperimentTreatmentLocalization.Data.Id
	}
	kind, targetID, oneTarget := targetOf(targets)
	placementType, group := string(asc.Value(in.PlacementType)), asc.Value(in.PlacementGroup)
	isEvent := placementType == "EVENT_CARD_ASSET" || placementType == "EVENT_DETAILS_PAGE_ASSET"
	switch {
	case (image == "") == (video == ""):
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "A placement needs exactly one image or video.", "/data/relationships")
		return
	case !oneTarget:
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "A placement needs exactly one localization.", "/data/relationships")
		return
	case placementType == "" || group == "":
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.REQUIRED", "placementType and placementGroup are required.", "/data/attributes")
		return
	case (placementType == "APP_SCREENSHOT" || placementType == "IMESSAGE_APP_SCREENSHOT") && image == "",
		placementType == "APP_PREVIEW" && video == "":
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A "+placementType+" placement can't show this media type.", "/data/attributes/placementType")
		return
	case isEvent != (kind == eventTarget):
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Only in-app event localizations take event placements.", "/data/attributes/placementType")
		return
	}
	assetID := image + video
	if s.findAsset(w, video != "", assetID) == nil || !s.checkTarget(w, kind, targetID) {
		return
	}
	key := placementKey(targetID, group)
	if limit, ok := placementLimits[placementType]; ok {
		n := 0
		for _, id := range s.lib.order[key] {
			if s.lib.placements[id].placementType == placementType {
				n++
			}
		}
		if n >= limit {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The placement group is full.", "/data/relationships")
			return
		}
	}
	p := &placement{
		id: s.newID("PL"), assetID: assetID, video: video != "", targetKind: kind, targetID: targetID,
		placementType: placementType, group: group, created: s.now().UTC().Truncate(time.Second),
	}
	s.lib.placements[p.id] = p
	s.lib.order[key] = append(s.lib.order[key], p.id)
	writeJSON(w, http.StatusCreated, gen.AppAssetLibraryPlacementResponse{Data: s.renderPlacement(r, p), Links: docLinks(r)})
}

func (s *Server) findPlacement(w http.ResponseWriter, id string) *placement {
	p, ok := s.lib.placements[id]
	if !ok {
		notFound(w, "appAssetLibraryPlacements", id)
	}
	return p
}

func (s *Server) AppAssetLibraryPlacementsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppAssetLibraryPlacementsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.findPlacement(w, id); p != nil {
		writeJSON(w, http.StatusOK, gen.AppAssetLibraryPlacementResponse{Data: s.renderPlacement(r, p), Links: docLinks(r)})
	}
}

func (s *Server) AppAssetLibraryPlacementsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.findPlacement(w, id)
	if p == nil || !s.checkTarget(w, p.targetKind, p.targetID) {
		return
	}
	delete(s.lib.placements, id)
	key := placementKey(p.targetID, p.group)
	s.lib.order[key] = slices.DeleteFunc(s.lib.order[key], func(other string) bool { return other == id })
	w.WriteHeader(http.StatusNoContent)
}

// listPlacements writes the placements that pass keep and the type and
// group filters, in display order when byPosition is set.
func (s *Server) listPlacements(w http.ResponseWriter, r *http.Request, keep func(*placement) bool, types []string, groups *[]string, byPosition bool, limit *int) {
	var ps []*placement
	for _, p := range sortedValues(s.lib.placements) {
		if keep(p) && (types == nil || slices.Contains(types, p.placementType)) && matches(groups, p.group) {
			ps = append(ps, p)
		}
	}
	if byPosition {
		slices.SortStableFunc(ps, func(a, b *placement) int {
			if a.targetID != b.targetID || a.group != b.group {
				return 0
			}
			order := s.lib.order[placementKey(a.targetID, a.group)]
			return slices.Index(order, a.id) - slices.Index(order, b.id)
		})
	}
	items := make([]gen.AppAssetLibraryPlacement, len(ps))
	for i, p := range ps {
		items[i] = s.renderPlacement(r, p)
	}
	data, links, meta, ok := page(w, r, items, limit, maxLimit)
	if ok {
		writeJSON(w, http.StatusOK, gen.AppAssetLibraryPlacementsResponse{Data: nonNil(data), Links: links, Meta: meta})
	}
}

func stringsOf[T ~string](values *[]T) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(*values))
	for i, v := range *values {
		out[i] = string(v)
	}
	return out
}

func sortsByPosition[T ~string](sort *[]T) bool {
	return slices.Contains(stringsOf(sort), "placementGroupPosition")
}

func (s *Server) AppAssetLibraryImagesPlacementsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppAssetLibraryImagesPlacementsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findAsset(w, false, id) != nil {
		s.listPlacements(w, r, func(p *placement) bool { return p.assetID == id }, stringsOf(params.FilterPlacementType), params.FilterPlacementGroup, sortsByPosition(params.Sort), params.Limit)
	}
}

func (s *Server) AppAssetLibraryVideosPlacementsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppAssetLibraryVideosPlacementsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findAsset(w, true, id) != nil {
		s.listPlacements(w, r, func(p *placement) bool { return p.assetID == id }, stringsOf(params.FilterPlacementType), params.FilterPlacementGroup, sortsByPosition(params.Sort), params.Limit)
	}
}

func (s *Server) AppStoreVersionLocalizationsPlacementsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionLocalizationsPlacementsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, _ := s.findVersionLocalization(id); l == nil {
		notFound(w, "appStoreVersionLocalizations", id)
		return
	}
	s.listPlacements(w, r, func(p *placement) bool { return p.targetID == id }, stringsOf(params.FilterPlacementType), params.FilterPlacementGroup, sortsByPosition(params.Sort), params.Limit)
}

func (s *Server) targetPlacements(w http.ResponseWriter, r *http.Request, kind, id string, types []string, groups *[]string, byPosition bool, limit *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lib.targets[id] != kind {
		notFound(w, kind+"s", id)
		return
	}
	s.listPlacements(w, r, func(p *placement) bool { return p.targetID == id }, types, groups, byPosition, limit)
}

func (s *Server) AppCustomProductPageLocalizationsPlacementsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppCustomProductPageLocalizationsPlacementsGetToManyRelatedParams) {
	s.targetPlacements(w, r, productPageTarget, id, stringsOf(params.FilterPlacementType), params.FilterPlacementGroup, sortsByPosition(params.Sort), params.Limit)
}

func (s *Server) AppStoreVersionExperimentTreatmentLocalizationsPlacementsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionExperimentTreatmentLocalizationsPlacementsGetToManyRelatedParams) {
	s.targetPlacements(w, r, treatmentLocalizationTarget, id, stringsOf(params.FilterPlacementType), params.FilterPlacementGroup, sortsByPosition(params.Sort), params.Limit)
}

// AppAssetLibraryPlacementOrderingRequestsCreateInstance sets the display
// order of a localization's placement group, which must list each of the
// group's placements once. In-app event placements can't be ordered.
func (s *Server) AppAssetLibraryPlacementOrderingRequestsCreateInstance(w http.ResponseWriter, r *http.Request, _ gen.AppAssetLibraryPlacementOrderingRequestsCreateInstanceParams) {
	var req gen.AppAssetLibraryPlacementOrderingRequestCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := asc.Value(req.Data.Relationships)
	targets := map[string]string{}
	if rel.AppStoreVersionLocalization != nil && rel.AppStoreVersionLocalization.Data != nil {
		targets[versionLocalizationTarget] = rel.AppStoreVersionLocalization.Data.Id
	}
	if rel.AppCustomProductPageLocalization != nil && rel.AppCustomProductPageLocalization.Data != nil {
		targets[productPageTarget] = rel.AppCustomProductPageLocalization.Data.Id
	}
	if rel.AppStoreVersionExperimentTreatmentLocalization != nil && rel.AppStoreVersionExperimentTreatmentLocalization.Data != nil {
		targets[treatmentLocalizationTarget] = rel.AppStoreVersionExperimentTreatmentLocalization.Data.Id
	}
	kind, targetID, ok := targetOf(targets)
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "An ordering request needs exactly one localization.", "/data/relationships")
		return
	}
	group := asc.Value(asc.Value(req.Data.Attributes).PlacementGroup)
	if group == "" {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.REQUIRED", "placementGroup is required.", "/data/attributes/placementGroup")
		return
	}
	if !s.checkTarget(w, kind, targetID) {
		return
	}
	var ids []string
	if rel.OrderedPlacements != nil {
		for _, d := range asc.Value(rel.OrderedPlacements.Data) {
			ids = append(ids, d.Id)
		}
	}
	key := placementKey(targetID, group)
	current := slices.Sorted(slices.Values(s.lib.order[key]))
	if !slices.Equal(slices.Sorted(slices.Values(ids)), current) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "orderedPlacements must list each placement of the group once.", "/data/relationships/orderedPlacements")
		return
	}
	s.lib.order[key] = ids
	out := gen.AppAssetLibraryPlacementOrderingRequest{Id: s.newID("PO"), Type: gen.AppAssetLibraryPlacementOrderingRequestTypeAppAssetLibraryPlacementOrderingRequests}
	writeJSON(w, http.StatusCreated, gen.AppAssetLibraryPlacementOrderingRequestResponse{Data: out, Links: docLinks(r)})
}

// ---- reference data ----

// AppAssetLibraryRefDataGetCollection returns a small sample of Apple's
// reference data.
func (s *Server) AppAssetLibraryRefDataGetCollection(w http.ResponseWriter, r *http.Request, _ gen.AppAssetLibraryRefDataGetCollectionParams) {
	var d gen.AppAssetLibraryRefDatum
	if err := json.Unmarshal([]byte(refData), &d); err != nil {
		panic(err)
	}
	writeJSON(w, http.StatusOK, gen.AppAssetLibraryRefDataResponse{Data: []gen.AppAssetLibraryRefDatum{d}, Links: gen.PagedDocumentLinks{Self: selfURL(r)}})
}

const refData = `{
  "type": "appAssetLibraryRefData",
  "id": "REF",
  "attributes": {
    "features": [{
      "featureId": "APP_STORE_VERSIONS",
      "placementPolicies": [
        {"placementType": "APP_SCREENSHOT", "groupLimits": [{"groupIds": ["IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", "IPAD_13_PROFILE"], "maxCount": 10}]},
        {"placementType": "APP_PREVIEW", "groupLimits": [{"groupIds": ["IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"], "maxCount": 3}]}
      ]
    }],
    "placementProfileGroups": [
      {"placementProfileGroupId": "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", "platform": "IPHONE_APP_STORE", "displayClassId": "IPHONE_DYNAMIC_ISLAND_LARGE_DISPLAY"},
      {"placementProfileGroupId": "IPAD_13_PROFILE", "platform": "IPAD_APP_STORE", "displayClassId": "IPAD_13_DISPLAY"}
    ],
    "imageSpecs": [{
      "specId": "IMAGE_SPEC", "shortName": "iPhone 6.9-inch screenshot", "aspectRatio": "9:19.5",
      "dimensions": {"minWidth": 1290, "maxWidth": 1320, "minHeight": 2796, "maxHeight": 2868},
      "compatiblePlacementTypes": ["APP_SCREENSHOT"], "fileExtensions": ["png", "jpg"], "maxFileSize": 10000000
    }],
    "videoSpecs": [{
      "specId": "VIDEO_SPEC", "shortName": "iPhone 6.9-inch preview", "aspectRatio": "9:19.5",
      "dimensions": {"minWidth": 886, "maxWidth": 886, "minHeight": 1920, "maxHeight": 1920},
      "compatiblePlacementTypes": ["APP_PREVIEW"], "fileExtensions": ["mov", "mp4"], "maxFileSize": 500000000
    }]
  }
}`
