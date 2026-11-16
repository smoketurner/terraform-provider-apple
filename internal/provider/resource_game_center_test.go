package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func gameCenterConfig(groupName, detailGroup, enabled string) string {
	return versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version" "tv" {
  app_id         = local.app_id
  platform       = "TV_OS"
  version_string = "1.0.0"
}

resource "apple_game_center_group" "shared" {
  reference_name = "` + groupName + `"
}

resource "apple_game_center_detail" "app" {
  app_id = local.app_id
` + detailGroup + `
}

resource "apple_game_center_app_version" "ios" {
  app_store_version_id = apple_app_store_version.next.id
  enabled              = ` + enabled + `
  depends_on           = [apple_game_center_detail.app]
}

resource "apple_game_center_app_version" "tv" {
  app_store_version_id = apple_app_store_version.tv.id
  enabled              = true
  depends_on           = [apple_game_center_detail.app]
}
`
}

const gameCenterInGroup = `  game_center_group_id = apple_game_center_group.shared.id`

const gameCenterCompatibility = `
resource "apple_game_center_app_version_compatibility" "ios_tv" {
  game_center_app_version_id            = apple_game_center_app_version.ios.id
  compatible_game_center_app_version_id = apple_game_center_app_version.tv.id
}
`

const gameCenterLookups = `
data "apple_game_center_detail" "app" {
  app_id     = local.app_id
  depends_on = [apple_game_center_detail.app]
}

data "apple_game_center_groups" "all" {
  depends_on = [apple_game_center_group.shared]
}

