package provider

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func bundleIDConfig(identifier, name, extra string) string {
	return `
resource "apple_bundle_id" "test" {
  identifier = "` + identifier + `"
  name       = "` + name + `"
  platform   = "IOS"
` + extra + `
}`
}

func TestAccBundleID(t *testing.T) {
	env := newTestEnv(t)
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: `
resource "apple_bundle_id" "test" {
  identifier = "com.example.app"
  name       = "App"
  platform   = "ANDROID"
}`,
				ExpectError: regexp.MustCompile(`Attribute platform value must be one of`),
			},
			{
				Config: bundleIDConfig("com.example.app", "App", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_bundle_id.test", "id", &id),
					resource.TestCheckResourceAttr("apple_bundle_id.test", "seed_id", asctest.SeedID),
					resource.TestCheckResourceAttr("apple_bundle_id.test", "platform", "IOS"),
				),
			},
			{
				ResourceName:      "apple_bundle_id.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Import by bundle identifier.
				ResourceName:      "apple_bundle_id.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: attrIDFunc("apple_bundle_id.test", "identifier"),
			},
			{
				ResourceName:  "apple_bundle_id.test",
				ImportState:   true,
				ImportStateId: "com.example.missing",
				ExpectError:   regexp.MustCompile(`No resource has the ID or bundle identifier "com.example.missing"`),
			},
			{
				// The name changes in place.
				Config: bundleIDConfig("com.example.app", "Renamed", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_bundle_id.test", "id", &id),
					resource.TestCheckResourceAttr("apple_bundle_id.test", "name", "Renamed"),
				),
			},
			{
				// A rename outside Terraform is reverted.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.BundleIdUpdateRequest
					body.Data.Type, body.Data.Id = gen.BundleIdUpdateRequestDataTypeBundleIds, id
					asc.Alloc(&body.Data.Attributes).Name = new("Changed")
					out, err := c.BundleIdsUpdateInstanceWithResponse(ctx, id, body)
					return asc.Err(out, err)
				}),
				Config: bundleIDConfig("com.example.app", "Renamed", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id.test", plancheck.ResourceActionUpdate)},
				},
			},
			{
				// The identifier and platform force replacement.
				Config: bundleIDConfig("com.example.other", "Renamed", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id.test", plancheck.ResourceActionDestroyBeforeCreate)},
				},
			},
			{
				// A bundle ID deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					b, err := bundleIDByIdentifier(ctx, c, "com.example.other")
					if err != nil {
						return err
					}
					out, err := c.BundleIdsDeleteInstanceWithResponse(ctx, b.Id)
					return asc.Err(out, err)
				}),
				Config: bundleIDConfig("com.example.other", "Renamed", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id.test", plancheck.ResourceActionCreate)},
				},
			},
		},
	})
}

func TestAccBundleIDSeedAndConflict(t *testing.T) {
	env := newTestEnv(t)
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: bundleIDConfig("com.example.app", "App", `seed_id = "ZZZZZ99999"`),
				Check:  resource.TestCheckResourceAttr("apple_bundle_id.test", "seed_id", "ZZZZZ99999"),
			},
			{
				Config: bundleIDConfig("com.example.app", "App", `seed_id = "ZZZZZ99999"`) + `
resource "apple_bundle_id" "dup" {
  identifier = "com.example.app"
  name       = "Duplicate"
  platform   = "IOS"
}`,
				ExpectError: regexp.MustCompile(`(?s)Unable to create bundle ID: POST /v1/bundleIds: HTTP 409.*is not\s+available`),
			},
		},
	})
}
