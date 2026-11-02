package provider

import (
	"context"
	"reflect"
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

func TestAgeRatingFields(t *testing.T) {
	var req gen.AgeRatingDeclarationUpdateRequest
	typ := reflect.TypeOf(req.Data.Attributes).Elem()
	var want []string
	for i := range typ.NumField() {
		want = append(want, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	var got []string
	for _, f := range ageRatingFields {
		got = append(got, f.json)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("ageRatingFields = %v\nwant the attributes of AgeRatingDeclarationUpdateRequest: %v", got, want)
	}
	if got := (ageRatingField{json: "violenceRealisticProlongedGraphicOrSadistic"}).attr(); got != "violence_realistic_prolonged_graphic_or_sadistic" {
		t.Errorf("attr() = %q", got)
	}
}

func appInfoConfig(categories string) string {
	return `
resource "apple_app_info" "app" {
  app_id = local.app_id
` + categories + `
}
`
}

func TestAccAppInfo(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	games := `primary_category = "GAMES"
primary_subcategory_one = "GAMES_PUZZLE"
secondary_category = "PRODUCTIVITY"`
	var firstInfo string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + appInfoConfig(`primary_category = "PRODUCTIVITY"`+"\n"+`primary_subcategory_one = "GAMES_PUZZLE"`),
				ExpectError: regexp.MustCompile(`is not a subcategory of\s+'PRODUCTIVITY'`),
			},
			{
				Config: locals + appInfoConfig(games),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_info.app", "primary_subcategory_one", "GAMES_PUZZLE"),
					resource.TestCheckResourceAttr("apple_app_info.app", "state", "PREPARE_FOR_SUBMISSION"),
					captureAttr("apple_app_info.app", "app_info_id", &firstInfo),
				),
			},
			{ResourceName: "apple_app_info.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id"},
			{
				// Removing the secondary category clears it.
				Config: locals + appInfoConfig(`primary_category = "GAMES"`),
				Check:  resource.TestCheckNoResourceAttr("apple_app_info.app", "secondary_category"),
			},
			{
				// Once released, nothing can be edited until a new version
				// is prepared.
				PreConfig:   func() { env.fake.ReleaseApp(appID) },
				Config:      locals + appInfoConfig(games),
				ExpectError: regexp.MustCompile(`no version of the app is in an editable state`),
			},
			{
				// The new version's app info starts as a copy of the live one.
				PreConfig:        func() { env.fake.PrepareVersion(appID) },
				Config:           locals + appInfoConfig(`primary_category = "GAMES"`),
				ConfigPlanChecks: expectAction("apple_app_info.app", plancheck.ResourceActionNoop),
				Check: func(s *terraform.State) error {
					if s.RootModule().Resources["apple_app_info.app"].Primary.Attributes["app_info_id"] == firstInfo {
						t.Error("the app info of the new version was not read")
					}
					return nil
				},
			},
			{
				Config: locals + appInfoConfig(games),
				Check:  resource.TestCheckResourceAttr("apple_app_info.app", "secondary_category", "PRODUCTIVITY"),
			},
		},
	})
}

func appInfoLocalizationConfig(locale, name, extra string) string {
	return `
resource "apple_app_info_localization" "` + strings.ReplaceAll(locale, "-", "_") + `" {
  app_id = local.app_id
  locale = "` + locale + `"
  name   = "` + name + `"
` + extra + `
}
`
}

