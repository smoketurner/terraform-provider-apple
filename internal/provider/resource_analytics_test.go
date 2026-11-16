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
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func analyticsRequestConfig(accessType string) string {
	return `
resource "apple_analytics_report_request" "ongoing" {
  app_id      = local.app_id
  access_type = "` + accessType + `"
}
`
}

const analyticsReports = `
data "apple_analytics_report_requests" "all" {
  app_id     = local.app_id
  depends_on = [apple_analytics_report_request.ongoing]
}

data "apple_analytics_report_requests" "snapshots" {
  app_id       = local.app_id
  access_types = ["ONE_TIME_SNAPSHOT"]
  depends_on   = [apple_analytics_report_request.ongoing]
}

data "apple_analytics_reports" "all" {
  request_id = apple_analytics_report_request.ongoing.id
}

data "apple_analytics_reports" "commerce" {
  request_id = apple_analytics_report_request.ongoing.id
  categories = ["COMMERCE"]
}

data "apple_analytics_reports" "sessions" {
  request_id = apple_analytics_report_request.ongoing.id
  names      = ["App Sessions Standard"]
}
`

const analyticsDownloads = `
data "apple_analytics_report_instances" "all" {
  report_id = data.apple_analytics_reports.sessions.reports[0].id
}

data "apple_analytics_report_instances" "daily" {
  report_id        = data.apple_analytics_reports.sessions.reports[0].id
  granularities    = ["DAILY"]
  processing_dates = ["2026-11-16"]
}

data "apple_analytics_report_segments" "daily" {
  instance_id = data.apple_analytics_report_instances.daily.instances[0].id
}
`

