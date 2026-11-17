package asctest

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"sort"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Versioned Game Center items: achievements, and the leaderboards, sets,
// activities and challenges that follow the same model. An item belongs to
// an app's Game Center detail or to a group and has versions; a version has
// a localization per locale, each with an image, is edited while it is in
// Prepare for Submission, and goes through App Review as an item of a review
// submission
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center).
// The fake creates an item with its first version, starts a version with
// copies of the latest one's localizations and images, allows changes only
// to the version being prepared, and refuses to delete an item that has
// been live.

const (
	gcDraft    = "PREPARE_FOR_SUBMISSION"
	gcReady    = "READY_FOR_REVIEW"
	gcWaiting  = "WAITING_FOR_REVIEW"
	gcLive     = "LIVE"
	gcReplaced = "REPLACED_WITH_NEW_VERSION"
	gcRejected = "DEVELOPER_REJECTED"
)

// gcKind names the JSON:API types of one kind of versioned item.
type gcKind struct {
	items, versions, localizations, images string
	// parent is the version's relationship to its item, e.g. "achievement".
	parent string
}

var gcAchievements = gcKind{
	items: "gameCenterAchievements", versions: "gameCenterAchievementVersions", localizations: "gameCenterAchievementLocalizations",
	images: "gameCenterAchievementImages", parent: "achievement",
}

var gcLeaderboards = gcKind{
	items: "gameCenterLeaderboards", versions: "gameCenterLeaderboardVersions", localizations: "gameCenterLeaderboardLocalizations",
	images: "gameCenterLeaderboardImages", parent: "leaderboard",
}

type gcItem struct {
	id, kind          string
	detailID, groupID string
	attributes        map[string]any
}

type gcVersion struct {
	id, kind, itemID string
	number           int
	state            string
}

type gcLocalization struct {
	id, kind, versionID string
	attributes          map[string]any
}

type gcItemsState struct {
	items         map[string]*gcItem
	versions      map[string]*gcVersion
	localizations map[string]*gcLocalization
}

func newGCItemsState() *gcItemsState {
	return &gcItemsState{items: map[string]*gcItem{}, versions: map[string]*gcVersion{}, localizations: map[string]*gcLocalization{}}
}

// gcRequest is a JSON:API request body read without its generated type.
type gcRequest struct {
	Data struct {
		Attributes    map[string]any `json:"attributes"`
		Relationships map[string]struct {
			Data json.RawMessage `json:"data"`
		} `json:"relationships"`
	} `json:"data"`
}

// related returns the ID a to-one relationship of the request names, or "".
func (q *gcRequest) related(name string) string {
	var data struct {
		ID string `json:"id"`
	}
	if rel, ok := q.Data.Relationships[name]; ok && json.Unmarshal(rel.Data, &data) == nil {
		return data.ID
	}
	return ""
}

func writeDoc(w http.ResponseWriter, r *http.Request, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data, "links": docLinks(r)})
}

// ---- items ----

func (s *Server) gcItemVersions(itemID string) []*gcVersion {
	var out []*gcVersion
	for _, v := range s.gcItems.versions {
		if v.itemID == itemID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].number < out[j].number })
	return out
}

func (s *Server) renderGCItem(i *gcItem) map[string]any {
	var versions []string
	for _, v := range s.gcItemVersions(i.id) {
		versions = append(versions, v.id)
	}
	relationships := map[string]any{"versions": linkageData(i.kind+"Versions", versions...)}
	if i.detailID != "" {
		relationships["gameCenterDetail"] = oneLinkage("gameCenterDetails", i.detailID)
	} else {
		relationships["gameCenterGroup"] = oneLinkage("gameCenterGroups", i.groupID)
	}
	return map[string]any{"type": i.kind, "id": i.id, "attributes": i.attributes, "relationships": relationships}
}

func (s *Server) findGCItem(w http.ResponseWriter, kind gcKind, id string) *gcItem {
	i, ok := s.gcItems.items[id]
	if !ok || i.kind != kind.items {
		notFound(w, kind.items, id)
		return nil
	}
	return i
}

