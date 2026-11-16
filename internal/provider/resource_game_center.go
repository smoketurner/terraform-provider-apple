package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center details, groups and app versions
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center).
// A detail turns Game Center on for an app, a group shares leaderboards and
// achievements between apps, and a Game Center app version turns Game Center
// on for one App Store version.

// ---- apple_game_center_detail ----

var (
	_ resource.ResourceWithConfigure   = &gameCenterDetailResource{}
	_ resource.ResourceWithImportState = &gameCenterDetailResource{}
	_ resource.ResourceWithIdentity    = &gameCenterDetailResource{}
)

type gameCenterDetailResource struct {
	resourceIdentity
	client *asc.Client
}

type gameCenterDetailModel struct {
	AppID         types.String `tfsdk:"app_id"`
	GroupID       types.String `tfsdk:"game_center_group_id"`
	ArcadeEnabled types.Bool   `tfsdk:"arcade_enabled"`
	ID            types.String `tfsdk:"id"`
}

func newGameCenterDetailResource() resource.Resource {
	return &gameCenterDetailResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
	}}}
}

func (r *gameCenterDetailResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_detail"
}

func (r *gameCenterDetailResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Turns Game Center on for an app and puts the app in a Game Center group. An app has one " +
			"Game Center detail: the resource creates it, or manages the existing one. App Store Connect can't turn " +
			"Game Center off again, so destroying the resource only takes the app out of its group. Import it by the " +
			"app's ID.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app."),
			"game_center_group_id": optionalString("App Store Connect ID of the Game Center group the app shares leaderboards " +
				"and achievements with, e.g. `apple_game_center_group.example.id`."),
			"arcade_enabled": schema.BoolAttribute{MarkdownDescription: "Whether the app is on Apple Arcade.", Computed: true},
			"id":             idAttribute("App Store Connect ID of the Game Center detail."),
		},
	}
}

func (r *gameCenterDetailResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *gameCenterDetailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *gameCenterDetailModel) apply(d *gen.GameCenterDetail) {
	m.ID, m.ArcadeEnabled = types.StringValue(d.Id), types.BoolValue(asc.Value(asc.Value(d.Attributes).ArcadeEnabled))
	m.GroupID = types.StringNull()
	if rel := d.Relationships; rel != nil && rel.GameCenterGroup != nil && rel.GameCenterGroup.Data != nil {
		m.GroupID = types.StringValue(rel.GameCenterGroup.Data.Id)
	}
}

// appGameCenterDetail returns the Game Center detail of an app with its
// group.
func appGameCenterDetail(ctx context.Context, c *asc.Client, appID string) (*gen.GameCenterDetail, error) {
	out, err := c.AppsGameCenterDetailGetToOneRelatedWithResponse(ctx, appID, &gen.AppsGameCenterDetailGetToOneRelatedParams{
		Include: &[]gen.AppsGameCenterDetailGetToOneRelatedParamsInclude{gen.AppsGameCenterDetailGetToOneRelatedParamsIncludeGameCenterGroup},
	})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// setGroup moves the detail into a group, or out of its group for nil. The
// request is raw JSON so that the relationship's data can be null.
func (r *gameCenterDetailResource) setGroup(ctx context.Context, id string, groupID *string) (*gen.GameCenterDetail, error) {
	var group any
	if groupID != nil {
		group = map[string]any{"type": "gameCenterGroups", "id": *groupID}
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "gameCenterDetails", "id": id, "relationships": map[string]any{"gameCenterGroup": map[string]any{"data": group}},
	}})
	if err != nil {
		return nil, err
	}
	out, err := r.client.GameCenterDetailsUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// Create turns Game Center on unless it is, then sets the group.
