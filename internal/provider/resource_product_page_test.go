package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func productPageConfig(page, rest string) string {
	return `
resource "apple_app_custom_product_page" "summer" {
  app_id = local.app_id
` + page + `
}

data "apple_app_custom_product_pages" "all" {
  app_id     = local.app_id
  depends_on = [apple_app_custom_product_page.summer]
}
` + rest
}

func productPageLocalizationConfig(locale, text string) string {
	return `
resource "apple_app_custom_product_page_localization" "` + locale[:2] + `" {
  custom_product_page_id = apple_app_custom_product_page.summer.id
  locale                 = "` + locale + `"
` + text + `
}
`
}

// checkPageVersions checks the states of a page's versions, oldest first.
func (e *testEnv) checkPageVersions(id *string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := e.fake.CustomProductPageVersionStates(*id); !slices.Equal(got, want) {
			return fmt.Errorf("custom product page versions are %v, want %v", got, want)
		}
		return nil
	}
}

func TestAccCustomProductPage(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	keyword := env.fake.AddSearchKeyword(appID, gen.PlatformIOS, "en-US")
	french := env.fake.AddSearchKeyword(appID, gen.PlatformIOS, "fr-FR")
	dir := t.TempDir()
	writeImage(t, dir, "summer.png", "summer ")
	const address = "apple_app_custom_product_page.summer"
	const english = "apple_app_custom_product_page_localization.en"
	page := `name = "Summer"
  deep_link = "https://example.com/summer"`
	text := productPageLocalizationConfig("en-US", `promotional_text = "Play the summer cup."`)
	media := func(keywordID string) string {
		return `
resource "apple_app_asset_library_image" "summer" {
  app_id   = local.app_id
  file     = "` + filepath.Join(dir, "summer.png") + `"
  category = "APP_SCREENSHOTS_AND_PREVIEWS"
}

resource "apple_app_asset_library_placement" "summer" {
  image_id                            = apple_app_asset_library_image.summer.id
  custom_product_page_localization_id = apple_app_custom_product_page_localization.en.id
  placement_type                      = "APP_SCREENSHOT"
  placement_group                     = "` + iphoneGroup + `"
}

resource "apple_app_custom_product_page_localization_search_keyword" "summer" {
  localization_id = apple_app_custom_product_page_localization.en.id
  keyword_id      = "` + keywordID + `"
}
`
	}
	var pageID, localizationID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				// A keyword must be offered for the localization's locale.
				Config:      locals + productPageConfig(page, text+media(french)),
				ExpectError: regexp.MustCompile(`not offered for this app\s+and locale`),
			},
			{
				Config: locals + productPageConfig(page, text+media(keyword)),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &pageID),
					captureAttr(english, "id", &localizationID),
					resource.TestCheckResourceAttr(address, "visible", "true"),
					resource.TestCheckResourceAttr(address, "state", "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttr(address, "deep_link", "https://example.com/summer"),
					resource.TestMatchResourceAttr(address, "url", regexp.MustCompile(`ppid=`)),
					resource.TestCheckResourceAttr("data.apple_app_custom_product_pages.all", "custom_product_pages.#", "1"),
					resource.TestCheckResourceAttrPtr("data.apple_app_custom_product_pages.all", "custom_product_pages.0.id", &pageID),
					env.checkPageVersions(&pageID, "PREPARE_FOR_SUBMISSION"),
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: english, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return pageID + "/en-US", nil },
				ImportStateVerifyIdentifierAttribute: "locale",
			},
			{
				ResourceName: "apple_app_custom_product_page_localization_search_keyword.summer", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return localizationID + "/" + keyword, nil },
				ImportStateVerifyIdentifierAttribute: "keyword_id",
			},
			{
				// The name, visibility, deep link and text of a draft change
				// in place, without a new version.
				Config: locals + productPageConfig(`name = "Summer Cup"
  visible = false`, productPageLocalizationConfig("en-US", "")+media(keyword)),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "name", "Summer Cup"),
					resource.TestCheckResourceAttr(address, "visible", "false"),
					resource.TestCheckNoResourceAttr(address, "deep_link"),
					resource.TestCheckNoResourceAttr(english, "promotional_text"),
					resource.TestCheckResourceAttrPtr(english, "id", &localizationID),
					env.checkPageVersions(&pageID, "PREPARE_FOR_SUBMISSION"),
				),
			},
			{
				// A keyword removed outside Terraform is assigned again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppCustomProductPageLocalizationsSearchKeywordsDeleteToManyRelationshipWithResponse(ctx, localizationID, pageKeywordLinkage(keyword))
					return asc.Err(out, err)
				}),
				Config: locals + productPageConfig(`name = "Summer Cup"
  visible = false`, productPageLocalizationConfig("en-US", "")+media(keyword)),
				ConfigPlanChecks: expectAction("apple_app_custom_product_page_localization_search_keyword.summer", plancheck.ResourceActionCreate),
			},
			{
				// On an approved page, promotional text still changes in place.
				PreConfig: func() { env.fake.ApproveCustomProductPage(pageID) },
				Config:    locals + productPageConfig(`name = "Summer Cup"`, text+media(keyword)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "state", "APPROVED"),
					resource.TestCheckResourceAttr(english, "promotional_text", "Play the summer cup."),
					resource.TestCheckResourceAttrPtr(english, "id", &localizationID),
					env.checkPageVersions(&pageID, "APPROVED"),
				),
			},
			{
				// A new deep link starts the next version, which copies the
				// localizations, so the localization's ID changes.
				Config: locals + productPageConfig(`name = "Summer Cup"
  deep_link = "https://example.com/cup"`, text),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "state", "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttr(address, "deep_link", "https://example.com/cup"),
					env.checkPageVersions(&pageID, "APPROVED", "PREPARE_FOR_SUBMISSION"),
				),
			},
			{
				// A new locale goes to the version being prepared.
				Config: locals + productPageConfig(`name = "Summer Cup"
  deep_link = "https://example.com/cup"`, text+productPageLocalizationConfig("fr-FR", `promotional_text = "Jouez la coupe."`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_custom_product_page_localization.fr", "promotional_text", "Jouez la coupe."),
					env.checkPageVersions(&pageID, "APPROVED", "PREPARE_FOR_SUBMISSION"),
				),
			},
			{
				// A page deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppCustomProductPagesDeleteInstanceWithResponse(ctx, pageID)
					return asc.Err(out, err)
				}),
				Config: locals + productPageConfig(`name = "Summer Cup"`, text),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(english, plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

func TestAccCustomProductPageLimit(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{{
			Config: locals + `
resource "apple_app_custom_product_page" "many" {
  count  = 71
  app_id = local.app_id
  name   = "Page ${count.index}"
}
`,
			ExpectError: regexp.MustCompile(`already has 70 custom product pages`),
		}},
	})
}

