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
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// testflightApp adds an app to the fake and returns HCL locals naming it.
func (e *testEnv) testflightApp() (appID, locals string) {
	appID = e.fake.AddApp("Example", "com.example.app", "EX1")
	return appID, fmt.Sprintf("locals {\n  app_id = %q\n}\n", appID)
}

func expectAction(address string, action plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, action)}}
}

const internalGroup = `
resource "apple_beta_group" "internal" {
  app_id   = local.app_id
  name     = "Team"
  internal = true
}
`

func externalGroup(extra string) string {
	return `
resource "apple_beta_group" "external" {
  app_id     = local.app_id
  name       = "Public"
  depends_on = [apple_beta_group.internal]
` + extra + `
}
`
}

func TestAccBetaGroup(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + `
resource "apple_beta_group" "internal" {
  app_id              = local.app_id
  name                = "Team"
  internal            = true
  public_link_enabled = true
}
`,
				ExpectError: regexp.MustCompile(`Public links are only available for external groups`),
			},
			{
				Config: locals + `
resource "apple_beta_group" "external" {
  app_id                   = local.app_id
  name                     = "Public"
  has_access_to_all_builds = true
}
`,
				ExpectError: regexp.MustCompile(`has_access_to_all_builds can only be set on internal groups`),
			},
			{
				Config: locals + `
resource "apple_beta_group" "external" {
  app_id = local.app_id
  name   = "Public"
}
`,
				ExpectError: regexp.MustCompile(`first create an internal\s+group`),
			},
			{
				Config: locals + internalGroup + externalGroup(`public_link_enabled = true
public_link_limit_enabled = true
public_link_limit = 50
ios_builds_available_for_apple_silicon_mac = true`),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_beta_group.external", "id", &id),
					resource.TestCheckResourceAttr("apple_beta_group.internal", "internal", "true"),
					resource.TestCheckResourceAttr("apple_beta_group.internal", "has_access_to_all_builds", "false"),
					resource.TestCheckResourceAttr("apple_beta_group.external", "internal", "false"),
					resource.TestCheckResourceAttr("apple_beta_group.external", "feedback_enabled", "true"),
					resource.TestCheckResourceAttr("apple_beta_group.external", "public_link_limit", "50"),
					resource.TestCheckResourceAttr("apple_beta_group.external", "ios_builds_available_for_apple_silicon_mac", "true"),
					resource.TestMatchResourceAttr("apple_beta_group.external", "public_link", regexp.MustCompile(`^https://testflight\.apple\.com/join/`)),
				),
			},
			{ResourceName: "apple_beta_group.external", ImportState: true, ImportStateVerify: true},
			{
				Config: locals + internalGroup + strings.Replace(externalGroup(`public_link_enabled = false
feedback_enabled = false`), `"Public"`, `"Renamed"`, 1),
				ConfigPlanChecks: expectAction("apple_beta_group.external", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_beta_group.external", "id", &id),
					resource.TestCheckResourceAttr("apple_beta_group.external", "name", "Renamed"),
					resource.TestCheckNoResourceAttr("apple_beta_group.external", "public_link"),
				),
			},
			{
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.BetaGroupsDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config: locals + internalGroup + strings.Replace(externalGroup(`public_link_enabled = false
feedback_enabled = false`), `"Public"`, `"Renamed"`, 1),
				ConfigPlanChecks: expectAction("apple_beta_group.external", plancheck.ResourceActionCreate),
			},
			{
				Config:           locals + strings.Replace(internalGroup, "internal = true", "internal = false", 1),
				ConfigPlanChecks: expectAction("apple_beta_group.internal", plancheck.ResourceActionDestroyBeforeCreate),
				ExpectError:      regexp.MustCompile(`first create an internal\s+group`),
			},
			{Config: locals + internalGroup},
		},
	})
}

// testerConfig adds ann@example.com to a beta group, as the membership
// resource name.
func testerConfig(name, group string) string {
	return `
resource "apple_beta_group_tester" "` + name + `" {
  beta_group_id = ` + group + `
  email         = "ann@example.com"
  first_name    = "Ann"
  last_name     = "Tester"
}
`
}

// testerGroups returns the IDs of ann@example.com's groups at App Store
// Connect, or nil when she does not exist.
func (e *testEnv) testerGroups() ([]string, error) {
	ctx := context.Background()
	tester, err := betaTesterByEmail(ctx, e.client, "ann@example.com")
	if errors.Is(err, errNoMatch) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return betaTesterGroupIDs(ctx, e.client, tester.Id)
}