data "apple_game_center_app_versions" "app" {
  game_center_detail_id = apple_game_center_detail.app.id
  depends_on            = [apple_game_center_app_version.ios, apple_game_center_app_version.tv]
}
`

// gameCenter returns the app's Game Center group, and whether Game Center
// is on for each of its Game Center app versions with the versions each is
// compatible with.
func (e *testEnv) gameCenter(appID string) (group string, versions map[string]string, err error) {
	ctx := context.Background()
	detail, err := appGameCenterDetail(ctx, e.client, appID)
	if err != nil {
		return "", nil, err
	}
	if rel := detail.Relationships; rel != nil && rel.GameCenterGroup != nil && rel.GameCenterGroup.Data != nil {
		group = rel.GameCenterGroup.Data.Id
	}
	out, err := e.client.GameCenterDetailsGameCenterAppVersionsGetToManyRelatedWithResponse(ctx, detail.Id, nil)
	if err := asc.Err(out, err); err != nil {
		return "", nil, err
	}
	versions = map[string]string{}
	for _, v := range out.JSON200.Data {
		var compatible []string
		for _, d := range asc.Value(v.Relationships.CompatibilityVersions.Data) {
			compatible = append(compatible, d.Id)
		}
		versions[v.Id] = fmt.Sprint(*v.Attributes.Enabled, compatible)
	}
	return group, versions, nil
}

func TestAccGameCenter(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	const detail, group = "apple_game_center_detail.app", "apple_game_center_group.shared"
	const ios, tv, compatibility = "apple_game_center_app_version.ios", "apple_game_center_app_version.tv", "apple_game_center_app_version_compatibility.ios_tv"
	var detailID, groupID, iosID, tvID, versionID string
	check := func(wantGroup *string, wantIOS, wantTV string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			gotGroup, versions, err := env.gameCenter(appID)
			if err != nil {
				return err
			}
			if gotGroup != *wantGroup || versions[iosID] != wantIOS || versions[tvID] != wantTV {
				return fmt.Errorf("group = %q, versions = %v; want %q, %s and %s", gotGroup, versions, *wantGroup, wantIOS, wantTV)
			}
			return nil
		}
	}
	none := ""
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				// A version needs Game Center on for its app.
				Config: locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_game_center_app_version" "ios" {
  app_store_version_id = apple_app_store_version.next.id
  enabled              = true
}
`,
				ExpectError: regexp.MustCompile(`Game\s+Center\s+is\s+not\s+enabled\s+for\s+the\s+app`),
			},
			{
				Config: locals + gameCenterConfig("Shared", gameCenterInGroup, "false") + gameCenterCompatibility + gameCenterLookups,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(detail, "id", &detailID),
					captureAttr(group, "id", &groupID),
					captureAttr(ios, "id", &iosID),
					captureAttr(tv, "id", &tvID),
					captureAttr("apple_app_store_version.next", "id", &versionID),
					resource.TestCheckResourceAttr(detail, "arcade_enabled", "false"),
					resource.TestCheckResourceAttrPair("data.apple_game_center_detail.app", "id", detail, "id"),
					resource.TestCheckResourceAttrPair("data.apple_game_center_detail.app", "game_center_group_id", group, "id"),
					resource.TestCheckResourceAttr("data.apple_game_center_groups.all", "groups.#", "1"),
					resource.TestCheckResourceAttr("data.apple_game_center_groups.all", "groups.0.reference_name", "Shared"),
					resource.TestCheckResourceAttr("data.apple_game_center_app_versions.app", "app_versions.#", "2"),
					func(s *terraform.State) error { return check(&groupID, "false ["+tvID+"]", "true ["+iosID+"]")(s) },
				),
			},
			{
				ResourceName: detail, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID, nil },
			},
			{ResourceName: group, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: ios, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return versionID, nil },
			},
			{
				ResourceName: compatibility, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "game_center_app_version_id",
				ImportStateIdFunc: func(*terraform.State) (string, error) { return iosID + "/" + tvID, nil },
			},
			{
				// Compatibility removed outside Terraform is added again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.GameCenterAppVersionCompatibilityVersionsLinkagesRequest
					d := asc.Push(&body.Data)
					d.Id, d.Type = iosID, gen.GameCenterAppVersionCompatibilityVersionsLinkagesRequestDataTypeGameCenterAppVersions
					out, err := c.GameCenterAppVersionsCompatibilityVersionsDeleteToManyRelationshipWithResponse(ctx, tvID, body)
					return asc.Err(out, err)
				}),
				Config:           locals + gameCenterConfig("Shared", gameCenterInGroup, "false") + gameCenterCompatibility,
				ConfigPlanChecks: expectAction(compatibility, plancheck.ResourceActionCreate),
				Check:            func(s *terraform.State) error { return check(&groupID, "false ["+tvID+"]", "true ["+iosID+"]")(s) },
			},
			{
				// The group is renamed, the app leaves it, Game Center is
				// turned on for the version and the versions stop being
				// compatible, all in place.
				Config: locals + gameCenterConfig("Renamed", "", "true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(detail, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(group, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(ios, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(compatibility, plancheck.ResourceActionDestroy),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(detail, "id", &detailID),
					resource.TestCheckResourceAttr(group, "reference_name", "Renamed"),
					resource.TestCheckNoResourceAttr(detail, "game_center_group_id"),
					func(s *terraform.State) error { return check(&none, "true []", "true []")(s) },
				),
			},
			{
				// A change made outside Terraform is set back.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					r := gameCenterAppVersionResource{client: c}
					_, err := r.setEnabled(ctx, iosID, false)
					return err
				}),
				Config:           locals + gameCenterConfig("Renamed", gameCenterInGroup, "true"),
				ConfigPlanChecks: expectAction(ios, plancheck.ResourceActionUpdate),
				Check:            func(s *terraform.State) error { return check(&groupID, "true []", "true []")(s) },
			},
			{
				// Destroying the detail takes the app out of the group, so
				// that the group can be deleted; Game Center stays on. The
				// tvOS app version goes with its App Store version.
				Config: locals + versionConfig("IOS", "1.0.0", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error { return check(&none, "true []", "")(s) },
					func(*terraform.State) error {
						out, err := env.client.GameCenterGroupsGetInstanceWithResponse(context.Background(), groupID, nil)
						if err := asc.Err(out, err); !asc.IsNotFound(err) {
							return fmt.Errorf("group %s still exists (err %w)", groupID, err)
						}
						return nil
					},
				),
			},
			{
				// The detail and the app version that are left are taken
				// over instead of created again.
				Config: locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_game_center_detail" "app" {
  app_id = local.app_id
}

resource "apple_game_center_app_version" "ios" {
  app_store_version_id = apple_app_store_version.next.id
  enabled              = false
  depends_on           = [apple_game_center_detail.app]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(detail, "id", &detailID),
					resource.TestCheckResourceAttrPtr(ios, "id", &iosID),
					func(s *terraform.State) error { return check(&none, "false []", "")(s) },
				),
			},
			{
				Config:      `data "apple_game_center_detail" "none" { app_id = "nope" }`,
				ExpectError: regexp.MustCompile(`No Game Center detail matches`),
			},
		},
	})
}