func (r *gameCenterDetailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gameCenterDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	detail, err := appGameCenterDetail(ctx, r.client, plan.AppID.ValueString())
	if asc.IsNotFound(err) {
		var body gen.GameCenterDetailCreateRequest
		body.Data.Type = gen.GameCenterDetailCreateRequestDataTypeGameCenterDetails
		app := &body.Data.Relationships.App.Data
		app.Id, app.Type = plan.AppID.ValueString(), gen.GameCenterDetailCreateRequestDataRelationshipsAppDataTypeApps
		out, cerr := r.client.GameCenterDetailsCreateInstanceWithResponse(ctx, body)
		if err = asc.Err(out, cerr); err == nil {
			detail = &out.JSON201.Data
		}
	}
	if err == nil {
		var current gameCenterDetailModel
		current.apply(detail)
		if !current.GroupID.Equal(plan.GroupID) {
			detail, err = r.setGroup(ctx, detail.Id, knownString(plan.GroupID))
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "enable Game Center", err)
		return
	}
	group := plan.GroupID
	plan.apply(detail)
	plan.GroupID = group
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gameCenterDetailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state gameCenterDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	detail, err := appGameCenterDetail(ctx, r.client, state.AppID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center detail", err)
		return
	}
	state.apply(detail)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gameCenterDetailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gameCenterDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	detail, err := r.setGroup(ctx, plan.ID.ValueString(), knownString(plan.GroupID))
	if err != nil {
		apiError(&resp.Diagnostics, "update Game Center detail", err)
		return
	}
	group := plan.GroupID
	plan.apply(detail)
	plan.GroupID = group
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete takes the app out of its group, so that the group can be deleted:
// the API can't turn Game Center off.
func (r *gameCenterDetailResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gameCenterDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || state.GroupID.IsNull() {
		return
	}
	if _, err := r.setGroup(ctx, state.ID.ValueString(), nil); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "take the app out of its Game Center group", err)
	}
}

// ---- apple_game_center_group ----

var (
	_ resource.ResourceWithConfigure   = &gameCenterGroupResource{}
	_ resource.ResourceWithImportState = &gameCenterGroupResource{}
	_ resource.ResourceWithIdentity    = &gameCenterGroupResource{}
)

type gameCenterGroupResource struct {
	resourceIdentity
	client *asc.Client
}

type gameCenterGroupModel struct {
	ReferenceName types.String `tfsdk:"reference_name"`
	ID            types.String `tfsdk:"id"`
}

func newGameCenterGroupResource() resource.Resource {
	return &gameCenterGroupResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the Game Center group.")}}
}

func (r *gameCenterGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_group"
}

func (r *gameCenterGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center group, which shares leaderboards and achievements between apps. Put " +
			"an app in it with `apple_game_center_detail`. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"reference_name": schema.StringAttribute{MarkdownDescription: "Name of the group in App Store Connect.", Required: true},
			"id":             idAttribute("App Store Connect ID of the Game Center group."),
		},
	}
}

func (r *gameCenterGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *gameCenterGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *gameCenterGroupModel) apply(g *gen.GameCenterGroup) {
	m.ID, m.ReferenceName = types.StringValue(g.Id), stringValue(asc.Value(g.Attributes).ReferenceName)
}

func (r *gameCenterGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gameCenterGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterGroupCreateRequest
	body.Data.Type = gen.GameCenterGroupCreateRequestDataTypeGameCenterGroups
	asc.Alloc(&body.Data.Attributes).ReferenceName = new(plan.ReferenceName.ValueString())
	out, err := r.client.GameCenterGroupsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create Game Center group", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gameCenterGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state gameCenterGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterGroupsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center group", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gameCenterGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gameCenterGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterGroupUpdateRequest
	body.Data.Type, body.Data.Id = gen.GameCenterGroupUpdateRequestDataTypeGameCenterGroups, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).ReferenceName = new(plan.ReferenceName.ValueString())
	out, err := r.client.GameCenterGroupsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update Game Center group", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gameCenterGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gameCenterGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterGroupsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Game Center group", err)
	}
}

// ---- apple_game_center_app_version ----

var (
	_ resource.ResourceWithConfigure   = &gameCenterAppVersionResource{}
	_ resource.ResourceWithImportState = &gameCenterAppVersionResource{}
	_ resource.ResourceWithIdentity    = &gameCenterAppVersionResource{}
)

type gameCenterAppVersionResource struct {
	resourceIdentity
	client *asc.Client
}

type gameCenterAppVersionModel struct {
	VersionID types.String `tfsdk:"app_store_version_id"`
	Enabled   types.Bool   `tfsdk:"enabled"`
	ID        types.String `tfsdk:"id"`
}

