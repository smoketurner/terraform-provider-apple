package asctest

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Screenshots and app previews. A version localization has at most one set
// per display type, holding up to 10 screenshots or 3 previews in order
// ("Screenshot specifications",
// https://developer.apple.com/help/app-store-connect/reference/screenshot-specifications).
// Each file is uploaded in three steps: reserve it, which returns upload
// operations; send its parts; and commit it with its MD5 checksum, which
// fails it if the parts don't match
// (https://developer.apple.com/documentation/appstoreconnectapi/uploading-assets-to-app-store-connect).

// uploadPartSize is the fake's part size, small so tests send several parts.
const uploadPartSize = 64

type mediaKind struct {
	set, item string // JSON:API types, e.g. "appScreenshotSets", "appScreenshots"
	max       int
}

var (
	screenshotKind = mediaKind{set: "appScreenshotSets", item: "appScreenshots", max: 10}
	previewKind    = mediaKind{set: "appPreviewSets", item: "appPreviews", max: 3}
)

type mediaSet struct {
	kind           mediaKind
	id             string
	localizationID string
	displayType    string
	items          []string
}

type mediaItem struct {
	id, setID string
	fileName  string
	size      int
	parts     map[int][]byte
	checksum  string
	state     string // AWAITING_UPLOAD, COMPLETE or FAILED
	errors    []gen.AppMediaStateError
	frameTime *string
}

type mediaState struct {
	sets  map[string]*mediaSet
	items map[string]*mediaItem
}

func newMediaState() *mediaState {
	return &mediaState{sets: map[string]*mediaSet{}, items: map[string]*mediaItem{}}
}

// uploadHandler receives file parts at /upload/{item}/{part}. Upload URLs
// are pre-signed, so a request that carries an API token is a bug.
func (s *Server) uploadHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "" {
		http.Error(w, "upload requests must not carry the API token", http.StatusBadRequest)
		return
	}
	if queued := s.failNext[r.Method+" /upload"]; len(queued) > 0 {
		s.failNext[r.Method+" /upload"] = queued[1:]
		http.Error(w, "injected failure", queued[0])
		return
	}
	item, ok := s.media.items[r.PathValue("item")]
	part, err := strconv.Atoi(r.PathValue("part"))
	if !ok || err != nil || r.Method != http.MethodPut || item.state != "AWAITING_UPLOAD" {
		http.Error(w, "no upload is waiting for this part", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	item.parts[part] = body
	w.WriteHeader(http.StatusOK)
}

func (s *Server) uploadOperations(r *http.Request, item *mediaItem) []gen.UploadOperation {
	var ops []gen.UploadOperation
	for i, offset := 0, 0; offset < item.size; i, offset = i+1, offset+uploadPartSize {
		length := min(uploadPartSize, item.size-offset)
		ops = append(ops, gen.UploadOperation{
			Method: new(http.MethodPut), Url: new(fmt.Sprintf("http://%s/upload/%s/%d", r.Host, item.id, i)),
			Offset: new(offset), Length: new(length),
			RequestHeaders: &[]gen.HttpHeader{{Name: new("Content-Type"), Value: new("application/octet-stream")}},
		})
	}
	return ops
}

// data returns the parts received so far, in order.
func (item *mediaItem) data() []byte {
	var data []byte
	for i := 0; i*uploadPartSize < item.size; i++ {
		data = append(data, item.parts[i]...)
	}
	return data
}

// commit checks a committed file's parts against its checksum.
func (item *mediaItem) commit(checksum string) {
	data := item.data()
	item.checksum = checksum
	switch {
	case len(data) != item.size:
		item.state = "FAILED"
		item.errors = []gen.AppMediaStateError{{Code: new("UPLOAD_INCOMPLETE"), Description: new(fmt.Sprintf("received %d of %d bytes", len(data), item.size))}}
	case asc.Checksum(data) != checksum:
		item.state = "FAILED"
		item.errors = []gen.AppMediaStateError{{Code: new("CHECKSUM_MISMATCH"), Description: new("The file doesn't match its checksum.")}}
	default:
		item.state, item.errors = "COMPLETE", nil
	}
}

// editableLocalization reports whether a version localization exists and
// its version is editable, writing an error when it doesn't.
func (s *Server) editableLocalization(w http.ResponseWriter, id string) bool {
	l, v := s.findVersionLocalization(id)
	if l == nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersionLocalizations with id '"+id+"'", "/data/relationships/appStoreVersionLocalization")
		return false
	}
	if !v.editable() {
		stateError(w, "The version cannot be edited in its current state: "+string(*v.Attributes.AppVersionState)+".")
		return false
	}
	return true
}

