package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
)

// xcodeCloudApp adds an app with an Xcode Cloud product and two
// repositories, and another app's product with its own repository. It
// returns HCL locals naming them and the IDs of the first product's primary
// repository and its main branch.
func (e *testEnv) xcodeCloudApp() (locals, repositoryID, mainID string) {
	appID, locals := e.testflightApp()
	repositoryID, mainID = e.fake.AddScmRepository("example", "app")
	libraryID, _ := e.fake.AddScmRepository("example", "library")
	otherID, _ := e.fake.AddScmRepository("example", "unrelated")
	productID := e.fake.AddCiProduct(appID, "Example", repositoryID, libraryID)
	e.fake.AddCiProduct(e.fake.AddApp("Other", "com.example.other", "EX2"), "Other", otherID)
	return locals + fmt.Sprintf("locals {\n  product_id = %q\n  repository_id = %q\n  unrelated_id = %q\n}\n", productID, repositoryID, otherID), repositoryID, mainID
}

const ciBuildAction = `
    {
      name                = "Build - iOS"
      action_type         = "BUILD"
      scheme              = "Example"
      platform            = "IOS"
      destination         = "ANY_IOS_DEVICE"
      is_required_to_pass = true
    },
`

const ciTestAction = `
    {
      name                = "Test - iOS"
      action_type         = "TEST"
      scheme              = "Example"
      platform            = "IOS"
      destination         = "ANY_IOS_SIMULATOR"
      is_required_to_pass = false
      test_configuration = {
        kind           = "SPECIFIC_TEST_PLANS"
        test_plan_name = "Unit"
        test_destinations = [
          {
            kind                   = "SIMULATOR"
            device_type_name       = "iPhone 17"
            device_type_identifier = "com.apple.CoreSimulator.SimDeviceType.iPhone-17"
            runtime_name           = "Latest from Selected Xcode"
            runtime_identifier     = "default"
          },
        ]
      }
    },
`

func ciWorkflowConfig(repository, xcode, mac, enabled, conditions, actions string) string {
	return `
resource "apple_ci_workflow" "ci" {
  product_id          = local.product_id
  repository_id       = ` + repository + `
  xcode_version_id    = "` + xcode + `"
  mac_os_version_id   = "` + mac + `"
  name                = "CI"
  description         = "Builds and tests every change."
  enabled             = ` + enabled + `
  clean               = false
  container_file_path = "Example.xcodeproj"
` + conditions + `
  actions = [` + actions + `  ]
}
`
}

const ciFullConditions = `
  branch_start_condition = {
    source      = { patterns = [{ pattern = "main" }, { pattern = "release/", is_prefix = true }] }
    auto_cancel = true
    files_and_folders_rule = {
      mode     = "START_IF_ANY_FILE_MATCHES"
      matchers = [{ directory = "/Sources", file_extension = "swift" }]
    }
  }
  pull_request_start_condition = {
    source      = { is_all_match = true }
    destination = { patterns = [{ pattern = "main" }] }
  }
  scheduled_start_condition = {
    source = { patterns = [{ pattern = "main" }] }
    schedule = {
      frequency = "WEEKLY"
      days      = ["MONDAY", "THURSDAY"]
      hour      = 0
      minute    = 30
      timezone  = "America/Los_Angeles"
    }
  }
  manual_branch_start_condition = {
    source = { is_all_match = true }
  }
`

