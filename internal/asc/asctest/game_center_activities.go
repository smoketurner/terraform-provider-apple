package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center activities, as versioned Game Center items. An activity's
// versions also have a fallback URL and a default image, and an activity
// links to achievements and leaderboards of its app or group.

func (s *Server) GameCenterActivitiesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateItem(w, r, gcActivities)
}

func (s *Server) GameCenterActivitiesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterActivitiesGetInstanceParams) {
	s.gcGetItem(w, r, gcActivities, id)
}

func (s *Server) GameCenterActivitiesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateItem(w, r, gcActivities, id)
}

func (s *Server) GameCenterActivitiesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteItem(w, gcActivities, id)
}

func (s *Server) GameCenterDetailsGameCenterActivitiesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterDetailsGameCenterActivitiesGetToManyRelatedParams) {
	s.gcListItems(w, r, gcActivities, id, "", params.Limit, func(*gcItem) bool { return true })
}

func (s *Server) GameCenterGroupsGameCenterActivitiesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterGroupsGameCenterActivitiesGetToManyRelatedParams) {
	s.gcListItems(w, r, gcActivities, "", id, params.Limit, func(*gcItem) bool { return true })
}

func (s *Server) GameCenterActivitiesVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterActivitiesVersionsGetToManyRelatedParams) {
	s.gcListVersions(w, r, gcActivities, id, params.Limit)
}

func (s *Server) GameCenterActivityVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateVersion(w, r, gcActivities)
}

func (s *Server) GameCenterActivityVersionsLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterActivityVersionsLocalizationsGetToManyRelatedParams) {
	s.gcListLocalizations(w, r, gcActivities, id, params.Limit)
}

func (s *Server) GameCenterActivityLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateLocalization(w, r, gcActivities)
}

func (s *Server) GameCenterActivityLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateLocalization(w, r, gcActivities, id)
}

func (s *Server) GameCenterActivityLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteLocalization(w, gcActivities, id)
}

func (s *Server) GameCenterActivityLocalizationsImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterActivityLocalizationsImageGetToOneRelatedParams) {
	s.gcOwnerImage(w, r, gcActivities, id)
}

func (s *Server) GameCenterActivityImagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateImage(w, r, gcActivities)
}

func (s *Server) GameCenterActivityImagesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateImage(w, r, gcActivities, id)
}

func (s *Server) GameCenterActivityImagesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteImage(w, gcActivities, id)
}

func (s *Server) GameCenterActivityVersionsDefaultImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterActivityVersionsDefaultImageGetToOneRelatedParams) {
	s.gcOwnerImage(w, r, gcActivities, id)
}

func (s *Server) GameCenterActivityVersionsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateVersion(w, r, gcActivities, id)
}

func (s *Server) GameCenterActivitiesAchievementsV2CreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcSetLinks(w, r, gcActivities, id, "achievementsV2", true)
}

func (s *Server) GameCenterActivitiesAchievementsV2DeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcSetLinks(w, r, gcActivities, id, "achievementsV2", false)
}

func (s *Server) GameCenterActivitiesLeaderboardsV2CreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcSetLinks(w, r, gcActivities, id, "leaderboardsV2", true)
}

func (s *Server) GameCenterActivitiesLeaderboardsV2DeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.gcSetLinks(w, r, gcActivities, id, "leaderboardsV2", false)
}
