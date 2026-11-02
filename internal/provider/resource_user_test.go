package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

const devEmail = "dev@example.com"

func userConfig(roles, extra string) string {
	return `
resource "apple_user" "dev" {
  email      = "` + devEmail + `"
  first_name = "Dev"
  last_name  = "Eloper"
  roles      = [` + roles + `]
` + extra + `
}
`
}

func visibleAppConfig() string {
	return `
resource "apple_user_visible_app" "dev" {
  user_email = apple_user.dev.email
  app_id     = local.app_id
}
`
}

// checkUser checks the user's roles at App Store Connect, or that there is
// no user when roles is nil.
func (e *testEnv) checkUser(roles ...gen.UserRole) resource.TestCheckFunc {
	return func(*terraform.State) error {
		u, err := userByEmail(context.Background(), e.client, devEmail)
		if roles == nil {
			if !errors.Is(err, errNoMatch) {
				return fmt.Errorf("user still exists (%w)", err)
			}
			return nil
		}
		if err != nil {
			return err
		}
		got := slices.Clone(*u.Attributes.Roles)
		slices.Sort(got)
		slices.Sort(roles)
		if !slices.Equal(got, roles) {
			return fmt.Errorf("roles = %v, want %v", got, roles)
		}
		return nil
	}
}

func (e *testEnv) checkVisibleApps(want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		u, err := userByEmail(context.Background(), e.client, devEmail)
		if err != nil {
			return err
		}
		got, err := userVisibleAppIDs(context.Background(), e.client, u.Id)
		if err != nil {
			return err
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("visible apps = %v, want %v", got, want)
		}
		return nil
	}
}

func TestAccUser(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	limited := "all_apps_visible = false"
	var invitation string
	env.run(resource.TestCase{
		CheckDestroy: env.checkUser(),
		Steps: []resource.TestStep{
			{
				Config:      userConfig(`"ADMIN"`, limited),
				ExpectError: regexp.MustCompile(`ADMIN users can see every app, so all_apps_visible cannot be false`),
			},
			{
				Config:      userConfig(`"ACCOUNT_HOLDER"`, ""),
				ExpectError: regexp.MustCompile(`value must be one of`),
			},
			{
				Config: userConfig(`"DEVELOPER"`, limited),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_user.dev", "status", "INVITED"),
					resource.TestCheckNoResourceAttr("apple_user.dev", "user_id"),
					resource.TestCheckResourceAttr("apple_user.dev", "all_apps_visible", "false"),
					resource.TestCheckResourceAttrSet("apple_user.dev", "invitation_expiration_date"),
					captureAttr("apple_user.dev", "invitation_id", &invitation),
				),
			},
			{
				ResourceName: "apple_user.dev", ImportState: true, ImportStateVerify: true, ImportStateId: devEmail,
				ImportStateVerifyIdentifierAttribute: "email",
			},
			{
				// App access needs the invitation accepted.
				Config:      locals + userConfig(`"DEVELOPER"`, limited) + visibleAppConfig(),
				ExpectError: regexp.MustCompile(`is not a team member yet`),
			},
			{
				// A pending invitation cannot be changed, so it is sent again.
				Config: userConfig(`"DEVELOPER", "MARKETING"`, limited),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_user.dev", "status", "INVITED"),
					resource.TestCheckResourceAttr("apple_user.dev", "roles.#", "2"),
					func(s *terraform.State) error {
						if s.RootModule().Resources["apple_user.dev"].Primary.Attributes["invitation_id"] == invitation {
							return errors.New("the invitation was not sent again")
						}
						return nil
					},
				),
			},
			{
				// Once accepted, the resource manages the user.
				PreConfig: func() {
					if env.fake.AcceptInvitation(devEmail) == "" {
						t.Fatal("no invitation to accept")
					}
				},
				Config: locals + userConfig(`"DEVELOPER", "MARKETING"`, limited) + visibleAppConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_user.dev", "status", "ACTIVE"),
					resource.TestCheckResourceAttrSet("apple_user.dev", "user_id"),
					resource.TestCheckNoResourceAttr("apple_user.dev", "invitation_id"),
					resource.TestCheckResourceAttrPair("apple_user_visible_app.dev", "user_id", "apple_user.dev", "user_id"),
					env.checkVisibleApps(appID),
				),
			},
			{
				ResourceName: "apple_user_visible_app.dev", ImportState: true, ImportStateVerify: true,
				ImportStateId:                        devEmail + "/" + appID,
				ImportStateVerifyIdentifierAttribute: "user_id",
			},
			{
				// Roles change in place.
				Config:           locals + userConfig(`"APP_MANAGER"`, limited) + visibleAppConfig(),
				ConfigPlanChecks: expectAction("apple_user.dev", plancheck.ResourceActionUpdate),
				Check:            env.checkUser(gen.UserRoleAPPMANAGER),
			},
			{
				// App access removed outside Terraform is granted again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					u, err := userByEmail(ctx, c, devEmail)
					if err != nil {
						return err
					}
					out, err := c.UsersVisibleAppsDeleteToManyRelationshipWithResponse(ctx, u.Id, appLinkage(appID))
					return asc.Err(out, err)
				}),
				Config:           locals + userConfig(`"APP_MANAGER"`, limited) + visibleAppConfig(),
				ConfigPlanChecks: expectAction("apple_user_visible_app.dev", plancheck.ResourceActionCreate),
				Check:            env.checkVisibleApps(appID),
			},
			{
				// Destroying the grant removes access to that app only.
				Config: locals + userConfig(`"APP_MANAGER"`, limited),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkVisibleApps(),
					env.checkUser(gen.UserRoleAPPMANAGER),
				),
			},
			{
				// Roles that see all apps.
				Config: userConfig(`"ADMIN"`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkUser(gen.UserRoleADMIN),
					resource.TestCheckResourceAttr("apple_user.dev", "all_apps_visible", "true"),
				),
			},
		},
	})
}

