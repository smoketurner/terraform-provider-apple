package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccBundleIDDataSources(t *testing.T) {
	env := newTestEnv(t)
	cfg := bundleIDConfig("com.example.app", "App", "") + `
resource "apple_bundle_id" "mac" {
  identifier = "com.example.mac"
  name       = "Mac App"
  platform   = "MAC_OS"
}
`
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg + `
data "apple_bundle_id" "by_identifier" {
  identifier = apple_bundle_id.test.identifier
}
data "apple_bundle_id" "by_id" {
  id = apple_bundle_id.mac.id
}
data "apple_bundle_ids" "ios" {
  platforms  = ["IOS"]
  depends_on = [apple_bundle_id.test, apple_bundle_id.mac]
}
data "apple_bundle_ids" "all" {
  depends_on = [apple_bundle_id.test, apple_bundle_id.mac]
}
data "apple_bundle_ids" "none" {
  identifiers = ["com.example.missing"]
  depends_on  = [apple_bundle_id.test]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.apple_bundle_id.by_identifier", "id", "apple_bundle_id.test", "id"),
					resource.TestCheckResourceAttr("data.apple_bundle_id.by_identifier", "name", "App"),
					resource.TestCheckResourceAttr("data.apple_bundle_id.by_id", "identifier", "com.example.mac"),
					resource.TestCheckResourceAttr("data.apple_bundle_ids.ios", "bundle_ids.#", "1"),
					resource.TestCheckResourceAttr("data.apple_bundle_ids.ios", "bundle_ids.0.identifier", "com.example.app"),
					resource.TestCheckResourceAttr("data.apple_bundle_ids.all", "bundle_ids.#", "2"),
					resource.TestCheckResourceAttr("data.apple_bundle_ids.none", "bundle_ids.#", "0"),
				),
			},
			{
				Config:      cfg + `data "apple_bundle_id" "x" {}`,
				ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured: \[id,identifier\]`),
			},
			{
				Config:      cfg + `data "apple_bundle_id" "x" { identifier = "com.example.missing" }`,
				ExpectError: regexp.MustCompile(`No bundle ID matches the given arguments`),
			},
			{
				Config:      cfg + `data "apple_bundle_id" "x" { id = "MISSING" }`,
				ExpectError: regexp.MustCompile(`No bundle ID matches the given arguments`),
			},
			{
				Config:      cfg + `data "apple_bundle_ids" "x" { platforms = ["ANDROID"] }`,
				ExpectError: regexp.MustCompile(`value must be one of`),
			},
			// The post-test destroy uses the last configuration.
			{Config: cfg},
		},
	})
}
