package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center matchmaking: a rule set holds the rules and teams that decide
// which players are matched, and a queue matches the players who ask for a
// match with a rule set
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center-matchmaking).
// The fake requires unique rule set names, at least as many maximum as
// minimum players, and refuses to delete a rule set a queue uses.

type matchmakingObject struct {
	id, kind, ruleSetID string
	attributes          map[string]any
	// experimentID is a queue's experiment rule set, or "".
	experimentID string
}

const (
	matchRuleSets = "gameCenterMatchmakingRuleSets"
	matchRules    = "gameCenterMatchmakingRules"
	matchTeams    = "gameCenterMatchmakingTeams"
	matchQueues   = "gameCenterMatchmakingQueues"
)

func renderMatchmaking(o *matchmakingObject) map[string]any {
	doc := map[string]any{"type": o.kind, "id": o.id, "attributes": o.attributes}
	if o.kind == matchQueues {
		relationships := map[string]any{"ruleSet": oneLinkage(matchRuleSets, o.ruleSetID)}
		if o.experimentID != "" {
			relationships["experimentRuleSet"] = oneLinkage(matchRuleSets, o.experimentID)
		}
		doc["relationships"] = relationships
	}
	return doc
}

func (s *Server) findMatchmaking(w http.ResponseWriter, kind, id string) *matchmakingObject {
	o, ok := s.matchmaking[id]
	if !ok || o.kind != kind {
		notFound(w, kind, id)
		return nil
	}
	return o
}

// validPlayers writes an error and returns false when the attributes ask
// for fewer maximum than minimum players.
func validPlayers(w http.ResponseWriter, attributes map[string]any) bool {
	low, _ := attributes["minPlayers"].(float64)
	high, _ := attributes["maxPlayers"].(float64)
	if high < low {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "maxPlayers is less than minPlayers.", "/data/attributes/maxPlayers")
		return false
	}
	return true
}

// matchmakingRuleSet returns the rule set a relationship of the request
// names, or writes an error and returns false. A missing relationship is
// "", true unless it is required.
func (s *Server) matchmakingRuleSet(w http.ResponseWriter, req *gcRequest, name string, required bool) (string, bool) {
	rel, given := req.Data.Relationships[name]
	id := req.related(name)
	if !given || id == "" && string(rel.Data) == "null" {
		if required {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "The "+name+" relationship is required.", "/data/relationships/"+name)
		}
		return "", !required
	}
	if o, ok := s.matchmaking[id]; !ok || o.kind != matchRuleSets {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+matchRuleSets+" with id '"+id+"'", "/data/relationships/"+name)
		return "", false
	}
	return id, true
}

// createMatchmaking creates a rule set, or a rule, team or queue of one.
func (s *Server) createMatchmaking(w http.ResponseWriter, r *http.Request, kind string) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validPlayers(w, req.Data.Attributes) {
		return
	}
	o := &matchmakingObject{kind: kind, attributes: req.Data.Attributes}
	if kind == matchRuleSets {
		for _, other := range s.matchmaking {
			if other.kind == kind && other.attributes["referenceName"] == o.attributes["referenceName"] {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "A rule set already has the reference name.", "/data/attributes/referenceName")
				return
			}
		}
	} else {
		var ok bool
		if o.ruleSetID, ok = s.matchmakingRuleSet(w, &req, "ruleSet", true); !ok {
			return
		}
		if o.experimentID, ok = s.matchmakingRuleSet(w, &req, "experimentRuleSet", false); !ok {
			return
		}
	}
	o.id = s.newID("MM")
	s.matchmaking[o.id] = o
	writeDoc(w, r, http.StatusCreated, renderMatchmaking(o))
}

// updateMatchmaking merges the request's attributes, and for a queue sets
// the rule sets the request names; null data removes the experiment one.
func (s *Server) updateMatchmaking(w http.ResponseWriter, r *http.Request, kind, id string) {
	var req gcRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.findMatchmaking(w, kind, id)
	if o == nil {
		return
	}
	merged := map[string]any{}
	for name, value := range o.attributes {
		merged[name] = value
	}
	for name, value := range req.Data.Attributes {
		if value == nil {
			delete(merged, name)
		} else {
			merged[name] = value
		}
	}
	if !validPlayers(w, merged) {
		return
	}
	if kind == matchQueues {
		if ruleSet, ok := s.matchmakingRuleSet(w, &req, "ruleSet", false); !ok {
			return
		} else if ruleSet != "" {
			o.ruleSetID = ruleSet
		}
		if _, given := req.Data.Relationships["experimentRuleSet"]; given {
			experiment, ok := s.matchmakingRuleSet(w, &req, "experimentRuleSet", false)
			if !ok {
				return
			}
			o.experimentID = experiment
		}
	}
	o.attributes = merged
	writeDoc(w, r, http.StatusOK, renderMatchmaking(o))
}

