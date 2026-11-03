package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func nominationConfig(extra string) string {
	return `
resource "apple_nomination" "launch" {
  name               = "Example 2.0"
  type               = "APP_ENHANCEMENTS"
  description        = "A redesigned editor."
  app_ids            = [local.app_id]
  publish_start_date = "2099-03-01T16:00:00Z"
` + extra + `
}

data "apple_nominations" "drafts" {
  states     = ["DRAFT"]
  app_ids    = [local.app_id]
  depends_on = [apple_nomination.launch]
}
`
}

func TestAccNomination(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	full := `publish_end_date = "2099-04-01T16:00:00Z"
device_families = ["IPHONE", "IPAD"]
locales = ["en-US"]
territory_ids = ["USA", "CAN"]
supplemental_materials_uris = ["https://example.com/press"]
notes = "Embargoed until launch."`
	var draftID string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.NominationsGetInstanceWithResponse(context.Background(), draftID, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("nomination %s still exists: %w", draftID, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      locals + nominationConfig(`device_families = ["PHONE"]`),
				ExpectError: regexp.MustCompile(`value must be one of`),
			},
			{
				Config:      locals + nominationConfig(`publish_end_date = "2099-02-01T16:00:00Z"`),
				ExpectError: regexp.MustCompile(`publishEndDate must be after publishStartDate`),
			},
			{
				Config: locals + nominationConfig(full),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_nomination.launch", "state", "DRAFT"),
					resource.TestCheckResourceAttr("apple_nomination.launch", "submitted", "false"),
					resource.TestCheckResourceAttr("apple_nomination.launch", "territory_ids.#", "2"),
					resource.TestCheckResourceAttr("data.apple_nominations.drafts", "nominations.#", "1"),
					captureAttr("apple_nomination.launch", "id", &draftID),
				),
			},
			{ResourceName: "apple_nomination.launch", ImportState: true, ImportStateVerify: true},
			{
				// Removing optional values clears them.
				Config:           locals + nominationConfig(""),
				ConfigPlanChecks: expectAction("apple_nomination.launch", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_nomination.launch", "publish_end_date"),
					resource.TestCheckNoResourceAttr("apple_nomination.launch", "notes"),
					resource.TestCheckNoResourceAttr("apple_nomination.launch", "territory_ids"),
					resource.TestCheckNoResourceAttr("apple_nomination.launch", "device_families"),
				),
			},
			{
				Config: locals + nominationConfig("submitted = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_nomination.launch", "state", "SUBMITTED"),
					resource.TestCheckResourceAttrSet("apple_nomination.launch", "submitted_date"),
					resource.TestCheckResourceAttr("data.apple_nominations.drafts", "nominations.#", "0"),
				),
			},
			{
				// A submitted nomination can't return to draft, so it is
				// replaced.
				Config:           locals + nominationConfig("submitted = false"),
				ConfigPlanChecks: expectAction("apple_nomination.launch", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_nomination.launch", "state", "DRAFT"),
					captureAttr("apple_nomination.launch", "id", &draftID),
				),
			},
		},
	})
}

func searchKeywordConfig(keyword string) string {
	return versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id = apple_app_store_version.next.id
  locale     = "en-US"
}

data "apple_app_search_keywords" "en_US" {
  app_id   = local.app_id
  platform = "IOS"
  locale   = "en-US"
}

resource "apple_app_store_version_localization_search_keyword" "k" {
  localization_id = apple_app_store_version_localization.en_US.id
  keyword_id      = "` + keyword + `"
}
`
}

func TestAccSearchKeyword(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	english := env.fake.AddSearchKeyword(appID, gen.PlatformIOS, "en-US")
	other := env.fake.AddSearchKeyword(appID, gen.PlatformIOS, "en-US")
	french := env.fake.AddSearchKeyword(appID, gen.PlatformIOS, "fr-FR")
	var localizationID string
	linked := func(want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got, err := localizationKeywords(context.Background(), env.client, localizationID)
			if err != nil {
				return err
			}
			if !slices.Equal(got, want) {
				return fmt.Errorf("keywords = %v, want %v", got, want)
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + searchKeywordConfig(french),
				ExpectError: regexp.MustCompile(`is not offered for this app,\s+platform and\s+locale`),
			},
			{
				Config: locals + searchKeywordConfig(english),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_app_search_keywords.en_US", "ids.#", "2"),
					resource.TestCheckTypeSetElemAttr("data.apple_app_search_keywords.en_US", "ids.*", other),
					captureAttr("apple_app_store_version_localization.en_US", "id", &localizationID),
					linked(english),
				),
			},
			{
				ResourceName: "apple_app_store_version_localization_search_keyword.k", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return localizationID + "/" + english, nil },
				ImportStateVerifyIdentifierAttribute: "keyword_id",
			},
			{
				// A keyword removed outside Terraform is added again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					m := searchKeywordModel{KeywordID: types.StringValue(english)}
					out, err := c.AppStoreVersionLocalizationsSearchKeywordsDeleteToManyRelationshipWithResponse(ctx, localizationID, keywordLinkage(&m))
					return asc.Err(out, err)
				}),
				Config:           locals + searchKeywordConfig(english),
				ConfigPlanChecks: expectAction("apple_app_store_version_localization_search_keyword.k", plancheck.ResourceActionCreate),
				Check:            linked(english),
			},
			{
				// Changing the keyword replaces the link.
				Config: locals + searchKeywordConfig(other),
				Check:  linked(other),
			},
			{
				Config: locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id = apple_app_store_version.next.id
  locale     = "en-US"
}
`,
				Check: linked(),
			},
		},
	})
}