func newGameCenterAppVersionResource() resource.Resource {
	return &gameCenterAppVersionResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_store_version_id", description: "App Store Connect ID of the App Store version.", state: "app_store_version_id"},
	}}}
}

func (r *gameCenterAppVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_app_version"
}

func (r *gameCenterAppVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Turns Game Center on or off for one App Store version of an app that has " +
			"`apple_game_center_detail`. A version has one Game Center app version: the resource creates it, or manages " +
			"the existing one. App Store Connect can't delete it, so destroying the resource leaves it as it is; set " +
			"`enabled = false` to turn Game Center off for the version. Import it by the App Store version's ID.",
		Attributes: map[string]schema.Attribute{
			"app_store_version_id": replacedString("App Store Connect ID of the App Store version, e.g. `apple_app_store_version.example.id`."),
			"enabled":              schema.BoolAttribute{MarkdownDescription: "Whether Game Center is on for the version.", Required: true},
			"id":                   idAttribute("App Store Connect ID of the Game Center app version."),
		},
	}
}

func (r *gameCenterAppVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *gameCenterAppVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *gameCenterAppVersionModel) apply(v *gen.GameCenterAppVersion) {
	m.ID, m.Enabled = types.StringValue(v.Id), types.BoolValue(asc.Value(asc.Value(v.Attributes).Enabled))
}

func (r *gameCenterAppVersionResource) existing(ctx context.Context, versionID string) (*gen.GameCenterAppVersion, error) {
	out, err := r.client.AppStoreVersionsGameCenterAppVersionGetToOneRelatedWithResponse(ctx, versionID, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *gameCenterAppVersionResource) setEnabled(ctx context.Context, id string, enabled bool) (*gen.GameCenterAppVersion, error) {
	var body gen.GameCenterAppVersionUpdateRequest
	body.Data.Type, body.Data.Id = gen.GameCenterAppVersionUpdateRequestDataTypeGameCenterAppVersions, id
	asc.Alloc(&body.Data.Attributes).Enabled = &enabled
	out, err := r.client.GameCenterAppVersionsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// Create creates the version's Game Center app version unless it has one,
// then turns it on or off.
func (r *gameCenterAppVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gameCenterAppVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	version, err := r.existing(ctx, plan.VersionID.ValueString())
	if asc.IsNotFound(err) {
		var body gen.GameCenterAppVersionCreateRequest
		body.Data.Type = gen.GameCenterAppVersionCreateRequestDataTypeGameCenterAppVersions
		v := &body.Data.Relationships.AppStoreVersion.Data
		v.Id, v.Type = plan.VersionID.ValueString(), gen.GameCenterAppVersionCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		out, cerr := r.client.GameCenterAppVersionsCreateInstanceWithResponse(ctx, body)
		if err = asc.Err(out, cerr); err == nil {
			version = &out.JSON201.Data
		}
	}
	if err == nil && asc.Value(asc.Value(version.Attributes).Enabled) != plan.Enabled.ValueBool() {
		version, err = r.setEnabled(ctx, version.Id, plan.Enabled.ValueBool())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create Game Center app version", err)
		return
	}
	plan.apply(version)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gameCenterAppVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state gameCenterAppVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	version, err := r.existing(ctx, state.VersionID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center app version", err)
		return
	}
	state.apply(version)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gameCenterAppVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gameCenterAppVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	version, err := r.setEnabled(ctx, plan.ID.ValueString(), plan.Enabled.ValueBool())
	if err != nil {
		apiError(&resp.Diagnostics, "update Game Center app version", err)
		return
	}
	plan.apply(version)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete only forgets the app version: the API has no way to delete one.
func (r *gameCenterAppVersionResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_game_center_app_version_compatibility ----

var (
	_ resource.ResourceWithConfigure   = &gameCenterCompatibilityResource{}
	_ resource.ResourceWithImportState = &gameCenterCompatibilityResource{}
	_ resource.ResourceWithIdentity    = &gameCenterCompatibilityResource{}
)

type gameCenterCompatibilityResource struct {
	resourceIdentity
	client *asc.Client
}

type gameCenterCompatibilityModel struct {
	AppVersionID types.String `tfsdk:"game_center_app_version_id"`
	CompatibleID types.String `tfsdk:"compatible_game_center_app_version_id"`
}

func newGameCenterCompatibilityResource() resource.Resource {
	return &gameCenterCompatibilityResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "game_center_app_version_id", description: "App Store Connect ID of the Game Center app version.", state: "game_center_app_version_id"},
		{
			name: "compatible_game_center_app_version_id", description: "App Store Connect ID of the compatible Game Center app version.",
			state: "compatible_game_center_app_version_id",
		},
	}}}
}

