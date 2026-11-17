package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center leaderboard sets
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center-leaderboard-sets).
// A set groups leaderboards of an app or of a group. Its localizations and
// their images belong to versions, which go through App Review; the
// provider hides the versions, as it does for in-app purchases.

var setMetadata = metadataVersions{
	noun: "Game Center leaderboard set",
	list: func(ctx context.Context, c *asc.Client, setID string) ([]versionInfo, error) {
		out, err := c.GameCenterLeaderboardSetsV2VersionsGetToManyRelatedWithResponse(ctx, setID,
			&gen.GameCenterLeaderboardSetsV2VersionsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterLeaderboardSetVersionsV2Response) []gen.GameCenterLeaderboardSetVersionV2 {
				return p.Data
			},
			func(p *gen.GameCenterLeaderboardSetVersionsV2Response) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		versions := make([]versionInfo, len(all))
		for i, v := range all {
			a := asc.Value(v.Attributes)
			versions[i] = versionInfo{id: v.Id, number: asc.Value(a.Version), state: string(asc.Value(a.State))}
		}
		return versions, nil
	},
	create: func(ctx context.Context, c *asc.Client, setID string) (string, error) {
		var body gen.GameCenterLeaderboardSetVersionV2CreateRequest
		body.Data.Type = gen.GameCenterLeaderboardSetVersionV2CreateRequestDataTypeGameCenterLeaderboardSetVersions
		d := &body.Data.Relationships.LeaderboardSet.Data
		d.Id, d.Type = setID, gen.GameCenterLeaderboardSetVersionV2CreateRequestDataRelationshipsLeaderboardSetDataTypeGameCenterLeaderboardSets
		out, err := c.GameCenterLeaderboardSetVersionsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

// ---- apple_game_center_leaderboard_set ----

var (
	_ resource.ResourceWithConfigure        = &leaderboardSetResource{}
	_ resource.ResourceWithImportState      = &leaderboardSetResource{}
	_ resource.ResourceWithIdentity         = &leaderboardSetResource{}
	_ resource.ResourceWithConfigValidators = &leaderboardSetResource{}
)

type leaderboardSetResource struct {
	resourceIdentity
	client *asc.Client
}

type leaderboardSetModel struct {
	DetailID         types.String `tfsdk:"game_center_detail_id"`
	GroupID          types.String `tfsdk:"game_center_group_id"`
	ReferenceName    types.String `tfsdk:"reference_name"`
	VendorIdentifier types.String `tfsdk:"vendor_identifier"`
	ID               types.String `tfsdk:"id"`
}

func newLeaderboardSetResource() resource.Resource {
	return &leaderboardSetResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the Game Center leaderboard set.")}}
}

func (r *leaderboardSetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_leaderboard_set"
}

func (r *leaderboardSetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center leaderboard set, which groups leaderboards of an app or of a group of " +
			"apps. Add leaderboards with `apple_game_center_leaderboard_set_member`, its name with " +
			"`apple_game_center_leaderboard_set_localization` and its image with " +
			"`apple_game_center_leaderboard_set_image`, then submit it with `apple_submit_game_center_leaderboard_set`. " +
			"Destroying the resource deletes the set; one that has been live can't be deleted and stays in App Store " +
			"Connect. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"game_center_detail_id": replacedOptionalString("App Store Connect ID of the app's Game Center detail, e.g. " +
				"`apple_game_center_detail.example.id`. Set this or `game_center_group_id`."),
			"game_center_group_id": replacedOptionalString("App Store Connect ID of a Game Center group, for a set its apps " +
				"share. Set this or `game_center_detail_id`."),
			"reference_name":    schema.StringAttribute{MarkdownDescription: "Name of the set in App Store Connect.", Required: true},
			"vendor_identifier": replacedString("Identifier of the set, e.g. `com.example.seasons`. Changing it creates a new set."),
			"id":                idAttribute("App Store Connect ID of the Game Center leaderboard set."),
		},
	}
}

