package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Single uploaded files: App Review attachments of a version's review
// details, the document of an encryption declaration, and a version's
// routing app coverage file. They are uploaded like screenshots, and their
// parts arrive at the same upload endpoint.

type fileKind string

const (
	reviewAttachment    fileKind = "appStoreReviewAttachments"
	declarationDocument fileKind = "appEncryptionDeclarationDocuments"
	routingCoverage     fileKind = "routingAppCoverages"
)

type uploadedFile struct {
	*mediaItem
	kind     fileKind
	parentID string
}

func (s *Server) newFile(kind fileKind, parentID, name string, size int) *uploadedFile {
	item := &mediaItem{id: s.newID("J"), fileName: name, size: size, parts: map[int][]byte{}, state: "AWAITING_UPLOAD"}
	s.media.items[item.id] = item
	f := &uploadedFile{mediaItem: item, kind: kind, parentID: parentID}
	s.files[item.id] = f
	return f
}

func (s *Server) findFile(w http.ResponseWriter, kind fileKind, id string) *uploadedFile {
	f, ok := s.files[id]
	if !ok || f.kind != kind {
		notFound(w, string(kind), id)
		return nil
	}
	return f
}

func (s *Server) removeFile(f *uploadedFile) {
	delete(s.files, f.id)
	delete(s.media.items, f.id)
}

// fileAttributes renders the attributes the three kinds share.
func (s *Server) fileAttributes(r *http.Request, f *uploadedFile) (state *gen.AppMediaAssetState, ops *[]gen.UploadOperation, checksum *string) {
	state = &gen.AppMediaAssetState{State: new(gen.AppMediaAssetStateState(f.state))}
	if f.errors != nil {
		state.Errors = new(f.errors)
	}
	if f.state == "AWAITING_UPLOAD" {
		return state, new(s.uploadOperations(r, f.mediaItem)), nil
	}
	return state, nil, new(f.checksum)
}

// commitFile commits a file when the request says it is uploaded.
func commitFile(f *uploadedFile, uploaded *bool, checksum *string) {
	if asc.Value(uploaded) && f.state == "AWAITING_UPLOAD" {
		f.commit(asc.Value(checksum))
	}
}

// ---- App Review attachments ----

func (s *Server) reviewDetailVersion(id string) *version {
	for _, v := range s.versions.versions {
		if v.reviewDetail != nil && v.reviewDetail.Id == id {
			return v
		}
	}
	return nil
}

func (s *Server) renderAttachment(r *http.Request, f *uploadedFile) gen.AppStoreReviewAttachment {
	out := gen.AppStoreReviewAttachment{Id: f.id, Type: gen.AppStoreReviewAttachmentTypeAppStoreReviewAttachments}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, a.SourceFileChecksum = s.fileAttributes(r, f)
	d := alloc(&alloc(&alloc(&out.Relationships).AppStoreReviewDetail).Data)
	d.Id, d.Type = f.parentID, "appStoreReviewDetails"
	return out
}

func (s *Server) AppStoreReviewAttachmentsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreReviewAttachmentCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	detail := req.Data.Relationships.AppStoreReviewDetail.Data.Id
	v := s.reviewDetailVersion(detail)
	if v == nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreReviewDetails with id '"+detail+"'", "/data/relationships/appStoreReviewDetail")
		return
	}
	if s.editableVersion(w, v.Id) == nil {
		return
	}
	f := s.newFile(reviewAttachment, detail, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.AppStoreReviewAttachmentResponse{Data: s.renderAttachment(r, f), Links: docLinks(r)})
}

func (s *Server) AppStoreReviewAttachmentsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreReviewAttachmentsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, reviewAttachment, id); f != nil {
		writeJSON(w, http.StatusOK, gen.AppStoreReviewAttachmentResponse{Data: s.renderAttachment(r, f), Links: docLinks(r)})
	}
}

func (s *Server) AppStoreReviewAttachmentsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreReviewAttachmentUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, reviewAttachment, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	commitFile(f, in.Uploaded, in.SourceFileChecksum)
	writeJSON(w, http.StatusOK, gen.AppStoreReviewAttachmentResponse{Data: s.renderAttachment(r, f), Links: docLinks(r)})
}

func (s *Server) AppStoreReviewAttachmentsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, reviewAttachment, id)
	if f == nil || s.editableVersion(w, s.reviewDetailVersion(f.parentID).Id) == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

// ---- encryption declaration documents ----