func (s *Server) createMediaSet(w http.ResponseWriter, kind mediaKind, localizationID, displayType string) *mediaSet {
	if localizationID == "" {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "The fake supports only sets of version localizations.", "/data/relationships")
		return nil
	}
	if !s.editableLocalization(w, localizationID) {
		return nil
	}
	for _, set := range s.media.sets {
		if set.kind == kind && set.localizationID == localizationID && set.displayType == displayType {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The localization already has a set for "+displayType+".", "/data/attributes")
			return nil
		}
	}
	set := &mediaSet{kind: kind, id: s.newID("M"), localizationID: localizationID, displayType: displayType}
	s.media.sets[set.id] = set
	return set
}

func (s *Server) mediaSetsOf(kind mediaKind, localizationID string) []*mediaSet {
	var out []*mediaSet
	for _, set := range sortedValues(s.media.sets) {
		if set.kind == kind && set.localizationID == localizationID {
			out = append(out, set)
		}
	}
	return out
}

func (s *Server) findMediaSet(w http.ResponseWriter, kind mediaKind, id string) *mediaSet {
	set, ok := s.media.sets[id]
	if !ok || set.kind != kind {
		notFound(w, kind.set, id)
		return nil
	}
	return set
}

func (s *Server) deleteMediaSet(w http.ResponseWriter, kind mediaKind, id string) {
	set := s.findMediaSet(w, kind, id)
	if set == nil || !s.editableLocalization(w, set.localizationID) {
		return
	}
	for _, item := range set.items {
		delete(s.media.items, item)
	}
	delete(s.media.sets, id)
	w.WriteHeader(http.StatusNoContent)
}

// reserve adds a file to a set, awaiting upload.
func (s *Server) reserve(w http.ResponseWriter, kind mediaKind, setID, fileName string, size int) *mediaItem {
	set := s.findMediaSet(w, kind, setID)
	if set == nil || !s.editableLocalization(w, set.localizationID) {
		return nil
	}
	if len(set.items) >= kind.max {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", fmt.Sprintf("A set can have at most %d files.", kind.max), "/data/relationships")
		return nil
	}
	if size <= 0 || strings.TrimSpace(fileName) == "" {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "fileName and a positive fileSize are required.", "/data/attributes")
		return nil
	}
	item := &mediaItem{id: s.newID("I"), setID: setID, fileName: fileName, size: size, parts: map[int][]byte{}, state: "AWAITING_UPLOAD"}
	s.media.items[item.id] = item
	set.items = append(set.items, item.id)
	return item
}

func (s *Server) findMediaItem(w http.ResponseWriter, kind mediaKind, id string) *mediaItem {
	item, ok := s.media.items[id]
	if ok {
		set, inSet := s.media.sets[item.setID]
		ok = inSet && set.kind == kind
	}
	if !ok {
		notFound(w, kind.item, id)
		return nil
	}
	return item
}

func (s *Server) deleteMediaItem(w http.ResponseWriter, kind mediaKind, id string) {
	item := s.findMediaItem(w, kind, id)
	if item == nil {
		return
	}
	set := s.media.sets[item.setID]
	if !s.editableLocalization(w, set.localizationID) {
		return
	}
	set.items = slices.DeleteFunc(set.items, func(i string) bool { return i == id })
	delete(s.media.items, id)
	w.WriteHeader(http.StatusNoContent)
}

// reorder replaces the order of a set's files, which must be the same files.
func (s *Server) reorder(w http.ResponseWriter, kind mediaKind, setID string, ids []string) {
	set := s.findMediaSet(w, kind, setID)
	if set == nil || !s.editableLocalization(w, set.localizationID) {
		return
	}
	if len(ids) != len(set.items) || slices.ContainsFunc(ids, func(id string) bool { return !slices.Contains(set.items, id) }) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The order must list each of the set's files once.", "/data")
		return
	}
	set.items = slices.Clone(ids)
	w.WriteHeader(http.StatusNoContent)
}

// ---- screenshots ----