func (s *Server) getMatchmaking(w http.ResponseWriter, r *http.Request, kind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.findMatchmaking(w, kind, id); o != nil {
		writeDoc(w, r, http.StatusOK, renderMatchmaking(o))
	}
}

// deleteMatchmaking deletes an object; a rule set goes with its rules and
// teams, unless a queue uses it.
func (s *Server) deleteMatchmaking(w http.ResponseWriter, kind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findMatchmaking(w, kind, id) == nil {
		return
	}
	if kind == matchRuleSets {
		for _, o := range s.matchmaking {
			if o.kind == matchQueues && (o.ruleSetID == id || o.experimentID == id) {
				stateError(w, "A matchmaking queue uses the rule set.")
				return
			}
		}
		for otherID, o := range s.matchmaking {
			if o.ruleSetID == id {
				delete(s.matchmaking, otherID)
			}
		}
	}
	delete(s.matchmaking, id)
	w.WriteHeader(http.StatusNoContent)
}

// listMatchmaking writes the objects of a kind, of a rule set unless
// ruleSetID is "".
func (s *Server) listMatchmaking(w http.ResponseWriter, r *http.Request, kind, ruleSetID string, limit *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ruleSetID != "" && s.findMatchmaking(w, matchRuleSets, ruleSetID) == nil {
		return
	}
	var items []map[string]any
	for _, o := range sortedValues(s.matchmaking) {
		if o.kind == kind && (ruleSetID == "" || o.ruleSetID == ruleSetID) {
			items = append(items, renderMatchmaking(o))
		}
	}
	s.writeGCPage(w, r, items, limit)
}

func (s *Server) GameCenterMatchmakingRuleSetsCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createMatchmaking(w, r, matchRuleSets)
}

func (s *Server) GameCenterMatchmakingRuleSetsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterMatchmakingRuleSetsGetInstanceParams) {
	s.getMatchmaking(w, r, matchRuleSets, id)
}

func (s *Server) GameCenterMatchmakingRuleSetsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateMatchmaking(w, r, matchRuleSets, id)
}

func (s *Server) GameCenterMatchmakingRuleSetsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.deleteMatchmaking(w, matchRuleSets, id)
}

func (s *Server) GameCenterMatchmakingRuleSetsGetCollection(w http.ResponseWriter, r *http.Request, params gen.GameCenterMatchmakingRuleSetsGetCollectionParams) {
	s.listMatchmaking(w, r, matchRuleSets, "", params.Limit)
}

func (s *Server) GameCenterMatchmakingRuleSetsRulesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterMatchmakingRuleSetsRulesGetToManyRelatedParams) {
	s.listMatchmaking(w, r, matchRules, id, params.Limit)
}

func (s *Server) GameCenterMatchmakingRuleSetsTeamsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.GameCenterMatchmakingRuleSetsTeamsGetToManyRelatedParams) {
	s.listMatchmaking(w, r, matchTeams, id, params.Limit)
}

func (s *Server) GameCenterMatchmakingRulesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createMatchmaking(w, r, matchRules)
}

func (s *Server) GameCenterMatchmakingRulesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateMatchmaking(w, r, matchRules, id)
}

func (s *Server) GameCenterMatchmakingRulesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.deleteMatchmaking(w, matchRules, id)
}

func (s *Server) GameCenterMatchmakingTeamsCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createMatchmaking(w, r, matchTeams)
}

func (s *Server) GameCenterMatchmakingTeamsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateMatchmaking(w, r, matchTeams, id)
}

func (s *Server) GameCenterMatchmakingTeamsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.deleteMatchmaking(w, matchTeams, id)
}

func (s *Server) GameCenterMatchmakingQueuesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createMatchmaking(w, r, matchQueues)
}

func (s *Server) GameCenterMatchmakingQueuesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.GameCenterMatchmakingQueuesGetInstanceParams) {
	s.getMatchmaking(w, r, matchQueues, id)
}

func (s *Server) GameCenterMatchmakingQueuesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateMatchmaking(w, r, matchQueues, id)
}

func (s *Server) GameCenterMatchmakingQueuesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.deleteMatchmaking(w, matchQueues, id)
}

func (s *Server) GameCenterMatchmakingQueuesGetCollection(w http.ResponseWriter, r *http.Request, params gen.GameCenterMatchmakingQueuesGetCollectionParams) {
	s.listMatchmaking(w, r, matchQueues, "", params.Limit)
}