func (r *leaderboardSetResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id"))}
}

func (r *leaderboardSetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *leaderboardSetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *leaderboardSetModel) apply(set *gen.GameCenterLeaderboardSetV2) {
	a := asc.Value(set.Attributes)
	m.ID, m.ReferenceName, m.VendorIdentifier = types.StringValue(set.Id), stringValue(a.ReferenceName), stringValue(a.VendorIdentifier)
	rel := set.Relationships
	if rel == nil {
		return
	}
	if rel.GameCenterDetail != nil && rel.GameCenterDetail.Data != nil {
		m.DetailID = types.StringValue(rel.GameCenterDetail.Data.Id)
	}
	if rel.GameCenterGroup != nil && rel.GameCenterGroup.Data != nil {
		m.GroupID = types.StringValue(rel.GameCenterGroup.Data.Id)
	}
}

// Create creates the set with its first version, which the generated
// request type can't express.
func (r *leaderboardSetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan leaderboardSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const version = "${version}"
	relationships := map[string]any{"versions": map[string]any{"data": []any{map[string]any{"type": "gameCenterLeaderboardSetVersions", "id": version}}}}
	if id := plan.DetailID.ValueString(); id != "" {
		relationships["gameCenterDetail"] = linkage("gameCenterDetails", id)
	} else {
		relationships["gameCenterGroup"] = linkage("gameCenterGroups", plan.GroupID.ValueString())
	}
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"type":          "gameCenterLeaderboardSets",
			"attributes":    map[string]any{"referenceName": plan.ReferenceName.ValueString(), "vendorIdentifier": plan.VendorIdentifier.ValueString()},
			"relationships": relationships,
		},
		"included": []any{map[string]any{"type": "gameCenterLeaderboardSetVersions", "id": version}},
	})
	if err == nil {
		out, e := r.client.GameCenterLeaderboardSetsV2CreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON201.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create Game Center leaderboard set", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *leaderboardSetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state leaderboardSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardSetsV2GetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.GameCenterLeaderboardSetsV2GetInstanceParams{
		Include: &[]gen.GameCenterLeaderboardSetsV2GetInstanceParamsInclude{
			gen.GameCenterLeaderboardSetsV2GetInstanceParamsIncludeGameCenterDetail, gen.GameCenterLeaderboardSetsV2GetInstanceParamsIncludeGameCenterGroup,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center leaderboard set", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *leaderboardSetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan leaderboardSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterLeaderboardSetV2UpdateRequest
	body.Data.Type, body.Data.Id = gen.GameCenterLeaderboardSetV2UpdateRequestDataTypeGameCenterLeaderboardSets, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).ReferenceName = new(plan.ReferenceName.ValueString())
	out, err := r.client.GameCenterLeaderboardSetsV2UpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update Game Center leaderboard set", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves a set that has been live, which App Store Connect refuses
// to delete and which, unlike a leaderboard, can't be archived.
func (r *leaderboardSetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state leaderboardSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardSetsV2DeleteInstanceWithResponse(ctx, state.ID.ValueString())
	err = asc.Err(out, err)
	if asc.HasCode(err, "STATE_ERROR") {
		resp.Diagnostics.AddWarning("Game Center leaderboard set left in place",
			"The leaderboard set "+state.ID.ValueString()+" has been live and can't be deleted, so it stays in App Store Connect.")
		return
	}
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Game Center leaderboard set", err)
	}
}

// ---- apple_game_center_leaderboard_set_localization ----

func setLocalization(l *gen.GameCenterLeaderboardSetLocalizationV2) localization {
	a := asc.Value(l.Attributes)
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: map[string]*string{"name": a.Name}}
}

