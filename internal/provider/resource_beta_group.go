package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var (
	_ resource.ResourceWithConfigure      = &betaGroupResource{}
	_ resource.ResourceWithImportState    = &betaGroupResource{}
	_ resource.ResourceWithIdentity       = &betaGroupResource{}
	_ resource.ResourceWithValidateConfig = &betaGroupResource{}
)

type betaGroupResource struct {
	resourceIdentity
	client *asc.Client
}

type betaGroupModel struct {
	ID                     types.String `tfsdk:"id"`
	AppID                  types.String `tfsdk:"app_id"`
	Name                   types.String `tfsdk:"name"`
	Internal               types.Bool   `tfsdk:"internal"`
	HasAccessToAllBuilds   types.Bool   `tfsdk:"has_access_to_all_builds"`
	FeedbackEnabled        types.Bool   `tfsdk:"feedback_enabled"`
	PublicLinkEnabled      types.Bool   `tfsdk:"public_link_enabled"`
	PublicLinkLimitEnabled types.Bool   `tfsdk:"public_link_limit_enabled"`
	PublicLinkLimit        types.Int64  `tfsdk:"public_link_limit"`
	AppleSiliconMac        types.Bool   `tfsdk:"ios_builds_available_for_apple_silicon_mac"`
	AppleVision            types.Bool   `tfsdk:"ios_builds_available_for_apple_vision"`
	PublicLink             types.String `tfsdk:"public_link"`
	PublicLinkID           types.String `tfsdk:"public_link_id"`
	CreatedDate            types.String `tfsdk:"created_date"`
}

func newBetaGroupResource() resource.Resource {
	return &betaGroupResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the beta group.")}}
}

func (r *betaGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_group"
}

// optionalBool is an Optional+Computed boolean that keeps App Store
// Connect's value when unset.
func optionalBool(desc string, modifiers ...planmodifier.Bool) schema.BoolAttribute {
	return schema.BoolAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Computed:            true,
		PlanModifiers:       append([]planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, modifiers...),
	}
}

func (r *betaGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a TestFlight beta group. Internal groups are for App Store Connect users; external " +
			"groups are for anyone, invited by email or a public link. An app needs an internal group before it can have an " +
			"external one. Add builds to a group with the `apple_add_build_to_beta_groups` action, and testers with " +
			"`apple_beta_tester`.",
		Attributes: map[string]schema.Attribute{
			"id":     idAttribute("App Store Connect ID of the beta group."),
			"app_id": replacedString("App Store Connect ID of the app, e.g. `data.apple_app.example.id`."),
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the group.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"internal": schema.BoolAttribute{
				MarkdownDescription: "Whether the group is for internal testers (App Store Connect users). Defaults to " +
					"`false`. Changing it creates a new group.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"has_access_to_all_builds": optionalBool("Internal groups only: whether testers get every build automatically. "+
				"Changing it creates a new group.", boolplanmodifier.RequiresReplace()),
			"feedback_enabled":          optionalBool("Whether testers can send feedback through TestFlight."),
			"public_link_enabled":       optionalBool("External groups only: whether testers can join with a public link."),
			"public_link_limit_enabled": optionalBool("External groups only: whether `public_link_limit` caps the testers who join with the public link."),
			"public_link_limit": schema.Int64Attribute{
				MarkdownDescription: "External groups only: the most testers who can join with the public link (1-10,000).",
				Optional:            true,
				Computed:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 10000)},
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"ios_builds_available_for_apple_silicon_mac": optionalBool("Whether iOS builds are available to testers on Apple silicon Macs."),
			"ios_builds_available_for_apple_vision":      optionalBool("Whether iOS builds are available to testers on Apple Vision Pro."),
			"public_link":                                schema.StringAttribute{MarkdownDescription: "The public link testers join with, when enabled.", Computed: true},
			"public_link_id":                             schema.StringAttribute{MarkdownDescription: "ID of the public link, when enabled.", Computed: true},
			"created_date":                               schema.StringAttribute{MarkdownDescription: "When the group was created, in RFC 3339 format.", Computed: true, PlanModifiers: keep},
		},
	}
}

func (r *betaGroupResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg betaGroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Internal.IsUnknown() {
		return
	}
	internal := cfg.Internal.ValueBool()
	if !internal && !cfg.HasAccessToAllBuilds.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("has_access_to_all_builds"), "Internal groups only",
			"has_access_to_all_builds can only be set on internal groups.")
	}
	if internal {
		for name, v := range map[string]bool{
			"public_link_enabled":       !cfg.PublicLinkEnabled.IsNull() && !cfg.PublicLinkEnabled.IsUnknown() && cfg.PublicLinkEnabled.ValueBool(),
			"public_link_limit_enabled": !cfg.PublicLinkLimitEnabled.IsNull() && !cfg.PublicLinkLimitEnabled.IsUnknown() && cfg.PublicLinkLimitEnabled.ValueBool(),
			"public_link_limit":         !cfg.PublicLinkLimit.IsNull(),
		} {
			if v {
				resp.Diagnostics.AddAttributeError(path.Root(name), "External groups only", "Public links are only available for external groups.")
			}
		}
	}
}

