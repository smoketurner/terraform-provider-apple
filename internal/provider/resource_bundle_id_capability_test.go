package provider

import (
	"context"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func TestCapabilityEnums(t *testing.T) {
	for _, v := range capabilityTypes {
		if !gen.CapabilityType(v).Valid() {
			t.Errorf("%s is not a CapabilityType", v)
		}
	}
	if !slices.IsSorted(capabilityTypes) || len(capabilityTypes) != 28 {
		t.Errorf("capabilityTypes must list the spec's 28 values, sorted")
	}
}

func capabilityConfig(capType, settings string) string {
	return bundleIDConfig("com.example.app", "App", "") + `
resource "apple_bundle_id_capability" "test" {
  bundle_id       = apple_bundle_id.test.id
  capability_type = "` + capType + `"
` + settings + `
}`
}

func TestAccBundleIDCapability(t *testing.T) {
	env := newTestEnv(t)
	icloud := func(options string) string { return `settings = { ICLOUD_VERSION = [` + options + `] }` }
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      capabilityConfig("TELEPORTATION", ""),
				ExpectError: regexp.MustCompile(`capability_type value must be one of`),
			},
			{
				Config:      capabilityConfig("ICLOUD", `settings = { ICLOUD_VERSION = ["XCODE_99"] }`),
				ExpectError: regexp.MustCompile(`value must be one of`),
			},
			{
				Config: capabilityConfig("ICLOUD", icloud(`"XCODE_6"`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("apple_bundle_id_capability.test", "id"),
					resource.TestCheckTypeSetElemAttr("apple_bundle_id_capability.test", "settings.ICLOUD_VERSION.*", "XCODE_6"),
				),
			},
			{
				ResourceName:      "apple_bundle_id_capability.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["apple_bundle_id.test"].Primary.ID + "/ICLOUD", nil
				},
				ImportStateVerifyIdentifierAttribute: "capability_type",
			},
			{
				// Import with the bundle identifier.
				ResourceName:                         "apple_bundle_id_capability.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "com.example.app/ICLOUD",
				ImportStateVerifyIdentifierAttribute: "capability_type",
			},
			{
				ResourceName:  "apple_bundle_id_capability.test",
				ImportState:   true,
				ImportStateId: "com.example.app",
				ExpectError:   regexp.MustCompile(`Expected an import ID in the form "bundle_id/capability_type"`),
			},
			{
				// Settings change in place.
				Config: capabilityConfig("ICLOUD", icloud(`"XCODE_5", "XCODE_6"`)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id_capability.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr("apple_bundle_id_capability.test", "settings.ICLOUD_VERSION.#", "2"),
			},
			{
				// Settings changed outside Terraform are restored.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					b, err := bundleIDByIdentifier(ctx, c, "com.example.app")
					if err != nil {
						return err
					}
					found, err := findCapability(ctx, c, b.Id, "ICLOUD")
					if err != nil {
						return err
					}
					var body gen.BundleIdCapabilityUpdateRequest
					body.Data.Type, body.Data.Id = gen.BundleIdCapabilityUpdateRequestDataTypeBundleIdCapabilities, found.Id
					asc.Alloc(&body.Data.Attributes).Settings = &[]gen.CapabilitySetting{}
					out, err := c.BundleIdCapabilitiesUpdateInstanceWithResponse(ctx, found.Id, body)
					return asc.Err(out, err)
				}),
				Config: capabilityConfig("ICLOUD", icloud(`"XCODE_5", "XCODE_6"`)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id_capability.test", plancheck.ResourceActionUpdate)},
				},
			},
			{
				// Unmanaged settings are left alone.
				Config: capabilityConfig("ICLOUD", ""),
				Check:  resource.TestCheckNoResourceAttr("apple_bundle_id_capability.test", "settings.%"),
			},
			{
				Config:   capabilityConfig("ICLOUD", ""),
				PlanOnly: true,
			},
			{
				// A capability disabled outside Terraform is enabled again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					b, err := bundleIDByIdentifier(ctx, c, "com.example.app")
					if err != nil {
						return err
					}
					found, err := findCapability(ctx, c, b.Id, "ICLOUD")
					if err != nil {
						return err
					}
					out, err := c.BundleIdCapabilitiesDeleteInstanceWithResponse(ctx, found.Id)
					return asc.Err(out, err)
				}),
				Config: capabilityConfig("ICLOUD", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id_capability.test", plancheck.ResourceActionCreate)},
				},
			},
			{
				Config: capabilityConfig("PUSH_NOTIFICATIONS", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_bundle_id_capability.test", plancheck.ResourceActionDestroyBeforeCreate)},
				},
			},
		},
	})
}