func TestAccUserInvitationExpiry(t *testing.T) {
	env := newTestEnv(t)
	cfg := userConfig(`"DEVELOPER"`, "")
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if _, err := pendingInvitation(context.Background(), env.client, devEmail, time.Now()); !errors.Is(err, errNoMatch) {
				return fmt.Errorf("invitation not cancelled: %w", err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				// An expired invitation is sent again.
				PreConfig:        func() { env.fake.ExpireInvitation(devEmail) },
				Config:           cfg,
				ConfigPlanChecks: expectAction("apple_user.dev", plancheck.ResourceActionCreate),
				Check:            resource.TestCheckResourceAttr("apple_user.dev", "status", "INVITED"),
			},
		},
	})
}

// TestAccUserAdopt checks that an existing team member is adopted.
func TestAccUserAdopt(t *testing.T) {
	env := newTestEnv(t)
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: userConfig(`"DEVELOPER"`, ""),
			},
			{
				PreConfig: func() { env.fake.AcceptInvitation(devEmail) },
				Config:    userConfig(`"DEVELOPER"`, ""),
				Check:     resource.TestCheckResourceAttr("apple_user.dev", "status", "ACTIVE"),
			},
			{
				// Forget the user, then adopt them again.
				Config: `
removed {
  from = apple_user.dev
  lifecycle {
    destroy = false
  }
}
`,
			},
			{
				Config: userConfig(`"DEVELOPER", "SALES"`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_user.dev", "status", "ACTIVE"),
					env.checkUser(gen.UserRoleDEVELOPER, gen.UserRoleSALES),
				),
			},
		},
	})
}

func TestAccUserDataSources(t *testing.T) {
	env := newTestEnv(t)
	env.run(resource.TestCase{
		Steps: []resource.TestStep{{
			Config: userConfig(`"DEVELOPER"`, "") + `
data "apple_user" "holder" {
  email = "HOLDER@example.com"
}
data "apple_users" "admins" {
  roles = ["ADMIN"]
}
data "apple_user_invitations" "dev" {
  emails     = [apple_user.dev.email]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.apple_user.holder", "first_name", "Account"),
				resource.TestCheckTypeSetElemAttr("data.apple_user.holder", "roles.*", "ACCOUNT_HOLDER"),
				resource.TestCheckResourceAttr("data.apple_users.admins", "users.#", "1"),
				resource.TestCheckResourceAttr("data.apple_user_invitations.dev", "invitations.#", "1"),
				resource.TestCheckResourceAttr("data.apple_user_invitations.dev", "invitations.0.roles.0", "DEVELOPER"),
			),
		}},
	})
}
