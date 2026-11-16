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

const achievementOwner = `
resource "apple_game_center_detail" "app" {
  app_id = local.app_id
}
`

func achievementConfig(owner, name, points, extra string) string {
	return achievementOwner + `
resource "apple_game_center_achievement" "first_win" {
` + owner + `
  reference_name     = "` + name + `"
  vendor_identifier  = "com.example.first_win"
  points             = ` + points + `
  show_before_earned = true
  repeatable         = false
` + extra + `
}
`
}

const achievementOfApp = `  game_center_detail_id = apple_game_center_detail.app.id`

func achievementLocalizationConfig(name string) string {
	return `
resource "apple_game_center_achievement_localization" "en_US" {
  achievement_id            = apple_game_center_achievement.first_win.id
  locale                    = "en-US"
  name                      = "` + name + `"
  before_earned_description = "Win a match."
  after_earned_description  = "You won a match."
}
`
}

func achievementImageConfig(file string) string {
	return `
resource "apple_game_center_achievement_image" "en_US" {
  achievement_id = apple_game_center_achievement.first_win.id
  locale         = apple_game_center_achievement_localization.en_US.locale
  file           = "` + filepath.ToSlash(file) + `"
}
`
}

// achievementLocalizations returns the names of an achievement's
// localizations in its latest version by locale, and whether each has an
// image.
func (e *testEnv) achievementLocalizations(achievementID string) (map[string]string, error) {
	ctx := context.Background()
	versions, err := achievementMetadata.list(ctx, e.client, achievementID)
	if err != nil {
		return nil, err
	}
	_, _, latest := pickVersions(versions)
	localizations, err := achievementLocalizationAPI.list(ctx, e.client, latest.id)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, l := range localizations {
		image, err := e.client.GameCenterAchievementLocalizationsV2ImageGetToOneRelatedWithResponse(ctx, l.id, nil)
		err = asc.Err(image, err)
		if err != nil && !asc.IsNotFound(err) {
			return nil, err
		}
		out[l.locale] = fmt.Sprintf("%s image=%t", *l.values["name"], err == nil)
	}
	return out, nil
}

func (e *testEnv) checkAchievement(id *string, wantStates string, wantLocalizations map[string]string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		states := fmt.Sprint(e.fake.GameCenterItemVersions(*id))
		localizations, err := e.achievementLocalizations(*id)
		if err != nil {
			return err
		}
		if states != wantStates || fmt.Sprint(localizations) != fmt.Sprint(wantLocalizations) {
			return fmt.Errorf("versions = %s, localizations = %v; want %s and %v", states, localizations, wantStates, wantLocalizations)
		}
		return nil
	}
}

