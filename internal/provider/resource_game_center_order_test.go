package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func gameCenterOrderConfig(achievements, leaderboards, members string) string {
	return gameCenterItems + `
resource "apple_game_center_achievement" "streak" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Win streak"
  vendor_identifier     = "com.example.streak"
  points                = 20
  show_before_earned    = true
  repeatable            = false
}

resource "apple_game_center_leaderboard_set" "seasons" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Seasons"
  vendor_identifier     = "com.example.seasons"
}

resource "apple_game_center_leaderboard_set" "modes" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Modes"
  vendor_identifier     = "com.example.modes"
}

resource "apple_game_center_leaderboard_set_member" "scores" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  leaderboard_id     = apple_game_center_leaderboard.scores.id
}

resource "apple_game_center_leaderboard_set_member" "weekly" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  leaderboard_id     = apple_game_center_leaderboard.weekly.id
}

resource "apple_game_center_achievement_order" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  achievement_ids       = [` + achievements + `]
}

resource "apple_game_center_leaderboard_order" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  leaderboard_ids       = [` + leaderboards + `]
}

resource "apple_game_center_leaderboard_set_order" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  leaderboard_set_ids   = [apple_game_center_leaderboard_set.modes.id, apple_game_center_leaderboard_set.seasons.id]
}

resource "apple_game_center_leaderboard_set_member_order" "seasons" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  leaderboard_ids    = [` + members + `]
}
`
}

const (
	firstWin, streak = "apple_game_center_achievement.first_win.id", "apple_game_center_achievement.streak.id"
	scores, weekly   = "apple_game_center_leaderboard.scores.id", "apple_game_center_leaderboard.weekly.id"
	memberScores     = "apple_game_center_leaderboard_set_member.scores.leaderboard_id"
	memberWeekly     = "apple_game_center_leaderboard_set_member.weekly.leaderboard_id"
)

func TestAccGameCenterOrder(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	const achievements, leaderboards = "apple_game_center_achievement_order.app", "apple_game_center_leaderboard_order.app"
	const sets, members = "apple_game_center_leaderboard_set_order.app", "apple_game_center_leaderboard_set_member_order.seasons"
	var detailID, setID string
	apis := map[string]func() fwresource.Resource{
		achievements: newAchievementOrderResource, leaderboards: newLeaderboardOrderResource, sets: newLeaderboardSetOrderResource, members: newSetMemberOrderResource,
	}
	// inOrder checks that the API lists the items in the order of the named
	// resources, and that the state starts with the first. Items are created
	// in parallel, so their IDs don't follow the configuration.
	inOrder := func(address, attr string, want ...string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			r, _ := apis[address]().(*orderResource)
			ownerID := detailID
			if address == members {
				ownerID = setID
			}
			got, err := r.api.list(context.Background(), env.client, 0, ownerID)
			if err != nil {
				return err
			}
			var wantIDs []string
			for _, w := range want {
				wantIDs = append(wantIDs, s.RootModule().Resources[w].Primary.ID)
			}
			if first := s.RootModule().Resources[address].Primary.Attributes[attr+".0"]; fmt.Sprint(got) != fmt.Sprint(wantIDs) || first != wantIDs[0] {
				return fmt.Errorf("%s: order = %v, state starts with %s; want %v", address, got, first, wantIDs)
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				// An order names every item.
				Config:      locals + gameCenterOrderConfig(firstWin, scores+", "+weekly, memberScores+", "+memberWeekly),
				ExpectError: regexp.MustCompile(`must\s+name\s+every\s+one\s+of\s+the\s+gameCenterAchievements`),
			},
			{
				Config: locals + gameCenterOrderConfig(streak+", "+firstWin, weekly+", "+scores, memberWeekly+", "+memberScores),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_game_center_detail.app", "id", &detailID),
					captureAttr("apple_game_center_leaderboard_set.seasons", "id", &setID),
					inOrder(achievements, "achievement_ids", "apple_game_center_achievement.streak", "apple_game_center_achievement.first_win"),
					inOrder(leaderboards, "leaderboard_ids", "apple_game_center_leaderboard.weekly", "apple_game_center_leaderboard.scores"),
					inOrder(sets, "leaderboard_set_ids", "apple_game_center_leaderboard_set.modes", "apple_game_center_leaderboard_set.seasons"),
					inOrder(members, "leaderboard_ids", "apple_game_center_leaderboard.weekly", "apple_game_center_leaderboard.scores"),
				),
			},
			{
				ResourceName: achievements, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return "detail/" + detailID, nil },
			},
			{
				ResourceName: members, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return "set/" + setID, nil },
			},
			{
				ResourceName: achievements, ImportState: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return "app/" + detailID, nil },
				ExpectError:       regexp.MustCompile(`doesn't\s+name\s+the\s+owner`),
			},
			{
				// The orders change in place.
				Config: locals + gameCenterOrderConfig(firstWin+", "+streak, scores+", "+weekly, memberScores+", "+memberWeekly),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(achievements, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(leaderboards, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(members, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(sets, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					inOrder(achievements, "achievement_ids", "apple_game_center_achievement.first_win", "apple_game_center_achievement.streak"),
					inOrder(leaderboards, "leaderboard_ids", "apple_game_center_leaderboard.scores", "apple_game_center_leaderboard.weekly"),
					inOrder(members, "leaderboard_ids", "apple_game_center_leaderboard.scores", "apple_game_center_leaderboard.weekly"),
				),
			},
		},
	})
}

func TestAccGameCenterChallengeVersions(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	const detail = "apple_game_center_detail.app"
	config := func(versions string) string {
		return locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_game_center_detail" "app" {
  app_id = local.app_id
` + versions + `
}
`
	}
	var versionID string
	versions := func(want func() string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			d, err := appGameCenterDetail(context.Background(), env.client, appID)
			if err != nil {
				return err
			}
			var got []string
			for _, v := range *d.Relationships.ChallengesMinimumPlatformVersions.Data {
				got = append(got, v.Id)
			}
			if fmt.Sprint(got) != want() {
				return fmt.Errorf("challenge versions = %v, want %s", got, want())
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: config(`  challenges_minimum_platform_version_ids = [apple_app_store_version.next.id]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_store_version.next", "id", &versionID),
					resource.TestCheckResourceAttr(detail, "challenges_minimum_platform_version_ids.#", "1"),
					versions(func() string { return "[" + versionID + "]" }),
				),
			},
			{
				// Unset, the versions are left as they are.
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(detail, "challenges_minimum_platform_version_ids.#"),
					versions(func() string { return "[" + versionID + "]" }),
				),
			},
			{
				// An empty set removes them.
				Config:           config(`  challenges_minimum_platform_version_ids = []`),
				ConfigPlanChecks: expectAction(detail, plancheck.ResourceActionUpdate),
				Check:            versions(func() string { return "[]" }),
			},
			{
				Config:      config(`  challenges_minimum_platform_version_ids = ["nope"]`),
				ExpectError: regexp.MustCompile(`app\s+has\s+no\s+appStoreVersions`),
			},
		},
	})
}