func TestAccAnalyticsReportRequest(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	const address = "apple_analytics_report_request.ongoing"
	var id, reportID, dailyID, replaced string
	requests := func(want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			out, err := env.client.AppsAnalyticsReportRequestsGetToManyRelatedWithResponse(context.Background(), appID, nil)
			if err := asc.Err(out, err); err != nil {
				return err
			}
			var got []string
			for _, q := range out.JSON200.Data {
				got = append(got, string(*q.Attributes.AccessType))
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				return fmt.Errorf("requests = %v, want %v", got, want)
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		CheckDestroy: requests(),
		Steps: []resource.TestStep{
			{
				Config: locals + analyticsRequestConfig("ONGOING") + analyticsReports,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					captureAttr("data.apple_analytics_reports.sessions", "reports.0.id", &reportID),
					resource.TestCheckResourceAttr(address, "stopped_due_to_inactivity", "false"),
					resource.TestCheckResourceAttr("data.apple_analytics_report_requests.all", "requests.#", "1"),
					resource.TestCheckResourceAttr("data.apple_analytics_report_requests.all", "requests.0.access_type", "ONGOING"),
					resource.TestCheckResourceAttr("data.apple_analytics_report_requests.snapshots", "requests.#", "0"),
					resource.TestCheckResourceAttr("data.apple_analytics_reports.all", "reports.#", "3"),
					resource.TestCheckResourceAttr("data.apple_analytics_reports.commerce", "reports.#", "1"),
					resource.TestCheckResourceAttr("data.apple_analytics_reports.commerce", "reports.0.name", "App Store Purchases Standard"),
					resource.TestCheckResourceAttr("data.apple_analytics_reports.sessions", "reports.#", "1"),
					resource.TestCheckResourceAttr("data.apple_analytics_reports.sessions", "reports.0.category", "APP_USAGE"),
				),
			},
			{
				ResourceName: address, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID + "/" + id, nil },
			},
			{
				// An app has one request per access type.
				Config: locals + analyticsRequestConfig("ONGOING") + `
resource "apple_analytics_report_request" "again" {
  app_id      = local.app_id
  access_type = "ONGOING"
  depends_on  = [apple_analytics_report_request.ongoing]
}
`,
				ExpectError: regexp.MustCompile(`already\s+has\s+an\s+ONGOING\s+analytics\s+report\s+request`),
			},
			{
				// Apple generates the instances; each has segments to
				// download.
				PreConfig: func() {
					dailyID = env.fake.AddAnalyticsReportInstance(reportID, "DAILY", "2026-11-16")
					env.fake.AddAnalyticsReportInstance(reportID, "DAILY", "2026-11-15")
					env.fake.AddAnalyticsReportInstance(reportID, "WEEKLY", "2026-11-16")
				},
				Config: locals + analyticsRequestConfig("ONGOING") + analyticsReports + analyticsDownloads,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_analytics_report_instances.all", "instances.#", "3"),
					resource.TestCheckResourceAttr("data.apple_analytics_report_instances.daily", "instances.#", "1"),
					resource.TestCheckResourceAttrPtr("data.apple_analytics_report_instances.daily", "instances.0.id", &dailyID),
					resource.TestCheckResourceAttr("data.apple_analytics_report_instances.daily", "instances.0.processing_date", "2026-11-16"),
					resource.TestCheckResourceAttr("data.apple_analytics_report_segments.daily", "segments.#", "1"),
					resource.TestMatchResourceAttr("data.apple_analytics_report_segments.daily", "segments.0.url", regexp.MustCompile(`\.csv\.gz$`)),
					resource.TestCheckResourceAttr("data.apple_analytics_report_segments.daily", "segments.0.size_in_bytes", "2048"),
					resource.TestMatchResourceAttr("data.apple_analytics_report_segments.daily", "segments.0.checksum", regexp.MustCompile(`^md5-`)),
				),
			},
			{
				// A request App Store Connect stopped is replaced, which
				// deletes the stopped one.
				PreConfig:        func() { env.fake.StopAnalyticsReportRequest(id) },
				Config:           locals + analyticsRequestConfig("ONGOING"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &replaced),
					resource.TestCheckResourceAttr(address, "stopped_due_to_inactivity", "false"),
					requests("ONGOING"),
					func(*terraform.State) error {
						if replaced == id {
							return fmt.Errorf("the stopped request %s was not replaced", id)
						}
						return nil
					},
				),
			},
			{
				// The access type can't change, so another one replaces the
				// request.
				Config:           locals + analyticsRequestConfig("ONE_TIME_SNAPSHOT"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionDestroyBeforeCreate),
				Check:            requests("ONE_TIME_SNAPSHOT"),
			},
			{
				// A request deleted outside Terraform is made again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppsAnalyticsReportRequestsGetToManyRelatedWithResponse(ctx, appID, nil)
					if err := asc.Err(out, err); err != nil {
						return err
					}
					deleted, err := c.AnalyticsReportRequestsDeleteInstanceWithResponse(ctx, out.JSON200.Data[0].Id)
					return asc.Err(deleted, err)
				}),
				Config:           locals + analyticsRequestConfig("ONE_TIME_SNAPSHOT"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
			},
			{
				Config:      `data "apple_analytics_reports" "missing" { request_id = "nope" }`,
				ExpectError: regexp.MustCompile(`No analytics report request matches`),
			},
		},
	})
}

func TestAccBuildBundleFileSizes(t *testing.T) {
	env := newTestEnv(t)
	appID, _ := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "1", Marketing: "1.0", UsesNonExemptEncryption: new(false)})
	bundleID := env.fake.AddBuildBundle(buildID, "com.example.app", gen.BuildBundleTypeAPP)
	const address = "data.apple_build_bundle_file_sizes.app"
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: `data "apple_build_bundle_file_sizes" "app" { build_bundle_id = "` + bundleID + `" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "file_sizes.#", "2"),
					resource.TestCheckResourceAttr(address, "file_sizes.0.device_model", "Universal"),
					resource.TestCheckResourceAttr(address, "file_sizes.0.download_bytes", "52428800"),
					resource.TestCheckResourceAttr(address, "file_sizes.1.os_version", "26.0"),
					resource.TestCheckResourceAttr(address, "file_sizes.1.install_bytes", "41943040"),
				),
			},
			{
				Config:      `data "apple_build_bundle_file_sizes" "missing" { build_bundle_id = "nope" }`,
				ExpectError: regexp.MustCompile(`No build bundle matches`),
			},
		},
	})
}
