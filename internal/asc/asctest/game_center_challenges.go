package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center challenges, as versioned Game Center items. A challenge's
// versions also have a default image, and a challenge is for a leaderboard
// of its app or group.

func (s *Server) GameCenterChallengesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateItem(w, r, gcChallenges)
}

func (s *Server) GameCenterChallengesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterChallengesGetInstanceParams) {
	s.gcGetItem(w, r, gcChallenges, id)
}

func (s *Server) GameCenterChallengesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateItem(w, r, gcChallenges, id)
}

func (s *Server) GameCenterChallengesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteItem(w, gcChallenges, id)
}

func (s *Server) GameCenterDetailsGameCenterChallengesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterDetailsGameCenterChallengesGetToManyRelatedParams) {
	s.gcListItems(w, r, gcChallenges, id, "", params.Limit, func(*gcItem) bool { return true })
}

func (s *Server) GameCenterGroupsGameCenterChallengesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterGroupsGameCenterChallengesGetToManyRelatedParams) {
	s.gcListItems(w, r, gcChallenges, "", id, params.Limit, func(*gcItem) bool { return true })
}

func (s *Server) GameCenterChallengesVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterChallengesVersionsGetToManyRelatedParams) {
	s.gcListVersions(w, r, gcChallenges, id, params.Limit)
}

func (s *Server) GameCenterChallengeVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateVersion(w, r, gcChallenges)
}

func (s *Server) GameCenterChallengeVersionsLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterChallengeVersionsLocalizationsGetToManyRelatedParams) {
	s.gcListLocalizations(w, r, gcChallenges, id, params.Limit)
}

func (s *Server) GameCenterChallengeLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateLocalization(w, r, gcChallenges)
}

func (s *Server) GameCenterChallengeLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateLocalization(w, r, gcChallenges, id)
}

func (s *Server) GameCenterChallengeLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteLocalization(w, gcChallenges, id)
}

func (s *Server) GameCenterChallengeLocalizationsImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterChallengeLocalizationsImageGetToOneRelatedParams) {
	s.gcOwnerImage(w, r, gcChallenges, id)
}

func (s *Server) GameCenterChallengeImagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.gcCreateImage(w, r, gcChallenges)
}

func (s *Server) GameCenterChallengeImagesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.gcUpdateImage(w, r, gcChallenges, id)
}

func (s *Server) GameCenterChallengeImagesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.gcDeleteImage(w, gcChallenges, id)
}

func (s *Server) GameCenterChallengeVersionsDefaultImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterChallengeVersionsDefaultImageGetToOneRelatedParams) {
	s.gcOwnerImage(w, r, gcChallenges, id)
}