var setLocalizationAPI = localizationAPI{
	typeName: "game_center_leaderboard_set_localization",
	description: "Sets the name of a Game Center leaderboard set for one locale; `apple_game_center_leaderboard_set_image` " +
		"adds the locale's image.",
	parent:     "leaderboard_set_id",
	parentDesc: "App Store Connect ID of the leaderboard set, e.g. `apple_game_center_leaderboard_set.example.id`.",
	versions:   setMetadata,
	fields:     []localizationField{{attr: "name", json: "name", desc: "Name of the leaderboard set.", required: true, min: 1, max: 255}},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.GameCenterLeaderboardSetVersionsV2LocalizationsGetToManyRelatedWithResponse(ctx, versionID,
			&gen.GameCenterLeaderboardSetVersionsV2LocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterLeaderboardSetLocalizationsV2Response) []gen.GameCenterLeaderboardSetLocalizationV2 {
				return p.Data
			},
			func(p *gen.GameCenterLeaderboardSetLocalizationsV2Response) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		localizations := make([]localization, len(all))
		for i := range all {
			localizations[i] = setLocalization(&all[i])
		}
		return localizations, nil
	},
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		var body gen.GameCenterLeaderboardSetLocalizationV2CreateRequest
		body.Data.Type = gen.GameCenterLeaderboardSetLocalizationV2CreateRequestDataTypeGameCenterLeaderboardSetLocalizations
		body.Data.Attributes.Locale, body.Data.Attributes.Name = locale, asc.Value(values["name"])
		v := &body.Data.Relationships.Version.Data
		v.Id, v.Type = versionID, gen.GameCenterLeaderboardSetLocalizationV2CreateRequestDataRelationshipsVersionDataTypeGameCenterLeaderboardSetVersions
		out, err := c.GameCenterLeaderboardSetLocalizationsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(setLocalization(&out.JSON201.Data)), nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		var body gen.GameCenterLeaderboardSetLocalizationV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.GameCenterLeaderboardSetLocalizationV2UpdateRequestDataTypeGameCenterLeaderboardSetLocalizations, id
		asc.Alloc(&body.Data.Attributes).Name = values["name"]
		out, err := c.GameCenterLeaderboardSetLocalizationsV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(setLocalization(&out.JSON200.Data)), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterLeaderboardSetLocalizationsV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var newSetLocalizationResource = newVersionedLocalizationResource(setLocalizationAPI)

// ---- apple_game_center_leaderboard_set_image ----

