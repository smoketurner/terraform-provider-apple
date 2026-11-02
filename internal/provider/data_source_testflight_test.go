package provider

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func TestAccBuildDataSources(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	now := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	env.fake.SetClock(func() time.Time { return now })
	older := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "1", Marketing: "1.0", UsesNonExemptEncryption: new(false)})
	now = now.Add(time.Hour)
	newer := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "2", Marketing: "1.0"})
	now = now.Add(time.Hour)
	mac := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "3", Marketing: "2.0", Platform: gen.PlatformMACOS, InternalOnly: true})
	env.fake.SetClock(time.Now)
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + fmt.Sprintf(`
data "apple_build" "by_id" {
  id = %q
}
data "apple_build" "by_version" {
  app_id              = local.app_id
  version             = "2"
  pre_release_version = "1.0"
}
data "apple_build" "latest_1_0" {
  app_id              = local.app_id
  pre_release_version = "1.0"
  latest              = true
}
data "apple_builds" "all" {
  app_id = local.app_id
}
data "apple_builds" "mac" {
  app_id    = local.app_id
  platforms = ["MAC_OS"]
}
data "apple_pre_release_versions" "all" {
  app_id = local.app_id
}
`, older),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_build.by_id", "app_id", appID),
					resource.TestCheckResourceAttr("data.apple_build.by_id", "pre_release_version", "1.0"),
					resource.TestCheckResourceAttr("data.apple_build.by_id", "platform", "IOS"),
					resource.TestCheckResourceAttr("data.apple_build.by_id", "uses_non_exempt_encryption", "false"),
					resource.TestCheckResourceAttr("data.apple_build.by_id", "internal_build_state", "IN_BETA_TESTING"),
					resource.TestCheckResourceAttr("data.apple_build.by_id", "external_build_state", "READY_FOR_BETA_SUBMISSION"),
					resource.TestCheckResourceAttr("data.apple_build.by_id", "expiration_date", "2027-05-30T00:00:00Z"),
					resource.TestCheckResourceAttr("data.apple_build.by_version", "id", newer),
					resource.TestCheckNoResourceAttr("data.apple_build.by_version", "uses_non_exempt_encryption"),
					resource.TestCheckResourceAttr("data.apple_build.by_version", "internal_build_state", "MISSING_EXPORT_COMPLIANCE"),
					resource.TestCheckResourceAttr("data.apple_build.latest_1_0", "id", newer),
					resource.TestCheckResourceAttr("data.apple_builds.all", "builds.#", "3"),
					resource.TestCheckResourceAttr("data.apple_builds.all", "builds.0.id", mac),
					resource.TestCheckResourceAttr("data.apple_builds.all", "builds.2.id", older),
					resource.TestCheckResourceAttr("data.apple_builds.mac", "builds.#", "1"),
					resource.TestCheckResourceAttr("data.apple_builds.mac", "builds.0.build_audience_type", "INTERNAL_ONLY"),
					resource.TestCheckResourceAttr("data.apple_pre_release_versions.all", "pre_release_versions.#", "2"),
				),
			},
			{
				Config:      locals + `data "apple_build" "x" { app_id = local.app_id }` + "\n",
				ExpectError: regexp.MustCompile(`3 builds match`),
			},
			{
				Config:      locals + `data "apple_build" "x" { id = "MISSING" }` + "\n",
				ExpectError: regexp.MustCompile(`No build matches`),
			},
			{
				Config:      locals + `data "apple_build" "x" {` + "\n" + `app_id = local.app_id` + "\n" + `version = "99"` + "\n}\n",
				ExpectError: regexp.MustCompile(`No build matches`),
			},
		},
	})
}

func TestAccBetaGroupAndTesterDataSources(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	cfg := locals + internalGroup + externalGroup("public_link_enabled = true") + testerConfig("apple_beta_group.internal.id")
	deps := "depends_on = [apple_beta_group.internal, apple_beta_group.external, apple_beta_tester.ann]"
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg + `
data "apple_beta_groups" "all" {
  app_id = local.app_id
  ` + deps + `
}
data "apple_beta_groups" "external" {
  app_id   = local.app_id
  internal = false
  ` + deps + `
}
data "apple_beta_tester" "ann" {
  email = "Ann@Example.com"
  ` + deps + `
}
data "apple_beta_testers" "internal" {
  beta_group_id = apple_beta_group.internal.id
  ` + deps + `
}
data "apple_beta_testers" "external" {
  beta_group_id = apple_beta_group.external.id
  ` + deps + `
}
data "apple_beta_recruitment_criterion_options" "all" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_beta_groups.all", "beta_groups.#", "2"),
					resource.TestCheckResourceAttr("data.apple_beta_groups.external", "beta_groups.#", "1"),
					resource.TestCheckResourceAttrPair("data.apple_beta_groups.external", "beta_groups.0.public_link", "apple_beta_group.external", "public_link"),
					resource.TestCheckResourceAttrPair("data.apple_beta_tester.ann", "id", "apple_beta_tester.ann", "id"),
					resource.TestCheckResourceAttr("data.apple_beta_testers.internal", "beta_testers.#", "1"),
					resource.TestCheckResourceAttr("data.apple_beta_testers.external", "beta_testers.#", "0"),
					resource.TestCheckResourceAttr("data.apple_beta_recruitment_criterion_options.all", "options.#", "3"),
					resource.TestCheckResourceAttr("data.apple_beta_recruitment_criterion_options.all", "options.1.device_family", "IPHONE"),
				),
			},
		},
	})
}

// TestFindBuildsPages checks that builds on later pages keep their
// included versions and beta states.
func TestFindBuildsPages(t *testing.T) {
	env := newTestEnv(t)
	appID := env.fake.AddApp("Example", "com.example.app", "EX1")
	for i := range listAll + 5 {
		env.fake.AddBuild(appID, asctest.BuildOptions{Version: strconv.Itoa(i), Marketing: "1.0"})
	}
	builds, err := findBuilds(t.Context(), env.client, gen.BuildsGetCollectionParams{FilterApp: &[]string{appID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != listAll+5 {
		t.Fatalf("got %d builds", len(builds))
	}
	for _, b := range builds {
		if b.PreReleaseVersion.ValueString() != "1.0" || b.InternalBuildState.IsNull() || b.AppID.ValueString() != appID {
			t.Fatalf("build %s lost its included objects: %+v", b.ID.ValueString(), b)
		}
	}
}