func TestAccAppInfoLocalization(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	cfg := func(subtitle string) string {
		return locals + appInfoLocalizationConfig("en-US", "Example", subtitle+"\nprivacy_policy_url = \"https://example.com/privacy\"") +
			appInfoLocalizationConfig("fr-FR", "Muster", "")
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + appInfoLocalizationConfig("en-US", "X", ""),
				ExpectError: regexp.MustCompile(`string length must be between 2 and 30`),
			},
			{
				Config: cfg(`subtitle = "Examples, fast"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_info_localization.en_US", "subtitle", "Examples, fast"),
					resource.TestCheckResourceAttr("apple_app_info_localization.fr_FR", "name", "Muster"),
				),
			},
			{
				ResourceName: "apple_app_info_localization.en_US", ImportState: true, ImportStateVerify: true,
				ImportStateId: appID + "/en-US", ImportStateVerifyIdentifierAttribute: "locale",
			},
			{
				Config:           cfg(""),
				ConfigPlanChecks: expectAction("apple_app_info_localization.en_US", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckNoResourceAttr("apple_app_info_localization.en_US", "subtitle"),
			},
			{
				// A new version copies the localizations, with new IDs.
				PreConfig: func() {
					env.fake.ReleaseApp(appID)
					env.fake.PrepareVersion(appID)
				},
				Config:           cfg(""),
				ConfigPlanChecks: expectAction("apple_app_info_localization.fr_FR", plancheck.ResourceActionNoop),
			},
			{
				// Removing a localization deletes it from the new version.
				Config: locals + appInfoLocalizationConfig("en-US", "Example", `privacy_policy_url = "https://example.com/privacy"`),
				Check: func(*terraform.State) error {
					_, current, err := appInfos(context.Background(), env.client, appID)
					if err != nil {
						return err
					}
					if _, err := findAppInfoLocalization(context.Background(), env.client, current.Id, "fr-FR"); err == nil {
						t.Error("fr-FR was not deleted")
					}
					return nil
				},
			},
		},
	})
}

func TestAccAgeRatingDeclaration(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	cfg := func(answers string) string {
		return locals + `
resource "apple_age_rating_declaration" "app" {
  app_id = local.app_id
` + answers + `
}
`
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      cfg(`violence_cartoon_or_fantasy = "SOMETIMES"`),
				ExpectError: regexp.MustCompile(`violence_cartoon_or_fantasy value must be one of`),
			},
			{
				Config: cfg(`violence_cartoon_or_fantasy = "INFREQUENT_OR_MILD"
gambling = false
kids_age_band = "SIX_TO_EIGHT"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_age_rating_declaration.app", "violence_cartoon_or_fantasy", "INFREQUENT_OR_MILD"),
					resource.TestCheckResourceAttr("apple_age_rating_declaration.app", "kids_age_band", "SIX_TO_EIGHT"),
				),
			},
			{ResourceName: "apple_age_rating_declaration.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id"},
			{
				// An answer changed outside Terraform is restored; answers not
				// in the configuration are left alone.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					editable, _, err := appInfos(ctx, c, appID)
					if err != nil {
						return err
					}
					d, err := c.AppInfosAgeRatingDeclarationGetToOneRelatedWithResponse(ctx, editable.Id, nil)
					if err := asc.Err(d, err); err != nil {
						return err
					}
					var body gen.AgeRatingDeclarationUpdateRequest
					body.Data.Type, body.Data.Id = gen.AgeRatingDeclarationUpdateRequestDataTypeAgeRatingDeclarations, d.JSON200.Data.Id
					a := asc.Alloc(&body.Data.Attributes)
					a.Gambling, a.LootBox = new(true), new(true)
					out, err := c.AgeRatingDeclarationsUpdateInstanceWithResponse(ctx, d.JSON200.Data.Id, body)
					return asc.Err(out, err)
				}),
				Config: cfg(`violence_cartoon_or_fantasy = "INFREQUENT_OR_MILD"
gambling = false
kids_age_band = "SIX_TO_EIGHT"`),
				ConfigPlanChecks: expectAction("apple_age_rating_declaration.app", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_age_rating_declaration.app", "gambling", "false"),
					resource.TestCheckResourceAttr("apple_age_rating_declaration.app", "loot_box", "true"),
				),
			},
		},
	})
}

func TestAccAppSettings(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	cfg := func(extra string) string {
		return locals + `
resource "apple_app_settings" "app" {
  app_id                          = local.app_id
  content_rights_declaration      = "DOES_NOT_USE_THIRD_PARTY_CONTENT"
  subscription_status_url         = "https://example.com/notifications"
  subscription_status_url_version = "V2"
` + extra + `
}
`
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      cfg(`primary_locale = "English"`),
				ExpectError: regexp.MustCompile(`must be a locale`),
			},
			{
				Config: cfg(`primary_locale = "fr-FR"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_settings.app", "primary_locale", "fr-FR"),
					resource.TestCheckResourceAttr("apple_app_settings.app", "bundle_id", "com.example.app"),
					resource.TestCheckResourceAttr("apple_app_settings.app", "subscription_status_url_version", "V2"),
				),
			},
			{ResourceName: "apple_app_settings.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id"},
			{
				Config:           cfg(`primary_locale = "en-US"`),
				ConfigPlanChecks: expectAction("apple_app_settings.app", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("apple_app_settings.app", "primary_locale", "en-US"),
			},
		},
	})
}

func TestAccEndUserLicenseAgreement(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	cfg := func(text, territories string) string {
		return locals + `
resource "apple_end_user_license_agreement" "app" {
  app_id         = local.app_id
  agreement_text = "` + text + `"
  territory_ids  = [` + territories + `]
}
`
	}
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      cfg("Terms.", `"usa"`),
				ExpectError: regexp.MustCompile(`must be a territory ID`),
			},
			{
				Config: cfg("Terms.", `"USA", "CAN"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_end_user_license_agreement.app", "id", &id),
					resource.TestCheckResourceAttr("apple_end_user_license_agreement.app", "territory_ids.#", "2"),
				),
			},
			{ResourceName: "apple_end_user_license_agreement.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id"},
			{
				Config:           cfg("New terms.", `"USA"`),
				ConfigPlanChecks: expectAction("apple_end_user_license_agreement.app", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_end_user_license_agreement.app", "id", &id),
					resource.TestCheckResourceAttr("apple_end_user_license_agreement.app", "territory_ids.#", "1"),
				),
			},
			{
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.EndUserLicenseAgreementsDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           cfg("New terms.", `"USA"`),
				ConfigPlanChecks: expectAction("apple_end_user_license_agreement.app", plancheck.ResourceActionCreate),
			},
		},
	})
}

