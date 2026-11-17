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

func leaderboardConfig(formatter, extra, localization string) string {
	return achievementOwner + `
resource "apple_game_center_leaderboard" "scores" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "High scores"
  vendor_identifier     = "com.example.high_scores"
  default_formatter     = "` + formatter + `"
  submission_type       = "BEST_SCORE"
  score_sort_type       = "DESC"
` + extra + `
}

resource "apple_game_center_leaderboard_localization" "en_US" {
  leaderboard_id = apple_game_center_leaderboard.scores.id
  locale         = "en-US"
  name           = "High Scores"
` + localization + `
}
`
}

func leaderboardImageConfig(file string) string {
	return `
resource "apple_game_center_leaderboard_image" "en_US" {
  leaderboard_id = apple_game_center_leaderboard.scores.id
  locale         = apple_game_center_leaderboard_localization.en_US.locale
  file           = "` + filepath.ToSlash(file) + `"
}
`
}

const leaderboardRecurring = `
  score_range_start     = "0"
  score_range_end       = "1000000"
  recurrence_start_date = "2026-12-01T16:00:00Z"
  recurrence_duration   = "P1D"
  recurrence_rule       = "FREQ=WEEKLY;INTERVAL=1"
  visibility            = "SHOW_FOR_ALL"
  activity_properties   = { mode = "ranked" }
`

const leaderboardFormatting = `
  description               = "The best scores of the week."
  formatter_override        = "DECIMAL_POINT_1_PLACE"
  formatter_suffix          = " points"
  formatter_suffix_singular = " point"
`

func (e *testEnv) leaderboard(id string) (*gen.GameCenterLeaderboardV2, error) {
	out, err := e.client.GameCenterLeaderboardsV2GetInstanceWithResponse(context.Background(), id, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// leaderboardLocalization returns the en-US localization of the
// leaderboard's latest version, and whether it has an image.
func (e *testEnv) leaderboardLocalization(leaderboardID string) (*localization, bool, error) {
	ctx := context.Background()
	versions, err := leaderboardMetadata.list(ctx, e.client, leaderboardID)
	if err != nil {
		return nil, false, err
	}
	_, _, latest := pickVersions(versions)
	localizations, err := leaderboardLocalizationAPI.list(ctx, e.client, latest.id)
	if err != nil || len(localizations) != 1 {
		return nil, false, fmt.Errorf("localizations = %v (err %w)", localizations, err)
	}
	image, err := e.client.GameCenterLeaderboardLocalizationsV2ImageGetToOneRelatedWithResponse(ctx, localizations[0].id, nil)
	err = asc.Err(image, err)
	if err != nil && !asc.IsNotFound(err) {
		return nil, false, err
	}
	return &localizations[0], err == nil, nil
}

func TestAccGameCenterLeaderboard(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	podium, sum := writeImage(t, dir, "podium.png", "podium ")
	const address, english, image = "apple_game_center_leaderboard.scores", "apple_game_center_leaderboard_localization.en_US", "apple_game_center_leaderboard_image.en_US"
	const list = "data.apple_game_center_leaderboards.app"
	lookups := `
data "apple_game_center_leaderboards" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  archived              = false
  depends_on            = [apple_game_center_leaderboard.scores]
}
`
	var id string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if _, err := env.leaderboard(id); !asc.IsNotFound(err) {
				return fmt.Errorf("leaderboard %s still exists (err %w)", id, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: locals + leaderboardConfig("INTEGER", leaderboardRecurring, leaderboardFormatting) + leaderboardImageConfig(podium) + lookups,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					resource.TestCheckResourceAttr(address, "archived", "false"),
					resource.TestCheckResourceAttrPtr(image, "checksum", &sum),
					resource.TestCheckResourceAttr(list, "leaderboards.#", "1"),
					resource.TestCheckResourceAttr(list, "leaderboards.0.recurrence_rule", "FREQ=WEEKLY;INTERVAL=1"),
					resource.TestCheckResourceAttr(list, "leaderboards.0.default_formatter", "INTEGER"),
					func(*terraform.State) error {
						leaderboard, err := env.leaderboard(id)
						if err != nil {
							return err
						}
						a := leaderboard.Attributes
						if *a.ScoreRangeEnd != "1000000" || a.RecurrenceDuration.String() != "P1D" || *a.RecurrenceRule != "FREQ=WEEKLY;INTERVAL=1" ||
							a.RecurrenceStartDate.UTC().Format("2006-01-02T15:04") != "2026-12-01T16:00" || (*a.ActivityProperties)["mode"] != "ranked" {
							return fmt.Errorf("unexpected attributes %+v", a)
						}
						l, hasImage, err := env.leaderboardLocalization(id)
						if err != nil {
							return err
						}
						if !hasImage || *l.values["formatterOverride"] != "DECIMAL_POINT_1_PLACE" || *l.values["formatterSuffixSingular"] != " point" {
							return fmt.Errorf("unexpected localization %+v, image %t", l.values, hasImage)
						}
						return nil
					},
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: english, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "leaderboard_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/en-US", nil },
			},
			{
				ResourceName: image, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "leaderboard_id",
				ImportStateVerifyIgnore: []string{"file", "checksum"},
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return id + "/en-US", nil },
			},
			{
				// The formatter changes and the optional attributes clear,
				// which makes the leaderboard a classic one, in place. The
				// visibility is kept.
				Config: locals + leaderboardConfig("ELAPSED_TIME_SECOND", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(english, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(image, plancheck.ResourceActionDestroy),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id),
					resource.TestCheckResourceAttr(address, "visibility", "SHOW_FOR_ALL"),
					resource.TestCheckNoResourceAttr(address, "recurrence_rule"),
					resource.TestCheckNoResourceAttr(english, "formatter_override"),
					func(*terraform.State) error {
						leaderboard, err := env.leaderboard(id)
						if err != nil {
							return err
						}
						a := leaderboard.Attributes
						if *a.DefaultFormatter != "ELAPSED_TIME_SECOND" || a.ScoreRangeStart != nil || a.RecurrenceStartDate != nil || a.RecurrenceDuration != nil ||
							a.RecurrenceRule != nil || a.ActivityProperties != nil {
							return fmt.Errorf("unexpected attributes %+v", a)
						}
						l, hasImage, err := env.leaderboardLocalization(id)
						if err != nil {
							return err
						}
						if hasImage || l.values["formatterOverride"] != nil || l.values["description"] != nil || l.values["formatterSuffix"] != nil {
							return fmt.Errorf("unexpected localization %+v, image %t", l.values, hasImage)
						}
						return nil
					},
				),
			},
			{
				// A leaderboard can be created only for a detail or a group.
				Config: locals + achievementOwner + `
resource "apple_game_center_leaderboard" "both" {
  game_center_detail_id = apple_game_center_detail.app.id
  game_center_group_id  = "group"
  reference_name        = "Both"
  vendor_identifier     = "com.example.both"
  default_formatter     = "INTEGER"
  submission_type       = "BEST_SCORE"
  score_sort_type       = "ASC"
}
`,
				ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
			},
		},
	})
}