// ciWorkflowAttributes returns a workflow's attributes as the API holds them.
func (e *testEnv) ciWorkflowAttributes(id string) (map[string]any, error) {
	out, err := e.client.CiWorkflowsGetInstanceWithResponse(context.Background(), id, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	return doc.Data.Attributes, json.Unmarshal(out.Body, &doc)
}

// dig follows keys and list indexes into a decoded JSON value.
func dig(v any, keys ...any) any {
	for _, key := range keys {
		switch k := key.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func patchCiWorkflow(id *string, attributes string) func(ctx context.Context, c *asc.Client) error {
	return func(ctx context.Context, c *asc.Client) error {
		body := `{"data":{"type":"ciWorkflows","id":"` + *id + `","attributes":` + attributes + `}}`
		out, err := c.CiWorkflowsUpdateInstanceWithBodyWithResponse(ctx, *id, "application/json", bytes.NewReader([]byte(body)))
		return asc.Err(out, err)
	}
}

func TestAccCiWorkflow(t *testing.T) {
	env := newTestEnv(t)
	locals, repositoryID, mainID := env.xcodeCloudApp()
	const address = "apple_ci_workflow.ci"
	full := locals + ciWorkflowConfig("local.repository_id", asctest.Xcode16, asctest.MacOS15, "true", ciFullConditions, ciBuildAction+ciTestAction)
	lookups := `
data "apple_ci_products" "app" {
  app_id = local.app_id
}

data "apple_ci_products" "all" {}

data "apple_scm_repositories" "product" {
  product_id = local.product_id
}

data "apple_scm_repositories" "all" {}

data "apple_scm_git_references" "branches" {
  repository_id = local.repository_id
  kinds         = ["BRANCH"]
}

data "apple_ci_xcode_versions" "all" {}

data "apple_ci_mac_os_versions" "all" {}

data "apple_ci_workflows" "product" {
  product_id = local.product_id
  depends_on = [apple_ci_workflow.ci]
}
`
	var id, recreated string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if _, err := env.ciWorkflowAttributes(recreated); !asc.IsNotFound(err) {
				return fmt.Errorf("workflow %s still exists (err %w)", recreated, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// The macOS version must run the Xcode version.
				Config:      locals + ciWorkflowConfig("local.repository_id", asctest.Xcode26, asctest.MacOS15, "true", "", ciBuildAction),
				ExpectError: regexp.MustCompile(`doesn't\s+run\s+the\s+Xcode\s+version`),
			},
			{
				// The repository must belong to the product.
				Config:      locals + ciWorkflowConfig("local.unrelated_id", asctest.Xcode26, asctest.MacOS26, "true", "", ciBuildAction),
				ExpectError: regexp.MustCompile(`product\s+has\s+no\s+repository`),
			},
			{
				Config: full + lookups,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					resource.TestCheckResourceAttr(address, "locked_for_editing", "false"),
					resource.TestCheckResourceAttrSet(address, "last_modified_date"),
					resource.TestCheckResourceAttr(address, "actions.#", "2"),
					resource.TestCheckResourceAttr("data.apple_ci_products.app", "products.#", "1"),
					resource.TestCheckResourceAttr("data.apple_ci_products.app", "products.0.primary_repository_id", repositoryID),
					resource.TestCheckResourceAttr("data.apple_ci_products.app", "products.0.product_type", "APP"),
					resource.TestCheckResourceAttr("data.apple_ci_products.all", "products.#", "2"),
					resource.TestCheckResourceAttr("data.apple_scm_repositories.all", "repositories.#", "3"),
					resource.TestCheckNoResourceAttr("data.apple_scm_repositories.all", "repositories.0.primary"),
					resource.TestCheckResourceAttr("data.apple_scm_repositories.product", "repositories.#", "2"),
					resource.TestCheckResourceAttr("data.apple_scm_repositories.product", "repositories.0.id", repositoryID),
					resource.TestCheckResourceAttr("data.apple_scm_repositories.product", "repositories.0.primary", "true"),
					resource.TestCheckResourceAttr("data.apple_scm_repositories.product", "repositories.0.default_branch_id", mainID),
					resource.TestCheckResourceAttr("data.apple_scm_repositories.product", "repositories.1.repository_name", "library"),
					resource.TestCheckResourceAttr("data.apple_scm_repositories.product", "repositories.1.primary", "false"),
					resource.TestCheckResourceAttr("data.apple_scm_git_references.branches", "git_references.#", "1"),
					resource.TestCheckResourceAttr("data.apple_scm_git_references.branches", "git_references.0.canonical_name", "refs/heads/main"),
					resource.TestCheckResourceAttr("data.apple_ci_xcode_versions.all", "xcode_versions.#", "2"),
					resource.TestCheckResourceAttr("data.apple_ci_xcode_versions.all", "xcode_versions.0.id", asctest.Xcode16),
					resource.TestCheckResourceAttr("data.apple_ci_xcode_versions.all", "xcode_versions.0.mac_os_version_ids.#", "2"),
					resource.TestCheckResourceAttr("data.apple_ci_mac_os_versions.all", "mac_os_versions.#", "2"),
					resource.TestCheckResourceAttr("data.apple_ci_mac_os_versions.all", "mac_os_versions.0.id", asctest.MacOS15),
					resource.TestCheckResourceAttr("data.apple_ci_mac_os_versions.all", "mac_os_versions.0.xcode_version_ids.#", "1"),
					resource.TestCheckResourceAttr("data.apple_ci_workflows.product", "workflows.#", "1"),
					resource.TestCheckResourceAttr("data.apple_ci_workflows.product", "workflows.0.name", "CI"),
					func(*terraform.State) error {
						a, err := env.ciWorkflowAttributes(id)
						if err != nil {
							return err
						}
						for _, check := range []struct {
							want any
							path []any
						}{
							{"release/", []any{"branchStartCondition", "source", "patterns", 1, "pattern"}},
							{true, []any{"branchStartCondition", "source", "patterns", 1, "isPrefix"}},
							{"swift", []any{"branchStartCondition", "filesAndFoldersRule", "matchers", 0, "fileExtension"}},
							{true, []any{"pullRequestStartCondition", "source", "isAllMatch"}},
							{"THURSDAY", []any{"scheduledStartCondition", "schedule", "days", 1}},
							{float64(0), []any{"scheduledStartCondition", "schedule", "hour"}},
							{float64(30), []any{"scheduledStartCondition", "schedule", "minute"}},
							{nil, []any{"tagStartCondition"}},
							{"Unit", []any{"actions", 1, "testConfiguration", "testPlanName"}},
							{"default", []any{"actions", 1, "testConfiguration", "testDestinations", 0, "runtimeIdentifier"}},
							{"Example.xcodeproj", []any{"containerFilePath"}},
						} {
							if got := dig(a, check.path...); got != check.want {
								return fmt.Errorf("%v = %v, want %v", check.path, got, check.want)
							}
						}
						return nil
					},
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				// What the API spells out, such as a false it was not sent,
				// is not a change.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					return patchCiWorkflow(&id, `{"manualBranchStartCondition":{"source":{"isAllMatch":true,"patterns":[]}},`+
						`"pullRequestStartCondition":{"source":{"isAllMatch":true,"patterns":[]},"destination":{"isAllMatch":false,`+
						`"patterns":[{"pattern":"main","isPrefix":false}]},"autoCancel":false}}`)(ctx, c)
				}),
				Config:           full,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// A change made outside Terraform is set back.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					return patchCiWorkflow(&id, `{"manualBranchStartCondition":{"source":{"patterns":[{"pattern":"develop"}]}}}`)(ctx, c)
				}),
				Config:           full,
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: func(*terraform.State) error {
					a, err := env.ciWorkflowAttributes(id)
					if err != nil {
						return err
					}
					if got := dig(a, "manualBranchStartCondition", "source", "isAllMatch"); got != true {
						return fmt.Errorf("manual branch start matches all = %v, want true", got)
					}
					return nil
				},
			},
			{
				// Start conditions are removed and added, and the versions
				// and actions change, in place.
				Config: locals + ciWorkflowConfig("local.repository_id", asctest.Xcode26, asctest.MacOS26, "false", `
  tag_start_condition = {
    source = { patterns = [{ pattern = "v", is_prefix = true }] }
  }
`, ciBuildAction),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id),
					resource.TestCheckResourceAttr(address, "xcode_version_id", asctest.Xcode26),
					resource.TestCheckNoResourceAttr(address, "branch_start_condition.%"),
					func(*terraform.State) error {
						a, err := env.ciWorkflowAttributes(id)
						if err != nil {
							return err
						}
						for _, gone := range []string{"branchStartCondition", "pullRequestStartCondition", "scheduledStartCondition", "manualBranchStartCondition"} {
							if a[gone] != nil {
								return fmt.Errorf("%s = %v, want none", gone, a[gone])
							}
						}
						if got := dig(a, "tagStartCondition", "source", "patterns", 0, "pattern"); got != "v" {
							return fmt.Errorf("tag pattern = %v", got)
						}
						if actions, _ := a["actions"].([]any); len(actions) != 1 || a["isEnabled"] != false {
							return fmt.Errorf("actions = %v, enabled = %v", a["actions"], a["isEnabled"])
						}
						return nil
					},
				),
			},
			{
				// A workflow deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.CiWorkflowsDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           full,
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
				Check:            captureAttr(address, "id", &recreated),
			},
			{
				Config:      `data "apple_ci_build_runs" "none" {}`,
				ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
			},
			{
				Config:      `data "apple_scm_git_references" "missing" { repository_id = "nope" }`,
				ExpectError: regexp.MustCompile(`No repository matches`),
			},
		},
	})
}

