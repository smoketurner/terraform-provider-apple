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
)

func (e *testEnv) checkFile(id *string, want *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := e.fake.FileChecksum(*id); got != *want {
			return fmt.Errorf("file %s checksum = %q, want %q", *id, got, *want)
		}
		return nil
	}
}

func TestAccReviewAttachment(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	dir := t.TempDir()
	file, sum := writeMedia(t, dir, "demo.mov", "demo ")
	cfg := func(path string) string {
		return locals + reviewDetailConfig(`contact_email = "ann@example.com"`) + `
resource "apple_app_store_review_attachment" "demo" {
  review_detail_id = apple_app_store_review_detail.next.id
  file             = "` + path + `"
}
`
	}
	var first, second string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			// The version was released, so its attachment stays.
			if got := env.fake.FileChecksum(second); got != sum {
				return fmt.Errorf("attachment after destroy = %q, want it kept", got)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      cfg(filepath.Join(dir, "missing.mov")),
				ExpectError: regexp.MustCompile(`Cannot read App Review attachment`),
			},
			{
				Config: cfg(file),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_store_review_attachment.demo", "checksum", sum),
					captureAttr("apple_app_store_review_attachment.demo", "id", &first),
					env.checkFile(&first, &sum),
				),
			},
			{ResourceName: "apple_app_store_review_attachment.demo", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"file", "review_detail_id"}},
			{
				// New contents replace the attachment; the old one is deleted.
				PreConfig:        func() { file, sum = writeMedia(t, dir, "demo.mov", "new demo ") },
				Config:           cfg(file),
				ConfigPlanChecks: expectAction("apple_app_store_review_attachment.demo", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_store_review_attachment.demo", "id", &second),
					env.checkFile(&second, &sum),
					func(*terraform.State) error {
						if env.fake.FileChecksum(first) != "" {
							return fmt.Errorf("the replaced attachment %s still exists", first)
						}
						return nil
					},
				),
			},
			{
				// Moving the file without changing it changes nothing in App
				// Store Connect.
				PreConfig: func() {
					var moved string
					moved, sum = writeMedia(t, dir, "moved.mov", "new demo ")
					file = moved
				},
				Config:           cfg(filepath.Join(dir, "moved.mov")),
				ConfigPlanChecks: expectAction("apple_app_store_review_attachment.demo", plancheck.ResourceActionUpdate),
				Check:            env.checkFile(&second, &sum),
			},
			{
				// An attachment deleted outside Terraform is uploaded again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppStoreReviewAttachmentsDeleteInstanceWithResponse(ctx, second)
					return asc.Err(out, err)
				}),
				Config:           cfg(filepath.Join(dir, "moved.mov")),
				ConfigPlanChecks: expectAction("apple_app_store_review_attachment.demo", plancheck.ResourceActionCreate),
				Check:            captureAttr("apple_app_store_review_attachment.demo", "id", &second),
			},
			{
				PreConfig: func() { env.fake.ReleaseApp(appID) },
				Config:    cfg(filepath.Join(dir, "moved.mov")),
			},
		},
	})
}

func TestAccDeclarationDocument(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	file, sum := writeMedia(t, dir, "ccats.pdf", "ccats ")
	cfg := func() string {
		return locals + encryptionDeclarationConfig + `
resource "apple_app_encryption_declaration_document" "ccats" {
  declaration_id = apple_app_encryption_declaration.app.id
  file           = "` + file + `"
}
`
	}
	var first, second string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg(),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_encryption_declaration_document.ccats", "id", &first),
					env.checkFile(&first, &sum),
				),
			},
			{
				// A new document replaces the old one, which the API can't
				// delete.
				PreConfig:        func() { file, sum = writeMedia(t, dir, "ccats.pdf", "renewed ccats ") },
				Config:           cfg(),
				ConfigPlanChecks: expectAction("apple_app_encryption_declaration_document.ccats", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_encryption_declaration_document.ccats", "id", &second),
					env.checkFile(&second, &sum),
				),
			},
		},
	})
}

func TestAccRoutingAppCoverage(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	dir := t.TempDir()
	file, sum := writeMedia(t, dir, "coverage.geojson", `{"type":"MultiPolygon"} `)
	cfg := func() string {
		return locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_routing_app_coverage" "regions" {
  version_id = apple_app_store_version.next.id
  file       = "` + file + `"
}
`
	}
	var id string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if env.fake.FileChecksum(id) != "" {
				return fmt.Errorf("routing app coverage %s still exists", id)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// The version already has a file, uploaded elsewhere; the
				// resource replaces it.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					_, _, err := routingCoverageAPI.reserve(ctx, c, env.fake.FirstVersion(appID), "old.geojson", 10)
					return err
				}),
				Config: cfg(),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_store_version_routing_app_coverage.regions", "id", &id),
					env.checkFile(&id, &sum),
				),
			},
			{
				// A file deleted outside Terraform is uploaded again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.RoutingAppCoveragesDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           cfg(),
				ConfigPlanChecks: expectAction("apple_app_store_version_routing_app_coverage.regions", plancheck.ResourceActionCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_store_version_routing_app_coverage.regions", "id", &id),
					env.checkFile(&id, &sum),
				),
			},
			{
				PreConfig:        func() { file, sum = writeMedia(t, dir, "coverage.geojson", `{"type":"Polygon"} `) },
				Config:           cfg(),
				ConfigPlanChecks: expectAction("apple_app_store_version_routing_app_coverage.regions", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_store_version_routing_app_coverage.regions", "id", &id),
					env.checkFile(&id, &sum),
				),
			},
		},
	})
}
