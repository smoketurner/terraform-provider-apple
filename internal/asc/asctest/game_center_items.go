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
// to the version being prepared, refuses to delete an item that has been
// live, and removes a deleted leaderboard or set from its memberships.

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

var gcSets = gcKind{
	items: "gameCenterLeaderboardSets", versions: "gameCenterLeaderboardSetVersions", localizations: "gameCenterLeaderboardSetLocalizations",
	images: "gameCenterLeaderboardSetImages", parent: "leaderboardSet",
}

var (
	gcActivities = gcKind{
		items: "gameCenterActivities", versions: "gameCenterActivityVersions", localizations: "gameCenterActivityLocalizations",
		images: "gameCenterActivityImages", parent: "activity",
	}
	gcChallenges = gcKind{
		items: "gameCenterChallenges", versions: "gameCenterChallengeVersions", localizations: "gameCenterChallengeLocalizations",
		images: "gameCenterChallengeImages", parent: "challenge",
	}
	gcKinds = map[string]gcKind{
		gcAchievements.items: gcAchievements, gcLeaderboards.items: gcLeaderboards, gcSets.items: gcSets,
		gcActivities.items: gcActivities, gcChallenges.items: gcChallenges,
	}
	// gcLinkTypes maps a relationship from an item to other items to their
	// type: an activity's achievements and leaderboards, and a challenge's
	// leaderboard.
	gcLinkTypes = map[string]gcKind{"achievementsV2": gcAchievements, "leaderboardsV2": gcLeaderboards, "leaderboardV2": gcLeaderboards}
)

type gcItem struct {
	id, kind          string
	detailID, groupID string
	attributes        map[string]any
	// links holds the IDs of the items each relationship names.
	links map[string][]string
}

type gcVersion struct {
	id, kind, itemID string
	number           int
	state            string
	// attributes holds what a version has besides its number and state,
	// such as an activity's fallback URL.
	attributes map[string]any
}

type gcLocalization struct {
	id, kind, versionID string
	attributes          map[string]any
}

type gcItemsState struct {
	items         map[string]*gcItem
	versions      map[string]*gcVersion
	localizations map[string]*gcLocalization
	// setMembers maps a leaderboard set's ID to its leaderboards, in order.
	setMembers          map[string][]string
	memberLocalizations map[string]*setMemberLocalization
	// order maps a kind and the detail or group that owns its items to the
	// IDs in the order they are listed; items it doesn't name come last.
	order map[string][]string
}

func newGCItemsState() *gcItemsState {
	return &gcItemsState{
		items: map[string]*gcItem{}, versions: map[string]*gcVersion{}, localizations: map[string]*gcLocalization{},
		setMembers: map[string][]string{}, memberLocalizations: map[string]*setMemberLocalization{}, order: map[string][]string{},
	}
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
	relationships := map[string]any{"versions": linkageData(gcKinds[i.kind].versions, versions...)}
	for name, ids := range i.links {
		if name == "leaderboardV2" {
			relationships[name] = oneLinkage(gcLinkTypes[name].items, ids[0])
		} else {
			relationships[name] = linkageData(gcLinkTypes[name].items, ids...)
		}
	}
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
	if !s.setGCLeaderboard(w, i, &req) {
		return
	}
	i.id = s.newID("GI")
	i.attributes["archived"] = false
	s.gcItems.items[i.id] = i
	v := &gcVersion{id: s.newID("GN"), kind: kind.versions, itemID: i.id, number: 1, state: gcDraft, attributes: map[string]any{}}
	s.gcItems.versions[v.id] = v
	writeDoc(w, r, http.StatusCreated, s.renderGCItem(i))
}

// setGCLeaderboard records the leaderboard a challenge's request names,
// which belongs to the challenge's app or group.
func (s *Server) setGCLeaderboard(w http.ResponseWriter, i *gcItem, req *gcRequest) bool {
	id := req.related("leaderboardV2")
	if id == "" {
		return true
	}
	if !s.gcLinkable(i, gcLeaderboards, id) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The item's app or group has no gameCenterLeaderboards with id '"+id+"'", "/data/relationships/leaderboardV2")
		return false
	}
	i.links = map[string][]string{"leaderboardV2": {id}}
	return true
}