// gcCreateItem creates an item of a detail or a group with its first
// version. Its vendor identifier is unique among the kind's items.
func (s *Server) gcCreateItem(w http.ResponseWriter, r *http.Request, kind gcKind) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := &gcItem{kind: kind.items, detailID: req.related("gameCenterDetail"), groupID: req.related("gameCenterGroup"), attributes: req.Data.Attributes}
	_, hasDetail := s.gameCenter.details[i.detailID]
	_, hasGroup := s.gameCenter.groups[i.groupID]
	if (i.detailID == "") == (i.groupID == "") || i.detailID != "" && !hasDetail || i.groupID != "" && !hasGroup {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The item needs an existing Game Center detail or group, and not both.", "/data/relationships")
		return
	}
	for _, other := range s.gcItems.items {
		if other.kind == i.kind && other.attributes["vendorIdentifier"] == i.attributes["vendorIdentifier"] {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The vendor identifier is already used.", "/data/attributes/vendorIdentifier")
			return
		}
	}
	i.id = s.newID("GI")
	i.attributes["archived"] = false
	s.gcItems.items[i.id] = i
	v := &gcVersion{id: s.newID("GN"), kind: kind.versions, itemID: i.id, number: 1, state: gcDraft}
	s.gcItems.versions[v.id] = v
	writeDoc(w, r, http.StatusCreated, s.renderGCItem(i))
}

func (s *Server) gcGetItem(w http.ResponseWriter, r *http.Request, kind gcKind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.findGCItem(w, kind, id); i != nil {
		writeDoc(w, r, http.StatusOK, s.renderGCItem(i))
	}
}

// gcUpdateItem merges the request's attributes; a null removes one.
func (s *Server) gcUpdateItem(w http.ResponseWriter, r *http.Request, kind gcKind, id string) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findGCItem(w, kind, id)
	if i == nil {
		return
	}
	for name, value := range req.Data.Attributes {
		if value == nil {
			delete(i.attributes, name)
		} else {
			i.attributes[name] = value
		}
	}
	writeDoc(w, r, http.StatusOK, s.renderGCItem(i))
}

func (s *Server) gcDeleteItem(w http.ResponseWriter, kind gcKind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findGCItem(w, kind, id) == nil {
		return
	}
	versions := s.gcItemVersions(id)
	if slices.ContainsFunc(versions, func(v *gcVersion) bool { return v.state == gcLive || v.state == gcReplaced }) {
		stateError(w, "An item that has been live can't be deleted; archive it.")
		return
	}
	for _, v := range versions {
		for _, l := range s.gcVersionLocalizations(v.id) {
			s.removeGCLocalization(kind, l.id)
		}
		delete(s.gcItems.versions, v.id)
	}
	delete(s.gcItems.items, id)
	w.WriteHeader(http.StatusNoContent)
}

// gcListItems writes the kind's items of a detail or a group.
func (s *Server) gcListItems(w http.ResponseWriter, r *http.Request, kind gcKind, detailID, groupID string, limit *int, match func(*gcItem) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, hasDetail := s.gameCenter.details[detailID]
	_, hasGroup := s.gameCenter.groups[groupID]
	if !hasDetail && !hasGroup {
		notFound(w, "gameCenterDetails or gameCenterGroups", detailID+groupID)
		return
	}
	var items []map[string]any
	for _, i := range sortedValues(s.gcItems.items) {
		if i.kind == kind.items && i.detailID == detailID && i.groupID == groupID && match(i) {
			items = append(items, s.renderGCItem(i))
		}
	}
	s.writeGCPage(w, r, items, limit)
}

func (s *Server) writeGCPage(w http.ResponseWriter, r *http.Request, items []map[string]any, limit *int) {
	data, links, meta, ok := page(w, r, items, limit, maxLimit)
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(data), "links": links, "meta": meta})
	}
}

// gcItemMatches reports whether an item passes the list filters.
func gcItemMatches[A ~string](i *gcItem, names *[]string, archived *[]A, ids *[]string) bool {
	name, _ := i.attributes["referenceName"].(string)
	isArchived, _ := i.attributes["archived"].(bool)
	return matches(names, name) && matches(archived, boolString(&isArchived)) && matches(ids, i.id)
}

// ---- versions ----

func (s *Server) renderGCVersion(kind gcKind, v *gcVersion) map[string]any {
	var localizations []string
	for _, l := range s.gcVersionLocalizations(v.id) {
		localizations = append(localizations, l.id)
	}
	return map[string]any{
		"type": kind.versions, "id": v.id, "attributes": map[string]any{"version": v.number, "state": v.state},
		"relationships": map[string]any{kind.parent: oneLinkage(kind.items, v.itemID), "localizations": linkageData(kind.localizations, localizations...)},
	}
}

func (s *Server) gcListVersions(w http.ResponseWriter, r *http.Request, kind gcKind, itemID string, limit *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findGCItem(w, kind, itemID) == nil {
		return
	}
	var items []map[string]any
	for _, v := range s.gcItemVersions(itemID) {
		items = append(items, s.renderGCVersion(kind, v))
	}
	s.writeGCPage(w, r, items, limit)
}