func renderScreenshotSet(set *mediaSet) gen.AppScreenshotSet {
	out := gen.AppScreenshotSet{Id: set.id, Type: gen.AppScreenshotSetTypeAppScreenshotSets}
	alloc(&out.Attributes).ScreenshotDisplayType = new(gen.ScreenshotDisplayType(set.displayType))
	return out
}

func (s *Server) renderScreenshot(r *http.Request, item *mediaItem) gen.AppScreenshot {
	out := gen.AppScreenshot{Id: item.id, Type: gen.AppScreenshotTypeAppScreenshots}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(item.fileName), new(item.size)
	state := alloc(&a.AssetDeliveryState)
	state.State = new(gen.AppMediaAssetStateState(item.state))
	if item.errors != nil {
		state.Errors = new(item.errors)
	}
	if item.state == "AWAITING_UPLOAD" {
		a.UploadOperations = new(s.uploadOperations(r, item))
	} else {
		a.SourceFileChecksum = new(item.checksum)
	}
	return out
}

func (s *Server) AppStoreVersionLocalizationsAppScreenshotSetsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionLocalizationsAppScreenshotSetsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, _ := s.findVersionLocalization(id); l == nil {
		notFound(w, "appStoreVersionLocalizations", id)
		return
	}
	var items []gen.AppScreenshotSet
	for _, set := range s.mediaSetsOf(screenshotKind, id) {
		if matches(params.FilterScreenshotDisplayType, set.displayType) {
			items = append(items, renderScreenshotSet(set))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppScreenshotSetsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AppScreenshotSetsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppScreenshotSetCreateRequest
	if !decode(w, r, &req) {
		return
	}
	var localization string
	if rel := req.Data.Relationships; rel != nil && rel.AppStoreVersionLocalization != nil && rel.AppStoreVersionLocalization.Data != nil {
		localization = rel.AppStoreVersionLocalization.Data.Id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.createMediaSet(w, screenshotKind, localization, string(req.Data.Attributes.ScreenshotDisplayType)); set != nil {
		writeJSON(w, http.StatusCreated, gen.AppScreenshotSetResponse{Data: renderScreenshotSet(set), Links: docLinks(r)})
	}
}

func (s *Server) AppScreenshotSetsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppScreenshotSetsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.findMediaSet(w, screenshotKind, id); set != nil {
		writeJSON(w, http.StatusOK, gen.AppScreenshotSetResponse{Data: renderScreenshotSet(set), Links: docLinks(r)})
	}
}

func (s *Server) AppScreenshotSetsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteMediaSet(w, screenshotKind, id)
}

func (s *Server) AppScreenshotSetsAppScreenshotsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppScreenshotSetsAppScreenshotsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.findMediaSet(w, screenshotKind, id)
	if set == nil {
		return
	}
	var items []gen.AppScreenshot
	for _, item := range set.items {
		items = append(items, s.renderScreenshot(r, s.media.items[item]))
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppScreenshotsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AppScreenshotSetsAppScreenshotsReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppScreenshotSetAppScreenshotsLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	var ids []string
	for _, e := range req.Data {
		ids = append(ids, e.Id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reorder(w, screenshotKind, id, ids)
}

func (s *Server) AppScreenshotsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppScreenshotCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := req.Data.Attributes
	if item := s.reserve(w, screenshotKind, req.Data.Relationships.AppScreenshotSet.Data.Id, a.FileName, a.FileSize); item != nil {
		writeJSON(w, http.StatusCreated, gen.AppScreenshotResponse{Data: s.renderScreenshot(r, item), Links: docLinks(r)})
	}
}

func (s *Server) AppScreenshotsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppScreenshotUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.findMediaItem(w, screenshotKind, id)
	if item == nil {
		return
	}
	if in := asc.Value(req.Data.Attributes); asc.Value(in.Uploaded) && item.state == "AWAITING_UPLOAD" {
		item.commit(asc.Value(in.SourceFileChecksum))
	}
	writeJSON(w, http.StatusOK, gen.AppScreenshotResponse{Data: s.renderScreenshot(r, item), Links: docLinks(r)})
}

func (s *Server) AppScreenshotsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteMediaItem(w, screenshotKind, id)
}

// ---- previews ----

func renderPreviewSet(set *mediaSet) gen.AppPreviewSet {
	out := gen.AppPreviewSet{Id: set.id, Type: gen.AppPreviewSetTypeAppPreviewSets}
	alloc(&out.Attributes).PreviewType = new(gen.PreviewType(set.displayType))
	return out
}

