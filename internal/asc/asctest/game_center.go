package asctest

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center: an app's Game Center detail turns Game Center on, a group
// shares leaderboards and achievements between apps, and a Game Center app
// version turns Game Center on for one App Store version and lists the
// versions it is compatible with for multiplayer
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center).
// The fake allows an app one detail and an App Store version one Game Center
// app version, requires the app's detail before an app version, creates app
// versions enabled, refuses to delete a group that apps belong to, keeps
// compatibility symmetric, and deletes an app version with its App Store
// version.

type gameCenterDetail struct {
	id, appID, groupID string
	// challengeVersions holds the App Store versions that are the minimum
	// for challenges, one per platform.
	challengeVersions []string
}

type gameCenterAppVersion struct {
	id, versionID string
	enabled       bool
	compatible    map[string]bool
}

type gameCenterState struct {
	details     map[string]*gameCenterDetail
	groups      map[string]string // ID to reference name
	appVersions map[string]*gameCenterAppVersion
}

func newGameCenterState() *gameCenterState {
	return &gameCenterState{details: map[string]*gameCenterDetail{}, groups: map[string]string{}, appVersions: map[string]*gameCenterAppVersion{}}
}

// ---- details ----

func renderGameCenterDetail(d *gameCenterDetail) gen.GameCenterDetail {
	relationships := map[string]any{"app": oneLinkage("apps", d.appID), "challengesMinimumPlatformVersions": linkageData("appStoreVersions", d.challengeVersions...)}
	if d.groupID != "" {
		relationships["gameCenterGroup"] = oneLinkage("gameCenterGroups", d.groupID)
	}
	var out gen.GameCenterDetail
	object(&out, "gameCenterDetails", d.id, map[string]any{"arcadeEnabled": false}, relationships)
	return out
}

func (s *Server) appGameCenterDetail(appID string) *gameCenterDetail {
	for _, d := range s.gameCenter.details {
		if d.appID == appID {
			return d
		}
	}
	return nil
}

func (s *Server) GameCenterDetailsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.GameCenterDetailCreateRequest
	if !decode(w, r, &req) {
		return
	}
	appID := req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	if s.appGameCenterDetail(appID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "Game Center is already enabled for the app.", "/data/relationships/app")
		return
	}
	d := &gameCenterDetail{id: s.newID("GD"), appID: appID}
	s.gameCenter.details[d.id] = d
	writeJSON(w, http.StatusCreated, gen.GameCenterDetailResponse{Data: renderGameCenterDetail(d), Links: docLinks(r)})
}

func (s *Server) AppsGameCenterDetailGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsGameCenterDetailGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.appGameCenterDetail(id)
	if d == nil {
		notFound(w, "apps", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.GameCenterDetailResponse{Data: renderGameCenterDetail(d), Links: docLinks(r)})
}

