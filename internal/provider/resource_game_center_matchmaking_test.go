package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

func matchmakingConfig(maxPlayers, weight, teamMax, queue string) string {
	return `
resource "apple_game_center_matchmaking_rule_set" "ranked" {
  reference_name        = "Ranked"
  rule_language_version = 1
  min_players           = 2
  max_players           = ` + maxPlayers + `
}

resource "apple_game_center_matchmaking_rule_set" "trial" {
  reference_name        = "Trial"
  rule_language_version = 1
  min_players           = 2
  max_players           = 4
}

resource "apple_game_center_matchmaking_rule" "skill" {
  rule_set_id    = apple_game_center_matchmaking_rule_set.ranked.id
  reference_name = "skill"
  type           = "DISTANCE"
  description    = "Players of similar skill."
  expression     = "abs(requests[0].properties.skill - requests[1].properties.skill)"
` + weight + `
}

resource "apple_game_center_matchmaking_rule" "mode" {
  rule_set_id    = apple_game_center_matchmaking_rule_set.ranked.id
  reference_name = "mode"
  type           = "MATCH"
  description    = "Players in the same mode."
  expression     = "requests[0].properties.mode == requests[1].properties.mode"
}

resource "apple_game_center_matchmaking_team" "red" {
  rule_set_id    = apple_game_center_matchmaking_rule_set.ranked.id
  reference_name = "red"
  min_players    = 1
  max_players    = ` + teamMax + `
}

resource "apple_game_center_matchmaking_queue" "ranked" {
  reference_name = "ranked"
  rule_set_id    = apple_game_center_matchmaking_rule_set.ranked.id
` + queue + `
}
`
}

const matchmakingExperiment = `
  experiment_rule_set_id         = apple_game_center_matchmaking_rule_set.trial.id
  classic_matchmaking_bundle_ids = ["com.example.app"]
`

// matchmaking describes the rule set's players, rule and team, and the
// queue, as the API holds them.
func (e *testEnv) matchmaking(ruleSetID, queueID string) (string, error) {
	ctx := context.Background()
	set, err := e.client.GameCenterMatchmakingRuleSetsGetInstanceWithResponse(ctx, ruleSetID, nil)
	if err := asc.Err(set, err); err != nil {
		return "", err
	}
	rules, err := e.client.GameCenterMatchmakingRuleSetsRulesGetToManyRelatedWithResponse(ctx, ruleSetID, nil)
	if err := asc.Err(rules, err); err != nil {
		return "", err
	}
	teams, err := e.client.GameCenterMatchmakingRuleSetsTeamsGetToManyRelatedWithResponse(ctx, ruleSetID, nil)
	if err := asc.Err(teams, err); err != nil {
		return "", err
	}
	queue, err := e.client.GameCenterMatchmakingQueuesGetInstanceWithResponse(ctx, queueID, nil)
	if err := asc.Err(queue, err); err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "players=%d-%d", *set.JSON200.Data.Attributes.MinPlayers, *set.JSON200.Data.Attributes.MaxPlayers)
	for _, rule := range rules.JSON200.Data {
		if *rule.Attributes.ReferenceName != "skill" {
			continue
		}
		weight := "none"
		if w := float32Value(rule.Attributes.Weight); !w.IsNull() {
			weight = fmt.Sprint(w.ValueFloat64())
		}
		fmt.Fprintf(&out, " rule=%s/%s", *rule.Attributes.Description, weight)
	}
	for _, team := range teams.JSON200.Data {
		fmt.Fprintf(&out, " team=%d-%d", *team.Attributes.MinPlayers, *team.Attributes.MaxPlayers)
	}
	q := queue.JSON200.Data
	fmt.Fprintf(&out, " queue=%s experiment=%t bundles=%v", q.Relationships.RuleSet.Data.Id,
		q.Relationships.ExperimentRuleSet != nil && q.Relationships.ExperimentRuleSet.Data != nil, asc.Value(q.Attributes.ClassicMatchmakingBundleIds))
	return out.String(), nil
}

