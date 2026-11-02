package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccCertificateDataSources(t *testing.T) {
	env := newTestEnv(t)
	cfg := certificateConfig("dev", "DEVELOPMENT", testCSR(t, "Dev"), "") +
		certificateConfig("dist", "IOS_DISTRIBUTION", testCSR(t, "Dist"), "") +
		identifierConfig("merchant_id", "merchant.com.example", "Pay") +
		certificateConfig("pay", "APPLE_PAY", testCSR(t, "Pay"), "merchant_id = apple_merchant_id.test.id") +
		identifierConfig("pass_type_id", "pass.com.example", "Pass") +
		certificateConfig("pass", "PASS_TYPE_ID", testCSR(t, "Pass"), "pass_type_id = apple_pass_type_id.test.id")
	deps := "depends_on = [apple_certificate.dev, apple_certificate.dist, apple_certificate.pay, apple_certificate.pass]"
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg + `
data "apple_certificate" "by_serial" {
  serial_number = lower(apple_certificate.dist.serial_number)
}
data "apple_certificate" "by_id" {
  id = apple_certificate.dev.id
}
data "apple_certificates" "dev" {
  certificate_types = ["DEVELOPMENT"]
  ` + deps + `
}
data "apple_certificates" "all" {
  ` + deps + `
}
data "apple_certificates" "merchant" {
  merchant_id = apple_merchant_id.test.id
  ` + deps + `
}
data "apple_certificates" "pass" {
  pass_type_id = apple_pass_type_id.test.id
  ` + deps + `
}
data "apple_certificates" "serial" {
  serial_numbers = [apple_certificate.pay.serial_number]
  ` + deps + `
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.apple_certificate.by_serial", "id", "apple_certificate.dist", "id"),
					resource.TestCheckResourceAttrPair("data.apple_certificate.by_serial", "certificate_content", "apple_certificate.dist", "certificate_content"),
					resource.TestCheckResourceAttr("data.apple_certificate.by_id", "certificate_type", "DEVELOPMENT"),
					resource.TestCheckResourceAttr("data.apple_certificate.by_id", "activated", "true"),
					resource.TestCheckResourceAttr("data.apple_certificates.dev", "certificates.#", "1"),
					resource.TestCheckResourceAttr("data.apple_certificates.all", "certificates.#", "4"),
					resource.TestCheckResourceAttrPair("data.apple_certificates.merchant", "certificates.0.id", "apple_certificate.pay", "id"),
					resource.TestCheckResourceAttr("data.apple_certificates.merchant", "certificates.#", "1"),
					resource.TestCheckResourceAttrPair("data.apple_certificates.pass", "certificates.0.id", "apple_certificate.pass", "id"),
					resource.TestCheckResourceAttrPair("data.apple_certificates.serial", "certificates.0.id", "apple_certificate.pay", "id"),
				),
			},
			{
				Config:      cfg + `data "apple_certificates" "x" {` + "\n" + `merchant_id = "a"` + "\n" + `pass_type_id = "b"` + "\n}\n",
				ExpectError: regexp.MustCompile(`These attributes cannot be configured together: \[merchant_id,pass_type_id\]`),
			},
			{
				Config:      cfg + `data "apple_certificate" "x" { serial_number = "FFFF" }` + "\n",
				ExpectError: regexp.MustCompile(`No certificate matches`),
			},
			{Config: cfg},
		},
	})
}

func TestAccDeviceDataSources(t *testing.T) {
	env := newTestEnv(t)
	cfg := deviceConfig("Phone", "IOS", "") + `
resource "apple_device" "mac" {
  name     = "Mac"
  udid     = "MAC-UDID-1"
  platform = "MAC_OS"
  status   = "DISABLED"
}
`
	deps := "depends_on = [apple_device.test, apple_device.mac]"
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg + `
data "apple_device" "by_udid" {
  udid = apple_device.mac.udid
}
data "apple_device" "by_id" {
  id = apple_device.test.id
}
data "apple_devices" "enabled" {
  statuses = ["ENABLED"]
  ` + deps + `
}
data "apple_devices" "mac" {
  platforms = ["MAC_OS"]
  names     = ["Mac"]
  ` + deps + `
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.apple_device.by_udid", "id", "apple_device.mac", "id"),
					resource.TestCheckResourceAttr("data.apple_device.by_udid", "status", "DISABLED"),
					resource.TestCheckResourceAttr("data.apple_device.by_id", "udid", testUDID),
					resource.TestCheckResourceAttr("data.apple_devices.enabled", "devices.#", "1"),
					resource.TestCheckResourceAttrPair("data.apple_devices.enabled", "devices.0.id", "apple_device.test", "id"),
					resource.TestCheckResourceAttr("data.apple_devices.mac", "devices.0.device_class", "MAC"),
				),
			},
		},
	})
}

