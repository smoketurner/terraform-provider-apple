package provider

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// People join the team by accepting an invitation, so apple_user is keyed by
// email: it is an invitation until it is accepted, and a user after.

// userRoles are the roles the API can assign. The Account Holder role cannot
// be assigned.
var userRoles = enum(gen.UserRoleADMIN, gen.UserRoleFINANCE, gen.UserRoleSALES, gen.UserRoleMARKETING, gen.UserRoleAPPMANAGER,
	gen.UserRoleDEVELOPER, gen.UserRoleACCESSTOREPORTS, gen.UserRoleCUSTOMERSUPPORT, gen.UserRoleCREATEAPPS,
	gen.UserRoleCLOUDMANAGEDDEVELOPERID, gen.UserRoleCLOUDMANAGEDAPPDISTRIBUTION, gen.UserRoleGENERATEINDIVIDUALKEYS)

// allAppsRoles see every app, so their app access cannot be limited.
var allAppsRoles = enum(gen.UserRoleACCOUNTHOLDER, gen.UserRoleADMIN, gen.UserRoleFINANCE, gen.UserRoleACCESSTOREPORTS)

const (
	userStatusInvited = "INVITED"
	userStatusActive  = "ACTIVE"
)

var (
	_ resource.ResourceWithConfigure      = &userResource{}
	_ resource.ResourceWithImportState    = &userResource{}
	_ resource.ResourceWithIdentity       = &userResource{}
	_ resource.ResourceWithValidateConfig = &userResource{}
)

type userResource struct {
	resourceIdentity
	client *asc.Client
}

type userModel struct {
	Email               types.String `tfsdk:"email"`
	FirstName           types.String `tfsdk:"first_name"`
	LastName            types.String `tfsdk:"last_name"`
	Roles               types.Set    `tfsdk:"roles"`
	AllAppsVisible      types.Bool   `tfsdk:"all_apps_visible"`
	ProvisioningAllowed types.Bool   `tfsdk:"provisioning_allowed"`
	Status              types.String `tfsdk:"status"`
	UserID              types.String `tfsdk:"user_id"`
	InvitationID        types.String `tfsdk:"invitation_id"`
	InvitationExpires   types.String `tfsdk:"invitation_expiration_date"`
}

func newUserResource() resource.Resource {
	return &userResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "email", description: "Email address of the user.", state: "email"},
	}}}
}

func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Invites a person to the App Store Connect team and manages their roles once they accept. " +
			"The resource is an invitation (`status = \"INVITED\"`) until the person accepts it, and then the user " +
			"(`status = \"ACTIVE\"`). An invitation expires 3 days after it is sent; the next apply after that sends a " +
			"new one. A person who is already a user or has a pending invitation is adopted.\n\n" +
			"Grant access to specific apps with `apple_user_visible_app` once the invitation is accepted. Destroying " +
			"this resource cancels the invitation or removes the user from the team. Import it by email.",
		Attributes: map[string]schema.Attribute{
			"email": replacedString("Email address of the person. Changing it invites someone else.",
				stringvalidator.RegexMatches(emailRegexp, "must be an email address")),
			"first_name": schema.StringAttribute{
				MarkdownDescription: "First name, sent with the invitation. Users manage their own names, so changing it later changes nothing in App Store Connect.",
				Required:            true,
			},
			"last_name": schema.StringAttribute{
				MarkdownDescription: "Last name, sent with the invitation, like `first_name`.",
				Required:            true,
			},
			"roles": schema.SetAttribute{
				MarkdownDescription: "Roles of the user: " + docValues(userRoles) + ". Changes are made in place; a pending " +
					"invitation is sent again with the new roles.",
				Required:    true,
				ElementType: types.StringType,
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(oneOf(userRoles))},
			},
			"all_apps_visible": schema.BoolAttribute{
				MarkdownDescription: "Whether the user can see every app. Admin, Finance and Access to Reports users always " +
					"can. Set it to `false` to limit access to the apps granted with `apple_user_visible_app`.",
				Optional: true,
				Computed: true,
			},
			"provisioning_allowed": optionalBool("Whether the user can access Certificates, Identifiers & Profiles."),
			"status":               schema.StringAttribute{MarkdownDescription: "`INVITED` until the invitation is accepted, then `ACTIVE`.", Computed: true},
			"user_id":              schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the user, once the invitation is accepted.", Computed: true},
			"invitation_id":        schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the pending invitation.", Computed: true},
			"invitation_expiration_date": schema.StringAttribute{
				MarkdownDescription: "When the pending invitation expires, in RFC 3339 format.",
				Computed:            true,
			},
		},
	}
}