// GameCenterDetailsUpdateInstance moves the app into a group, or out of its
// group when the relationship's data is null.
func (s *Server) GameCenterDetailsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Data struct {
			Relationships struct {
				GameCenterGroup *struct {
					Data *struct {
						ID string `json:"id"`
					} `json:"data"`
				} `json:"gameCenterGroup"`
			} `json:"relationships"`
		} `json:"data"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || json.Unmarshal(body, &req) != nil {
		conflict(w, "ENTITY_ERROR", "The request body is not valid JSON.", "")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.gameCenter.details[id]
	if !ok {
		notFound(w, "gameCenterDetails", id)
		return
	}
	if group := req.Data.Relationships.GameCenterGroup; group != nil {
		groupID := ""
		if group.Data != nil {
			groupID = group.Data.ID
			if _, ok := s.gameCenter.groups[groupID]; !ok {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no gameCenterGroups with id '"+groupID+"'", "/data/relationships/gameCenterGroup")
				return
			}
		}
		d.groupID = groupID
	}
	writeJSON(w, http.StatusOK, gen.GameCenterDetailResponse{Data: renderGameCenterDetail(d), Links: docLinks(r)})
}

// GameCenterDetailsChallengesMinimumPlatformVersionsReplaceToManyRelationship sets
// the app's versions that are the minimum for challenges.
func (s *Server) GameCenterDetailsChallengesMinimumPlatformVersionsReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.GameCenterDetailChallengesMinimumPlatformVersionsLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.gameCenter.details[id]
	if !ok {
		notFound(w, "gameCenterDetails", id)
		return
	}
	versions := []string{}
	for _, v := range req.Data {
		if version, ok := s.versions.versions[v.Id]; !ok || version.appID != d.appID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app has no appStoreVersions with id '"+v.Id+"'", "/data")
			return
		}
		versions = append(versions, v.Id)
	}
	d.challengeVersions = versions
	w.WriteHeader(http.StatusNoContent)
}

// ---- groups ----

func renderGameCenterGroup(id, name string) gen.GameCenterGroup {
	var out gen.GameCenterGroup
	object(&out, "gameCenterGroups", id, map[string]any{"referenceName": name}, nil)
	return out
}

func (s *Server) GameCenterGroupsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.GameCenterGroupCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID("GG")
	if a := req.Data.Attributes; a != nil && a.ReferenceName != nil {
		s.gameCenter.groups[id] = *a.ReferenceName
	} else {
		s.gameCenter.groups[id] = ""
	}
	writeJSON(w, http.StatusCreated, gen.GameCenterGroupResponse{Data: renderGameCenterGroup(id, s.gameCenter.groups[id]), Links: docLinks(r)})
}

func (s *Server) GameCenterGroupsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterGroupsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.gameCenter.groups[id]
	if !ok {
		notFound(w, "gameCenterGroups", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.GameCenterGroupResponse{Data: renderGameCenterGroup(id, name), Links: docLinks(r)})
}

func (s *Server) GameCenterGroupsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.GameCenterGroupUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.gameCenter.groups[id]; !ok {
		notFound(w, "gameCenterGroups", id)
		return
	}
	if a := req.Data.Attributes; a != nil && a.ReferenceName != nil {
		s.gameCenter.groups[id] = *a.ReferenceName
	}
	writeJSON(w, http.StatusOK, gen.GameCenterGroupResponse{Data: renderGameCenterGroup(id, s.gameCenter.groups[id]), Links: docLinks(r)})
}

func (s *Server) GameCenterGroupsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.gameCenter.groups[id]; !ok {
		notFound(w, "gameCenterGroups", id)
		return
	}
	for _, d := range s.gameCenter.details {
		if d.groupID == id {
			stateError(w, "The group has apps and can't be deleted.")
			return
		}
	}
	delete(s.gameCenter.groups, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GameCenterGroupsGetCollection(w http.ResponseWriter, r *http.Request, params gen.GameCenterGroupsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.GameCenterGroup
	for _, id := range sortedKeys(s.gameCenter.groups) {
		items = append(items, renderGameCenterGroup(id, s.gameCenter.groups[id]))
	}
	writePage[gen.GameCenterGroup, gen.GameCenterGroupsResponse](w, r, items, params.Limit)
}

// ---- app versions ----

func renderGameCenterAppVersion(v *gameCenterAppVersion) gen.GameCenterAppVersion {
	var out gen.GameCenterAppVersion
	object(&out, "gameCenterAppVersions", v.id, map[string]any{"enabled": v.enabled}, map[string]any{
		"appStoreVersion": oneLinkage("appStoreVersions", v.versionID), "compatibilityVersions": linkageData("gameCenterAppVersions", sortedKeys(v.compatible)...),
	})
	return out
}

func (s *Server) versionGameCenterAppVersion(versionID string) *gameCenterAppVersion {
	for _, v := range s.gameCenter.appVersions {
		if v.versionID == versionID {
			return v
		}
	}
	return nil
}

func (s *Server) GameCenterAppVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.GameCenterAppVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	versionID := req.Data.Relationships.AppStoreVersion.Data.Id
	const pointer = "/data/relationships/appStoreVersion"
	s.mu.Lock()
	defer s.mu.Unlock()
	version, ok := s.versions.versions[versionID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersions with id '"+versionID+"'", pointer)
		return
	}
	if s.appGameCenterDetail(version.appID) == nil {
		stateError(w, "Game Center is not enabled for the app.")
		return
	}
	if s.versionGameCenterAppVersion(versionID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has a Game Center app version.", pointer)
		return
	}
	v := &gameCenterAppVersion{id: s.newID("GV"), versionID: versionID, enabled: true, compatible: map[string]bool{}}
	s.gameCenter.appVersions[v.id] = v
	writeJSON(w, http.StatusCreated, gen.GameCenterAppVersionResponse{Data: renderGameCenterAppVersion(v), Links: docLinks(r)})
}

func (s *Server) AppStoreVersionsGameCenterAppVersionGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionsGameCenterAppVersionGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.versionGameCenterAppVersion(id)
	if v == nil {
		notFound(w, "appStoreVersions", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.GameCenterAppVersionResponse{Data: renderGameCenterAppVersion(v), Links: docLinks(r)})
}

func (s *Server) GameCenterAppVersionsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.GameCenterAppVersionUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.gameCenter.appVersions[id]
	if !ok {
		notFound(w, "gameCenterAppVersions", id)
		return
	}
	if a := req.Data.Attributes; a != nil && a.Enabled != nil {
		v.enabled = *a.Enabled
	}
	writeJSON(w, http.StatusOK, gen.GameCenterAppVersionResponse{Data: renderGameCenterAppVersion(v), Links: docLinks(r)})
}

func (s *Server) GameCenterDetailsGameCenterAppVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterDetailsGameCenterAppVersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.gameCenter.details[id]
	if !ok {
		notFound(w, "gameCenterDetails", id)
		return
	}
	var items []gen.GameCenterAppVersion
	for _, v := range sortedValues(s.gameCenter.appVersions) {
		if s.versions.versions[v.versionID].appID == d.appID && matches(params.FilterEnabled, boolString(new(v.enabled))) {
			items = append(items, renderGameCenterAppVersion(v))
		}
	}
	writePage[gen.GameCenterAppVersion, gen.GameCenterAppVersionsResponse](w, r, items, params.Limit)
}

func (s *Server) GameCenterAppVersionsCompatibilityVersionsGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterAppVersionsCompatibilityVersionsGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.gameCenter.appVersions[id]
	if !ok {
		notFound(w, "gameCenterAppVersions", id)
		return
	}
	items := []map[string]any{}
	for _, other := range sortedKeys(v.compatible) {
		items = append(items, map[string]any{"type": "gameCenterAppVersions", "id": other})
	}
	writePage[map[string]any, gen.GameCenterAppVersionCompatibilityVersionsLinkagesResponse](w, r, items, params.Limit)
}

// setCompatibility adds or removes compatibility between an app version and
// the versions a linkage request names, in both directions.
func (s *Server) setCompatibility(w http.ResponseWriter, r *http.Request, id string, compatible bool) {
	var req gen.GameCenterAppVersionCompatibilityVersionsLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.gameCenter.appVersions[id]
	if !ok {
		notFound(w, "gameCenterAppVersions", id)
		return
	}
	for _, d := range req.Data {
		other, ok := s.gameCenter.appVersions[d.Id]
		if !ok || other == v {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no other gameCenterAppVersions with id '"+d.Id+"'", "/data")
			return
		}
		if compatible {
			v.compatible[other.id], other.compatible[v.id] = true, true
		} else {
			delete(v.compatible, other.id)
			delete(other.compatible, v.id)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GameCenterAppVersionsCompatibilityVersionsCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.setCompatibility(w, r, id, true)
}

func (s *Server) GameCenterAppVersionsCompatibilityVersionsDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.setCompatibility(w, r, id, false)
}
