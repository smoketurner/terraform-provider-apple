package provider

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func advancedExperienceConfig(file, body string) string {
	return `
data "apple_app_clips" "all" {
  app_id = local.app_id
}

resource "apple_app_clip_advanced_experience" "cafe" {
  app_clip_id       = data.apple_app_clips.all.app_clips[0].id
  link              = "https://example.com/order/cafe"
  is_powered_by     = false
  header_image_file = "` + filepath.ToSlash(file) + `"
` + body + `
}
`
}

const advancedEnglish = `
  default_language = "EN"
  localizations = [
    { language = "EN", title = "Example Cafe", subtitle = "Order ahead." },
  ]
`

const advancedPlace = `
  place = {
    names          = ["Example Cafe"]
    map_action     = "RESTAURANT_ORDER_FOOD"
    relationship   = "OWNER"
    home_page      = "https://example.com"
    phone_number   = "+14085550100"
    phone_type     = "LANDLINE"
    street_address = ["1 Example Way"]
    locality       = "Cupertino"
    country_code   = "US"
    latitude       = 37.3349
    longitude      = -122.009
  }
`

func (e *testEnv) advancedExperience(id string) (*gen.AppClipAdvancedExperience, error) {
	out, err := e.client.AppClipAdvancedExperiencesGetInstanceWithResponse(context.Background(), id, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (e *testEnv) checkAdvancedStatus(id *string, want gen.AppClipAdvancedExperienceAttributesStatus) resource.TestCheckFunc {
	return func(*terraform.State) error {
		experience, err := e.advancedExperience(*id)
		if err != nil {
			return err
		}
		if got := asc.Value(experience.Attributes.Status); got != want {
			return fmt.Errorf("experience %s is %s, want %s", *id, got, want)
		}
		return nil
	}
}

// patchAdvanced changes an experience outside Terraform.
func patchAdvanced(id *string, attributes string) func(ctx context.Context, c *asc.Client) error {
	return func(ctx context.Context, c *asc.Client) error {
		body := `{"data":{"type":"appClipAdvancedExperiences","id":"` + *id + `","attributes":` + attributes + `}}`
		out, err := c.AppClipAdvancedExperiencesUpdateInstanceWithBodyWithResponse(ctx, *id, "application/json", bytes.NewReader([]byte(body)))
		return asc.Err(out, err)
	}
}

func TestAccAppClipAdvancedExperience(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	env.fake.AddAppClip(appID, "com.example.app.Clip")
	dir := t.TempDir()
	card, sum := writeImage(t, dir, "card.png", "card ")
	other, otherSum := writeImage(t, dir, "other.png", "other ")
	const address = "apple_app_clip_advanced_experience.cafe"
	full := advancedExperienceConfig(card, advancedEnglish+advancedPlace+`
  action            = "OPEN"
  business_category = "FOOD_AND_DRINK"
`)
	var id, imageID, secondImageID, recreated string
	env.run(resource.TestCase{
		CheckDestroy: env.checkAdvancedStatus(&recreated, gen.AppClipAdvancedExperienceAttributesStatusDEACTIVATED),
		Steps: []resource.TestStep{
			{
				Config:      locals + advancedExperienceConfig(filepath.Join(dir, "missing.png"), advancedEnglish),
				ExpectError: regexp.MustCompile(`Cannot read App Clip header image`),
			},
			{
				// The default language needs a localization.
				Config: locals + advancedExperienceConfig(card, `
  default_language = "FR"
  localizations = [
    { language = "EN", title = "Example Cafe" },
  ]
`),
				ExpectError: regexp.MustCompile(`needs\s+a\s+localization\s+in\s+its\s+default\s+language`),
			},
			{
				Config: locals + full,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					captureAttr(address, "header_image_id", &imageID),
					resource.TestCheckResourceAttr(address, "status", "RECEIVED"),
					resource.TestCheckResourceAttr(address, "place_status", "PENDING"),
					resource.TestCheckResourceAttr(address, "action", "OPEN"),
					resource.TestCheckResourceAttr(address, "business_category", "FOOD_AND_DRINK"),
					resource.TestCheckResourceAttr(address, "localizations.#", "1"),
					resource.TestCheckResourceAttr(address, "place.latitude", "37.3349"),
					resource.TestCheckResourceAttrPtr(address, "header_image_checksum", &sum),
					env.checkFile(&imageID, &sum),
					func(*terraform.State) error {
						experience, err := env.advancedExperience(id)
						if err != nil {
							return err
						}
						place := experience.Attributes.Place
						if place == nil || asc.Value(place.MainAddress.StructuredAddress.Locality) != "Cupertino" ||
							asc.Value(place.PhoneNumber.Number) != "+14085550100" || asc.Value(place.DisplayPoint.Coordinates.Longitude) != -122.009 {
							return fmt.Errorf("unexpected place %+v", place)
						}
						return nil
					},
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"header_image_file"}},
			{
				// What Apple Maps adds to the place while matching it is not
				// a change.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					return patchAdvanced(&id, `{"place":{"placeId":"MAPS1","names":["Example Cafe"],"mapAction":"RESTAURANT_ORDER_FOOD",`+
						`"relationship":"OWNER","homePage":"https://example.com","phoneNumber":{"number":"+14085550100","type":"LANDLINE"},`+
						`"mainAddress":{"fullAddress":"1 Example Way, Cupertino","structuredAddress":{"streetAddress":["1 Example Way"],`+
						`"locality":"Cupertino","countryCode":"US"}},"displayPoint":{"coordinates":{"latitude":37.3349,"longitude":-122.009},`+
						`"source":"CALCULATED"}}}`)(ctx, c)
				}),
				Config:           locals + full,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// A managed place attribute changed outside Terraform is set
				// back.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					return patchAdvanced(&id, `{"place":{"names":["Example Cafe"],"homePage":"https://other.example.com"}}`)(ctx, c)
				}),
				Config:           locals + full,
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(address, "place.home_page", "https://example.com"),
			},
			{
				// The action, category and place clear; the image and the
				// localizations are replaced, all in place.
				Config: locals + advancedExperienceConfig(other, `
  default_language = "FR"
  localizations = [
    { language = "EN", title = "Example Cafe" },
    { language = "FR", title = "Le Café", subtitle = "Commandez à l'avance." },
  ]
`),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id),
					captureAttr(address, "header_image_id", &secondImageID),
					resource.TestCheckNoResourceAttr(address, "action"),
					resource.TestCheckNoResourceAttr(address, "business_category"),
					resource.TestCheckNoResourceAttr(address, "place.%"),
					resource.TestCheckNoResourceAttr(address, "place_status"),
					resource.TestCheckResourceAttr(address, "default_language", "FR"),
					resource.TestCheckResourceAttr(address, "localizations.#", "2"),
					resource.TestCheckResourceAttrPtr(address, "header_image_checksum", &otherSum),
					func(*terraform.State) error {
						if secondImageID == imageID {
							return fmt.Errorf("the header image %s was not replaced", imageID)
						}
						experience, err := env.advancedExperience(id)
						if err != nil {
							return err
						}
						if a := experience.Attributes; a.Place != nil || a.Action != nil || a.BusinessCategory != nil {
							return fmt.Errorf("unexpected attributes %+v", a)
						}
						if got := experience.Relationships.HeaderImage.Data.Id; got != secondImageID {
							return fmt.Errorf("header image = %s, want %s", got, secondImageID)
						}
						return nil
					},
				),
			},
			{
				// Only the image file's path changes: nothing is uploaded.
				PreConfig: func() {
					if _, sum := writeImage(t, dir, "same.png", "other "); sum != otherSum {
						t.Fatal("the copy has another checksum")
					}
				},
				Config: locals + advancedExperienceConfig(filepath.Join(dir, "same.png"), `
  default_language = "FR"
  localizations = [
    { language = "EN", title = "Example Cafe" },
    { language = "FR", title = "Le Café", subtitle = "Commandez à l'avance." },
  ]
`),
				Check: resource.TestCheckResourceAttrPtr(address, "header_image_id", &secondImageID),
			},
			{
				// An experience removed outside Terraform is deactivated for
				// good, so a new one is created.
				PreConfig:        env.outside(func(ctx context.Context, c *asc.Client) error { return patchAdvanced(&id, `{"removed":true}`)(ctx, c) }),
				Config:           locals + full,
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &recreated),
					env.checkAdvancedStatus(&id, gen.AppClipAdvancedExperienceAttributesStatusDEACTIVATED),
					env.checkAdvancedStatus(&recreated, gen.AppClipAdvancedExperienceAttributesStatusRECEIVED),
				),
			},
		},
	})
}

