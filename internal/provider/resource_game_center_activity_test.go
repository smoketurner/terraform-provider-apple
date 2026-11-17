package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// gameCenterItems is an app's Game Center detail with an achievement and
// two leaderboards, for activities and challenges to refer to.
const gameCenterItems = achievementOwner + `
resource "apple_game_center_achievement" "first_win" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "First win"
  vendor_identifier     = "com.example.first_win"
  points                = 10
  show_before_earned    = true
  repeatable            = false
}

resource "apple_game_center_leaderboard" "scores" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "High scores"
  vendor_identifier     = "com.example.high_scores"
  default_formatter     = "INTEGER"
  submission_type       = "BEST_SCORE"
  score_sort_type       = "DESC"
}

resource "apple_game_center_leaderboard" "weekly" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Weekly scores"
  vendor_identifier     = "com.example.weekly_scores"
  default_formatter     = "INTEGER"
  submission_type       = "BEST_SCORE"
  score_sort_type       = "DESC"
}
`

func activityConfig(extra, localization, rest string) string {
	return gameCenterItems + `
resource "apple_game_center_activity" "daily" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Daily puzzle"
  vendor_identifier     = "com.example.daily_puzzle"
` + extra + `
}

resource "apple_game_center_activity_localization" "en_US" {
  activity_id = apple_game_center_activity.daily.id
  locale      = "en-US"
  name        = "Daily Puzzle"
` + localization + `
}
` + rest
}

const activityOptions = `
  play_style            = "ASYNCHRONOUS"
  minimum_players_count = 1
  maximum_players_count = 4
  supports_party_code   = true
  properties            = { level = "daily" }
  fallback_url          = "https://example.com/daily"
`

func activityExtras(defaultImage, image string) string {
	return `
resource "apple_game_center_activity_default_image" "daily" {
  activity_id = apple_game_center_activity.daily.id
  file        = "` + filepath.ToSlash(defaultImage) + `"
}

resource "apple_game_center_activity_image" "en_US" {
  activity_id = apple_game_center_activity.daily.id
  locale      = apple_game_center_activity_localization.en_US.locale
  file        = "` + filepath.ToSlash(image) + `"
}

resource "apple_game_center_activity_achievement" "first_win" {
  activity_id    = apple_game_center_activity.daily.id
  achievement_id = apple_game_center_achievement.first_win.id
}

resource "apple_game_center_activity_leaderboard" "scores" {
  activity_id    = apple_game_center_activity.daily.id
  leaderboard_id = apple_game_center_leaderboard.scores.id
}

data "apple_game_center_activities" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  depends_on            = [apple_game_center_activity.daily]
}
`
}

// activity describes an activity as the API holds it: its attributes, what
// it links to, and the fallback URL, default image and en-US localization
// of its latest version.
func (e *testEnv) activity(id string) (string, error) {
	ctx := context.Background()
	out, err := e.client.GameCenterActivitiesGetInstanceWithResponse(ctx, id, &gen.GameCenterActivitiesGetInstanceParams{
		Include: &[]gen.GameCenterActivitiesGetInstanceParamsInclude{
			gen.GameCenterActivitiesGetInstanceParamsIncludeAchievementsV2, gen.GameCenterActivitiesGetInstanceParamsIncludeLeaderboardsV2,
		},
	})
	if err := asc.Err(out, err); err != nil {
		return "", err
	}
	a := out.JSON200.Data.Attributes
	links := 0
	if rel := out.JSON200.Data.Relationships; rel != nil {
		if rel.AchievementsV2 != nil {
			links += len(asc.Value(rel.AchievementsV2.Data))
		}
		if rel.LeaderboardsV2 != nil {
			links += 10 * len(asc.Value(rel.LeaderboardsV2.Data))
		}
	}
	versions, err := activityVersions(ctx, e.client, id)
	if err != nil {
		return "", err
	}
	latest := versions[len(versions)-1]
	_, err = activityImageAPI(true).existing(ctx, e.client, latest.Id)
	if err != nil && !asc.IsNotFound(err) {
		return "", err
	}
	hasDefault := err == nil
	localizations, err := activityLocalizationAPI.list(ctx, e.client, latest.Id)
	if err != nil || len(localizations) != 1 {
		return "", fmt.Errorf("localizations = %v (err %w)", localizations, err)
	}
	_, err = activityImageAPI(false).existing(ctx, e.client, localizations[0].id)
	if err != nil && !asc.IsNotFound(err) {
		return "", err
	}
	return fmt.Sprintf("players=%d-%d properties=%v archived=%t links=%d url=%s default=%t description=%s image=%t",
		asc.Value(a.MinimumPlayersCount), asc.Value(a.MaximumPlayersCount), asc.Value(a.Properties), *a.Archived, links,
		asc.Value(latest.Attributes.FallbackUrl), hasDefault, asc.Value(localizations[0].values["description"]), err == nil), nil
}

func (e *testEnv) checkActivity(id *string, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := e.activity(*id)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("activity = %s\nwant       %s", got, want)
		}
		return nil
	}
}