func (r *userResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg userModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.AllAppsVisible.IsNull() || cfg.AllAppsVisible.IsUnknown() || cfg.AllAppsVisible.ValueBool() {
		return
	}
	for _, role := range setStrings(ctx, cfg.Roles, &resp.Diagnostics) {
		if slices.Contains(allAppsRoles, role) {
			resp.Diagnostics.AddAttributeError(path.Root("all_apps_visible"), "App access cannot be limited",
				role+" users can see every app, so all_apps_visible cannot be false.")
			return
		}
	}
}

func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func rolesOf(ctx context.Context, set types.Set) []gen.UserRole {
	var out []gen.UserRole
	for _, r := range ids(ctx, set) {
		out = append(out, gen.UserRole(r))
	}
	slices.Sort(out)
	return out
}

// rolesSet converts roles to a set of strings.
func rolesSet(roles *[]gen.UserRole) types.Set {
	elems := []attr.Value{}
	for _, r := range asc.Value(roles) {
		elems = append(elems, types.StringValue(string(r)))
	}
	return types.SetValueMust(types.StringType, elems)
}

func (m *userModel) applyUser(u *gen.User) {
	a := asc.Value(u.Attributes)
	m.Status, m.UserID = types.StringValue(userStatusActive), types.StringValue(u.Id)
	m.InvitationID, m.InvitationExpires = types.StringNull(), types.StringNull()
	m.Roles = rolesSet(a.Roles)
	m.AllAppsVisible, m.ProvisioningAllowed = types.BoolPointerValue(a.AllAppsVisible), types.BoolPointerValue(a.ProvisioningAllowed)
}

func (m *userModel) applyInvitation(inv *gen.UserInvitation) {
	a := asc.Value(inv.Attributes)
	m.Status, m.UserID = types.StringValue(userStatusInvited), types.StringNull()
	m.InvitationID, m.InvitationExpires = types.StringValue(inv.Id), timeValue(a.ExpirationDate)
	m.Roles = rolesSet(a.Roles)
	m.AllAppsVisible, m.ProvisioningAllowed = types.BoolPointerValue(a.AllAppsVisible), types.BoolPointerValue(a.ProvisioningAllowed)
	if m.FirstName.IsNull() {
		m.FirstName = stringValue(a.FirstName)
	}
	if m.LastName.IsNull() {
		m.LastName = stringValue(a.LastName)
	}
}

// emailFilter returns the email, and its lowercase form when different, for
// API filters that may compare case-sensitively.
func emailFilter(email string) *[]string {
	filter := []string{email}
	if lower := strings.ToLower(email); lower != email {
		filter = append(filter, lower)
	}
	return &filter
}

