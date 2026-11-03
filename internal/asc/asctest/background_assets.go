package asctest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Apple-hosted background assets
// (https://developer.apple.com/documentation/appstoreconnectapi/managing-apple-hosted-background-assets).
// An app's asset pack has numbered versions. A version takes an asset pack
// archive, and optionally its manifest, uploaded like screenshots; a manifest
// that isn't JSON fails. When the archive is committed, the version is
// processed and an internal beta release makes it ready for TestFlight,
// superseding the previous version's. Asset packs can be archived but not
// deleted, and versions can't be deleted.

type backgroundAsset struct {
	id, appID, identifier string
	archived              bool
	created               time.Time
}

type backgroundAssetVersion struct {
	id, assetID string
	number      int
	state       string // AWAITING_UPLOAD, PROCESSING, FAILED or COMPLETE
	created     time.Time
	// files maps an asset type, ASSET or MANIFEST, to an upload file ID.
	files map[string]string
	// releases maps a release kind to its ID.
	releases map[releaseKind]string
}

type backgroundAssetFile struct {
	*mediaItem
	versionID, assetType string
}

type releaseKind string

const (
	internalBetaRelease releaseKind = "backgroundAssetVersionInternalBetaReleases"
	externalBetaRelease releaseKind = "backgroundAssetVersionExternalBetaReleases"
	appStoreRelease     releaseKind = "backgroundAssetVersionAppStoreReleases"
)

type backgroundAssetRelease struct {
	id, versionID, state string
	kind                 releaseKind
}

type backgroundAssetsState struct {
	assets   map[string]*backgroundAsset
	versions map[string]*backgroundAssetVersion
	files    map[string]*backgroundAssetFile
	releases map[string]*backgroundAssetRelease
}

func newBackgroundAssetsState() *backgroundAssetsState {
	return &backgroundAssetsState{
		assets:   map[string]*backgroundAsset{},
		versions: map[string]*backgroundAssetVersion{},
		files:    map[string]*backgroundAssetFile{},
		releases: map[string]*backgroundAssetRelease{},
	}
}

func renderBackgroundAsset(a *backgroundAsset) gen.BackgroundAsset {
	out := gen.BackgroundAsset{Id: a.id, Type: gen.BackgroundAssetTypeBackgroundAssets}
	attrs := alloc(&out.Attributes)
	attrs.AssetPackIdentifier, attrs.Archived = new(a.identifier), new(a.archived)
	attrs.CreatedDate = new(gen.DateTime{Time: a.created})
	d := alloc(&alloc(&alloc(&out.Relationships).App).Data)
	d.Id, d.Type = a.appID, gen.BackgroundAssetRelationshipsAppDataTypeApps
	return out
}

func (s *Server) BackgroundAssetsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BackgroundAssetCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	appID, identifier := req.Data.Relationships.App.Data.Id, req.Data.Attributes.AssetPackIdentifier
	if !s.appExists(appID) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	if identifier == "" {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.REQUIRED", "assetPackIdentifier is required.", "/data/attributes/assetPackIdentifier")
		return
	}
	for _, a := range s.bg.assets {
		if a.appID == appID && a.identifier == identifier {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The app already has an asset pack '"+identifier+"'.", "/data/attributes/assetPackIdentifier")
			return
		}
	}
	a := &backgroundAsset{id: s.newID("BA"), appID: appID, identifier: identifier, created: s.now().UTC().Truncate(time.Second)}
	s.bg.assets[a.id] = a
	writeJSON(w, http.StatusCreated, gen.BackgroundAssetResponse{Data: renderBackgroundAsset(a), Links: docLinks(r)})
}

func (s *Server) findBackgroundAsset(w http.ResponseWriter, id string) *backgroundAsset {
	a, ok := s.bg.assets[id]
	if !ok {
		notFound(w, "backgroundAssets", id)
	}
	return a
}

func (s *Server) BackgroundAssetsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BackgroundAssetsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.findBackgroundAsset(w, id); a != nil {
		writeJSON(w, http.StatusOK, gen.BackgroundAssetResponse{Data: renderBackgroundAsset(a), Links: docLinks(r)})
	}
}

func (s *Server) BackgroundAssetsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BackgroundAssetUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findBackgroundAsset(w, id)
	if a == nil {
		return
	}
	if in := asc.Value(req.Data.Attributes); in.Archived != nil {
		a.archived = *in.Archived
	}
	writeJSON(w, http.StatusOK, gen.BackgroundAssetResponse{Data: renderBackgroundAsset(a), Links: docLinks(r)})
}

func (s *Server) AppsBackgroundAssetsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsBackgroundAssetsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.appExists(id) {
		notFound(w, "apps", id)
		return
	}
	var items []gen.BackgroundAsset
	for _, a := range sortedValues(s.bg.assets) {
		if a.appID == id && matches(params.FilterAssetPackIdentifier, a.identifier) && matches(params.FilterArchived, strconv.FormatBool(a.archived)) {
			items = append(items, renderBackgroundAsset(a))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BackgroundAssetsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- versions ----

func renderBackgroundAssetVersion(v *backgroundAssetVersion) gen.BackgroundAssetVersion {
	out := gen.BackgroundAssetVersion{Id: v.id, Type: gen.BackgroundAssetVersionTypeBackgroundAssetVersions}
	a := alloc(&out.Attributes)
	a.Version, a.State = new(strconv.Itoa(v.number)), new(gen.BackgroundAssetVersionState(v.state))
	a.CreatedDate = new(gen.DateTime{Time: v.created})
	rel := alloc(&out.Relationships)
	d := alloc(&alloc(&rel.BackgroundAsset).Data)
	d.Id, d.Type = v.assetID, gen.BackgroundAssetVersionRelationshipsBackgroundAssetDataTypeBackgroundAssets
	if id, ok := v.files["ASSET"]; ok {
		f := alloc(&alloc(&rel.AssetFile).Data)
		f.Id, f.Type = id, gen.BackgroundAssetVersionRelationshipsAssetFileDataTypeBackgroundAssetUploadFiles
	}
	if id, ok := v.files["MANIFEST"]; ok {
		f := alloc(&alloc(&rel.ManifestFile).Data)
		f.Id, f.Type = id, gen.BackgroundAssetVersionRelationshipsManifestFileDataTypeBackgroundAssetUploadFiles
	}
	if id, ok := v.releases[internalBetaRelease]; ok {
		f := alloc(&alloc(&rel.InternalBetaRelease).Data)
		f.Id, f.Type = id, gen.BackgroundAssetVersionRelationshipsInternalBetaReleaseDataTypeBackgroundAssetVersionInternalBetaReleases
	}
	if id, ok := v.releases[externalBetaRelease]; ok {
		f := alloc(&alloc(&rel.ExternalBetaRelease).Data)
		f.Id, f.Type = id, gen.BackgroundAssetVersionRelationshipsExternalBetaReleaseDataTypeBackgroundAssetVersionExternalBetaReleases
	}
	if id, ok := v.releases[appStoreRelease]; ok {
		f := alloc(&alloc(&rel.AppStoreRelease).Data)
		f.Id, f.Type = id, gen.BackgroundAssetVersionRelationshipsAppStoreReleaseDataTypeBackgroundAssetVersionAppStoreReleases
	}
	return out
}

func (s *Server) BackgroundAssetVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BackgroundAssetVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	assetID := req.Data.Relationships.BackgroundAsset.Data.Id
	a, ok := s.bg.assets[assetID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no backgroundAssets with id '"+assetID+"'", "/data/relationships/backgroundAsset")
		return
	}
	if a.archived {
		conflict(w, "STATE_ERROR", "The asset pack is archived.", "/data/relationships/backgroundAsset")
		return
	}
	number := 1
	for _, v := range s.bg.versions {
		if v.assetID == assetID {
			number = max(number, v.number+1)
		}
	}
	v := &backgroundAssetVersion{
		id: s.newID("BV"), assetID: assetID, number: number, state: "AWAITING_UPLOAD",
		created: s.now().UTC().Truncate(time.Second), files: map[string]string{}, releases: map[releaseKind]string{},
	}
	s.bg.versions[v.id] = v
	writeJSON(w, http.StatusCreated, gen.BackgroundAssetVersionResponse{Data: renderBackgroundAssetVersion(v), Links: docLinks(r)})
}

func (s *Server) BackgroundAssetVersionsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BackgroundAssetVersionsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.bg.versions[id]
	if !ok {
		notFound(w, "backgroundAssetVersions", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.BackgroundAssetVersionResponse{Data: renderBackgroundAssetVersion(v), Links: docLinks(r)})
}

func (s *Server) BackgroundAssetsVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.BackgroundAssetsVersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findBackgroundAsset(w, id) == nil {
		return
	}
	var versions []*backgroundAssetVersion
	for _, v := range s.bg.versions {
		if v.assetID == id && matches(params.FilterState, v.state) && matches(params.FilterVersion, strconv.Itoa(v.number)) {
			versions = append(versions, v)
		}
	}
	slices.SortFunc(versions, func(a, b *backgroundAssetVersion) int { return a.number - b.number })
	items := make([]gen.BackgroundAssetVersion, len(versions))
	for i, v := range versions {
		items[i] = renderBackgroundAssetVersion(v)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BackgroundAssetVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- upload files ----

func (s *Server) renderBackgroundAssetFile(r *http.Request, f *backgroundAssetFile) gen.BackgroundAssetUploadFile {
	out := gen.BackgroundAssetUploadFile{Id: f.id, Type: gen.BackgroundAssetUploadFileTypeBackgroundAssetUploadFiles}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(int64(f.size))
	a.AssetType = new(gen.BackgroundAssetUploadFileAttributesAssetType(f.assetType))
	state := alloc(&a.AssetDeliveryState)
	state.State = new(gen.AppMediaAssetStateState(f.state))
	if f.errors != nil {
		state.Errors = new(f.errors)
	}
	if f.state == "AWAITING_UPLOAD" {
		ops := s.uploadOperations(r, f.mediaItem)
		delivery := make([]gen.DeliveryFileUploadOperation, len(ops))
		for i, op := range ops {
			delivery[i] = gen.DeliveryFileUploadOperation{
				Method: op.Method, Url: op.Url, RequestHeaders: op.RequestHeaders,
				Offset: new(int64(*op.Offset)), Length: new(int64(*op.Length)), PartNumber: new(int64(i + 1)),
			}
		}
		a.UploadOperations = &delivery
		return out
	}
	file := alloc(&alloc(&a.SourceFileChecksums).File)
	file.Hash, file.Algorithm = new(f.checksum), new(gen.ChecksumAlgorithmMD5)
	return out
}

func (s *Server) BackgroundAssetUploadFilesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BackgroundAssetUploadFileCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	versionID := req.Data.Relationships.BackgroundAssetVersion.Data.Id
	v, ok := s.bg.versions[versionID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no backgroundAssetVersions with id '"+versionID+"'", "/data/relationships/backgroundAssetVersion")
		return
	}
	in := req.Data.Attributes
	assetType := string(in.AssetType)
	switch {
	case assetType != "ASSET" && assetType != "MANIFEST":
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "assetType must be ASSET or MANIFEST.", "/data/attributes/assetType")
		return
	case in.FileSize < 1:
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "fileSize must be at least 1.", "/data/attributes/fileSize")
		return
	case v.state != "AWAITING_UPLOAD":
		conflict(w, "STATE_ERROR", "The version isn't awaiting an upload.", "/data/relationships/backgroundAssetVersion")
		return
	}
	if old, ok := v.files[assetType]; ok {
		delete(s.bg.files, old)
		delete(s.media.items, old)
	}
	item := &mediaItem{id: s.newID("BF"), fileName: in.FileName, size: int(in.FileSize), parts: map[int][]byte{}, state: "AWAITING_UPLOAD"}
	s.media.items[item.id] = item
	f := &backgroundAssetFile{mediaItem: item, versionID: versionID, assetType: assetType}
	s.bg.files[item.id] = f
	v.files[assetType] = item.id
	writeJSON(w, http.StatusCreated, gen.BackgroundAssetUploadFileResponse{Data: s.renderBackgroundAssetFile(r, f), Links: docLinks(r)})
}

