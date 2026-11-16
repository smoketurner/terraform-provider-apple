package provider

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

func appClipConfig(experience, subtitle, urls, rest string) string {
	return versionConfig("IOS", "1.0.0", "") + `
data "apple_app_clips" "all" {
  app_id = local.app_id
}

resource "apple_app_clip_default_experience" "card" {
  app_clip_id                       = data.apple_app_clips.all.app_clips[0].id
  release_with_app_store_version_id = apple_app_store_version.next.id
` + experience + `
}

resource "apple_app_clip_default_experience_localization" "en_US" {
  default_experience_id = apple_app_clip_default_experience.card.id
  locale                = "en-US"
` + subtitle + `
}

resource "apple_app_clip_app_store_review_detail" "card" {
  default_experience_id = apple_app_clip_default_experience.card.id
  invocation_urls       = [` + urls + `]
}
` + rest
}

func TestAccAppClipDefaultExperience(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	clipID := env.fake.AddAppClip(appID, "com.example.app.Clip")
	dir := t.TempDir()
	card, sum := writeImage(t, dir, "card.png", "card ")
	other, otherSum := writeImage(t, dir, "other.png", "other ")
	image := func(file string) string {
		return `
resource "apple_app_clip_header_image" "en_US" {
  localization_id = apple_app_clip_default_experience_localization.en_US.id
  file            = "` + filepath.ToSlash(file) + `"
}
`
	}
	const experience = "apple_app_clip_default_experience.card"
	const english = "apple_app_clip_default_experience_localization.en_US"
	const detail = "apple_app_clip_app_store_review_detail.card"
	const header = "apple_app_clip_header_image.en_US"
	full := appClipConfig(`action = "OPEN"`, `subtitle = "Order ahead and skip the line."`, `"https://example.com/order"`, image(card))
	var experienceID, localizationID, versionID, imageID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + full,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(experience, "id", &experienceID),
					captureAttr(english, "id", &localizationID),
					captureAttr("apple_app_store_version.next", "id", &versionID),
					captureAttr(header, "id", &imageID),
					resource.TestCheckResourceAttr("data.apple_app_clips.all", "app_clips.#", "1"),
					resource.TestCheckResourceAttr("data.apple_app_clips.all", "app_clips.0.id", clipID),
					resource.TestCheckResourceAttr("data.apple_app_clips.all", "app_clips.0.bundle_id", "com.example.app.Clip"),
					resource.TestCheckResourceAttr(experience, "action", "OPEN"),
					resource.TestCheckResourceAttr(detail, "invocation_urls.#", "1"),
					resource.TestCheckResourceAttrPtr(header, "checksum", &sum),
					env.checkFile(&imageID, &sum),
				),
			},
			{ResourceName: experience, ImportState: true, ImportStateVerify: true},
			{ResourceName: english, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: english, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return experienceID + "/en-US", nil },
			},
			{
				ResourceName: english, ImportState: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return experienceID + "/fr-FR", nil },
				ExpectError:       regexp.MustCompile(`No resource has the ID or default experience ID and locale`),
			},
			{ResourceName: detail, ImportState: true, ImportStateVerify: true},
			{
				// The action, subtitle and URLs change in place; removed
				// values clear; a changed image replaces the header image.
				Config: locals + appClipConfig("", "", `"https://example.com/order", "https://example.com/menu"`, image(other)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(experience, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(english, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(detail, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(header, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(experience, "action"),
					resource.TestCheckNoResourceAttr(english, "subtitle"),
					resource.TestCheckResourceAttr(detail, "invocation_urls.1", "https://example.com/menu"),
					resource.TestCheckResourceAttrPtr(header, "checksum", &otherSum),
					resource.TestCheckResourceAttrPtr(experience, "id", &experienceID),
				),
			},
			{
				// An experience has one localization per locale.
				Config: locals + appClipConfig("", "", `"https://example.com/order"`, `
resource "apple_app_clip_default_experience_localization" "again" {
  default_experience_id = apple_app_clip_default_experience.card.id
  locale                = "en-US"
}
`),
				ExpectError: regexp.MustCompile(`already has a\s+localization\s+for\s+en-US`),
			},
			{
				// A localization deleted outside Terraform is created again,
				// with its header image.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppClipDefaultExperienceLocalizationsDeleteInstanceWithResponse(ctx, localizationID)
					return asc.Err(out, err)
				}),
				Config: locals + full,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(english, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(header, plancheck.ResourceActionCreate),
				}},
			},
			{
				// Once the version is released, the experience can't change.
				// Destroying it then leaves it in App Store Connect.
				PreConfig:   func() { env.fake.ApproveVersion(versionID) },
				Config:      locals + appClipConfig(`action = "PLAY"`, `subtitle = "Order ahead and skip the line."`, `"https://example.com/order"`, image(card)),
				ExpectError: regexp.MustCompile(`released App Clip`),
			},
		},
	})
}

func TestAccAppClipDefaultExperienceVersion(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	env.fake.AddAppClip(appID, "com.example.app.Clip")
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				// A version has one default experience.
				Config: locals + appClipConfig("", "", `"https://example.com/order"`, `
resource "apple_app_clip_default_experience" "second" {
  app_clip_id                       = data.apple_app_clips.all.app_clips[0].id
  release_with_app_store_version_id = apple_app_store_version.next.id
  depends_on                        = [apple_app_clip_default_experience.card]
}
`),
				ExpectError: regexp.MustCompile(`already has a default App Clip\s+experience`),
			},
			{
				// An experience without a version is deleted on destroy.
				Config: locals + `
data "apple_app_clips" "all" {
  app_id = local.app_id
}

resource "apple_app_clip_default_experience" "draft" {
  app_clip_id = data.apple_app_clips.all.app_clips[0].id
}
`,
				Check: resource.TestCheckNoResourceAttr("apple_app_clip_default_experience.draft", "release_with_app_store_version_id"),
			},
		},
	})
}
