package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var (
	_ resource.ResourceWithConfigure   = &userVisibleAppResource{}
	_ resource.ResourceWithImportState = &userVisibleAppResource{}
	_ resource.ResourceWithIdentity    = &userVisibleAppResource{}
)

type userVisibleAppResource struct {
	resourceIdentity
	client *asc.Client
}

type userVisibleAppModel struct {
	UserEmail types.String `tfsdk:"user_email"`
	AppID     types.String `tfsdk:"app_id"`
	UserID    types.String `tfsdk:"user_id"`
}

func newUserVisibleAppResource() resource.Resource {
	return &userVisibleAppResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "user_id", description: "App Store Connect ID of the user.", state: "user_id"},
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
	}}}
}

func (r *userVisibleAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_visible_app"
}

func (r *userVisibleAppResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Gives a team member access to one app. Each resource manages one app, so several " +
			"configurations can grant their own apps to the same user. The user needs `all_apps_visible = false`, and " +
			"must have accepted their invitation: until then, applying fails and the next apply after they accept " +
			"succeeds. Destroying it removes access to this app only. Import it with `<user email or ID>/<app_id>`.",
		Attributes: map[string]schema.Attribute{
			"user_email": replacedString("Email address of the user, e.g. `apple_user.dev.email`.",
				stringvalidator.RegexMatches(emailRegexp, "must be an email address")),
			"app_id": replacedString("App Store Connect ID of the app."),
			"user_id": schema.StringAttribute{
				MarkdownDescription: "App Store Connect ID of the user.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *userVisibleAppResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

// ImportState accepts "<user email or ID>/<app_id>" or the identity.
func (r *userVisibleAppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := r.importParts(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.ID != "" {
		user, err := r.lookup(ctx, parts[0])
		if err != nil {
			resp.Diagnostics.AddError("Cannot import", fmt.Sprintf("No user has the ID or email %q: %v", parts[0], err))
			return
		}
		parts[0] = user.Id
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), parts[1])...)
}

// lookup finds a user by ID or email.
func (r *userVisibleAppResource) lookup(ctx context.Context, key string) (*gen.User, error) {
	out, err := r.client.UsersGetInstanceWithResponse(ctx, key, nil)
	err = asc.Err(out, err)
	if err == nil {
		return &out.JSON200.Data, nil
	}
	if !asc.IsNotFound(err) {
		return nil, err
	}
	return userByEmail(ctx, r.client, key)
}

func appLinkage(appID string) gen.UserVisibleAppsLinkagesRequest {
	var body gen.UserVisibleAppsLinkagesRequest
	e := asc.Push(&body.Data)
	e.Id, e.Type = appID, gen.UserVisibleAppsLinkagesRequestDataTypeApps
	return body
}

func userVisibleAppIDs(ctx context.Context, c *asc.Client, userID string) ([]string, error) {
	out, err := c.UsersVisibleAppsGetToManyRelationshipWithResponse(ctx, userID,
		&gen.UsersVisibleAppsGetToManyRelationshipParams{Limit: new(linkageLimit)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.UserVisibleAppsLinkagesResponse) []string {
			var ids []string
			for _, d := range p.Data {
				ids = append(ids, d.Id)
			}
			return ids
		},
		func(p *gen.UserVisibleAppsLinkagesResponse) *string { return p.Links.Next })
}

func (r *userVisibleAppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan userVisibleAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	email := plan.UserEmail.ValueString()
	user, err := userByEmail(ctx, r.client, email)
	if errors.Is(err, errNoMatch) {
		resp.Diagnostics.AddAttributeError(path.Root("user_email"), "User has not joined the team",
			email+" is not a team member yet. App access can be granted once they accept their invitation; apply again after that.")
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "look up user", err)
		return
	}
	out, err := r.client.UsersVisibleAppsCreateToManyRelationshipWithResponse(ctx, user.Id, appLinkage(plan.AppID.ValueString()))
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "give user access to app", err)
		return
	}
	plan.UserID = types.StringValue(user.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userVisibleAppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state userVisibleAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.UsersGetInstanceWithResponse(ctx, state.UserID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read user", err)
		return
	}
	apps, err := userVisibleAppIDs(ctx, r.client, state.UserID.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "read user apps", err)
		return
	}
	if !slices.Contains(apps, state.AppID.ValueString()) {
		resp.State.RemoveResource(ctx)
		return
	}
	if state.UserEmail.IsNull() {
		state.UserEmail = stringValue(asc.Value(out.JSON200.Data.Attributes).Username)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument forces replacement.
func (r *userVisibleAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.Append(resp.State.Set(ctx, req.Plan.Raw)...)
}

func (r *userVisibleAppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userVisibleAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.UsersVisibleAppsDeleteToManyRelationshipWithResponse(ctx, state.UserID.ValueString(), appLinkage(state.AppID.ValueString()))
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "remove user access to app", err)
	}
}