var newSetImageResource = newLocalizationImageResource(localizationImageAPI{
	typeName:      "game_center_leaderboard_set_image",
	noun:          "Game Center leaderboard set image",
	description:   "Uploads the image of a Game Center leaderboard set for one locale. A localization has one image; uploading another replaces it.",
	localizations: setLocalizationAPI,
	existing: func(ctx context.Context, c *asc.Client, localizationID string) (string, error) {
		out, err := c.GameCenterLeaderboardSetLocalizationsV2ImageGetToOneRelatedWithResponse(ctx, localizationID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	reserve: func(ctx context.Context, c *asc.Client, localizationID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.GameCenterLeaderboardSetImageV2CreateRequest
		body.Data.Type = gen.GameCenterLeaderboardSetImageV2CreateRequestDataTypeGameCenterLeaderboardSetImages
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		l := &body.Data.Relationships.Localization.Data
		l.Id, l.Type = localizationID, gen.GameCenterLeaderboardSetImageV2CreateRequestDataRelationshipsLocalizationDataTypeGameCenterLeaderboardSetLocalizations
		out, err := c.GameCenterLeaderboardSetImagesV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		var body gen.GameCenterLeaderboardSetImageV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.GameCenterLeaderboardSetImageV2UpdateRequestDataTypeGameCenterLeaderboardSetImages, id
		asc.Alloc(&body.Data.Attributes).Uploaded = new(true)
		out, err := c.GameCenterLeaderboardSetImagesV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterLeaderboardSetImagesV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
})

// ---- action.apple_submit_game_center_leaderboard_set ----

var newSubmitSetAction = newSubmitVersionedAction("submit_game_center_leaderboard_set", "Game Center leaderboard set",
	"apple_game_center_leaderboard_set.example.id", setMetadata,
	func(item *gen.ReviewSubmissionItemCreateRequest, versionID string) {
		v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.GameCenterLeaderboardSetVersion).Data)
		v.Id, v.Type = versionID, gen.ReviewSubmissionItemCreateRequestDataRelationshipsGameCenterLeaderboardSetVersionDataTypeGameCenterLeaderboardSetVersions
	})

// ---- apple_game_center_leaderboard_set_member ----

var (
	_ resource.ResourceWithConfigure   = &setMemberResource{}
	_ resource.ResourceWithImportState = &setMemberResource{}
	_ resource.ResourceWithIdentity    = &setMemberResource{}
)

type setMemberResource struct {
	resourceIdentity
	client *asc.Client
}

type setMemberModel struct {
	SetID         types.String `tfsdk:"leaderboard_set_id"`
	LeaderboardID types.String `tfsdk:"leaderboard_id"`
}

var setMemberIdentity = []identityAttribute{
	{name: "leaderboard_set_id", description: "App Store Connect ID of the leaderboard set.", state: "leaderboard_set_id"},
	{name: "leaderboard_id", description: "App Store Connect ID of the leaderboard.", state: "leaderboard_id"},
}

func newSetMemberResource() resource.Resource {
	return &setMemberResource{resourceIdentity: resourceIdentity{attrs: setMemberIdentity}}
}

func (r *setMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_leaderboard_set_member"
}

func (r *setMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a leaderboard to a Game Center leaderboard set of the same app or group. Import it with " +
			"`<leaderboard_set_id>/<leaderboard_id>`.",
		Attributes: map[string]schema.Attribute{
			"leaderboard_set_id": replacedString("App Store Connect ID of the leaderboard set, e.g. `apple_game_center_leaderboard_set.example.id`."),
			"leaderboard_id":     replacedString("App Store Connect ID of the leaderboard, e.g. `apple_game_center_leaderboard.example.id`."),
		},
	}
}

func (r *setMemberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *setMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *setMemberModel) linkage() gen.GameCenterLeaderboardSetV2GameCenterLeaderboardsLinkagesRequest {
	var body gen.GameCenterLeaderboardSetV2GameCenterLeaderboardsLinkagesRequest
	d := asc.Push(&body.Data)
	d.Id, d.Type = m.LeaderboardID.ValueString(), gen.GameCenterLeaderboardSetV2GameCenterLeaderboardsLinkagesRequestDataTypeGameCenterLeaderboards
	return body
}

func (r *setMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan setMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardSetsV2GameCenterLeaderboardsCreateToManyRelationshipWithResponse(ctx, plan.SetID.ValueString(), plan.linkage())
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "add leaderboard to Game Center leaderboard set", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read finds the leaderboard among the set's, with the ID filter.
func (r *setMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state setMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.LeaderboardID.ValueString()
	out, err := r.client.GameCenterLeaderboardSetsV2GameCenterLeaderboardsGetToManyRelatedWithResponse(ctx, state.SetID.ValueString(),
		&gen.GameCenterLeaderboardSetsV2GameCenterLeaderboardsGetToManyRelatedParams{FilterId: &[]string{id}, Limit: new(listAll)})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) || err == nil && !slices.ContainsFunc(out.JSON200.Data, func(l gen.GameCenterLeaderboardV2) bool { return l.Id == id }) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center leaderboard set member", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: both attributes replace the resource.
func (r *setMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *setMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state setMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardSetsV2GameCenterLeaderboardsDeleteToManyRelationshipWithResponse(ctx, state.SetID.ValueString(), state.linkage())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "remove leaderboard from Game Center leaderboard set", err)
	}
}

// ---- apple_game_center_leaderboard_set_member_localization ----