func (s *Server) findBackgroundAssetFile(w http.ResponseWriter, id string) *backgroundAssetFile {
	f, ok := s.bg.files[id]
	if !ok {
		notFound(w, "backgroundAssetUploadFiles", id)
	}
	return f
}

func (s *Server) BackgroundAssetUploadFilesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BackgroundAssetUploadFilesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findBackgroundAssetFile(w, id); f != nil {
		writeJSON(w, http.StatusOK, gen.BackgroundAssetUploadFileResponse{Data: s.renderBackgroundAssetFile(r, f), Links: docLinks(r)})
	}
}

// BackgroundAssetUploadFilesUpdateInstance commits an upload. Committing the
// archive processes the version, which the fake finishes at once: the
// version becomes ready for internal testing, superseding the previous one.
func (s *Server) BackgroundAssetUploadFilesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BackgroundAssetUploadFileUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findBackgroundAssetFile(w, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	if asc.Value(in.Uploaded) && f.state == "AWAITING_UPLOAD" {
		f.commit(asc.Value(asc.Value(asc.Value(in.SourceFileChecksums).File).Hash))
		if f.assetType == "MANIFEST" && f.state == "COMPLETE" && !json.Valid(f.data()) {
			f.state = "FAILED"
			f.errors = []gen.AppMediaStateError{{Code: new("MANIFEST_INVALID"), Description: new("The manifest isn't valid JSON.")}}
		}
		if f.assetType == "ASSET" {
			s.processBackgroundAssetVersion(s.bg.versions[f.versionID], f)
		}
	}
	writeJSON(w, http.StatusOK, gen.BackgroundAssetUploadFileResponse{Data: s.renderBackgroundAssetFile(r, f), Links: docLinks(r)})
}