func (e *testEnv) achievement(id string) (*gen.GameCenterAchievementV2, error) {
	out, err := e.client.GameCenterAchievementsV2GetInstanceWithResponse(context.Background(), id, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func TestAccGameCenterAchievement(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	trophy, sum := writeImage(t, dir, "trophy.png", "trophy ")
	medal, medalSum := writeImage(t, dir, "medal.png", "medal ")
	const address, english, image = "apple_game_center_achievement.first_win", "apple_game_center_achievement_localization.en_US", "apple_game_center_achievement_image.en_US"
	const list = "data.apple_game_center_achievements.app"
	lookups := `
data "apple_game_center_achievements" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  depends_on            = [apple_game_center_achievement.first_win]
}

data "apple_game_center_achievements" "archived" {
  game_center_detail_id = apple_game_center_detail.app.id
  archived              = true
  depends_on            = [apple_game_center_achievement.first_win]
}
`
	var id, imageID string
	const draft = "[PREPARE_FOR_SUBMISSION]"
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if _, err := env.achievement(id); !asc.IsNotFound(err) {
				return fmt.Errorf("achievement %s still exists (err %w)", id, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      locals + achievementConfig("", "First win", "10", ""),
				ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
			},
			{
				// The image needs the locale's localization.
				Config: locals + achievementConfig(achievementOfApp, "First win", "10", "") + `
resource "apple_game_center_achievement_image" "en_US" {
  achievement_id = apple_game_center_achievement.first_win.id
  locale         = "en-US"
  file           = "` + filepath.ToSlash(trophy) + `"
}
`,
				ExpectError: regexp.MustCompile(`has no en-US\s+localization`),
			},
			{
				Config: locals + achievementConfig(achievementOfApp, "First win", "10", "") + achievementLocalizationConfig("First Win") + achievementImageConfig(trophy) + lookups,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					captureAttr(image, "id", &imageID),
					resource.TestCheckResourceAttr(address, "archived", "false"),
					resource.TestCheckResourceAttrPtr(image, "checksum", &sum),
					resource.TestCheckResourceAttr(list, "achievements.#", "1"),
					resource.TestCheckResourceAttr(list, "achievements.0.vendor_identifier", "com.example.first_win"),
					resource.TestCheckResourceAttr(list, "achievements.0.points", "10"),
					resource.TestCheckResourceAttr("data.apple_game_center_achievements.archived", "achievements.#", "0"),
					env.checkFile(&imageID, &sum),
					func(s *terraform.State) error {
						return env.checkAchievement(&id, draft, map[string]string{"en-US": "First Win image=true"})(s)
					},
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: english, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "achievement_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/en-US", nil },
			},
			{
				ResourceName: image, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "achievement_id",
				ImportStateVerifyIgnore: []string{"file", "checksum"},
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return id + "/en-US", nil },
			},
			{
				// An identifier is used once.
				Config: locals + achievementConfig(achievementOfApp, "First win", "10", "") + achievementLocalizationConfig("First Win") + achievementImageConfig(trophy) + `
resource "apple_game_center_achievement" "again" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "Again"
  vendor_identifier     = "com.example.first_win"
  points                = 5
  show_before_earned    = true
  repeatable            = false
  depends_on            = [apple_game_center_achievement.first_win]
}
`,
				ExpectError: regexp.MustCompile(`vendor\s+identifier\s+is\s+already\s+used`),
			},
			{
				// The achievement, its text and its image change in place.
				Config: locals + achievementConfig(achievementOfApp, "First victory", "25", `  activity_properties = { mode = "ranked" }`) +
					achievementLocalizationConfig("First Victory") + achievementImageConfig(medal),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(english, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(image, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id),
					resource.TestCheckResourceAttrPtr(image, "checksum", &medalSum),
					func(s *terraform.State) error {
						return env.checkAchievement(&id, draft, map[string]string{"en-US": "First Victory image=true"})(s)
					},
					func(s *terraform.State) error {
						if got := s.RootModule().Resources[image].Primary.ID; got == imageID {
							return fmt.Errorf("the image %s was not replaced", imageID)
						}
						achievement, err := env.achievement(id)
						if err != nil {
							return err
						}
						a := achievement.Attributes
						if *a.ReferenceName != "First victory" || *a.Points != 25 || (*a.ActivityProperties)["mode"] != "ranked" {
							return fmt.Errorf("unexpected attributes %+v", a)
						}
						return nil
					},
				),
			},
			{
				// The properties clear, the achievement is archived and the
				// image is deleted.
				Config: locals + achievementConfig(achievementOfApp, "First victory", "25", `  archived = true`) + achievementLocalizationConfig("First Victory") + lookups,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(address, "activity_properties.%"),
					resource.TestCheckResourceAttr(list, "achievements.0.archived", "true"),
					resource.TestCheckResourceAttr("data.apple_game_center_achievements.archived", "achievements.#", "1"),
					func(s *terraform.State) error {
						return env.checkAchievement(&id, draft, map[string]string{"en-US": "First Victory image=false"})(s)
					},
					func(*terraform.State) error {
						achievement, err := env.achievement(id)
						if err != nil {
							return err
						}
						if a := achievement.Attributes; !*a.Archived || a.ActivityProperties != nil {
							return fmt.Errorf("unexpected attributes %+v", a)
						}
						return nil
					},
				),
			},
			{
				// A localization deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					versions, err := achievementMetadata.list(ctx, c, id)
					if err != nil {
						return err
					}
					localizations, err := achievementLocalizationAPI.list(ctx, c, versions[0].id)
					if err != nil {
						return err
					}
					return achievementLocalizationAPI.remove(ctx, c, localizations[0].id)
				}),
				Config:           locals + achievementConfig(achievementOfApp, "First victory", "25", `  archived = true`) + achievementLocalizationConfig("First Victory"),
				ConfigPlanChecks: expectAction(english, plancheck.ResourceActionCreate),
			},
		},
	})
}