func TestAccGameCenterActivity(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	cover, _ := writeImage(t, dir, "cover.png", "cover ")
	english, _ := writeImage(t, dir, "english.png", "english ")
	const address, localization = "apple_game_center_activity.daily", "apple_game_center_activity_localization.en_US"
	const defaultImage, achievement, leaderboard = "apple_game_center_activity_default_image.daily", "apple_game_center_activity_achievement.first_win", "apple_game_center_activity_leaderboard.scores"
	full := locals + activityConfig(activityOptions, `  description = "A new puzzle every day."`, activityExtras(cover, english))
	const fullState = "players=1-4 properties=map[level:daily] archived=false links=11 url=https://example.com/daily default=true description=A new puzzle every day. image=true"
	var id, achievementID string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.GameCenterActivitiesGetInstanceWithResponse(context.Background(), id, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("activity %s still exists (err %w)", id, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: full,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					captureAttr("apple_game_center_achievement.first_win", "id", &achievementID),
					resource.TestCheckResourceAttr(address, "archived", "false"),
					resource.TestCheckResourceAttr("data.apple_game_center_activities.app", "activities.#", "1"),
					resource.TestCheckResourceAttr("data.apple_game_center_activities.app", "activities.0.play_style", "ASYNCHRONOUS"),
					func(s *terraform.State) error { return env.checkActivity(&id, fullState)(s) },
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: localization, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "activity_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/en-US", nil },
			},
			{
				ResourceName: defaultImage, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "activity_id",
				ImportStateVerifyIgnore: []string{"file", "checksum"},
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return id, nil },
			},
			{
				ResourceName: achievement, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "activity_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/" + achievementID, nil },
			},
			{
				// A link removed outside Terraform is added again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.GameCenterActivityAchievementsV2LinkagesRequest
					d := asc.Push(&body.Data)
					d.Id, d.Type = achievementID, gen.GameCenterActivityAchievementsV2LinkagesRequestDataTypeGameCenterAchievements
					out, err := c.GameCenterActivitiesAchievementsV2DeleteToManyRelationshipWithResponse(ctx, id, body)
					return asc.Err(out, err)
				}),
				Config: full,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(achievement, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(leaderboard, plancheck.ResourceActionNoop),
				}},
				Check: func(s *terraform.State) error { return env.checkActivity(&id, fullState)(s) },
			},
			{
				// The optional attributes clear, and the images and links are
				// removed, in place. The play style and party code are kept.
				Config: locals + activityConfig("", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(localization, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id),
					resource.TestCheckResourceAttr(address, "play_style", "ASYNCHRONOUS"),
					resource.TestCheckResourceAttr(address, "supports_party_code", "true"),
					resource.TestCheckNoResourceAttr(address, "fallback_url"),
					func(s *terraform.State) error {
						return env.checkActivity(&id, "players=0-0 properties=map[] archived=false links=0 url= default=false description= image=false")(s)
					},
				),
			},
			{
				// An activity links only to items of its app or group.
				Config: locals + activityConfig("", "", `
resource "apple_game_center_group" "other" {
  reference_name = "Other"
}

resource "apple_game_center_achievement" "other" {
  game_center_group_id = apple_game_center_group.other.id
  reference_name       = "Other"
  vendor_identifier    = "grp.com.example.other"
  points               = 5
  show_before_earned   = true
  repeatable           = false
}

resource "apple_game_center_activity_achievement" "other" {
  activity_id    = apple_game_center_activity.daily.id
  achievement_id = apple_game_center_achievement.other.id
}
`),
				ExpectError: regexp.MustCompile(`app\s+or\s+group\s+has\s+no\s+gameCenterAchievements`),
			},
		},
	})
}

func TestAccSubmitGameCenterActivity(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	cover, _ := writeImage(t, dir, "cover.png", "cover ")
	const address = "apple_game_center_activity.daily"
	config := func(url string) string {
		return locals + activityConfig(`  fallback_url = "`+url+`"`, "", `
resource "apple_game_center_activity_default_image" "daily" {
  activity_id = apple_game_center_activity.daily.id
  file        = "`+filepath.ToSlash(cover)+`"
}

action "apple_submit_game_center_activity" "daily" {
  config {
    app_id  = local.app_id
    item_id = apple_game_center_activity.daily.id
  }
}
`)
	}
	var id string
	env.run(resource.TestCase{
		TerraformVersionChecks: requiresActions,
		CheckDestroy: func(*terraform.State) error {
			// An activity that has been live is archived, not deleted.
			out, err := env.client.GameCenterActivitiesGetInstanceWithResponse(context.Background(), id, nil)
			if err := asc.Err(out, err); err != nil {
				return err
			}
			if !*out.JSON200.Data.Attributes.Archived {
				return fmt.Errorf("activity %s is not archived", id)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("https://example.com/daily"), Check: captureAttr(address, "id", &id)},
			{
				Config: config("https://example.com/daily") + triggerAction("1", "apple_submit_game_center_activity.daily"),
				Check: func(*terraform.State) error {
					if got := fmt.Sprint(env.fake.GameCenterItemVersions(id)); got != "[WAITING_FOR_REVIEW]" {
						return fmt.Errorf("versions = %s", got)
					}
					return nil
				},
			},
			{
				// The fallback URL belongs to a version, so it can't change
				// during review.
				Config:      config("https://example.com/puzzle"),
				ExpectError: regexp.MustCompile(`is\s+in\s+review`),
			},
			{
				// Once the version is live, a new URL starts the next
				// version, which keeps the default image.
				PreConfig: func() { env.fake.ApproveGameCenterItem(id) },
				Config:    config("https://example.com/puzzle"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "fallback_url", "https://example.com/puzzle"),
					func(s *terraform.State) error {
						if got := fmt.Sprint(env.fake.GameCenterItemVersions(id)); got != "[LIVE PREPARE_FOR_SUBMISSION]" {
							return fmt.Errorf("versions = %s", got)
						}
						return env.checkActivity(&id, "players=0-0 properties=map[] archived=false links=0 url=https://example.com/puzzle default=true description= image=false")(s)
					},
				),
			},
		},
	})
}

