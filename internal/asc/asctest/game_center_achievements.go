package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center achievements, as versioned Game Center items.

func (s *Server) GameCenterAchievementsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateItem(w, r, gcAchievements)
}

func (s *Server) GameCenterAchievementsV2GetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterAchievementsV2GetInstanceParams) {
	s.gcGetItem(w, r, gcAchievements, id)
}

func (s *Server) GameCenterAchievementsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateItem(w, r, gcAchievements, id)
}

func (s *Server) GameCenterAchievementsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteItem(w, gcAchievements, id)
}

func (s *Server) GameCenterDetailsGameCenterAchievementsV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterDetailsGameCenterAchievementsV2GetToManyRelatedParams) {
	s.gcListItems(w, r, gcAchievements, id, "", params.Limit, func(i *gcItem) bool {
		return gcItemMatches(i, params.FilterReferenceName, params.FilterArchived, params.FilterId)
	})
}

func (s *Server) GameCenterGroupsGameCenterAchievementsV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterGroupsGameCenterAchievementsV2GetToManyRelatedParams) {
	s.gcListItems(w, r, gcAchievements, "", id, params.Limit, func(i *gcItem) bool {
		return gcItemMatches(i, params.FilterReferenceName, params.FilterArchived, params.FilterId)
	})
}

func (s *Server) GameCenterAchievementsV2VersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterAchievementsV2VersionsGetToManyRelatedParams) {
	s.gcListVersions(w, r, gcAchievements, id, params.Limit)
}

func (s *Server) GameCenterAchievementVersionsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateVersion(w, r, gcAchievements)
}

func (s *Server) GameCenterAchievementVersionsV2LocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterAchievementVersionsV2LocalizationsGetToManyRelatedParams) {
	s.gcListLocalizations(w, r, gcAchievements, id, params.Limit)
}

func (s *Server) GameCenterAchievementLocalizationsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateLocalization(w, r, gcAchievements)
}

func (s *Server) GameCenterAchievementLocalizationsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateLocalization(w, r, gcAchievements, id)
}

func (s *Server) GameCenterAchievementLocalizationsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteLocalization(w, gcAchievements, id)
}

func (s *Server) GameCenterAchievementLocalizationsV2ImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterAchievementLocalizationsV2ImageGetToOneRelatedParams) {
	s.gcOwnerImage(w, r, gcAchievements, id)
}

func (s *Server) GameCenterAchievementImagesV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateImage(w, r, gcAchievements)
}

func (s *Server) GameCenterAchievementImagesV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateImage(w, r, gcAchievements, id)
}

func (s *Server) GameCenterAchievementImagesV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteImage(w, gcAchievements, id)
}

func (s *Server) GameCenterDetailsGameCenterAchievementsV2ReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcReplaceOrder(w, r, gcAchievements, id, "")
}

func (s *Server) GameCenterGroupsGameCenterAchievementsV2ReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcReplaceOrder(w, r, gcAchievements, "", id)
}