func (r *betaGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *betaGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *betaGroupModel) apply(g *gen.BetaGroup) {
	m.ID = types.StringValue(g.Id)
	a := asc.Value(g.Attributes)
	m.Name = stringValue(a.Name)
	m.Internal = types.BoolValue(asc.Value(a.IsInternalGroup))
	m.HasAccessToAllBuilds = types.BoolPointerValue(a.HasAccessToAllBuilds)
	m.FeedbackEnabled = types.BoolPointerValue(a.FeedbackEnabled)
	m.PublicLinkEnabled = types.BoolPointerValue(a.PublicLinkEnabled)
	m.PublicLinkLimitEnabled = types.BoolPointerValue(a.PublicLinkLimitEnabled)
	m.AppleSiliconMac = types.BoolPointerValue(a.IosBuildsAvailableForAppleSiliconMac)
	m.AppleVision = types.BoolPointerValue(a.IosBuildsAvailableForAppleVision)
	m.PublicLink = stringValue(a.PublicLink)
	m.PublicLinkID = stringValue(a.PublicLinkId)
	m.CreatedDate = timeValue(a.CreatedDate)
	m.PublicLinkLimit = types.Int64Null()
	if a.PublicLinkLimit != nil {
		m.PublicLinkLimit = types.Int64Value(int64(*a.PublicLinkLimit))
	}
	if g.Relationships != nil && g.Relationships.App != nil && g.Relationships.App.Data != nil {
		m.AppID = types.StringValue(g.Relationships.App.Data.Id)
	}
}

// boolPtr returns a known boolean's value, or nil.
func boolPtr(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return new(v.ValueBool())
}

func intPtr(v types.Int64) *int {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return new(int(v.ValueInt64()))
}

// update sends the group's updatable attributes from m.
func (r *betaGroupResource) update(ctx context.Context, m *betaGroupModel) (*gen.BetaGroup, error) {
	var body gen.BetaGroupUpdateRequest
	body.Data.Type, body.Data.Id = gen.BetaGroupUpdateRequestDataTypeBetaGroups, m.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.Name = new(m.Name.ValueString())
	a.FeedbackEnabled = boolPtr(m.FeedbackEnabled)
	a.IosBuildsAvailableForAppleSiliconMac = boolPtr(m.AppleSiliconMac)
	a.IosBuildsAvailableForAppleVision = boolPtr(m.AppleVision)
	if !m.Internal.ValueBool() {
		a.PublicLinkEnabled = boolPtr(m.PublicLinkEnabled)
		a.PublicLinkLimitEnabled = boolPtr(m.PublicLinkLimitEnabled)
		a.PublicLinkLimit = intPtr(m.PublicLinkLimit)
	}
	out, err := r.client.BetaGroupsUpdateInstanceWithResponse(ctx, m.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *betaGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaGroupCreateRequest
	body.Data.Type = gen.BetaGroupCreateRequestDataTypeBetaGroups
	a := &body.Data.Attributes
	a.Name, a.IsInternalGroup = plan.Name.ValueString(), new(plan.Internal.ValueBool())
	a.FeedbackEnabled = boolPtr(plan.FeedbackEnabled)
	if plan.Internal.ValueBool() {
		a.HasAccessToAllBuilds = boolPtr(plan.HasAccessToAllBuilds)
	} else {
		a.PublicLinkEnabled, a.PublicLinkLimitEnabled = boolPtr(plan.PublicLinkEnabled), boolPtr(plan.PublicLinkLimitEnabled)
		a.PublicLinkLimit = intPtr(plan.PublicLinkLimit)
	}
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.BetaGroupCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.BetaGroupsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create beta group", err)
		return
	}
	group := &out.JSON201.Data
	// The Apple silicon and Vision settings can only be changed after
	// creation.
	silicon, vision := plan.AppleSiliconMac, plan.AppleVision
	plan.apply(group)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if boolPtr(silicon) != nil || boolPtr(vision) != nil {
		plan.AppleSiliconMac, plan.AppleVision = silicon, vision
		if group, err = r.update(ctx, &plan); err != nil {
			apiError(&resp.Diagnostics, "update beta group", err)
			return
		}
		plan.apply(group)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *betaGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaGroupsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.BetaGroupsGetInstanceParams{
		Include: &[]gen.BetaGroupsGetInstanceParamsInclude{gen.BetaGroupsGetInstanceParamsIncludeApp},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read beta group", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *betaGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	group, err := r.update(ctx, &plan)
	if err != nil {
		apiError(&resp.Diagnostics, "update beta group", err)
		return
	}
	appID := plan.AppID
	plan.apply(group)
	plan.AppID = appID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state betaGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaGroupsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete beta group", err)
	}
}
