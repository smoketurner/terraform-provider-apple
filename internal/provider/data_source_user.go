package provider

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var allUserRoles = append([]string{string(gen.UserRoleACCOUNTHOLDER)}, userRoles...)

type userData struct {
	ID                  types.String `tfsdk:"id"`
	Email               types.String `tfsdk:"email"`
	FirstName           types.String `tfsdk:"first_name"`
	LastName            types.String `tfsdk:"last_name"`
	Roles               types.Set    `tfsdk:"roles"`
	AllAppsVisible      types.Bool   `tfsdk:"all_apps_visible"`
	ProvisioningAllowed types.Bool   `tfsdk:"provisioning_allowed"`
}

func (m *userData) apply(u *gen.User) {
	a := asc.Value(u.Attributes)
	m.ID, m.Email = types.StringValue(u.Id), stringValue(a.Username)
	m.FirstName, m.LastName = stringValue(a.FirstName), stringValue(a.LastName)
	m.Roles = rolesSet(a.Roles)
	m.AllAppsVisible, m.ProvisioningAllowed = types.BoolPointerValue(a.AllAppsVisible), types.BoolPointerValue(a.ProvisioningAllowed)
}

var userDataAttributes = map[string]schema.Attribute{
	"id":                   dsComputed("App Store Connect ID of the user."),
	"email":                dsComputed("Email address the user signs in with."),
	"first_name":           dsComputed("First name of the user."),
	"last_name":            dsComputed("Last name of the user."),
	"roles":                dsComputedSet("Roles of the user."),
	"all_apps_visible":     schema.BoolAttribute{MarkdownDescription: "Whether the user can see every app.", Computed: true},
	"provisioning_allowed": schema.BoolAttribute{MarkdownDescription: "Whether the user can access Certificates, Identifiers & Profiles.", Computed: true},
}

var newUserDataSource = lookupDataSource("user", "Looks up a team member by App Store Connect ID or email.", "email",
	withAttributes(userDataAttributes, map[string]schema.Attribute{
		"id":    dsLookup("App Store Connect ID of the user. Set this or `email`."),
		"email": dsLookup("Email address of the user, in any case. Set this or `id`."),
	}),
	func(ctx context.Context, c *asc.Client, m *userData, diags *diag.Diagnostics) {
		var found *gen.User
		var err error
		if id := m.ID.ValueString(); id != "" {
			out, e := c.UsersGetInstanceWithResponse(ctx, id, nil)
			if err = asc.Err(out, e); err == nil {
				found = &out.JSON200.Data
			}
		} else {
			found, err = userByEmail(ctx, c, m.Email.ValueString())
		}
		if err != nil {
			lookupError(diags, "user", err)
			return
		}
		m.apply(found)
	})

type usersData struct {
	Roles types.Set    `tfsdk:"roles"`
	AppID types.String `tfsdk:"app_id"`
	Users []userData   `tfsdk:"users"`
}

var newUsersDataSource = listDataSource("users", "Lists the team members that match every given filter.",
	map[string]schema.Attribute{
		"roles":  dsFilter("Roles to match: users with any of them.", allUserRoles...),
		"app_id": schema.StringAttribute{MarkdownDescription: "Only users given access to this app with `apple_user_visible_app`.", Optional: true},
		"users":  listOf("The matching users, ordered by ID.", userDataAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *usersData, diags *diag.Diagnostics) {
		params := gen.UsersGetCollectionParams{
			FilterRoles: filterParam[gen.UsersGetCollectionParamsFilterRoles](ctx, m.Roles, diags),
			Limit:       new(listAll),
		}
		if !m.AppID.IsNull() {
			params.FilterVisibleApps = &[]string{m.AppID.ValueString()}
		}
		out, err := c.UsersGetCollectionWithResponse(ctx, &params)
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "list users", err)
			return
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.UsersResponse) []gen.User { return p.Data },
			func(p *gen.UsersResponse) *string { return p.Links.Next })
		if err != nil {
			apiError(diags, "list users", err)
			return
		}
		wanted := setStrings(ctx, m.Roles, diags)
		m.Users = []userData{}
		for i := range all {
			var u userData
			u.apply(&all[i])
			if len(wanted) == 0 || anyRole(ctx, u.Roles, wanted) {
				m.Users = append(m.Users, u)
			}
		}
	})

func anyRole(ctx context.Context, roles types.Set, wanted []string) bool {
	return slices.ContainsFunc(ids(ctx, roles), func(r string) bool { return slices.Contains(wanted, r) })
}

type userInvitationData struct {
	ID             types.String `tfsdk:"id"`
	Email          types.String `tfsdk:"email"`
	Roles          types.Set    `tfsdk:"roles"`
	ExpirationDate types.String `tfsdk:"expiration_date"`
}

type userInvitationsData struct {
	Emails      types.Set            `tfsdk:"emails"`
	Invitations []userInvitationData `tfsdk:"invitations"`
}

var newUserInvitationsDataSource = listDataSource("user_invitations", "Lists invitations to join the team, including expired ones.",
	map[string]schema.Attribute{
		"emails": dsFilter("Email addresses to match."),
		"invitations": listOf("The matching invitations, ordered by ID.", map[string]schema.Attribute{
			"id":              dsComputed("App Store Connect ID of the invitation."),
			"email":           dsComputed("Email address the invitation was sent to."),
			"roles":           dsComputedSet("Roles the invitation grants."),
			"expiration_date": dsComputed("When the invitation expires, in RFC 3339 format."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *userInvitationsData, diags *diag.Diagnostics) {
		out, err := c.UserInvitationsGetCollectionWithResponse(ctx, &gen.UserInvitationsGetCollectionParams{
			FilterEmail: filterParam[string](ctx, m.Emails, diags),
			Limit:       new(listAll),
		})
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "list user invitations", err)
			return
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.UserInvitationsResponse) []gen.UserInvitation { return p.Data },
			func(p *gen.UserInvitationsResponse) *string { return p.Links.Next })
		if err != nil {
			apiError(diags, "list user invitations", err)
			return
		}
		m.Invitations = []userInvitationData{}
		for _, inv := range all {
			a := asc.Value(inv.Attributes)
			email := string(asc.Value(a.Email))
			if passes(ctx, m.Emails, email, diags) {
				m.Invitations = append(m.Invitations, userInvitationData{
					ID: types.StringValue(inv.Id), Email: types.StringValue(email), Roles: rolesSet(a.Roles), ExpirationDate: timeValue(a.ExpirationDate),
				})
			}
		}
	})