func invocationConfig(bundle, url, localizations, rest string) string {
	return `
data "apple_build_bundles" "all" {
  build_id = local.build_id
}

locals {
  bundles = { for b in data.apple_build_bundles.all.build_bundles : b.bundle_type => b.id }
}

resource "apple_beta_app_clip_invocation" "order" {
  build_bundle_id = local.bundles["` + bundle + `"]
  url             = "` + url + `"
  localizations = [
` + localizations + `
  ]
}
` + rest
}

func (e *testEnv) checkInvocation(id *string, url string, titles map[string]string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		out, err := e.client.BetaAppClipInvocationsGetInstanceWithResponse(context.Background(), *id, &gen.BetaAppClipInvocationsGetInstanceParams{
			Include: &[]gen.BetaAppClipInvocationsGetInstanceParamsInclude{gen.BetaAppClipInvocationsGetInstanceParamsIncludeBetaAppClipInvocationLocalizations},
		})
		if err := asc.Err(out, err); err != nil {
			return err
		}
		got := map[string]string{}
		for _, l := range asc.Value(out.JSON200.Included) {
			got[*l.Attributes.Locale] = *l.Attributes.Title
		}
		if gotURL := *out.JSON200.Data.Attributes.Url; gotURL != url || fmt.Sprint(got) != fmt.Sprint(titles) {
			return fmt.Errorf("invocation = %s %v, want %s %v", gotURL, got, url, titles)
		}
		return nil
	}
}