// gcCreateVersion starts an item's next version, unless one is being
// prepared or is in review, with copies of the latest version's
// localizations and images.
func (s *Server) gcCreateVersion(w http.ResponseWriter, r *http.Request, kind gcKind) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	itemID := req.related(kind.parent)
	if i, ok := s.gcItems.items[itemID]; !ok || i.kind != kind.items {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+kind.items+" with id '"+itemID+"'", "/data/relationships/"+kind.parent)
		return
	}
	versions := s.gcItemVersions(itemID)
	latest := versions[len(versions)-1]
	if latest.state != gcLive && latest.state != gcReplaced {
		stateError(w, "The item has a version that is being prepared or reviewed.")
		return
	}
	v := &gcVersion{id: s.newID("GN"), kind: kind.versions, itemID: itemID, number: latest.number + 1, state: gcDraft}
	s.gcItems.versions[v.id] = v
	for _, l := range s.gcVersionLocalizations(latest.id) {
		c := &gcLocalization{id: s.newID("GL"), kind: l.kind, versionID: v.id, attributes: maps.Clone(l.attributes)}
		s.gcItems.localizations[c.id] = c
		if image := s.gcImage(kind, l.id); image != nil {
			copied := s.newFile(fileKind(kind.images), c.id, image.fileName, image.size)
			copied.state = image.state
		}
	}
	writeDoc(w, r, http.StatusCreated, s.renderGCVersion(kind, v))
}

// ---- localizations ----

func (s *Server) gcVersionLocalizations(versionID string) []*gcLocalization {
	var out []*gcLocalization
	for _, l := range sortedValues(s.gcItems.localizations) {
		if l.versionID == versionID {
			out = append(out, l)
		}
	}
	return out
}

func renderGCLocalization(kind gcKind, l *gcLocalization) map[string]any {
	return map[string]any{
		"type": kind.localizations, "id": l.id, "attributes": l.attributes,
		"relationships": map[string]any{"version": oneLinkage(kind.versions, l.versionID)},
	}
}

// removeGCLocalization deletes a localization with its image.
func (s *Server) removeGCLocalization(kind gcKind, id string) {
	if image := s.gcImage(kind, id); image != nil {
		s.removeFile(image)
	}
	delete(s.gcItems.localizations, id)
}

// draftGCVersion returns the version, or writes an error and returns nil
// unless it is being prepared.
func (s *Server) draftGCVersion(w http.ResponseWriter, kind gcKind, id, pointer string) *gcVersion {
	v, ok := s.gcItems.versions[id]
	if !ok || v.kind != kind.versions {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+kind.versions+" with id '"+id+"'", pointer)
		return nil
	}
	if v.state != gcDraft {
		stateError(w, "The version is "+v.state+" and can't change.")
		return nil
	}
	return v
}

// draftGCLocalization returns the localization, or writes an error and
// returns nil unless its version is being prepared.
func (s *Server) draftGCLocalization(w http.ResponseWriter, kind gcKind, id string) *gcLocalization {
	l, ok := s.gcItems.localizations[id]
	if !ok || l.kind != kind.localizations {
		notFound(w, kind.localizations, id)
		return nil
	}
	if s.draftGCVersion(w, kind, l.versionID, "") == nil {
		return nil
	}
	return l
}

func (s *Server) gcListLocalizations(w http.ResponseWriter, r *http.Request, kind gcKind, versionID string, limit *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.gcItems.versions[versionID]; !ok || v.kind != kind.versions {
		notFound(w, kind.versions, versionID)
		return
	}
	var items []map[string]any
	for _, l := range s.gcVersionLocalizations(versionID) {
		items = append(items, renderGCLocalization(kind, l))
	}
	s.writeGCPage(w, r, items, limit)
}

func (s *Server) gcCreateLocalization(w http.ResponseWriter, r *http.Request, kind gcKind) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.draftGCVersion(w, kind, req.related("version"), "/data/relationships/version")
	if v == nil {
		return
	}
	for _, other := range s.gcVersionLocalizations(v.id) {
		if other.attributes["locale"] == req.Data.Attributes["locale"] {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The version already has a localization for the locale.", "/data/attributes/locale")
			return
		}
	}
	l := &gcLocalization{id: s.newID("GL"), kind: kind.localizations, versionID: v.id, attributes: req.Data.Attributes}
	s.gcItems.localizations[l.id] = l
	writeDoc(w, r, http.StatusCreated, renderGCLocalization(kind, l))
}

func (s *Server) gcUpdateLocalization(w http.ResponseWriter, r *http.Request, kind gcKind, id string) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.draftGCLocalization(w, kind, id)
	if l == nil {
		return
	}
	for name, value := range req.Data.Attributes {
		if value == nil {
			delete(l.attributes, name)
		} else {
			l.attributes[name] = value
		}
	}
	writeDoc(w, r, http.StatusOK, renderGCLocalization(kind, l))
}