func TestAccSubmitGameCenterLeaderboard(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	const address = "apple_game_center_leaderboard.scores"
	config := func(localization string) string {
		return locals + leaderboardConfig("INTEGER", "", localization) + `
action "apple_submit_game_center_leaderboard" "scores" {
  config {
    app_id  = local.app_id
    item_id = apple_game_center_leaderboard.scores.id
  }
}
`
	}
	states := func(id *string, want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := fmt.Sprint(env.fake.GameCenterItemVersions(*id)); got != want {
				return fmt.Errorf("versions = %s, want %s", got, want)
			}
			return nil
		}
	}
	var id string
	env.run(resource.TestCase{
		TerraformVersionChecks: requiresActions,
		CheckDestroy: func(*terraform.State) error {
			// A leaderboard that has been live is archived, not deleted.
			leaderboard, err := env.leaderboard(id)
			if err != nil {
				return err
			}
			if !*leaderboard.Attributes.Archived {
				return fmt.Errorf("leaderboard %s is not archived", id)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(""), Check: captureAttr(address, "id", &id)},
			{
				Config: config("") + triggerAction("1", "apple_submit_game_center_leaderboard.scores"),
				Check:  func(s *terraform.State) error { return states(&id, "[WAITING_FOR_REVIEW]")(s) },
			},
			{
				// Once the version is live, a change starts the next one.
				PreConfig: func() { env.fake.ApproveGameCenterItem(id) },
				Config:    config(`  description = "The best scores."`),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error { return states(&id, "[LIVE PREPARE_FOR_SUBMISSION]")(s) },
					func(*terraform.State) error {
						l, _, err := env.leaderboardLocalization(id)
						if err != nil {
							return err
						}
						if *l.values["description"] != "The best scores." {
							return fmt.Errorf("unexpected localization %+v", l.values)
						}
						return nil
					},
				),
			},
		},
	})
}