func (r *gameCenterCompatibilityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_app_version_compatibility"
}

func (r *gameCenterCompatibilityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Makes two Game Center app versions compatible, so that players on both can play multiplayer " +
			"games together. Import it with `<game_center_app_version_id>/<compatible_game_center_app_version_id>`.",
		Attributes: map[string]schema.Attribute{
			"game_center_app_version_id": replacedString("App Store Connect ID of a Game Center app version, e.g. " +
				"`apple_game_center_app_version.example.id`."),
			"compatible_game_center_app_version_id": replacedString("App Store Connect ID of the Game Center app version it is compatible with."),
		},
	}
}

func (r *gameCenterCompatibilityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *gameCenterCompatibilityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *gameCenterCompatibilityModel) linkage() gen.GameCenterAppVersionCompatibilityVersionsLinkagesRequest {
	var body gen.GameCenterAppVersionCompatibilityVersionsLinkagesRequest
	d := asc.Push(&body.Data)
	d.Id, d.Type = m.CompatibleID.ValueString(), gen.GameCenterAppVersionCompatibilityVersionsLinkagesRequestDataTypeGameCenterAppVersions
	return body
}

func (r *gameCenterCompatibilityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gameCenterCompatibilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterAppVersionsCompatibilityVersionsCreateToManyRelationshipWithResponse(ctx, plan.AppVersionID.ValueString(), plan.linkage())
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "make Game Center app versions compatible", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gameCenterCompatibilityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state gameCenterCompatibilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterAppVersionsCompatibilityVersionsGetToManyRelationshipWithResponse(ctx, state.AppVersionID.ValueString(),
		&gen.GameCenterAppVersionsCompatibilityVersionsGetToManyRelationshipParams{Limit: new(listAll)})
	var ids []string
	if err = asc.Err(out, err); err == nil {
		ids, err = asc.All(ctx, r.client, out.JSON200,
			func(p *gen.GameCenterAppVersionCompatibilityVersionsLinkagesResponse) []string {
				page := make([]string, len(p.Data))
				for i, d := range p.Data {
					page[i] = d.Id
				}
				return page
			},
			func(p *gen.GameCenterAppVersionCompatibilityVersionsLinkagesResponse) *string { return p.Links.Next })
	}
	if asc.IsNotFound(err) || err == nil && !slices.Contains(ids, state.CompatibleID.ValueString()) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center app version compatibility", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: both attributes replace the resource.
func (r *gameCenterCompatibilityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *gameCenterCompatibilityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gameCenterCompatibilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterAppVersionsCompatibilityVersionsDeleteToManyRelationshipWithResponse(ctx, state.AppVersionID.ValueString(), state.linkage())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "remove Game Center app version compatibility", err)
	}
}

// ---- data sources ----

type gameCenterDetailData struct {
	AppID         types.String `tfsdk:"app_id"`
	ID            types.String `tfsdk:"id"`
	GroupID       types.String `tfsdk:"game_center_group_id"`
	ArcadeEnabled types.Bool   `tfsdk:"arcade_enabled"`
}