func (s *Server) renderPreview(r *http.Request, item *mediaItem) gen.AppPreview {
	out := gen.AppPreview{Id: item.id, Type: gen.AppPreviewTypeAppPreviews}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize, a.PreviewFrameTimeCode = new(item.fileName), new(item.size), item.frameTime
	state := alloc(&a.VideoDeliveryState)
	videoState := map[string]gen.AppMediaVideoStateState{
		"AWAITING_UPLOAD": gen.AppMediaVideoStateStateAWAITINGUPLOAD, "COMPLETE": gen.AppMediaVideoStateStateCOMPLETE, "FAILED": gen.AppMediaVideoStateStateFAILED,
	}[item.state]
	state.State = new(videoState)
	if item.errors != nil {
		state.Errors = new(item.errors)
	}
	if item.state == "AWAITING_UPLOAD" {
		a.UploadOperations = new(s.uploadOperations(r, item))
	} else {
		a.SourceFileChecksum = new(item.checksum)
	}
	return out
}

func (s *Server) AppStoreVersionLocalizationsAppPreviewSetsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionLocalizationsAppPreviewSetsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, _ := s.findVersionLocalization(id); l == nil {
		notFound(w, "appStoreVersionLocalizations", id)
		return
	}
	var items []gen.AppPreviewSet
	for _, set := range s.mediaSetsOf(previewKind, id) {
		if matches(params.FilterPreviewType, set.displayType) {
			items = append(items, renderPreviewSet(set))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppPreviewSetsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AppPreviewSetsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppPreviewSetCreateRequest
	if !decode(w, r, &req) {
		return
	}
	var localization string
	if rel := req.Data.Relationships; rel != nil && rel.AppStoreVersionLocalization != nil && rel.AppStoreVersionLocalization.Data != nil {
		localization = rel.AppStoreVersionLocalization.Data.Id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.createMediaSet(w, previewKind, localization, string(req.Data.Attributes.PreviewType)); set != nil {
		writeJSON(w, http.StatusCreated, gen.AppPreviewSetResponse{Data: renderPreviewSet(set), Links: docLinks(r)})
	}
}

func (s *Server) AppPreviewSetsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppPreviewSetsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.findMediaSet(w, previewKind, id); set != nil {
		writeJSON(w, http.StatusOK, gen.AppPreviewSetResponse{Data: renderPreviewSet(set), Links: docLinks(r)})
	}
}

func (s *Server) AppPreviewSetsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteMediaSet(w, previewKind, id)
}

func (s *Server) AppPreviewSetsAppPreviewsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppPreviewSetsAppPreviewsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.findMediaSet(w, previewKind, id)
	if set == nil {
		return
	}
	var items []gen.AppPreview
	for _, item := range set.items {
		items = append(items, s.renderPreview(r, s.media.items[item]))
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppPreviewsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AppPreviewSetsAppPreviewsReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppPreviewSetAppPreviewsLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	var ids []string
	for _, e := range req.Data {
		ids = append(ids, e.Id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reorder(w, previewKind, id, ids)
}

func (s *Server) AppPreviewsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppPreviewCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := req.Data.Attributes
	if item := s.reserve(w, previewKind, req.Data.Relationships.AppPreviewSet.Data.Id, a.FileName, a.FileSize); item != nil {
		item.frameTime = a.PreviewFrameTimeCode
		writeJSON(w, http.StatusCreated, gen.AppPreviewResponse{Data: s.renderPreview(r, item), Links: docLinks(r)})
	}
}

func (s *Server) AppPreviewsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppPreviewUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.findMediaItem(w, previewKind, id)
	if item == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	if in.PreviewFrameTimeCode != nil {
		item.frameTime = new(*in.PreviewFrameTimeCode)
	}
	if asc.Value(in.Uploaded) && item.state == "AWAITING_UPLOAD" {
		item.commit(asc.Value(in.SourceFileChecksum))
	}
	writeJSON(w, http.StatusOK, gen.AppPreviewResponse{Data: s.renderPreview(r, item), Links: docLinks(r)})
}

func (s *Server) AppPreviewsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteMediaItem(w, previewKind, id)
}

// MediaFiles returns the checksums of a screenshot or preview set's files,
// in order.
func (s *Server) MediaFiles(setID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range s.media.sets[setID].items {
		out = append(out, s.media.items[id].checksum)
	}
	return out
}