// checkTesterGroups checks ann@example.com's groups against the IDs of the
// named resources.
func (e *testEnv) checkTesterGroups(groups ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		got, err := e.testerGroups()
		if err != nil {
			return err
		}
		var want []string
		for _, g := range groups {
			want = append(want, s.RootModule().Resources[g].Primary.ID)
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return fmt.Errorf("tester groups = %v, want %v", got, want)
		}
		return nil
	}
}

func TestAccBetaGroupTester(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	groups := internalGroup + externalGroup("")
	both := locals + groups + testerConfig("internal", "apple_beta_group.internal.id") + testerConfig("external", "apple_beta_group.external.id")
	var testerID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + groups + testerConfig("internal", "apple_beta_group.internal.id"),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_beta_group_tester.internal", "beta_tester_id", &testerID),
					resource.TestCheckResourceAttr("apple_beta_group_tester.internal", "state", "INVITED"),
					resource.TestCheckResourceAttr("apple_beta_group_tester.internal", "invite_type", "EMAIL"),
					env.checkTesterGroups("apple_beta_group.internal"),
				),
			},
			{
				// Import by email, in any case, or by tester ID. Names are
				// used only when creating a tester, so they are not read.
				ResourceName: "apple_beta_group_tester.internal", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore:              []string{"first_name", "last_name"},
				ImportStateVerifyIdentifierAttribute: "beta_tester_id",
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["apple_beta_group.internal"].Primary.ID + "/ANN@example.com", nil
				},
			},
			{
				ResourceName: "apple_beta_group_tester.internal", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore:              []string{"first_name", "last_name"},
				ImportStateVerifyIdentifierAttribute: "beta_tester_id",
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["apple_beta_group.internal"].Primary.ID + "/" + testerID, nil
				},
			},
			{
				ResourceName:  "apple_beta_group_tester.internal",
				ImportState:   true,
				ImportStateId: "G1/nobody@example.com",
				ExpectError:   regexp.MustCompile(`No beta tester has the ID or email "nobody@example.com"`),
			},
			{
				// The same tester joins a second group.
				Config: both,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_beta_group_tester.external", "beta_tester_id", &testerID),
					env.checkTesterGroups("apple_beta_group.internal", "apple_beta_group.external"),
				),
			},
			{
				// Names apply only to new testers: changing them updates
				// state alone.
				Config:           strings.Replace(both, `"Ann"`, `"Annie"`, 1),
				ConfigPlanChecks: expectAction("apple_beta_group_tester.internal", plancheck.ResourceActionUpdate),
			},
			{
				// Destroying one membership leaves the tester's other group.
				Config: locals + groups + testerConfig("external", "apple_beta_group.external.id"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_beta_group_tester.external", "beta_tester_id", &testerID),
					env.checkTesterGroups("apple_beta_group.external"),
				),
			},
			{
				// A membership removed outside Terraform is added again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					groups, err := betaTesterGroupIDs(ctx, c, testerID)
					if err != nil {
						return err
					}
					out, err := c.BetaTestersBetaGroupsDeleteToManyRelationshipWithResponse(ctx, testerID, groupLinkage(groups[0]))
					return asc.Err(out, err)
				}),
				Config:           locals + groups + testerConfig("external", "apple_beta_group.external.id"),
				ConfigPlanChecks: expectAction("apple_beta_group_tester.external", plancheck.ResourceActionCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_beta_group_tester.external", "beta_tester_id", &testerID),
					env.checkTesterGroups("apple_beta_group.external"),
				),
			},
			{
				// A tester deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.BetaTestersDeleteInstanceWithResponse(ctx, testerID)
					return asc.Err(out, err)
				}),
				Config:           locals + groups + testerConfig("external", "apple_beta_group.external.id"),
				ConfigPlanChecks: expectAction("apple_beta_group_tester.external", plancheck.ResourceActionCreate),
				Check:            env.checkTesterGroups("apple_beta_group.external"),
			},
			{
				// Moving the membership to another group replaces it.
				Config:           locals + groups + testerConfig("external", "apple_beta_group.internal.id"),
				ConfigPlanChecks: expectAction("apple_beta_group_tester.external", plancheck.ResourceActionDestroyBeforeCreate),
				Check:            env.checkTesterGroups("apple_beta_group.internal"),
			},
		},
	})
}

