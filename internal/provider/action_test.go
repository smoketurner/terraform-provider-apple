package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// requiresActions skips Terraform versions without actions.
var requiresActions = []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)}

// triggerAction runs the named actions after terraform_data.trigger is
// created and each time its input changes.
func triggerAction(input string, actions ...string) string {
	for i, a := range actions {
		actions[i] = "action." + a
	}
	return `
resource "terraform_data" "trigger" {
  input = "` + input + `"
  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [` + strings.Join(actions, ", ") + `]
    }
  }
}
`
}

func (e *testEnv) actionCase(steps ...resource.TestStep) {
	e.t.Helper()
	e.run(resource.TestCase{TerraformVersionChecks: requiresActions, Steps: steps})
}

func (e *testEnv) checkBuild(buildID string, wantTesters, wantGroups []string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		testers, groups := e.fake.BuildTesters(buildID)
		resolve := func(refs []string) []string {
			var out []string
			// A reference is a resource address, for its ID, or
			// "<address>.<attribute>".
			for _, r := range refs {
				if rs, ok := s.RootModule().Resources[r]; ok {
					out = append(out, rs.Primary.ID)
					continue
				}
				i := strings.LastIndex(r, ".")
				rs, ok := s.RootModule().Resources[r[:i]]
				if !ok {
					return []string{"missing " + r}
				}
				out = append(out, rs.Primary.Attributes[r[i+1:]])
			}
			slices.Sort(out)
			return out
		}
		if want := resolve(wantTesters); !slices.Equal(testers, want) {
			return fmt.Errorf("build testers = %v, want %v", testers, want)
		}
		if want := resolve(wantGroups); !slices.Equal(groups, want) {
			return fmt.Errorf("build groups = %v, want %v", groups, want)
		}
		return nil
	}
}

func (e *testEnv) checkBuildStates(buildID string, internal gen.InternalBetaState, external gen.ExternalBetaState) resource.TestCheckFunc {
	return func(*terraform.State) error {
		d, err := findBuildBetaDetail(context.Background(), e.client, buildID)
		if err != nil {
			return err
		}
		if *d.Attributes.InternalBuildState != internal || *d.Attributes.ExternalBuildState != external {
			return fmt.Errorf("build states = %s/%s, want %s/%s", *d.Attributes.InternalBuildState, *d.Attributes.ExternalBuildState, internal, external)
		}
		return nil
	}
}

