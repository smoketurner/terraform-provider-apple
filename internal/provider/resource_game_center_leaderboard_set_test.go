package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func leaderboardSetConfig(name, localized, memberName, rest string) string {
	return leaderboardConfig("INTEGER", "", "") + `
resource "apple_game_center_leaderboard_set" "seasons" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "` + name + `"
  vendor_identifier     = "com.example.seasons"
}

resource "apple_game_center_leaderboard_set_localization" "en_US" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  locale             = "en-US"
  name               = "` + localized + `"
}

resource "apple_game_center_leaderboard_set_member" "scores" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  leaderboard_id     = apple_game_center_leaderboard.scores.id
}

resource "apple_game_center_leaderboard_set_member_localization" "en_US" {
  leaderboard_set_id = apple_game_center_leaderboard_set_member.scores.leaderboard_set_id
  leaderboard_id     = apple_game_center_leaderboard_set_member.scores.leaderboard_id
  locale             = "en-US"
  name               = "` + memberName + `"
}

resource "apple_game_center_leaderboard_set_member_localization" "fr_FR" {
  leaderboard_set_id = apple_game_center_leaderboard_set_member.scores.leaderboard_set_id
  leaderboard_id     = apple_game_center_leaderboard_set_member.scores.leaderboard_id
  locale             = "fr-FR"
  name               = "Cette saison"
}
` + rest
}

func leaderboardSetImageConfig(file string) string {
	return `
resource "apple_game_center_leaderboard_set_image" "en_US" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  locale             = apple_game_center_leaderboard_set_localization.en_US.locale
  file               = "` + filepath.ToSlash(file) + `"
}
`
}

// leaderboardSet returns the set's reference name, the names of its members
// in the set by leaderboard ID, and the en-US name of its latest version.
func (e *testEnv) leaderboardSet(id string) (string, error) {
	ctx := context.Background()
	set, err := e.client.GameCenterLeaderboardSetsV2GetInstanceWithResponse(ctx, id, nil)
	if err := asc.Err(set, err); err != nil {
		return "", err
	}
	members, err := e.client.GameCenterLeaderboardSetsV2GameCenterLeaderboardsGetToManyRelatedWithResponse(ctx, id, nil)
	if err := asc.Err(members, err); err != nil {
		return "", err
	}
	names := map[string]string{}
	for _, member := range members.JSON200.Data {
		out, err := e.client.GameCenterLeaderboardSetMemberLocalizationsGetCollectionWithResponse(ctx, &gen.GameCenterLeaderboardSetMemberLocalizationsGetCollectionParams{
			FilterGameCenterLeaderboardSet: []string{id}, FilterGameCenterLeaderboard: []string{member.Id},
		})
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		var localized []string
		for _, l := range out.JSON200.Data {
			localized = append(localized, *l.Attributes.Locale+"="+*l.Attributes.Name)
		}
		slices.Sort(localized)
		names[member.Id] = strings.Join(localized, ",")
	}
	versions, err := setMetadata.list(ctx, e.client, id)
	if err != nil {
		return "", err
	}
	_, _, latest := pickVersions(versions)
	localizations, err := setLocalizationAPI.list(ctx, e.client, latest.id)
	if err != nil {
		return "", err
	}
	var localized []string
	for _, l := range localizations {
		localized = append(localized, l.locale+"="+*l.values["name"])
	}
	return fmt.Sprintf("%s members=%v localized=%v", *set.JSON200.Data.Attributes.ReferenceName, names, localized), nil
}