// gcLinkable reports whether an item of a kind exists and belongs to the
// same app or group as i.
func (s *Server) gcLinkable(i *gcItem, kind gcKind, id string) bool {
	other, ok := s.gcItems.items[id]
	return ok && other.kind == kind.items && other.detailID == i.detailID && other.groupID == i.groupID
}

// gcSetLinks adds items to a to-many relationship of an item, or removes
// them.
func (s *Server) gcSetLinks(w http.ResponseWriter, r *http.Request, kind gcKind, id, name string, add bool) {
	var req struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findGCItem(w, kind, id)
	if i == nil {
		return
	}
	if i.links == nil {
		i.links = map[string][]string{}
	}
	for _, d := range req.Data {
		if !s.gcLinkable(i, gcLinkTypes[name], d.ID) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The item's app or group has no "+gcLinkTypes[name].items+" with id '"+d.ID+"'", "/data")
			return
		}
		links := slices.DeleteFunc(i.links[name], func(linked string) bool { return linked == d.ID })
		if add {
			links = append(links, d.ID)
		}
		i.links[name] = links
	}
	w.WriteHeader(http.StatusNoContent)
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
	if !s.setGCLeaderboard(w, i, &req) {
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
		if image := s.gcImage(kind, v.id); image != nil {
			s.removeFile(image)
		}
		delete(s.gcItems.versions, v.id)
	}
	for _, other := range s.gcItems.items {
		for name, ids := range other.links {
			if ids = slices.DeleteFunc(ids, func(linked string) bool { return linked == id }); len(ids) == 0 {
				delete(other.links, name)
			} else {
				other.links[name] = ids
			}
		}
	}
	delete(s.gcItems.items, id)
	delete(s.gcItems.setMembers, id)
	for setID, members := range s.gcItems.setMembers {
		s.gcItems.setMembers[setID] = slices.DeleteFunc(members, func(member string) bool { return member == id })
	}
	for localizationID, l := range s.gcItems.memberLocalizations {
		if l.setID == id || l.leaderboardID == id {
			delete(s.gcItems.memberLocalizations, localizationID)
		}
	}
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
	for _, i := range s.ownedGCItems(kind, detailID, groupID) {
		if match(i) {
			items = append(items, s.renderGCItem(i))
		}
	}
	s.writeGCPage(w, r, items, limit)
}

// ownedGCItems returns the kind's items of a detail or a group in their
// order.
func (s *Server) ownedGCItems(kind gcKind, detailID, groupID string) []*gcItem {
	var owned []*gcItem
	for _, i := range sortedValues(s.gcItems.items) {
		if i.kind == kind.items && i.detailID == detailID && i.groupID == groupID {
			owned = append(owned, i)
		}
	}
	order := s.gcItems.order[kind.items+"/"+detailID+groupID]
	position := func(i *gcItem) int {
		if at := slices.Index(order, i.id); at >= 0 {
			return at
		}
		return len(order)
	}
	slices.SortStableFunc(owned, func(a, b *gcItem) int { return position(a) - position(b) })
	return owned
}

// gcReplaceOrder sets the order of the kind's items of a detail or a group.
// The request names each of them once: the relationship can't add or remove
// items.
func (s *Server) gcReplaceOrder(w http.ResponseWriter, r *http.Request, kind gcKind, detailID, groupID string) {
	var req struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owned := s.ownedGCItems(kind, detailID, groupID)
	var ids []string
	for _, d := range req.Data {
		if !slices.ContainsFunc(owned, func(i *gcItem) bool { return i.id == d.ID }) || slices.Contains(ids, d.ID) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+kind.items+" with id '"+d.ID+"' here, or it is named twice.", "/data")
			return
		}
		ids = append(ids, d.ID)
	}
	if len(ids) != len(owned) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The request must name every one of the "+kind.items+".", "/data")
		return
	}
	s.gcItems.order[kind.items+"/"+detailID+groupID] = ids
	w.WriteHeader(http.StatusNoContent)
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
	attributes := maps.Clone(v.attributes)
	attributes["version"], attributes["state"] = v.number, v.state
	return map[string]any{
		"type": kind.versions, "id": v.id, "attributes": attributes,
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
	v := &gcVersion{id: s.newID("GN"), kind: kind.versions, itemID: itemID, number: latest.number + 1, state: gcDraft, attributes: maps.Clone(latest.attributes)}
	maps.Copy(v.attributes, req.Data.Attributes)
	s.gcItems.versions[v.id] = v
	s.copyGCImage(kind, latest.id, v.id)
	for _, l := range s.gcVersionLocalizations(latest.id) {
		c := &gcLocalization{id: s.newID("GL"), kind: l.kind, versionID: v.id, attributes: maps.Clone(l.attributes)}
		s.gcItems.localizations[c.id] = c
		s.copyGCImage(kind, l.id, c.id)
	}
	writeDoc(w, r, http.StatusCreated, s.renderGCVersion(kind, v))
}