func TestAccBetaAppClipInvocation(t *testing.T) {
	env := newTestEnv(t)
	appID, _ := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "1", Marketing: "1.0", UsesNonExemptEncryption: new(false)})
	env.fake.AddBuildBundle(buildID, "com.example.app", gen.BuildBundleTypeAPP)
	clipBundle := env.fake.AddBuildBundle(buildID, "com.example.app.Clip", gen.BuildBundleTypeAPPCLIP, "example.com", "order.example.com")
	locals := fmt.Sprintf("locals {\n  build_id = %q\n}\n", buildID)
	const address = "apple_beta_app_clip_invocation.order"
	const english = `    { locale = "en-US", title = "Order" },`
	const status = `
data "apple_app_clip_domain_status" "clip" {
  build_bundle_id = local.bundles["APP_CLIP"]
}
`
	var id, recreated string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.BetaAppClipInvocationsGetInstanceWithResponse(context.Background(), recreated, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("invocation %s still exists (err %w)", recreated, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// Only an App Clip's bundle takes invocations.
				Config:      locals + invocationConfig("APP", "https://example.com/order", english, ""),
				ExpectError: regexp.MustCompile(`is\s+not\s+an\s+App\s+Clip`),
			},
			{
				Config: locals + invocationConfig("APP_CLIP", "https://example.com/order", english, status),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					resource.TestCheckResourceAttr("data.apple_build_bundles.all", "build_bundles.#", "2"),
					resource.TestCheckResourceAttr("data.apple_build_bundles.all", "build_bundles.1.bundle_id", "com.example.app.Clip"),
					resource.TestCheckResourceAttr(address, "build_bundle_id", clipBundle),
					resource.TestCheckResourceAttr("data.apple_app_clip_domain_status.clip", "cache_domains.#", "2"),
					resource.TestCheckResourceAttr("data.apple_app_clip_domain_status.clip", "cache_domains.0.domain", "example.com"),
					resource.TestCheckResourceAttr("data.apple_app_clip_domain_status.clip", "cache_domains.0.is_valid", "true"),
					resource.TestCheckNoResourceAttr("data.apple_app_clip_domain_status.clip", "cache_domains.0.error_code"),
					resource.TestCheckResourceAttr("data.apple_app_clip_domain_status.clip", "debug_domains.1.domain", "order.example.com"),
					resource.TestCheckResourceAttrSet("data.apple_app_clip_domain_status.clip", "debug_last_updated_date"),
					env.checkInvocation(&id, "https://example.com/order", map[string]string{"en-US": "Order"}),
				),
			},
			{
				ResourceName: address, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return clipBundle + "/" + id, nil },
			},
			{
				// The URL and a title change, and a locale is added, in place.
				Config: locals + invocationConfig("APP_CLIP", "https://example.com/menu", `
    { locale = "en-US", title = "Menu" },
    { locale = "fr-FR", title = "Carte" },`, ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id),
					env.checkInvocation(&id, "https://example.com/menu", map[string]string{"en-US": "Menu", "fr-FR": "Carte"}),
				),
			},
			{
				// A removed locale is deleted.
				Config: locals + invocationConfig("APP_CLIP", "https://example.com/menu", `    { locale = "fr-FR", title = "Carte" },`, ""),
				Check:  env.checkInvocation(&id, "https://example.com/menu", map[string]string{"fr-FR": "Carte"}),
			},
			{
				// An invocation deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.BetaAppClipInvocationsDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           locals + invocationConfig("APP_CLIP", "https://example.com/menu", `    { locale = "fr-FR", title = "Carte" },`, ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
				Check:            captureAttr(address, "id", &recreated),
			},
			{
				// Only an App Clip's bundle has a domain status.
				Config: locals + `
data "apple_build_bundles" "all" {
  build_id = local.build_id
}

data "apple_app_clip_domain_status" "app" {
  build_bundle_id = data.apple_build_bundles.all.build_bundles[0].id
}
`,
				ExpectError: regexp.MustCompile(`is\s+not\s+an\s+App\s+Clip`),
			},
			{
				Config: locals + `
data "apple_build_bundles" "missing" {
  build_id = "nope"
}
`,
				ExpectError: regexp.MustCompile(`No build matches`),
			},
		},
	})
}