func TestAccGameCenterLeaderboardSet(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	banner, sum := writeImage(t, dir, "banner.png", "banner ")
	const set, english, image = "apple_game_center_leaderboard_set.seasons", "apple_game_center_leaderboard_set_localization.en_US", "apple_game_center_leaderboard_set_image.en_US"
	const member, memberName = "apple_game_center_leaderboard_set_member.scores", "apple_game_center_leaderboard_set_member_localization.en_US"
	const list = "data.apple_game_center_leaderboard_sets.app"
	lookups := `
data "apple_game_center_leaderboard_sets" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  depends_on            = [apple_game_center_leaderboard_set.seasons]
}
`
	var id, leaderboardID string
	check := func(want func() string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got, err := env.leaderboardSet(id)
			if err != nil {
				return err
			}
			if got != want() {
				return fmt.Errorf("set = %s, want %s", got, want())
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.GameCenterLeaderboardSetsV2GetInstanceWithResponse(context.Background(), id, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("leaderboard set %s still exists (err %w)", id, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: locals + leaderboardSetConfig("Seasons", "Seasons", "This Season", leaderboardSetImageConfig(banner)+lookups),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(set, "id", &id),
					captureAttr("apple_game_center_leaderboard.scores", "id", &leaderboardID),
					resource.TestCheckResourceAttrPtr(image, "checksum", &sum),
					resource.TestCheckResourceAttr(list, "leaderboard_sets.#", "1"),
					resource.TestCheckResourceAttr(list, "leaderboard_sets.0.vendor_identifier", "com.example.seasons"),
					check(func() string {
						return "Seasons members=map[" + leaderboardID + ":en-US=This Season,fr-FR=Cette saison] localized=[en-US=Seasons]"
					}),
				),
			},
			{ResourceName: set, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: english, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "leaderboard_set_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/en-US", nil },
			},
			{
				ResourceName: member, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "leaderboard_set_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/" + leaderboardID, nil },
			},
			{
				ResourceName: memberName, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return id + "/" + leaderboardID + "/en-US", nil },
			},
			{
				// The names change in place.
				Config: locals + leaderboardSetConfig("All seasons", "All Seasons", "Current Season", leaderboardSetImageConfig(banner)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(set, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(english, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(memberName, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(member, plancheck.ResourceActionNoop),
				}},
				Check: check(func() string {
					return "All seasons members=map[" + leaderboardID + ":en-US=Current Season,fr-FR=Cette saison] localized=[en-US=All Seasons]"
				}),
			},
			{
				// A leaderboard has one name per locale in a set.
				Config: locals + leaderboardSetConfig("All seasons", "All Seasons", "Current Season", `
resource "apple_game_center_leaderboard_set_member_localization" "again" {
  leaderboard_set_id = apple_game_center_leaderboard_set_member.scores.leaderboard_set_id
  leaderboard_id     = apple_game_center_leaderboard_set_member.scores.leaderboard_id
  locale             = "en-US"
  name               = "Again"
  depends_on         = [apple_game_center_leaderboard_set_member_localization.en_US]
}
`),
				ExpectError: regexp.MustCompile(`already\s+has\s+a\s+name\s+for\s+the\s+locale`),
			},
			{
				// A member and a name removed outside Terraform are added
				// again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					names, err := c.GameCenterLeaderboardSetMemberLocalizationsGetCollectionWithResponse(ctx, &gen.GameCenterLeaderboardSetMemberLocalizationsGetCollectionParams{
						FilterGameCenterLeaderboardSet: []string{id}, FilterGameCenterLeaderboard: []string{leaderboardID},
					})
					if err := asc.Err(names, err); err != nil {
						return err
					}
					for _, name := range names.JSON200.Data {
						deleted, err := c.GameCenterLeaderboardSetMemberLocalizationsDeleteInstanceWithResponse(ctx, name.Id)
						if err := asc.Err(deleted, err); err != nil {
							return err
						}
					}
					m := setMemberModel{LeaderboardID: stringValue(&leaderboardID)}
					out, err := c.GameCenterLeaderboardSetsV2GameCenterLeaderboardsDeleteToManyRelationshipWithResponse(ctx, id, m.linkage())
					return asc.Err(out, err)
				}),
				Config: locals + leaderboardSetConfig("All seasons", "All Seasons", "Current Season", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(member, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(memberName, plancheck.ResourceActionCreate),
				}},
				Check: check(func() string {
					return "All seasons members=map[" + leaderboardID + ":en-US=Current Season,fr-FR=Cette saison] localized=[en-US=All Seasons]"
				}),
			},
			{
				// Removing the member resources takes the leaderboard out of
				// the set.
				Config: locals + leaderboardConfig("INTEGER", "", "") + `
resource "apple_game_center_leaderboard_set" "seasons" {
  game_center_detail_id = apple_game_center_detail.app.id
  reference_name        = "All seasons"
  vendor_identifier     = "com.example.seasons"
}
`,
				Check: check(func() string { return "All seasons members=map[] localized=[]" }),
			},
		},
	})
}

func TestAccSubmitGameCenterLeaderboardSet(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	const address = "apple_game_center_leaderboard_set.seasons"
	config := locals + leaderboardSetConfig("Seasons", "Seasons", "This Season", `
action "apple_submit_game_center_leaderboard_set" "seasons" {
  config {
    app_id  = local.app_id
    item_id = apple_game_center_leaderboard_set.seasons.id
    submit  = false
  }
}
`)
	var id string
	env.run(resource.TestCase{
		TerraformVersionChecks: requiresActions,
		CheckDestroy: func(*terraform.State) error {
			// A set that has been live can't be deleted, and stays.
			out, err := env.client.GameCenterLeaderboardSetsV2GetInstanceWithResponse(context.Background(), id, nil)
			return asc.Err(out, err)
		},
		Steps: []resource.TestStep{
			{Config: config, Check: captureAttr(address, "id", &id)},
			{
				// With submit = false the version waits in the submission
				// for other items.
				Config: config + triggerAction("1", "apple_submit_game_center_leaderboard_set.seasons"),
				Check: func(*terraform.State) error {
					if got := fmt.Sprint(env.fake.GameCenterItemVersions(id)); got != "[READY_FOR_REVIEW]" {
						return fmt.Errorf("versions = %s", got)
					}
					env.fake.ApproveGameCenterItem(id)
					return nil
				},
			},
		},
	})
}