// TestAccBetaGroupTesterOtherApp checks that a tester another
// configuration manages for another app is adopted, and that destroying the
// membership leaves the other app's group alone.
func TestAccBetaGroupTesterOtherApp(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	otherApp := env.fake.AddApp("Other", "com.example.other", "EX2")
	var otherGroup string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			got, err := env.testerGroups()
			if err != nil {
				return err
			}
			if !slices.Equal(got, []string{otherGroup}) {
				return fmt.Errorf("after destroy, tester groups = %v, want only the other app's %s", got, otherGroup)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var group gen.BetaGroupCreateRequest
					group.Data.Type = gen.BetaGroupCreateRequestDataTypeBetaGroups
					group.Data.Attributes.Name, group.Data.Attributes.IsInternalGroup = "Other Team", new(true)
					group.Data.Relationships.App.Data.Id = otherApp
					group.Data.Relationships.App.Data.Type = gen.BetaGroupCreateRequestDataRelationshipsAppDataTypeApps
					g, err := c.BetaGroupsCreateInstanceWithResponse(ctx, group)
					if err := asc.Err(g, err); err != nil {
						return err
					}
					otherGroup = g.JSON201.Data.Id
					var body gen.BetaTesterCreateRequest
					body.Data.Type = gen.BetaTesterCreateRequestDataTypeBetaTesters
					body.Data.Attributes.Email = openapi_types.Email("ann@example.com")
					e := asc.Append(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).BetaGroups).Data)
					e.Id, e.Type = otherGroup, gen.BetaTesterCreateRequestDataRelationshipsBetaGroupsDataTypeBetaGroups
					out, err := c.BetaTestersCreateInstanceWithResponse(ctx, body)
					return asc.Err(out, err)
				}),
				Config: locals + internalGroup + testerConfig("internal", "apple_beta_group.internal.id"),
				Check: func(*terraform.State) error {
					got, err := env.testerGroups()
					if err != nil {
						return err
					}
					if len(got) != 2 || !slices.Contains(got, otherGroup) {
						return fmt.Errorf("tester groups = %v, want this app's group and %s", got, otherGroup)
					}
					return nil
				},
			},
			{Config: locals + internalGroup + testerConfig("internal", "apple_beta_group.internal.id"), PlanOnly: true},
		},
	})
}