func (s *Server) renderDocument(r *http.Request, f *uploadedFile) gen.AppEncryptionDeclarationDocument {
	out := gen.AppEncryptionDeclarationDocument{Id: f.id, Type: gen.AppEncryptionDeclarationDocumentTypeAppEncryptionDeclarationDocuments}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, a.SourceFileChecksum = s.fileAttributes(r, f)
	return out
}

// AppEncryptionDeclarationDocumentsCreateInstance uploads a declaration's
// document, which replaces any earlier one: a declaration has one.
func (s *Server) AppEncryptionDeclarationDocumentsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppEncryptionDeclarationDocumentCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	declaration := req.Data.Relationships.AppEncryptionDeclaration.Data.Id
	if _, ok := s.versions.declarations[declaration]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appEncryptionDeclarations with id '"+declaration+"'", "/data/relationships/appEncryptionDeclaration")
		return
	}
	for _, f := range s.files {
		if f.kind == declarationDocument && f.parentID == declaration {
			s.removeFile(f)
		}
	}
	f := s.newFile(declarationDocument, declaration, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.AppEncryptionDeclarationDocumentResponse{Data: s.renderDocument(r, f), Links: docLinks(r)})
}

func (s *Server) AppEncryptionDeclarationDocumentsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppEncryptionDeclarationDocumentsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, declarationDocument, id); f != nil {
		writeJSON(w, http.StatusOK, gen.AppEncryptionDeclarationDocumentResponse{Data: s.renderDocument(r, f), Links: docLinks(r)})
	}
}

func (s *Server) AppEncryptionDeclarationDocumentsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppEncryptionDeclarationDocumentUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, declarationDocument, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	commitFile(f, in.Uploaded, in.SourceFileChecksum)
	writeJSON(w, http.StatusOK, gen.AppEncryptionDeclarationDocumentResponse{Data: s.renderDocument(r, f), Links: docLinks(r)})
}

// ---- routing app coverage ----

func (s *Server) renderCoverage(r *http.Request, f *uploadedFile) gen.RoutingAppCoverage {
	out := gen.RoutingAppCoverage{Id: f.id, Type: gen.RoutingAppCoverageTypeRoutingAppCoverages}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, a.SourceFileChecksum = s.fileAttributes(r, f)
	d := alloc(&alloc(&alloc(&out.Relationships).AppStoreVersion).Data)
	d.Id, d.Type = f.parentID, "appStoreVersions"
	return out
}

// RoutingAppCoveragesCreateInstance uploads a version's routing app
// coverage file; a version has at most one.
func (s *Server) RoutingAppCoveragesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.RoutingAppCoverageCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	versionID := req.Data.Relationships.AppStoreVersion.Data.Id
	if s.editableVersion(w, versionID) == nil {
		return
	}
	for _, f := range s.files {
		if f.kind == routingCoverage && f.parentID == versionID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has a routing app coverage file.", "/data/relationships/appStoreVersion")
			return
		}
	}
	f := s.newFile(routingCoverage, versionID, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.RoutingAppCoverageResponse{Data: s.renderCoverage(r, f), Links: docLinks(r)})
}

func (s *Server) RoutingAppCoveragesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.RoutingAppCoveragesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, routingCoverage, id); f != nil {
		writeJSON(w, http.StatusOK, gen.RoutingAppCoverageResponse{Data: s.renderCoverage(r, f), Links: docLinks(r)})
	}
}

func (s *Server) AppStoreVersionsRoutingAppCoverageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionsRoutingAppCoverageGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.versions.versions[id]; !ok {
		notFound(w, "appStoreVersions", id)
		return
	}
	for _, f := range s.files {
		if f.kind == routingCoverage && f.parentID == id {
			writeJSON(w, http.StatusOK, gen.RoutingAppCoverageResponse{Data: s.renderCoverage(r, f), Links: docLinks(r)})
			return
		}
	}
	notFound(w, "routingAppCoverages", id)
}

func (s *Server) RoutingAppCoveragesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.RoutingAppCoverageUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, routingCoverage, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	commitFile(f, in.Uploaded, in.SourceFileChecksum)
	writeJSON(w, http.StatusOK, gen.RoutingAppCoverageResponse{Data: s.renderCoverage(r, f), Links: docLinks(r)})
}

func (s *Server) RoutingAppCoveragesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, routingCoverage, id)
	if f == nil || s.editableVersion(w, f.parentID) == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

// FileChecksum returns the checksum of an uploaded file, or "" before it is
// committed or after it is deleted.
func (s *Server) FileChecksum(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.files[id]; ok {
		return f.checksum
	}
	return ""
}
