package asctest

import (
	"net/http"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center leaderboard sets, as versioned Game Center items. A set groups
// leaderboards of the same app or group, and a leaderboard can have another
// name per locale within a set. The fake allows one such name per set,
// leaderboard and locale.

func (s *Server) GameCenterLeaderboardSetsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateItem(w, r, gcSets)
}

func (s *Server) GameCenterLeaderboardSetsV2GetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterLeaderboardSetsV2GetInstanceParams) {
	s.gcGetItem(w, r, gcSets, id)
}

func (s *Server) GameCenterLeaderboardSetsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateItem(w, r, gcSets, id)
}

func (s *Server) GameCenterLeaderboardSetsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteItem(w, gcSets, id)
}

func (s *Server) GameCenterDetailsGameCenterLeaderboardSetsV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterDetailsGameCenterLeaderboardSetsV2GetToManyRelatedParams) {
	s.gcListItems(w, r, gcSets, id, "", params.Limit, func(i *gcItem) bool {
		return gcItemMatches[string](i, params.FilterReferenceName, nil, params.FilterId)
	})
}

func (s *Server) GameCenterGroupsGameCenterLeaderboardSetsV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterGroupsGameCenterLeaderboardSetsV2GetToManyRelatedParams) {
	s.gcListItems(w, r, gcSets, "", id, params.Limit, func(i *gcItem) bool {
		return gcItemMatches[string](i, params.FilterReferenceName, nil, params.FilterId)
	})
}

func (s *Server) GameCenterLeaderboardSetsV2VersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterLeaderboardSetsV2VersionsGetToManyRelatedParams) {
	s.gcListVersions(w, r, gcSets, id, params.Limit)
}

func (s *Server) GameCenterLeaderboardSetVersionsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateVersion(w, r, gcSets)
}

func (s *Server) GameCenterLeaderboardSetVersionsV2LocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterLeaderboardSetVersionsV2LocalizationsGetToManyRelatedParams) {
	s.gcListLocalizations(w, r, gcSets, id, params.Limit)
}

func (s *Server) GameCenterLeaderboardSetLocalizationsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateLocalization(w, r, gcSets)
}

func (s *Server) GameCenterLeaderboardSetLocalizationsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateLocalization(w, r, gcSets, id)
}

func (s *Server) GameCenterLeaderboardSetLocalizationsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteLocalization(w, gcSets, id)
}

func (s *Server) GameCenterLeaderboardSetLocalizationsV2ImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterLeaderboardSetLocalizationsV2ImageGetToOneRelatedParams) {
	s.gcLocalizationImage(w, r, gcSets, id)
}

func (s *Server) GameCenterLeaderboardSetImagesV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateImage(w, r, gcSets)
}

func (s *Server) GameCenterLeaderboardSetImagesV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateImage(w, r, gcSets, id)
}

func (s *Server) GameCenterLeaderboardSetImagesV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteImage(w, gcSets, id)
}

// ---- members ----

type setMemberLocalization struct {
	id, setID, leaderboardID, locale, name string
}

func (s *Server) GameCenterLeaderboardSetsV2GameCenterLeaderboardsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterLeaderboardSetsV2GameCenterLeaderboardsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findGCItem(w, gcSets, id) == nil {
		return
	}
	var items []map[string]any
	for _, leaderboardID := range s.gcItems.setMembers[id] {
		if leaderboard := s.gcItems.items[leaderboardID]; gcItemMatches(leaderboard, params.FilterReferenceName, params.FilterArchived, params.FilterId) {
			items = append(items, s.renderGCItem(leaderboard))
		}
	}
	s.writeGCPage(w, r, items, params.Limit)
}

// setMembers adds leaderboards to a set, or removes them. A member belongs
// to the set's app or group.
func (s *Server) setMembers(w http.ResponseWriter, r *http.Request, id string, add bool) {
	var req gen.GameCenterLeaderboardSetV2GameCenterLeaderboardsLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.findGCItem(w, gcSets, id)
	if set == nil {
		return
	}
	for _, d := range req.Data {
		leaderboard, ok := s.gcItems.items[d.Id]
		if !ok || leaderboard.kind != gcLeaderboards.items || leaderboard.detailID != set.detailID || leaderboard.groupID != set.groupID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The set's app or group has no gameCenterLeaderboards with id '"+d.Id+"'", "/data")
			return
		}
		members := slices.DeleteFunc(s.gcItems.setMembers[id], func(member string) bool { return member == d.Id })
		if add {
			members = append(members, d.Id)
		}
		s.gcItems.setMembers[id] = members
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GameCenterLeaderboardSetsV2GameCenterLeaderboardsCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.setMembers(w, r, id, true)
}

func (s *Server) GameCenterLeaderboardSetsV2GameCenterLeaderboardsDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.setMembers(w, r, id, false)
}

// ---- member localizations ----

func renderSetMemberLocalization(l *setMemberLocalization) map[string]any {
	return map[string]any{
		"type": "gameCenterLeaderboardSetMemberLocalizations", "id": l.id, "attributes": map[string]any{"locale": l.locale, "name": l.name},
		"relationships": map[string]any{
			"gameCenterLeaderboardSet": oneLinkage(gcSets.items, l.setID), "gameCenterLeaderboard": oneLinkage(gcLeaderboards.items, l.leaderboardID),
		},
	}
}

func (s *Server) GameCenterLeaderboardSetMemberLocalizationsGetCollection(w http.ResponseWriter, r *http.Request, params gen.GameCenterLeaderboardSetMemberLocalizationsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []map[string]any
	for _, l := range sortedValues(s.gcItems.memberLocalizations) {
		if slices.Contains(params.FilterGameCenterLeaderboardSet, l.setID) && slices.Contains(params.FilterGameCenterLeaderboard, l.leaderboardID) {
			items = append(items, renderSetMemberLocalization(l))
		}
	}
	s.writeGCPage(w, r, items, params.Limit)
}

func (s *Server) GameCenterLeaderboardSetMemberLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := &setMemberLocalization{setID: req.related("gameCenterLeaderboardSet"), leaderboardID: req.related("gameCenterLeaderboard")}
	l.locale, _ = req.Data.Attributes["locale"].(string)
	l.name, _ = req.Data.Attributes["name"].(string)
	if !slices.Contains(s.gcItems.setMembers[l.setID], l.leaderboardID) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The leaderboard is not in the set.", "/data/relationships/gameCenterLeaderboard")
		return
	}
	for _, other := range s.gcItems.memberLocalizations {
		if other.setID == l.setID && other.leaderboardID == l.leaderboardID && other.locale == l.locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The leaderboard already has a name for the locale in the set.", "/data/attributes/locale")
			return
		}
	}
	l.id = s.newID("SM")
	s.gcItems.memberLocalizations[l.id] = l
	writeDoc(w, r, http.StatusCreated, renderSetMemberLocalization(l))
}

func (s *Server) GameCenterLeaderboardSetMemberLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.gcItems.memberLocalizations[id]
	if !ok {
		notFound(w, "gameCenterLeaderboardSetMemberLocalizations", id)
		return
	}
	if name, ok := req.Data.Attributes["name"].(string); ok {
		l.name = name
	}
	writeDoc(w, r, http.StatusOK, renderSetMemberLocalization(l))
}

func (s *Server) GameCenterLeaderboardSetMemberLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.gcItems.memberLocalizations[id]; !ok {
		notFound(w, "gameCenterLeaderboardSetMemberLocalizations", id)
		return
	}
	delete(s.gcItems.memberLocalizations, id)
	w.WriteHeader(http.StatusNoContent)
}