var (
	_ resource.ResourceWithConfigure   = &setMemberLocalizationResource{}
	_ resource.ResourceWithImportState = &setMemberLocalizationResource{}
	_ resource.ResourceWithIdentity    = &setMemberLocalizationResource{}
)

type setMemberLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type setMemberLocalizationModel struct {
	SetID         types.String `tfsdk:"leaderboard_set_id"`
	LeaderboardID types.String `tfsdk:"leaderboard_id"`
	Locale        types.String `tfsdk:"locale"`
	Name          types.String `tfsdk:"name"`
	ID            types.String `tfsdk:"id"`
}

func newSetMemberLocalizationResource() resource.Resource {
	return &setMemberLocalizationResource{resourceIdentity: resourceIdentity{
		attrs: append(slices.Clone(setMemberIdentity), identityAttribute{name: "locale", description: "Locale, e.g. `en-US`.", state: "locale"}),
	}}
}

func (r *setMemberLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_leaderboard_set_member_localization"
}

func (r *setMemberLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the name a leaderboard has within a Game Center leaderboard set, for one locale. The " +
			"leaderboard must be in the set; see `apple_game_center_leaderboard_set_member`. Import it with " +
			"`<leaderboard_set_id>/<leaderboard_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"leaderboard_set_id": replacedString("App Store Connect ID of the leaderboard set."),
			"leaderboard_id": replacedString("App Store Connect ID of the leaderboard, e.g. " +
				"`apple_game_center_leaderboard_set_member.example.leaderboard_id`."),
			"locale": replacedString("Locale, e.g. `en-US`.", localeValidator),
			"name":   schema.StringAttribute{MarkdownDescription: "Name of the leaderboard within the set.", Required: true},
			"id":     idAttribute("App Store Connect ID of the localization."),
		},
	}
}

func (r *setMemberLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *setMemberLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *setMemberLocalizationModel) apply(l *gen.GameCenterLeaderboardSetMemberLocalization) {
	a := asc.Value(l.Attributes)
	m.ID, m.Locale, m.Name = types.StringValue(l.Id), stringValue(a.Locale), stringValue(a.Name)
}

func (r *setMemberLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan setMemberLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterLeaderboardSetMemberLocalizationCreateRequest
	body.Data.Type = gen.GameCenterLeaderboardSetMemberLocalizationCreateRequestDataTypeGameCenterLeaderboardSetMemberLocalizations
	a := asc.Alloc(&body.Data.Attributes)
	a.Locale, a.Name = new(plan.Locale.ValueString()), new(plan.Name.ValueString())
	rel := &body.Data.Relationships
	rel.GameCenterLeaderboardSet.Data.Id, rel.GameCenterLeaderboardSet.Data.Type = plan.SetID.ValueString(),
		gen.GameCenterLeaderboardSetMemberLocalizationCreateRequestDataRelationshipsGameCenterLeaderboardSetDataTypeGameCenterLeaderboardSets
	rel.GameCenterLeaderboard.Data.Id, rel.GameCenterLeaderboard.Data.Type = plan.LeaderboardID.ValueString(),
		gen.GameCenterLeaderboardSetMemberLocalizationCreateRequestDataRelationshipsGameCenterLeaderboardDataTypeGameCenterLeaderboards
	out, err := r.client.GameCenterLeaderboardSetMemberLocalizationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create Game Center leaderboard set member localization", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read finds the localization by set, leaderboard and locale: the API can't
// read one by ID.
func (r *setMemberLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state setMemberLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardSetMemberLocalizationsGetCollectionWithResponse(ctx, &gen.GameCenterLeaderboardSetMemberLocalizationsGetCollectionParams{
		FilterGameCenterLeaderboardSet: []string{state.SetID.ValueString()}, FilterGameCenterLeaderboard: []string{state.LeaderboardID.ValueString()},
		Limit: new(listAll),
	})
	var all []gen.GameCenterLeaderboardSetMemberLocalization
	if err = asc.Err(out, err); err == nil {
		all, err = asc.All(ctx, r.client, out.JSON200,
			func(p *gen.GameCenterLeaderboardSetMemberLocalizationsResponse) []gen.GameCenterLeaderboardSetMemberLocalization {
				return p.Data
			},
			func(p *gen.GameCenterLeaderboardSetMemberLocalizationsResponse) *string { return p.Links.Next })
	}
	if err == nil {
		err = errNoMatch
		for i := range all {
			if asc.Value(asc.Value(all[i].Attributes).Locale) == state.Locale.ValueString() {
				state.apply(&all[i])
				err = nil
			}
		}
	}
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center leaderboard set member localization", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *setMemberLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan setMemberLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterLeaderboardSetMemberLocalizationUpdateRequest
	body.Data.Type, body.Data.Id = gen.GameCenterLeaderboardSetMemberLocalizationUpdateRequestDataTypeGameCenterLeaderboardSetMemberLocalizations, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).Name = new(plan.Name.ValueString())
	out, err := r.client.GameCenterLeaderboardSetMemberLocalizationsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update Game Center leaderboard set member localization", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *setMemberLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state setMemberLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardSetMemberLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Game Center leaderboard set member localization", err)
	}
}

