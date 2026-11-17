package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center leaderboards, as versioned Game Center items.

func (s *Server) GameCenterLeaderboardsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateItem(w, r, gcLeaderboards)
}

func (s *Server) GameCenterLeaderboardsV2GetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterLeaderboardsV2GetInstanceParams) {
	s.gcGetItem(w, r, gcLeaderboards, id)
}

func (s *Server) GameCenterLeaderboardsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateItem(w, r, gcLeaderboards, id)
}

func (s *Server) GameCenterLeaderboardsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteItem(w, gcLeaderboards, id)
}

func (s *Server) GameCenterDetailsGameCenterLeaderboardsV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterDetailsGameCenterLeaderboardsV2GetToManyRelatedParams) {
	s.gcListItems(w, r, gcLeaderboards, id, "", params.Limit, func(i *gcItem) bool {
		return gcItemMatches(i, params.FilterReferenceName, params.FilterArchived, params.FilterId)
	})
}

func (s *Server) GameCenterGroupsGameCenterLeaderboardsV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterGroupsGameCenterLeaderboardsV2GetToManyRelatedParams) {
	s.gcListItems(w, r, gcLeaderboards, "", id, params.Limit, func(i *gcItem) bool {
		return gcItemMatches(i, params.FilterReferenceName, params.FilterArchived, params.FilterId)
	})
}

func (s *Server) GameCenterLeaderboardsV2VersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterLeaderboardsV2VersionsGetToManyRelatedParams) {
	s.gcListVersions(w, r, gcLeaderboards, id, params.Limit)
}

func (s *Server) GameCenterLeaderboardVersionsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateVersion(w, r, gcLeaderboards)
}

func (s *Server) GameCenterLeaderboardVersionsV2LocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterLeaderboardVersionsV2LocalizationsGetToManyRelatedParams) {
	s.gcListLocalizations(w, r, gcLeaderboards, id, params.Limit)
}

func (s *Server) GameCenterLeaderboardLocalizationsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateLocalization(w, r, gcLeaderboards)
}

func (s *Server) GameCenterLeaderboardLocalizationsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateLocalization(w, r, gcLeaderboards, id)
}

func (s *Server) GameCenterLeaderboardLocalizationsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteLocalization(w, gcLeaderboards, id)
}

func (s *Server) GameCenterLeaderboardLocalizationsV2ImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterLeaderboardLocalizationsV2ImageGetToOneRelatedParams) {
	s.gcOwnerImage(w, r, gcLeaderboards, id)
}

func (s *Server) GameCenterLeaderboardImagesV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateImage(w, r, gcLeaderboards)
}

func (s *Server) GameCenterLeaderboardImagesV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateImage(w, r, gcLeaderboards, id)
}

func (s *Server) GameCenterLeaderboardImagesV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteImage(w, gcLeaderboards, id)
}

func (s *Server) GameCenterDetailsGameCenterLeaderboardsV2ReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcReplaceOrder(w, r, gcLeaderboards, id, "")
}

func (s *Server) GameCenterGroupsGameCenterLeaderboardsV2ReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcReplaceOrder(w, r, gcLeaderboards, "", id)
}