var newGameCenterDetailDataSource = listDataSource("game_center_detail",
	"Reads the Game Center detail of an app, which exists once Game Center is on for the app.",
	map[string]dsschema.Attribute{
		"app_id":               dsRequired("App Store Connect ID of the app."),
		"id":                   dsComputed("App Store Connect ID of the Game Center detail."),
		"game_center_group_id": dsComputed("App Store Connect ID of the app's Game Center group. Null without one."),
		"arcade_enabled":       dsBool("Whether the app is on Apple Arcade."),
	},
	func(ctx context.Context, c *asc.Client, m *gameCenterDetailData, diags *diag.Diagnostics) {
		detail, err := appGameCenterDetail(ctx, c, m.AppID.ValueString())
		if err != nil {
			lookupError(diags, "Game Center detail", err)
			return
		}
		var model gameCenterDetailModel
		model.apply(detail)
		m.ID, m.GroupID, m.ArcadeEnabled = model.ID, model.GroupID, model.ArcadeEnabled
	})

type gameCenterGroupsData struct {
	Groups []gameCenterGroupModel `tfsdk:"groups"`
}

var newGameCenterGroupsDataSource = listDataSource("game_center_groups",
	"Lists the account's Game Center groups.",
	map[string]dsschema.Attribute{
		"groups": listOf("The groups.", map[string]dsschema.Attribute{
			"id":             dsComputed("App Store Connect ID of the group."),
			"reference_name": dsComputed("Name of the group in App Store Connect."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *gameCenterGroupsData, diags *diag.Diagnostics) {
		out, err := c.GameCenterGroupsGetCollectionWithResponse(ctx, &gen.GameCenterGroupsGetCollectionParams{Limit: new(listAll)})
		var all []gen.GameCenterGroup
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.GameCenterGroupsResponse) []gen.GameCenterGroup { return p.Data },
				func(p *gen.GameCenterGroupsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list Game Center groups", err)
			return
		}
		m.Groups = []gameCenterGroupModel{}
		for _, g := range all {
			var item gameCenterGroupModel
			item.apply(&g)
			m.Groups = append(m.Groups, item)
		}
	})

type gameCenterAppVersionData struct {
	ID        types.String `tfsdk:"id"`
	Enabled   types.Bool   `tfsdk:"enabled"`
	VersionID types.String `tfsdk:"app_store_version_id"`
}

type gameCenterAppVersionsData struct {
	DetailID types.String               `tfsdk:"game_center_detail_id"`
	Versions []gameCenterAppVersionData `tfsdk:"app_versions"`
}

var newGameCenterAppVersionsDataSource = listDataSource("game_center_app_versions",
	"Lists the Game Center app versions of an app: the App Store versions Game Center is set up for.",
	map[string]dsschema.Attribute{
		"game_center_detail_id": dsRequired("App Store Connect ID of the app's Game Center detail, e.g. `apple_game_center_detail.example.id`."),
		"app_versions": listOf("The Game Center app versions.", map[string]dsschema.Attribute{
			"id":                   dsComputed("App Store Connect ID of the Game Center app version."),
			"enabled":              dsBool("Whether Game Center is on for the version."),
			"app_store_version_id": dsComputed("App Store Connect ID of the App Store version."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *gameCenterAppVersionsData, diags *diag.Diagnostics) {
		out, err := c.GameCenterDetailsGameCenterAppVersionsGetToManyRelatedWithResponse(ctx, m.DetailID.ValueString(), &gen.GameCenterDetailsGameCenterAppVersionsGetToManyRelatedParams{
			Include: &[]gen.GameCenterDetailsGameCenterAppVersionsGetToManyRelatedParamsInclude{gen.GameCenterDetailsGameCenterAppVersionsGetToManyRelatedParamsIncludeAppStoreVersion},
			Limit:   new(listAll),
		})
		var all []gen.GameCenterAppVersion
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.GameCenterAppVersionsResponse) []gen.GameCenterAppVersion { return p.Data },
				func(p *gen.GameCenterAppVersionsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "Game Center detail", err)
			return
		}
		m.Versions = []gameCenterAppVersionData{}
		for _, v := range all {
			item := gameCenterAppVersionData{ID: types.StringValue(v.Id), Enabled: types.BoolValue(asc.Value(asc.Value(v.Attributes).Enabled)), VersionID: types.StringNull()}
			if rel := v.Relationships; rel != nil && rel.AppStoreVersion != nil && rel.AppStoreVersion.Data != nil {
				item.VersionID = types.StringValue(rel.AppStoreVersion.Data.Id)
			}
			m.Versions = append(m.Versions, item)
		}
	})