func (s *Server) processBackgroundAssetVersion(v *backgroundAssetVersion, archive *backgroundAssetFile) {
	if archive.state != "COMPLETE" {
		v.state = "FAILED"
		return
	}
	if manifest, ok := v.files["MANIFEST"]; ok && s.bg.files[manifest].state == "FAILED" {
		v.state = "FAILED"
		return
	}
	v.state = "COMPLETE"
	for _, other := range s.bg.versions {
		if id, ok := other.releases[internalBetaRelease]; ok && other.assetID == v.assetID {
			s.bg.releases[id].state = "SUPERSEDED"
		}
	}
	s.addRelease(v, internalBetaRelease, "READY_FOR_TESTING")
}

func (s *Server) addRelease(v *backgroundAssetVersion, kind releaseKind, state string) {
	if id, ok := v.releases[kind]; ok {
		s.bg.releases[id].state = state
		return
	}
	rel := &backgroundAssetRelease{id: s.newID("BR"), versionID: v.id, kind: kind, state: state}
	s.bg.releases[rel.id] = rel
	v.releases[kind] = rel.id
}

// ReleaseBackgroundAssetVersion sets the state of a version's external beta
// or App Store release, as a review would.
func (s *Server) ReleaseBackgroundAssetVersion(versionID string, external bool, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kind := appStoreRelease
	if external {
		kind = externalBetaRelease
	}
	s.addRelease(s.bg.versions[versionID], kind, state)
}

// BackgroundAssetFileChecksum returns the committed checksum of a version's
// file of the given asset type, ASSET or MANIFEST, or "" when it has none.
func (s *Server) BackgroundAssetFileChecksum(versionID, assetType string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.bg.versions[versionID]
	if !ok {
		return ""
	}
	if f, ok := s.bg.files[v.files[assetType]]; ok {
		return f.checksum
	}
	return ""
}

// ---- releases ----

func (s *Server) findRelease(w http.ResponseWriter, kind releaseKind, id string) *backgroundAssetRelease {
	rel, ok := s.bg.releases[id]
	if !ok || rel.kind != kind {
		notFound(w, string(kind), id)
		return nil
	}
	return rel
}

func (s *Server) BackgroundAssetVersionInternalBetaReleasesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BackgroundAssetVersionInternalBetaReleasesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := s.findRelease(w, internalBetaRelease, id)
	if rel == nil {
		return
	}
	out := gen.BackgroundAssetVersionInternalBetaRelease{Id: rel.id, Type: gen.BackgroundAssetVersionInternalBetaReleaseTypeBackgroundAssetVersionInternalBetaReleases}
	alloc(&out.Attributes).State = new(gen.BackgroundAssetVersionInternalBetaReleaseAttributesState(rel.state))
	writeJSON(w, http.StatusOK, gen.BackgroundAssetVersionInternalBetaReleaseResponse{Data: out, Links: docLinks(r)})
}

func (s *Server) BackgroundAssetVersionExternalBetaReleasesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BackgroundAssetVersionExternalBetaReleasesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := s.findRelease(w, externalBetaRelease, id)
	if rel == nil {
		return
	}
	out := gen.BackgroundAssetVersionExternalBetaRelease{Id: rel.id, Type: gen.BackgroundAssetVersionExternalBetaReleaseTypeBackgroundAssetVersionExternalBetaReleases}
	alloc(&out.Attributes).State = new(gen.BackgroundAssetVersionExternalBetaReleaseState(rel.state))
	writeJSON(w, http.StatusOK, gen.BackgroundAssetVersionExternalBetaReleaseResponse{Data: out, Links: docLinks(r)})
}

func (s *Server) BackgroundAssetVersionAppStoreReleasesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.BackgroundAssetVersionAppStoreReleasesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := s.findRelease(w, appStoreRelease, id)
	if rel == nil {
		return
	}
	out := gen.BackgroundAssetVersionAppStoreRelease{Id: rel.id, Type: gen.BackgroundAssetVersionAppStoreReleaseTypeBackgroundAssetVersionAppStoreReleases}
	alloc(&out.Attributes).State = new(gen.BackgroundAssetVersionAppStoreReleaseState(rel.state))
	writeJSON(w, http.StatusOK, gen.BackgroundAssetVersionAppStoreReleaseResponse{Data: out, Links: docLinks(r)})
}