func TestAccProfileDataSources(t *testing.T) {
	env := newTestEnv(t)
	cfg := profileConfig(t, testCSR(t, "Dev"), "", "") + `
resource "apple_bundle_id" "other" {
  identifier = "com.example.other"
  name       = "Other"
  platform   = "IOS"
}
resource "apple_profile" "other" {
  name            = "Other Store"
  profile_type    = "IOS_APP_STORE"
  bundle_id       = apple_bundle_id.other.id
  certificate_ids = [apple_certificate.dev.id]
}
`
	deps := "depends_on = [apple_profile.test, apple_profile.other]"
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg + `
data "apple_profile" "by_name" {
  name = apple_profile.test.name
}
data "apple_profile" "by_id" {
  id = apple_profile.other.id
}
data "apple_profiles" "store" {
  profile_types = ["IOS_APP_STORE"]
  ` + deps + `
}
data "apple_profiles" "bundle" {
  bundle_id = apple_bundle_id.test.id
  ` + deps + `
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.apple_profile.by_name", "id", "apple_profile.test", "id"),
					resource.TestCheckResourceAttrPair("data.apple_profile.by_name", "bundle_id", "apple_bundle_id.test", "id"),
					resource.TestCheckResourceAttrPair("data.apple_profile.by_name", "certificate_ids.0", "apple_certificate.dev", "id"),
					resource.TestCheckResourceAttrPair("data.apple_profile.by_name", "device_ids.0", "apple_device.test", "id"),
					resource.TestCheckResourceAttrPair("data.apple_profile.by_name", "profile_content", "apple_profile.test", "profile_content"),
					resource.TestCheckResourceAttr("data.apple_profile.by_id", "device_ids.#", "0"),
					resource.TestCheckResourceAttr("data.apple_profiles.store", "profiles.#", "1"),
					resource.TestCheckResourceAttr("data.apple_profiles.store", "profiles.0.name", "Other Store"),
					resource.TestCheckResourceAttr("data.apple_profiles.bundle", "profiles.#", "1"),
					resource.TestCheckResourceAttrPair("data.apple_profiles.bundle", "profiles.0.id", "apple_profile.test", "id"),
				),
			},
		},
	})
}

func TestAccIdentifierDataSources(t *testing.T) {
	for _, kind := range []identifierKind{merchantIDKind, passTypeIDKind} {
		t.Run(kind.typeName, func(t *testing.T) {
			env := newTestEnv(t)
			plural := kind.typeName + "s"
			cfg := identifierConfig(kind.typeName, kind.example, "Example") + `
resource "apple_` + kind.typeName + `" "other" {
  identifier = "` + kind.example + `.other"
  name       = "Other"
}
`
			env.run(resource.TestCase{
				Steps: []resource.TestStep{
					{
						Config: cfg + `
data "apple_` + kind.typeName + `" "by_identifier" {
  identifier = apple_` + kind.typeName + `.test.identifier
}
data "apple_` + kind.typeName + `" "by_id" {
  id = apple_` + kind.typeName + `.other.id
}
data "apple_` + plural + `" "named" {
  names      = ["Other"]
  depends_on = [apple_` + kind.typeName + `.test, apple_` + kind.typeName + `.other]
}
`,
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrPair("data.apple_"+kind.typeName+".by_identifier", "id", "apple_"+kind.typeName+".test", "id"),
							resource.TestCheckResourceAttr("data.apple_"+kind.typeName+".by_id", "name", "Other"),
							resource.TestCheckResourceAttr("data.apple_"+plural+".named", plural+".#", "1"),
							resource.TestCheckResourceAttr("data.apple_"+plural+".named", plural+".0.identifier", kind.example+".other"),
						),
					},
				},
			})
		})
	}
}

func TestAccAppDataSources(t *testing.T) {
	env := newTestEnv(t)
	appID := env.fake.AddApp("Example", "com.example.app", "EX1")
	env.fake.AddApp("Other", "com.example.other", "EX2")
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: `
data "apple_app" "by_bundle_id" {
  bundle_id = "com.example.app"
}
data "apple_app" "by_id" {
  id = "` + appID + `"
}
data "apple_apps" "all" {}
data "apple_apps" "sku" {
  skus = ["EX2"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_app.by_bundle_id", "id", appID),
					resource.TestCheckResourceAttr("data.apple_app.by_bundle_id", "sku", "EX1"),
					resource.TestCheckResourceAttr("data.apple_app.by_id", "name", "Example"),
					resource.TestCheckResourceAttr("data.apple_app.by_id", "primary_locale", "en-US"),
					resource.TestCheckResourceAttr("data.apple_apps.all", "apps.#", "2"),
					resource.TestCheckResourceAttr("data.apple_apps.sku", "apps.#", "1"),
					resource.TestCheckResourceAttr("data.apple_apps.sku", "apps.0.bundle_id", "com.example.other"),
				),
			},
			{
				Config:      `data "apple_app" "x" { bundle_id = "com.example.missing" }`,
				ExpectError: regexp.MustCompile(`No app matches`),
			},
			{
				Config:      `data "apple_app" "x" { id = "MISSING" }`,
				ExpectError: regexp.MustCompile(`No app matches`),
			},
		},
	})
}