// copyGCImage gives an owner a copy of another owner's image, if any.
func (s *Server) copyGCImage(kind gcKind, from, to string) {
	if image := s.gcImage(kind, from); image != nil {
		copied := s.newFile(fileKind(kind.images), to, image.fileName, image.size)
		copied.state = image.state
	}
}

// gcUpdateVersion changes the attributes of a version being prepared; a
// null removes one.
func (s *Server) gcUpdateVersion(w http.ResponseWriter, r *http.Request, kind gcKind, id string) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.gcItems.versions[id]; !ok || v.kind != kind.versions {
		notFound(w, kind.versions, id)
		return
	}
	v := s.draftGCVersion(w, kind, id, "")
	if v == nil {
		return
	}
	for name, value := range req.Data.Attributes {
		if value == nil {
			delete(v.attributes, name)
		} else {
			v.attributes[name] = value
		}
	}
	writeDoc(w, r, http.StatusOK, s.renderGCVersion(kind, v))
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

// gcImage returns the image of a localization, or the default image of a
// version, or nil.
func (s *Server) gcImage(kind gcKind, ownerID string) *uploadedFile {
	for _, f := range s.files {
		if f.kind == fileKind(kind.images) && f.parentID == ownerID {
			return f
		}
	}
	return nil
}

func (s *Server) renderGCImage(r *http.Request, kind gcKind, f *uploadedFile) map[string]any {
	state, operations, _ := s.fileAttributes(r, f)
	image := map[string]any{
		"type": kind.images, "id": f.id,
		"attributes": map[string]any{"fileName": f.fileName, "fileSize": f.size, "assetDeliveryState": state, "uploadOperations": operations},
	}
	if _, ok := s.gcItems.localizations[f.parentID]; ok {
		image["relationships"] = map[string]any{"localization": oneLinkage(kind.localizations, f.parentID)}
	}
	return image
}

// draftGCImageOwner writes an error and returns false unless the owner is a
// localization of a version being prepared, or such a version.
func (s *Server) draftGCImageOwner(w http.ResponseWriter, kind gcKind, ownerID string) bool {
	if _, ok := s.gcItems.localizations[ownerID]; ok {
		return s.draftGCLocalization(w, kind, ownerID) != nil
	}
	return s.draftGCVersion(w, kind, ownerID, "/data/relationships/version") != nil
}

// gcCreateImage reserves the image of a localization, or the default image
// of a version; each has one.
func (s *Server) gcCreateImage(w http.ResponseWriter, r *http.Request, kind gcKind) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ownerID := req.related("localization")
	if ownerID != "" {
		if l, ok := s.gcItems.localizations[ownerID]; !ok || l.kind != kind.localizations {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+kind.localizations+" with id '"+ownerID+"'", "/data/relationships/localization")
			return
		}
	} else {
		ownerID = req.related("version")
	}
	if !s.draftGCImageOwner(w, kind, ownerID) {
		return
	}
	if s.gcImage(kind, ownerID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The localization or version already has an image.", "/data/relationships")
		return
	}
	name, _ := req.Data.Attributes["fileName"].(string)
	size, _ := req.Data.Attributes["fileSize"].(float64)
	writeDoc(w, r, http.StatusCreated, s.renderGCImage(r, kind, s.newFile(fileKind(kind.images), ownerID, name, int(size))))
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
	if f == nil || !s.draftGCImageOwner(w, kind, f.parentID) {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

// gcOwnerImage writes the image of a localization or the default image of a
// version.
func (s *Server) gcOwnerImage(w http.ResponseWriter, r *http.Request, kind gcKind, ownerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.gcImage(kind, ownerID)
	if f == nil {
		notFound(w, kind.images, ownerID)
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