func TestAccGameCenterGroupAchievement(t *testing.T) {
	env := newTestEnv(t)
	const list = "data.apple_game_center_achievements.group"
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: `
resource "apple_game_center_group" "shared" {
  reference_name = "Shared"
}

resource "apple_game_center_achievement" "first_win" {
  game_center_group_id = apple_game_center_group.shared.id
  reference_name       = "First win"
  vendor_identifier    = "grp.com.example.first_win"
  points               = 10
  show_before_earned   = false
  repeatable           = true
  archived             = true
}

data "apple_game_center_achievements" "group" {
  game_center_group_id = apple_game_center_group.shared.id
  depends_on           = [apple_game_center_achievement.first_win]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("apple_game_center_achievement.first_win", "game_center_group_id", "apple_game_center_group.shared", "id"),
					resource.TestCheckNoResourceAttr("apple_game_center_achievement.first_win", "game_center_detail_id"),
					resource.TestCheckResourceAttr(list, "achievements.#", "1"),
					resource.TestCheckResourceAttr(list, "achievements.0.repeatable", "true"),
					resource.TestCheckResourceAttr(list, "achievements.0.archived", "true"),
				),
			},
			{ResourceName: "apple_game_center_achievement.first_win", ImportState: true, ImportStateVerify: true},
		},
	})
}

func TestAccSubmitGameCenterAchievement(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	trophy, _ := writeImage(t, dir, "trophy.png", "trophy ")
	medal, _ := writeImage(t, dir, "medal.png", "medal ")
	const address, image = "apple_game_center_achievement.first_win", "apple_game_center_achievement_image.en_US"
	config := func(name, file string) string {
		return locals + achievementConfig(achievementOfApp, "First win", "10", "") + achievementLocalizationConfig(name) + achievementImageConfig(file) + `
action "apple_submit_game_center_achievement" "first_win" {
  config {
    app_id  = local.app_id
    item_id = apple_game_center_achievement.first_win.id
  }
}
`
	}
	var id string
	env.run(resource.TestCase{
		TerraformVersionChecks: requiresActions,
		CheckDestroy: func(*terraform.State) error {
			// An achievement that has been live is archived, not deleted.
			achievement, err := env.achievement(id)
			if err != nil {
				return err
			}
			if !*achievement.Attributes.Archived {
				return fmt.Errorf("achievement %s is not archived", id)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("First Win", trophy),
				Check:  captureAttr(address, "id", &id),
			},
			{
				Config: config("First Win", trophy) + triggerAction("1", "apple_submit_game_center_achievement.first_win"),
				Check: func(s *terraform.State) error {
					return env.checkAchievement(&id, "[WAITING_FOR_REVIEW]", map[string]string{"en-US": "First Win image=true"})(s)
				},
			},
			{
				// A version in review can't change, and has no version being
				// prepared to submit.
				Config:      config("First Victory", trophy),
				ExpectError: regexp.MustCompile(`is\s+in\s+review`),
			},
			{
				Config:      config("First Win", trophy) + triggerAction("2", "apple_submit_game_center_achievement.first_win"),
				ExpectError: regexp.MustCompile(`has\s+no\s+version\s+being\s+prepared`),
			},
			{
				// Once the version is live, a change starts the next version,
				// which keeps the image.
				PreConfig: func() { env.fake.ApproveGameCenterItem(id) },
				Config:    config("First Victory", trophy),
				Check: func(s *terraform.State) error {
					return env.checkAchievement(&id, "[LIVE PREPARE_FOR_SUBMISSION]", map[string]string{"en-US": "First Victory image=true"})(s)
				},
			},
			{
				// A changed image goes to the version being prepared.
				Config:           config("First Victory", medal),
				ConfigPlanChecks: expectAction(image, plancheck.ResourceActionUpdate),
				Check: func(s *terraform.State) error {
					return env.checkAchievement(&id, "[LIVE PREPARE_FOR_SUBMISSION]", map[string]string{"en-US": "First Victory image=true"})(s)
				},
			},
			{
				// The next version is submitted and replaces the live one.
				Config: config("First Victory", medal) + triggerAction("3", "apple_submit_game_center_achievement.first_win"),
				Check: func(s *terraform.State) error {
					env.fake.ApproveGameCenterItem(id)
					return env.checkAchievement(&id, "[REPLACED_WITH_NEW_VERSION LIVE]", map[string]string{"en-US": "First Victory image=true"})(s)
				},
			},
		},
	})
}