func TestAccStartCiBuildRun(t *testing.T) {
	env := newTestEnv(t)
	locals, repositoryID, mainID := env.xcodeCloudApp()
	env.fake.AddGitReference(repositoryID, "v1.0", "TAG")
	env.fake.AddPullRequest(repositoryID, 7, "Add ordering", "ordering")
	workflow := func(enabled string) string {
		return locals + ciWorkflowConfig("local.repository_id", asctest.Xcode26, asctest.MacOS26, enabled, "", ciBuildAction) + `
data "apple_scm_git_references" "tags" {
  repository_id = local.repository_id
  kinds         = ["TAG"]
}

data "apple_scm_pull_requests" "open" {
  repository_id = local.repository_id
}
`
	}
	action := func(source string) string {
		return `
action "apple_start_ci_build_run" "build" {
  config {
    workflow_id = apple_ci_workflow.ci.id
` + source + `
  }
}
`
	}
	const runs = `
data "apple_ci_build_runs" "workflow" {
  workflow_id = apple_ci_workflow.ci.id
}

data "apple_ci_build_runs" "latest" {
  product_id = local.product_id
  limit      = 1
}
`
	env.actionCase(
		resource.TestStep{Config: workflow("true")},
		resource.TestStep{
			Config:      workflow("true") + action("") + triggerAction("1", "apple_start_ci_build_run.build"),
			ExpectError: regexp.MustCompile(`exactly one of git_reference_id and\s+pull_request_id`),
		},
		resource.TestStep{
			// A disabled workflow doesn't start builds.
			Config:      workflow("false") + action(`    git_reference_id = "`+mainID+`"`) + triggerAction("2", "apple_start_ci_build_run.build"),
			ExpectError: regexp.MustCompile(`workflow is disabled`),
		},
		resource.TestStep{
			Config: workflow("true") + action(`    git_reference_id = data.apple_scm_git_references.tags.git_references[0].id
    clean            = true`) + triggerAction("3", "apple_start_ci_build_run.build"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.apple_scm_git_references.tags", "git_references.0.name", "v1.0"),
				resource.TestCheckResourceAttr("data.apple_scm_pull_requests.open", "pull_requests.0.number", "7"),
				resource.TestCheckResourceAttr("data.apple_scm_pull_requests.open", "pull_requests.0.destination_branch_name", "main"),
			),
		},
		resource.TestStep{
			Config: workflow("true") + action(`    pull_request_id = data.apple_scm_pull_requests.open.pull_requests[0].id`) +
				triggerAction("4", "apple_start_ci_build_run.build"),
		},
		resource.TestStep{
			Config: workflow("true") + runs,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.workflow", "build_runs.#", "2"),
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.workflow", "build_runs.0.number", "2"),
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.workflow", "build_runs.0.is_pull_request_build", "true"),
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.workflow", "build_runs.1.is_pull_request_build", "false"),
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.workflow", "build_runs.1.execution_progress", "PENDING"),
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.workflow", "build_runs.1.start_reason", "MANUAL"),
				resource.TestCheckNoResourceAttr("data.apple_ci_build_runs.workflow", "build_runs.1.completion_status"),
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.latest", "build_runs.#", "1"),
				resource.TestCheckResourceAttr("data.apple_ci_build_runs.latest", "build_runs.0.number", "2"),
			),
		},
	)
}
