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

const testUDID = "00008030-001A2B3C4D5E6F70"

func deviceConfig(name, platform, extra string) string {
	return fmt.Sprintf(`
resource "apple_device" "test" {
  name     = %q
  udid     = %q
  platform = %q
%s
}
`, name, testUDID, platform, extra)
}

// checkDeviceStatus checks the device's status at App Store Connect.
func (e *testEnv) checkDeviceStatus(want gen.DeviceAttributesStatus) resource.TestCheckFunc {
	return func(*terraform.State) error {
		d, err := deviceByUDID(context.Background(), e.client, testUDID)
		if err != nil {
			return err
		}
		if got := *d.Attributes.Status; got != want {
			return fmt.Errorf("device status = %s, want %s", got, want)
		}
		return nil
	}
}

func TestAccDevice(t *testing.T) {
	env := newTestEnv(t)
	var id string
	env.run(resource.TestCase{
		CheckDestroy: env.checkDeviceStatus(gen.DeviceAttributesStatusDISABLED),
		Steps: []resource.TestStep{
			{
				Config: deviceConfig("Phone", "IOS", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_device.test", "id", &id),
					resource.TestCheckResourceAttr("apple_device.test", "status", "ENABLED"),
					resource.TestCheckResourceAttr("apple_device.test", "device_class", "IPHONE"),
					resource.TestCheckResourceAttrSet("apple_device.test", "added_date"),
				),
			},
			{
				ResourceName:      "apple_device.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "apple_device.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     testUDID,
			},
			{
				// Name and status change in place.
				Config: deviceConfig("Renamed", "IOS", `status = "DISABLED"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_device.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_device.test", "id", &id),
					resource.TestCheckResourceAttr("apple_device.test", "name", "Renamed"),
					env.checkDeviceStatus(gen.DeviceAttributesStatusDISABLED),
				),
			},
			{
				// A device disabled outside Terraform is enabled again.
				Config: deviceConfig("Renamed", "IOS", ""),
				Check:  env.checkDeviceStatus(gen.DeviceAttributesStatusENABLED),
			},
			{
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.DeviceUpdateRequest
					body.Data.Type, body.Data.Id = gen.DeviceUpdateRequestDataTypeDevices, id
					asc.Alloc(&body.Data.Attributes).Status = new(gen.DeviceUpdateRequestDataAttributesStatusDISABLED)
					out, err := c.DevicesUpdateInstanceWithResponse(ctx, id, body)
					return asc.Err(out, err)
				}),
				Config: deviceConfig("Renamed", "IOS", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_device.test", plancheck.ResourceActionUpdate)},
				},
				Check: env.checkDeviceStatus(gen.DeviceAttributesStatusENABLED),
			},
			{
				// Destroying disables the device, and creating it again
				// adopts the same device.
				Config:  deviceConfig("Renamed", "IOS", ""),
				Destroy: true,
				Check:   env.checkDeviceStatus(gen.DeviceAttributesStatusDISABLED),
			},
			{
				Config: deviceConfig("Again", "IOS", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_device.test", "id", &id),
					resource.TestCheckResourceAttr("apple_device.test", "name", "Again"),
					env.checkDeviceStatus(gen.DeviceAttributesStatusENABLED),
				),
			},
		},
	})
}

func TestAccDeviceCreatedDisabled(t *testing.T) {
	env := newTestEnv(t)
	env.run(resource.TestCase{
		Steps: []resource.TestStep{{
			Config: deviceConfig("Spare", "IOS", `status = "DISABLED"`),
			Check:  env.checkDeviceStatus(gen.DeviceAttributesStatusDISABLED),
		}},
	})
}

func TestAccDeviceOtherPlatform(t *testing.T) {
	env := newTestEnv(t)
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{Config: deviceConfig("Phone", "IOS", "")},
			{Config: deviceConfig("Phone", "IOS", ""), Destroy: true},
			{
				Config:      deviceConfig("Phone", "MAC_OS", ""),
				ExpectError: regexp.MustCompile(`is already registered for IOS`),
			},
		},
	})
}