func TestAccBetaLocalizations(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "1", Marketing: "1.0"})
	cfg := func(description, whatsNew string) string {
		return locals + fmt.Sprintf(`
resource "apple_beta_app_localization" "en" {
  app_id         = local.app_id
  locale         = "en-US"
  feedback_email = "beta@example.com"
  %s
}
resource "apple_beta_build_localization" "en" {
  build_id  = %q
  locale    = "en-US"
  %s
}
`, description, buildID, whatsNew)
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + `resource "apple_beta_app_localization" "x" {` + "\n" + `app_id = local.app_id` + "\n" + `locale = "English"` + "\n}\n",
				ExpectError: regexp.MustCompile(`must be a locale`),
			},
			{
				Config: cfg(`description = "An app"`, `whats_new = "Everything"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_beta_app_localization.en", "description", "An app"),
					resource.TestCheckResourceAttr("apple_beta_app_localization.en", "app_id", appID),
					resource.TestCheckResourceAttr("apple_beta_build_localization.en", "whats_new", "Everything"),
				),
			},
			{ResourceName: "apple_beta_app_localization.en", ImportState: true, ImportStateVerify: true},
			{ResourceName: "apple_beta_build_localization.en", ImportState: true, ImportStateVerify: true},
			{
				Config:           cfg(`description = "Changed"`, `whats_new = "Login"`),
				ConfigPlanChecks: expectAction("apple_beta_app_localization.en", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_beta_app_localization.en", "description", "Changed"),
					resource.TestCheckResourceAttr("apple_beta_build_localization.en", "whats_new", "Login"),
				),
			},
			{
				// Removing an argument clears it.
				Config: cfg("", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_beta_app_localization.en", "description"),
					resource.TestCheckNoResourceAttr("apple_beta_build_localization.en", "whats_new"),
				),
			},
			{Config: cfg("", ""), PlanOnly: true},
			{
				Config: cfg("", "") + `
resource "apple_beta_app_localization" "dup" {
  app_id = local.app_id
  locale = "en-US"
}
`,
				ExpectError: regexp.MustCompile(`already exists`),
			},
			{Config: cfg("", "")},
		},
	})
}

func TestAccBetaSingletons(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "1", Marketing: "1.0"})
	cfg := func(notes, text string, notify bool) string {
		return locals + fmt.Sprintf(`
resource "apple_beta_app_review_detail" "app" {
  app_id                = local.app_id
  contact_email         = "review@example.com"
  contact_first_name    = "Rev"
  contact_last_name     = "Iewer"
  contact_phone         = "+1 555 0100"
  demo_account_required = true
  demo_account_name     = "demo"
  demo_account_password = "s3cret"
  notes                 = %q
}
resource "apple_beta_license_agreement" "app" {
  app_id         = local.app_id
  agreement_text = %q
}
resource "apple_build_beta_detail" "build" {
  build_id            = %q
  auto_notify_enabled = %t
}
`, notes, text, buildID, notify)
	}
	checkFake := func(text string, notify bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			ctx := context.Background()
			lic, err := env.client.BetaLicenseAgreementsGetCollectionWithResponse(ctx, &gen.BetaLicenseAgreementsGetCollectionParams{FilterApp: &[]string{appID}})
			if err := asc.Err(lic, err); err != nil {
				return err
			}
			if got := *lic.JSON200.Data[0].Attributes.AgreementText; got != text {
				return fmt.Errorf("agreement text = %q, want %q", got, text)
			}
			d, err := findBuildBetaDetail(ctx, env.client, buildID)
			if err != nil {
				return err
			}
			if got := *d.Attributes.AutoNotifyEnabled; got != notify {
				return fmt.Errorf("auto notify = %v, want %v", got, notify)
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		// Destroying leaves the settings in place.
		CheckDestroy: checkFake("Be nice.", true),
		Steps: []resource.TestStep{
			{
				Config: cfg("Use the demo account.", "Agree.", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_beta_app_review_detail.app", "id", appID),
					resource.TestCheckResourceAttr("apple_beta_app_review_detail.app", "demo_account_required", "true"),
					checkFake("Agree.", false),
				),
			},
			{ResourceName: "apple_beta_app_review_detail.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID,
				ImportStateVerifyIdentifierAttribute: "app_id"},
			{ResourceName: "apple_beta_license_agreement.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID,
				ImportStateVerifyIdentifierAttribute: "app_id"},
			{ResourceName: "apple_build_beta_detail.build", ImportState: true, ImportStateVerify: true, ImportStateId: buildID,
				ImportStateVerifyIdentifierAttribute: "build_id"},
			{
				Config:           cfg("Changed.", "Be nice.", true),
				ConfigPlanChecks: expectAction("apple_beta_license_agreement.app", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_beta_app_review_detail.app", "notes", "Changed."),
					checkFake("Be nice.", true),
				),
			},
			{
				Config:      locals + `resource "apple_beta_license_agreement" "x" {` + "\n" + `app_id = "MISSING"` + "\n" + `agreement_text = "x"` + "\n}\n",
				ExpectError: regexp.MustCompile(`The app has no beta license agreement`),
			},
		},
	})
}

func TestAccBetaRecruitmentCriteria(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	cfg := func(filters string) string {
		return locals + internalGroup + externalGroup("public_link_enabled = true") + `
resource "apple_beta_recruitment_criteria" "public" {
  beta_group_id = apple_beta_group.external.id
  filters       = [` + filters + `]
}
`
	}
	iphone := `{ device_family = "IPHONE", minimum_os_version = "17.0" }`
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      cfg(`{ device_family = "TOASTER" }`),
				ExpectError: regexp.MustCompile(`device_family value must be one of`),
			},
			{
				Config: cfg(iphone),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_beta_recruitment_criteria.public", "filters.0.device_family", "IPHONE"),
					resource.TestCheckResourceAttr("apple_beta_recruitment_criteria.public", "filters.0.minimum_os_version", "17.0"),
				),
			},
			{
				ResourceName: "apple_beta_recruitment_criteria.public", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    attrIDFunc("apple_beta_group.external", "id"),
				ImportStateVerifyIdentifierAttribute: "beta_group_id",
			},
			{
				Config:           cfg(iphone + `, { device_family = "IPAD" }`),
				ConfigPlanChecks: expectAction("apple_beta_recruitment_criteria.public", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("apple_beta_recruitment_criteria.public", "filters.#", "2"),
			},
			{
				Config: locals + internalGroup + `
resource "apple_beta_recruitment_criteria" "internal" {
  beta_group_id = apple_beta_group.internal.id
  filters       = [{ device_family = "IPHONE" }]
}
`,
				ExpectError: regexp.MustCompile(`public\s+links\s+of\s+external\s+groups`),
			},
			{Config: cfg(iphone)},
		},
	})
}
