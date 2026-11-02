package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func profileConfig(t *testing.T, csr, extraCert, profileExtra string) string {
	t.Helper()
	return bundleIDConfig("com.example.app", "App", "") +
		certificateConfig("dev", "DEVELOPMENT", csr, "") + extraCert +
		deviceConfig("Phone", "IOS", "") + `
resource "apple_profile" "test" {
  name            = "Example Dev"
  profile_type    = "IOS_APP_DEVELOPMENT"
  bundle_id       = apple_bundle_id.test.id
  certificate_ids = [apple_certificate.dev.id]
  device_ids      = [apple_device.test.id]
` + profileExtra + `
}`
}

func expectProfile(action plancheck.ResourceActionType) tfresource.ConfigPlanChecks {
	return tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_profile.test", action)}}
}

func TestAccProfile(t *testing.T) {
	env := newTestEnv(t)
	csr := testCSR(t, "Dev")
	cfg := profileConfig(t, csr, "", "")
	var id, certID, deviceID string
	replaced := func(s *terraform.State) error {
		if s.RootModule().Resources["apple_profile.test"].Primary.ID == id {
			return fmt.Errorf("profile %s was not replaced", id)
		}
		return nil
	}
	env.run(tfresource.TestCase{
		Steps: []tfresource.TestStep{
			{
				Config: cfg,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_profile.test", "id", &id),
					captureAttr("apple_certificate.dev", "id", &certID),
					captureAttr("apple_device.test", "id", &deviceID),
					tfresource.TestCheckResourceAttr("apple_profile.test", "profile_state", "ACTIVE"),
					tfresource.TestCheckResourceAttr("apple_profile.test", "platform", "IOS"),
					tfresource.TestCheckResourceAttrSet("apple_profile.test", "profile_content"),
					tfresource.TestCheckResourceAttrSet("apple_profile.test", "uuid"),
					tfresource.TestCheckResourceAttr("apple_profile.test", "device_ids.#", "1"),
				),
			},
			{
				ResourceName:      "apple_profile.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "apple_profile.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     "Example Dev",
			},
			{Config: cfg, PlanOnly: true},
			{
				// Disabling a device outside Terraform invalidates the
				// profile, which is replaced. The device is enabled again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.DeviceUpdateRequest
					body.Data.Type, body.Data.Id = gen.DeviceUpdateRequestDataTypeDevices, deviceID
					asc.Alloc(&body.Data.Attributes).Status = new(gen.DeviceUpdateRequestDataAttributesStatusDISABLED)
					out, err := c.DevicesUpdateInstanceWithResponse(ctx, deviceID, body)
					return asc.Err(out, err)
				}),
				Config:           cfg,
				ConfigPlanChecks: expectProfile(plancheck.ResourceActionDestroyBeforeCreate),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					replaced,
					captureAttr("apple_profile.test", "id", &id),
					tfresource.TestCheckResourceAttr("apple_profile.test", "profile_state", "ACTIVE"),
				),
			},
			{
				// Changing the bundle ID's capabilities invalidates it too,
				// so the plan after the apply replaces it.
				Config: cfg + `
resource "apple_bundle_id_capability" "push" {
  bundle_id       = apple_bundle_id.test.id
  capability_type = "PUSH_NOTIFICATIONS"
}`,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg + `
resource "apple_bundle_id_capability" "push" {
  bundle_id       = apple_bundle_id.test.id
  capability_type = "PUSH_NOTIFICATIONS"
}`,
				ConfigPlanChecks: expectProfile(plancheck.ResourceActionDestroyBeforeCreate),
				Check:            tfresource.ComposeAggregateTestCheckFunc(replaced, captureAttr("apple_profile.test", "id", &id)),
			},
			{
				// Revoking a certificate outside Terraform: the certificate
				// is created again, and the profile replaced with it.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.CertificatesDeleteInstanceWithResponse(ctx, certID)
					return asc.Err(out, err)
				}),
				Config:           cfg,
				ConfigPlanChecks: expectProfile(plancheck.ResourceActionDestroyBeforeCreate),
				Check:            tfresource.ComposeAggregateTestCheckFunc(replaced, captureAttr("apple_profile.test", "id", &id)),
			},
			{
				// A profile deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.ProfilesDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           cfg,
				ConfigPlanChecks: expectProfile(plancheck.ResourceActionCreate),
			},
			{
				// Every argument forces replacement.
				Config:           replaceName(profileConfig(t, csr, "", ""), "Renamed Dev"),
				ConfigPlanChecks: expectProfile(plancheck.ResourceActionDestroyBeforeCreate),
				Check:            tfresource.TestCheckResourceAttr("apple_profile.test", "name", "Renamed Dev"),
			},
		},
	})
}

func TestAccProfileValidation(t *testing.T) {
	env := newTestEnv(t)
	env.run(tfresource.TestCase{
		Steps: []tfresource.TestStep{
			{
				Config: bundleIDConfig("com.example.app", "App", "") + `
resource "apple_profile" "test" {
  name            = "x"
  profile_type    = "ANDROID_APP"
  bundle_id       = apple_bundle_id.test.id
  certificate_ids = []
}`,
				ExpectError: regexp.MustCompile(`(?s)profile_type value must be one of.*certificate_ids set must contain at least 1`),
			},
			{
				Config: bundleIDConfig("com.example.app", "App", "") + `
resource "apple_profile" "test" {
  name            = "x"
  profile_type    = "IOS_APP_STORE"
  bundle_id       = apple_bundle_id.test.id
  certificate_ids = ["MISSING"]
}`,
				ExpectError: regexp.MustCompile(`There is no certificates with id 'MISSING'`),
			},
		},
	})
}

// TestProfileModifyPlanExpired checks that a profile past its expiration
// date is replaced.
func TestProfileModifyPlanExpired(t *testing.T) {
	ctx := context.Background()
	r := &profileResource{now: func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	typ := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object) //nolint:forcetypeassert // A schema's type is always an object.
	values := map[string]tftypes.Value{}
	for name, attrType := range typ.AttributeTypes {
		values[name] = tftypes.NewValue(attrType, nil)
	}
	for exp, wantReplace := range map[string]bool{"2029-12-31T00:00:00Z": true, "2031-01-01T00:00:00Z": false} {
		values["profile_state"] = tftypes.NewValue(tftypes.String, "ACTIVE")
		values["expiration_date"] = tftypes.NewValue(tftypes.String, exp)
		raw := tftypes.NewValue(typ, values)
		req := resource.ModifyPlanRequest{
			State: tfsdk.State{Schema: schemaResp.Schema, Raw: raw},
			Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: raw},
		}
		resp := resource.ModifyPlanResponse{Plan: req.Plan}
		r.ModifyPlan(ctx, req, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if got := len(resp.RequiresReplace) > 0; got != wantReplace {
			t.Errorf("expiration %s: replace = %v, want %v", exp, got, wantReplace)
		}
	}
}

func replaceName(cfg, name string) string {
	return strings.Replace(cfg, `name            = "Example Dev"`, `name            = "`+name+`"`, 1)
}