func TestAccBuildDistributionActions(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "42", Marketing: "1.0"})
	base := locals + internalGroup + externalGroup("") + testerConfig("ann", "apple_beta_group.internal.id") + fmt.Sprintf(`
locals {
  build_id = %q
}
action "apple_set_build_export_compliance" "no_encryption" {
  config {
    build_id                   = local.build_id
    uses_non_exempt_encryption = false
  }
}
action "apple_add_build_to_beta_groups" "both" {
  config {
    build_id       = local.build_id
    beta_group_ids = [apple_beta_group.internal.id, apple_beta_group.external.id]
  }
}
action "apple_remove_build_from_beta_groups" "external" {
  config {
    build_id       = local.build_id
    beta_group_ids = [apple_beta_group.external.id]
  }
}
action "apple_add_build_testers" "ann" {
  config {
    build_id        = local.build_id
    beta_tester_ids = [apple_beta_group_tester.ann.beta_tester_id]
  }
}
action "apple_remove_build_testers" "ann" {
  config {
    build_id        = local.build_id
    beta_tester_ids = [apple_beta_group_tester.ann.beta_tester_id]
  }
}
action "apple_submit_beta_review" "build" {
  config {
    build_id = local.build_id
  }
}
action "apple_notify_beta_testers" "build" {
  config {
    build_id = local.build_id
  }
}
action "apple_invite_beta_tester" "ann" {
  config {
    app_id         = local.app_id
    beta_tester_id = apple_beta_group_tester.ann.beta_tester_id
  }
}
action "apple_expire_build" "build" {
  config {
    build_id = local.build_id
  }
}
action "apple_delete_beta_tester" "ann" {
  config {
    beta_tester_id = apple_beta_group_tester.ann.beta_tester_id
  }
}
action "apple_remove_beta_testers_from_app" "ann" {
  config {
    app_id          = local.app_id
    beta_tester_ids = [apple_beta_group_tester.ann.beta_tester_id]
  }
}
`, buildID)
	env.actionCase(
		resource.TestStep{
			// Without export compliance, beta review is refused.
			Config:      base + triggerAction("1", "apple_submit_beta_review.build"),
			ExpectError: regexp.MustCompile(`MISSING_EXPORT_COMPLIANCE`),
		},
		resource.TestStep{
			Config: base + triggerAction("2", "apple_set_build_export_compliance.no_encryption", "apple_add_build_to_beta_groups.both",
				"apple_add_build_testers.ann", "apple_submit_beta_review.build") + `
data "apple_beta_testers" "build" {
  build_id   = local.build_id
  depends_on = [terraform_data.trigger]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				env.checkBuild(buildID, []string{"apple_beta_group_tester.ann.beta_tester_id"}, []string{"apple_beta_group.external", "apple_beta_group.internal"}),
				resource.TestCheckResourceAttr("data.apple_beta_testers.build", "beta_testers.#", "1"),
				env.checkBuildStates(buildID, gen.InternalBetaStateINBETATESTING, gen.ExternalBetaStateWAITINGFORBETAREVIEW),
			),
		},
		resource.TestStep{
			Config: base + triggerAction("3", "apple_remove_build_from_beta_groups.external", "apple_remove_build_testers.ann",
				"apple_notify_beta_testers.build", "apple_invite_beta_tester.ann"),
			Check: resource.ComposeAggregateTestCheckFunc(
				env.checkBuild(buildID, nil, []string{"apple_beta_group.internal"}),
				func(*terraform.State) error {
					if got := env.fake.Notifications(); !slices.Equal(got, []string{buildID}) {
						return fmt.Errorf("notifications = %v", got)
					}
					if got := env.fake.Invitations(); len(got) != 1 || got[0].AppID != appID {
						return fmt.Errorf("invitations = %v", got)
					}
					return nil
				},
			),
		},
		resource.TestStep{
			Config: base + triggerAction("4", "apple_remove_beta_testers_from_app.ann", "apple_expire_build.build"),
			Check: resource.ComposeAggregateTestCheckFunc(
				env.checkBuildStates(buildID, gen.InternalBetaStateEXPIRED, gen.ExternalBetaStateEXPIRED),
				// The tester left the app's groups, which the resource then
				// sees as drift.
				func(*terraform.State) error {
					groups, err := betaTesterGroupIDs(context.Background(), env.client, mustTester(t, env))
					if err != nil {
						return err
					}
					if len(groups) != 0 {
						return fmt.Errorf("tester groups = %v", groups)
					}
					return nil
				},
			),
			ExpectNonEmptyPlan: true,
		},
		resource.TestStep{
			// Notifying about an expired build fails.
			Config:      base + triggerAction("5", "apple_notify_beta_testers.build"),
			ExpectError: regexp.MustCompile(`The build has expired`),
		},
		resource.TestStep{
			// Deleting the tester removes them everywhere; the membership
			// resource then plans to add them again.
			Config: base + triggerAction("6", "apple_delete_beta_tester.ann"),
			Check: func(*terraform.State) error {
				if _, err := betaTesterByEmail(context.Background(), env.client, "ann@example.com"); !errors.Is(err, errNoMatch) {
					return fmt.Errorf("tester lookup after delete: %w, want no match", err)
				}
				return nil
			},
			ExpectNonEmptyPlan: true,
		},
		resource.TestStep{Config: base + triggerAction("6", "apple_set_build_export_compliance.no_encryption")},
	)
}

func mustTester(t *testing.T, env *testEnv) string {
	t.Helper()
	tester, err := betaTesterByEmail(context.Background(), env.client, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return tester.Id
}