func TestAccGameCenterMatchmaking(t *testing.T) {
	env := newTestEnv(t)
	const ruleSet, rule, team, queue = "apple_game_center_matchmaking_rule_set.ranked", "apple_game_center_matchmaking_rule.skill", "apple_game_center_matchmaking_team.red", "apple_game_center_matchmaking_queue.ranked"
	lookups := `
data "apple_game_center_matchmaking_rule_sets" "all" {
  depends_on = [apple_game_center_matchmaking_rule_set.ranked, apple_game_center_matchmaking_rule_set.trial]
}

data "apple_game_center_matchmaking_queues" "all" {
  depends_on = [apple_game_center_matchmaking_queue.ranked]
}
`
	var ruleSetID, ruleID, teamID, queueID string
	check := func(want func() string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got, err := env.matchmaking(ruleSetID, queueID)
			if err != nil {
				return err
			}
			if got != want() {
				return fmt.Errorf("matchmaking = %s\nwant          %s", got, want())
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.GameCenterMatchmakingRuleSetsGetCollectionWithResponse(context.Background(), nil)
			if err := asc.Err(out, err); err != nil {
				return err
			}
			if n := len(out.JSON200.Data); n != 0 {
				return fmt.Errorf("%d rule sets are left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      matchmakingConfig("1", "", "2", ""),
				ExpectError: regexp.MustCompile(`maxPlayers\s+is\s+less\s+than\s+minPlayers`),
			},
			{
				Config: matchmakingConfig("8", "  weight = 0.3", "4", matchmakingExperiment) + lookups,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(ruleSet, "id", &ruleSetID),
					captureAttr(rule, "id", &ruleID),
					captureAttr(team, "id", &teamID),
					captureAttr(queue, "id", &queueID),
					resource.TestCheckResourceAttr(rule, "weight", "0.3"),
					resource.TestCheckResourceAttr("data.apple_game_center_matchmaking_rule_sets.all", "rule_sets.#", "2"),
					resource.TestCheckResourceAttr("data.apple_game_center_matchmaking_queues.all", "queues.#", "1"),
					resource.TestCheckResourceAttrPair("data.apple_game_center_matchmaking_queues.all", "queues.0.rule_set_id", ruleSet, "id"),
					resource.TestCheckResourceAttrPair("data.apple_game_center_matchmaking_queues.all", "queues.0.experiment_rule_set_id",
						"apple_game_center_matchmaking_rule_set.trial", "id"),
					check(func() string {
						return "players=2-8 rule=Players of similar skill./0.3 team=1-4 queue=" + ruleSetID + " experiment=true bundles=[com.example.app]"
					}),
				),
			},
			{ResourceName: ruleSet, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: rule, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return ruleSetID + "/" + ruleID, nil },
			},
			{
				ResourceName: team, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return ruleSetID + "/" + teamID, nil },
			},
			{ResourceName: queue, ImportState: true, ImportStateVerify: true},
			{
				// The player counts change, and the weight, the experiment
				// and the bundle IDs clear, in place.
				Config: matchmakingConfig("6", "", "3", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ruleSet, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(rule, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(team, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(queue, plancheck.ResourceActionUpdate),
				}},
				Check: check(func() string {
					return "players=2-6 rule=Players of similar skill./none team=1-3 queue=" + ruleSetID + " experiment=false bundles=[]"
				}),
			},
			{
				// A rule deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.GameCenterMatchmakingRulesDeleteInstanceWithResponse(ctx, ruleID)
					if err := asc.Err(out, err); err != nil {
						return err
					}
					deleted, err := c.GameCenterMatchmakingTeamsDeleteInstanceWithResponse(ctx, teamID)
					return asc.Err(deleted, err)
				}),
				Config: matchmakingConfig("6", "", "3", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(rule, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(team, plancheck.ResourceActionCreate),
				}},
			},
			{
				// A queue needs an existing rule set.
				Config: matchmakingConfig("6", "", "3", "") + `
resource "apple_game_center_matchmaking_queue" "trial" {
  reference_name = "trial"
  rule_set_id    = "nope"
}
`,
				ExpectError: regexp.MustCompile(`There\s+is\s+no\s+gameCenterMatchmakingRuleSets`),
			},
		},
	})
}