// userByEmail finds a team member by email, compared case-insensitively.
func userByEmail(ctx context.Context, c *asc.Client, email string) (*gen.User, error) {
	out, err := c.UsersGetCollectionWithResponse(ctx, &gen.UsersGetCollectionParams{FilterUsername: emailFilter(email), Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	all, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.UsersResponse) []gen.User { return p.Data },
		func(p *gen.UsersResponse) *string { return p.Links.Next })
	if err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(asc.Value(asc.Value(all[i].Attributes).Username), email) {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

// pendingInvitation finds an unexpired invitation for email.
func pendingInvitation(ctx context.Context, c *asc.Client, email string, now time.Time) (*gen.UserInvitation, error) {
	out, err := c.UserInvitationsGetCollectionWithResponse(ctx, &gen.UserInvitationsGetCollectionParams{FilterEmail: emailFilter(email), Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	for i := range out.JSON200.Data {
		a := asc.Value(out.JSON200.Data[i].Attributes)
		if strings.EqualFold(string(asc.Value(a.Email)), email) && (a.ExpirationDate == nil || now.Before(a.ExpirationDate.Time)) {
			return &out.JSON200.Data[i], nil
		}
	}
	return nil, errNoMatch
}

func (r *userResource) invite(ctx context.Context, m *userModel) (*gen.UserInvitation, error) {
	var body gen.UserInvitationCreateRequest
	body.Data.Type = gen.UserInvitationCreateRequestDataTypeUserInvitations
	a := &body.Data.Attributes
	a.Email = openapi_types.Email(m.Email.ValueString())
	a.FirstName, a.LastName = m.FirstName.ValueString(), m.LastName.ValueString()
	a.Roles = rolesOf(ctx, m.Roles)
	a.AllAppsVisible, a.ProvisioningAllowed = boolPtr(m.AllAppsVisible), boolPtr(m.ProvisioningAllowed)
	out, err := r.client.UserInvitationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON201.Data, nil
}

func (r *userResource) cancel(ctx context.Context, invitationID string) error {
	out, err := r.client.UserInvitationsDeleteInstanceWithResponse(ctx, invitationID)
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *userResource) updateUser(ctx context.Context, id string, m *userModel) (*gen.User, error) {
	var body gen.UserUpdateRequest
	body.Data.Type, body.Data.Id = gen.UserUpdateRequestDataTypeUsers, id
	a := asc.Alloc(&body.Data.Attributes)
	a.Roles = new(rolesOf(ctx, m.Roles))
	a.AllAppsVisible, a.ProvisioningAllowed = boolPtr(m.AllAppsVisible), boolPtr(m.ProvisioningAllowed)
	out, err := r.client.UsersUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// sameInvitation reports whether a pending invitation already has the
// planned roles and access, so it can be adopted as is.
func sameInvitation(ctx context.Context, inv *gen.UserInvitation, m *userModel) bool {
	a := asc.Value(inv.Attributes)
	roles := slices.Clone(asc.Value(a.Roles))
	slices.Sort(roles)
	return slices.Equal(roles, rolesOf(ctx, m.Roles)) &&
		(m.AllAppsVisible.IsUnknown() || m.AllAppsVisible.Equal(types.BoolPointerValue(a.AllAppsVisible))) &&
		(m.ProvisioningAllowed.IsUnknown() || m.ProvisioningAllowed.Equal(types.BoolPointerValue(a.ProvisioningAllowed)))
}

// apply makes App Store Connect match plan: it updates the user, or adopts
// or sends the invitation, and records the result in plan.
func (r *userResource) apply(ctx context.Context, plan *userModel) error {
	email := plan.Email.ValueString()
	first, last := plan.FirstName, plan.LastName
	defer func() { plan.FirstName, plan.LastName = first, last }()
	user, err := userByEmail(ctx, r.client, email)
	switch {
	case err == nil:
		updated, err := r.updateUser(ctx, user.Id, plan)
		if err != nil {
			return err
		}
		plan.applyUser(updated)
		return nil
	case !errors.Is(err, errNoMatch):
		return err
	}
	inv, err := pendingInvitation(ctx, r.client, email, time.Now())
	switch {
	case err == nil && sameInvitation(ctx, inv, plan):
		plan.applyInvitation(inv)
		return nil
	case err == nil:
		// Invitations cannot be changed: send a new one.
		if err := r.cancel(ctx, inv.Id); err != nil {
			return err
		}
	case !errors.Is(err, errNoMatch):
		return err
	}
	inv, err = r.invite(ctx, plan)
	if err != nil {
		return err
	}
	plan.applyInvitation(inv)
	return nil
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "invite user", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	email := state.Email.ValueString()
	user, err := userByEmail(ctx, r.client, email)
	if err == nil {
		state.applyUser(user)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	if !errors.Is(err, errNoMatch) {
		apiError(&resp.Diagnostics, "read user", err)
		return
	}
	inv, err := pendingInvitation(ctx, r.client, email, time.Now())
	if errors.Is(err, errNoMatch) {
		// The invitation expired or was declined or cancelled.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read user invitation", err)
		return
	}
	state.applyInvitation(inv)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "update user", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete cancels the invitation or removes the user from the team.
func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	email := state.Email.ValueString()
	user, err := userByEmail(ctx, r.client, email)
	switch {
	case err == nil:
		out, err := r.client.UsersDeleteInstanceWithResponse(ctx, user.Id)
		if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
			apiError(&resp.Diagnostics, "remove user", err)
		}
		return
	case !errors.Is(err, errNoMatch):
		apiError(&resp.Diagnostics, "look up user", err)
		return
	}
	inv, err := pendingInvitation(ctx, r.client, email, time.Now())
	if err == nil {
		err = r.cancel(ctx, inv.Id)
	}
	if err != nil && !errors.Is(err, errNoMatch) {
		apiError(&resp.Diagnostics, "cancel user invitation", err)
	}
}