// ---- data.apple_game_center_leaderboard_sets ----

type leaderboardSetData struct {
	ID               types.String `tfsdk:"id"`
	ReferenceName    types.String `tfsdk:"reference_name"`
	VendorIdentifier types.String `tfsdk:"vendor_identifier"`
}

type leaderboardSetsData struct {
	DetailID types.String         `tfsdk:"game_center_detail_id"`
	GroupID  types.String         `tfsdk:"game_center_group_id"`
	Sets     []leaderboardSetData `tfsdk:"leaderboard_sets"`
}

var newLeaderboardSetsDataSource = listDataSource("game_center_leaderboard_sets",
	"Lists the Game Center leaderboard sets of an app or of a group.",
	map[string]dsschema.Attribute{
		"game_center_detail_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of an app's Game Center detail. Set this or `game_center_group_id`.", Optional: true,
		},
		"game_center_group_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of a Game Center group. Set this or `game_center_detail_id`.", Optional: true,
		},
		"leaderboard_sets": listOf("The leaderboard sets.", map[string]dsschema.Attribute{
			"id":                dsComputed("App Store Connect ID of the leaderboard set."),
			"reference_name":    dsComputed("Name of the set in App Store Connect."),
			"vendor_identifier": dsComputed("Identifier of the set."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *leaderboardSetsData, diags *diag.Diagnostics) {
		var first *gen.GameCenterLeaderboardSetsV2Response
		var err error
		if id := m.DetailID.ValueString(); id != "" {
			out, e := c.GameCenterDetailsGameCenterLeaderboardSetsV2GetToManyRelatedWithResponse(ctx, id,
				&gen.GameCenterDetailsGameCenterLeaderboardSetsV2GetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		} else {
			out, e := c.GameCenterGroupsGameCenterLeaderboardSetsV2GetToManyRelatedWithResponse(ctx, m.GroupID.ValueString(),
				&gen.GameCenterGroupsGameCenterLeaderboardSetsV2GetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		}
		var all []gen.GameCenterLeaderboardSetV2
		if err == nil {
			all, err = asc.All(ctx, c, first,
				func(p *gen.GameCenterLeaderboardSetsV2Response) []gen.GameCenterLeaderboardSetV2 { return p.Data },
				func(p *gen.GameCenterLeaderboardSetsV2Response) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "Game Center detail or group", err)
			return
		}
		m.Sets = []leaderboardSetData{}
		for i := range all {
			var model leaderboardSetModel
			model.apply(&all[i])
			m.Sets = append(m.Sets, leaderboardSetData{ID: model.ID, ReferenceName: model.ReferenceName, VendorIdentifier: model.VendorIdentifier})
		}
	}, datasourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id")))