func (s *Server) gcDeleteLocalization(w http.ResponseWriter, kind gcKind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draftGCLocalization(w, kind, id) == nil {
		return
	}
	s.removeGCLocalization(kind, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- images ----

// gcImage returns a localization's image, or nil.
func (s *Server) gcImage(kind gcKind, localizationID string) *uploadedFile {
	for _, f := range s.files {
		if f.kind == fileKind(kind.images) && f.parentID == localizationID {
			return f
		}
	}
	return nil
}

func (s *Server) renderGCImage(r *http.Request, kind gcKind, f *uploadedFile) map[string]any {
	state, operations, _ := s.fileAttributes(r, f)
	return map[string]any{
		"type": kind.images, "id": f.id,
		"attributes":    map[string]any{"fileName": f.fileName, "fileSize": f.size, "assetDeliveryState": state, "uploadOperations": operations},
		"relationships": map[string]any{"localization": oneLinkage(kind.localizations, f.parentID)},
	}
}

// gcCreateImage reserves the image of a localization, which has one.
func (s *Server) gcCreateImage(w http.ResponseWriter, r *http.Request, kind gcKind) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	localizationID := req.related("localization")
	if l, ok := s.gcItems.localizations[localizationID]; !ok || l.kind != kind.localizations {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+kind.localizations+" with id '"+localizationID+"'", "/data/relationships/localization")
		return
	}
	if s.draftGCLocalization(w, kind, localizationID) == nil {
		return
	}
	if s.gcImage(kind, localizationID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The localization already has an image.", "/data/relationships/localization")
		return
	}
	name, _ := req.Data.Attributes["fileName"].(string)
	size, _ := req.Data.Attributes["fileSize"].(float64)
	writeDoc(w, r, http.StatusCreated, s.renderGCImage(r, kind, s.newFile(fileKind(kind.images), localizationID, name, int(size))))
}

func (s *Server) gcUpdateImage(w http.ResponseWriter, r *http.Request, kind gcKind, id string) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, fileKind(kind.images), id)
	if f == nil {
		return
	}
	// The request carries no checksum, so only the size is checked.
	if uploaded, _ := req.Data.Attributes["uploaded"].(bool); uploaded && f.state == "AWAITING_UPLOAD" {
		f.commit(asc.Checksum(f.data()))
	}
	writeDoc(w, r, http.StatusOK, s.renderGCImage(r, kind, f))
}

func (s *Server) gcDeleteImage(w http.ResponseWriter, kind gcKind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, fileKind(kind.images), id)
	if f == nil || s.draftGCLocalization(w, kind, f.parentID) == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) gcLocalizationImage(w http.ResponseWriter, r *http.Request, kind gcKind, localizationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.gcImage(kind, localizationID)
	if f == nil {
		notFound(w, kind.localizations, localizationID)
		return
	}
	writeDoc(w, r, http.StatusOK, s.renderGCImage(r, kind, f))
}

// ---- review ----

// addGCVersionItem adds a version being prepared to a review submission of
// an app the item belongs to. The version needs a localization.
func (s *Server) addGCVersionItem(w http.ResponseWriter, sub *reviewSubmission, kind gcKind, versionID string) bool {
	const pointer = "/data/relationships"
	v := s.draftGCVersion(w, kind, versionID, pointer)
	if v == nil {
		return false
	}
	item, detail := s.gcItems.items[v.itemID], s.appGameCenterDetail(sub.appID)
	if detail == nil || item.detailID != detail.id && (item.groupID == "" || item.groupID != detail.groupID) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The item belongs to another app.", pointer)
		return false
	}
	if len(s.gcVersionLocalizations(v.id)) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version has no localizations.", pointer)
		return false
	}
	v.state = gcReady
	sub.gcVersions = append(sub.gcVersions, v.id)
	return true
}

func (s *Server) setGCVersionStates(sub *reviewSubmission, state string) {
	for _, id := range sub.gcVersions {
		if v, ok := s.gcItems.versions[id]; ok {
			v.state = state
		}
	}
}

// ApproveGameCenterItem makes the item's version in review live, as App
// Review does, replaces the version that was, and completes the review
// submission.
func (s *Server) ApproveGameCenterItem(itemID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.gcItemVersions(itemID) {
		switch v.state {
		case gcLive:
			v.state = gcReplaced
		case gcReady, gcWaiting:
			v.state = gcLive
			for _, sub := range s.versions.submissions {
				if slices.Contains(sub.gcVersions, v.id) {
					sub.Attributes.State = new(gen.ReviewSubmissionAttributesStateCOMPLETE)
				}
			}
		}
	}
}

// GameCenterItemVersions returns the states of an item's versions, oldest
// first.
func (s *Server) GameCenterItemVersions(itemID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, v := range s.gcItemVersions(itemID) {
		out = append(out, v.state)
	}
	return out
}