func TestAccSubmitCustomProductPageAction(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	actions := `
action "apple_submit_app_custom_product_page" "summer" {
  config {
    app_id                 = local.app_id
    custom_product_page_id = apple_app_custom_product_page.summer.id
  }
}
action "apple_submit_app_custom_product_page" "prepared" {
  config {
    app_id                 = local.app_id
    custom_product_page_id = apple_app_custom_product_page.summer.id
    platform               = "IOS"
    submit                 = false
  }
}
`
	page := `name = "Summer"`
	text := productPageLocalizationConfig("en-US", `promotional_text = "Play the summer cup."`)
	var pageID string
	env.actionCase(
		resource.TestStep{
			// A page without localizations can't be submitted.
			Config:      locals + actions + productPageConfig(page, "") + triggerAction("1", "apple_submit_app_custom_product_page.summer"),
			ExpectError: regexp.MustCompile(`version has no\s+localizations`),
		},
		// The localization is created first: the trigger doesn't depend on it.
		resource.TestStep{Config: locals + actions + productPageConfig(page, text)},
		resource.TestStep{
			Config: locals + actions + productPageConfig(page, text) + triggerAction("2", "apple_submit_app_custom_product_page.prepared"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_custom_product_page.summer", "id", &pageID),
				env.checkPageVersions(&pageID, "READY_FOR_REVIEW"),
			),
		},
		resource.TestStep{
			// A page in review has no version being prepared, and a new
			// locale can't be added to it.
			Config:      locals + actions + productPageConfig(page, text) + triggerAction("3", "apple_submit_app_custom_product_page.summer"),
			ExpectError: regexp.MustCompile(`has\s+no\s+version\s+being\s+prepared`),
		},
		resource.TestStep{
			Config:      locals + actions + productPageConfig(page, text+productPageLocalizationConfig("fr-FR", "")),
			ExpectError: regexp.MustCompile(`custom product page\s+is\s+in\s+review`),
		},
		resource.TestStep{
			// The state App Review sets is read back.
			PreConfig: func() { env.fake.ApproveCustomProductPage(pageID) },
			Config:    locals + actions + productPageConfig(page, text),
			Check:     resource.TestCheckResourceAttr("apple_app_custom_product_page.summer", "state", "APPROVED"),
		},
	)
}

func TestAccSubmitCustomProductPageForReview(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	config := locals + `
action "apple_submit_app_custom_product_page" "summer" {
  config {
    app_id                 = local.app_id
    custom_product_page_id = apple_app_custom_product_page.summer.id
  }
}
` + productPageConfig(`name = "Summer"`, productPageLocalizationConfig("en-US", ""))
	var pageID string
	env.actionCase(
		// The localization is created first: the trigger doesn't depend on it.
		resource.TestStep{Config: config},
		resource.TestStep{
			Config: config + triggerAction("1", "apple_submit_app_custom_product_page.summer"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_custom_product_page.summer", "id", &pageID),
				env.checkPageVersions(&pageID, "WAITING_FOR_REVIEW"),
			),
		},
	)
}