func challengeConfig(leaderboard, extra, rest string) string {
	return gameCenterItems + `
resource "apple_game_center_challenge" "duel" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Weekly duel"
  vendor_identifier     = "com.example.weekly_duel"
  challenge_type        = "LEADERBOARD"
  leaderboard_id        = ` + leaderboard + `
` + extra + `
}

resource "apple_game_center_challenge_localization" "en_US" {
  challenge_id = apple_game_center_challenge.duel.id
  locale       = "en-US"
  name         = "Weekly Duel"
  description  = "Beat your friends this week."
}
` + rest
}

func (e *testEnv) challenge(id string) (*gen.GameCenterChallenge, error) {
	out, err := e.client.GameCenterChallengesGetInstanceWithResponse(context.Background(), id, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func TestAccGameCenterChallenge(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	cover, sum := writeImage(t, dir, "cover.png", "cover ")
	const address, localization, defaultImage = "apple_game_center_challenge.duel", "apple_game_center_challenge_localization.en_US", "apple_game_center_challenge_default_image.duel"
	images := `
resource "apple_game_center_challenge_default_image" "duel" {
  challenge_id = apple_game_center_challenge.duel.id
  file         = "` + filepath.ToSlash(cover) + `"
}

resource "apple_game_center_challenge_image" "en_US" {
  challenge_id = apple_game_center_challenge.duel.id
  locale       = apple_game_center_challenge_localization.en_US.locale
  file         = "` + filepath.ToSlash(cover) + `"
}

data "apple_game_center_challenges" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  depends_on            = [apple_game_center_challenge.duel]
}
`
	var id, scoresID, weeklyID string
	leaderboardIs := func(want *string, repeatable bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			challenge, err := env.challenge(id)
			if err != nil {
				return err
			}
			if got := challenge.Relationships.LeaderboardV2.Data.Id; got != *want || asc.Value(challenge.Attributes.Repeatable) != repeatable {
				return fmt.Errorf("leaderboard = %s, repeatable = %v; want %s and %t", got, challenge.Attributes.Repeatable, *want, repeatable)
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if _, err := env.challenge(id); !asc.IsNotFound(err) {
				return fmt.Errorf("challenge %s still exists (err %w)", id, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: locals + challengeConfig("apple_game_center_leaderboard.scores.id", "", images),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					captureAttr("apple_game_center_leaderboard.scores", "id", &scoresID),
					captureAttr("apple_game_center_leaderboard.weekly", "id", &weeklyID),
					resource.TestCheckResourceAttrPtr(defaultImage, "checksum", &sum),
					resource.TestCheckResourceAttrPtr("apple_game_center_challenge_image.en_US", "checksum", &sum),
					resource.TestCheckResourceAttr("data.apple_game_center_challenges.app", "challenges.#", "1"),
					resource.TestCheckResourceAttr("data.apple_game_center_challenges.app", "challenges.0.challenge_type", "LEADERBOARD"),
					func(s *terraform.State) error { return leaderboardIs(&scoresID, false)(s) },
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: localization, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "challenge_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/en-US", nil },
			},
			{
				ResourceName: defaultImage, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "challenge_id",
				ImportStateVerifyIgnore: []string{"file", "checksum"},
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return id, nil },
			},
			{
				// The leaderboard and repeatable change in place.
				Config:           locals + challengeConfig("apple_game_center_leaderboard.weekly.id", "  repeatable = true", images),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id),
					func(s *terraform.State) error { return leaderboardIs(&weeklyID, true)(s) },
				),
			},
			{
				// A challenge is played on a leaderboard of its app or group.
				Config: locals + challengeConfig("apple_game_center_leaderboard.weekly.id", "  repeatable = true", `
resource "apple_game_center_challenge" "other" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Other"
  vendor_identifier     = "com.example.other"
  challenge_type        = "LEADERBOARD"
  leaderboard_id        = "nope"
}
`),
				ExpectError: regexp.MustCompile(`app\s+or\s+group\s+has\s+no\s+gameCenterLeaderboards`),
			},
		},
	})
}