func TestAccAccessibilityDeclaration(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	cfg := func(extra string) string {
		return locals + `
resource "apple_accessibility_declaration" "iphone" {
  app_id                = local.app_id
  device_family         = "IPHONE"
  supports_voiceover    = true
  supports_larger_text  = true
` + extra + `
}
`
	}
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_accessibility_declaration.iphone", "id", &id),
					resource.TestCheckResourceAttr("apple_accessibility_declaration.iphone", "state", "DRAFT"),
					resource.TestCheckResourceAttr("apple_accessibility_declaration.iphone", "supports_captions", "false"),
				),
			},
			{ResourceName: "apple_accessibility_declaration.iphone", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"app_id"}},
			{
				Config:           cfg("publish = true\nsupports_captions = true"),
				ConfigPlanChecks: expectAction("apple_accessibility_declaration.iphone", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_accessibility_declaration.iphone", "id", &id),
					resource.TestCheckResourceAttr("apple_accessibility_declaration.iphone", "state", "PUBLISHED"),
				),
			},
			{
				// Unpublishing creates a new draft.
				Config:           cfg(""),
				ConfigPlanChecks: expectAction("apple_accessibility_declaration.iphone", plancheck.ResourceActionDestroyBeforeCreate),
				Check:            resource.TestCheckResourceAttr("apple_accessibility_declaration.iphone", "state", "DRAFT"),
			},
		},
	})
}

func TestAccAndroidMappingAndTags(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	env.fake.AddAppTag(appID, "Puzzle")
	cfg := func(fingerprints string, visible bool) string {
		v := "false"
		if visible {
			v = "true"
		}
		return locals + `
resource "apple_android_to_ios_app_mapping" "app" {
  app_id                                  = local.app_id
  package_name                            = "com.example.android"
  signing_certificate_sha256_fingerprints = [` + fingerprints + `]
}
resource "apple_app_tag" "puzzle" {
  app_id               = local.app_id
  name                 = "Puzzle"
  visible_in_app_store = ` + v + `
}
data "apple_app_categories" "all" {}
`
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg(`"AB:CD"`, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_android_to_ios_app_mapping.app", "signing_certificate_sha256_fingerprints.0", "AB:CD"),
					resource.TestCheckResourceAttr("apple_app_tag.puzzle", "visible_in_app_store", "false"),
					resource.TestCheckResourceAttr("data.apple_app_categories.all", "categories.#", "6"),
					resource.TestCheckResourceAttr("data.apple_app_categories.all", "categories.2.parent_id", "GAMES"),
				),
			},
			{ResourceName: "apple_android_to_ios_app_mapping.app", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"app_id"}},
			{ResourceName: "apple_app_tag.puzzle", ImportState: true, ImportStateVerify: true, ImportStateId: appID + "/Puzzle", ImportStateVerifyIdentifierAttribute: "name"},
			{
				Config:           cfg(`"AB:CD", "EF:01"`, true),
				ConfigPlanChecks: expectAction("apple_android_to_ios_app_mapping.app", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("apple_app_tag.puzzle", "visible_in_app_store", "true"),
			},
			{
				Config:      locals + `resource "apple_app_tag" "x" {` + "\n" + `app_id = local.app_id` + "\n" + `name = "Nope"` + "\n" + `visible_in_app_store = true` + "\n}\n",
				ExpectError: regexp.MustCompile(`The app has no tag named Nope`),
			},
		},
	})
}
