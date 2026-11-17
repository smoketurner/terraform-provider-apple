package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center activities
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center-activities).
// An activity links players to a part of the game, and to the achievements
// and leaderboards of that part. It belongs to an app's Game Center detail
// or to a group. Its fallback URL, default image, localizations and their
// images belong to versions, which go through App Review; the provider
// hides the versions, as it does for in-app purchases.

var activityPlayStyles = enum(gen.GameCenterActivityAttributesPlayStyleASYNCHRONOUS, gen.GameCenterActivityAttributesPlayStyleSYNCHRONOUS)

// activityVersions lists an activity's versions.
func activityVersions(ctx context.Context, c *asc.Client, activityID string) ([]gen.GameCenterActivityVersion, error) {
	out, err := c.GameCenterActivitiesVersionsGetToManyRelatedWithResponse(ctx, activityID, &gen.GameCenterActivitiesVersionsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.GameCenterActivityVersionsResponse) []gen.GameCenterActivityVersion { return p.Data },
		func(p *gen.GameCenterActivityVersionsResponse) *string { return p.Links.Next })
}

var activityMetadata = metadataVersions{
	noun: "Game Center activity",
	list: func(ctx context.Context, c *asc.Client, activityID string) ([]versionInfo, error) {
		all, err := activityVersions(ctx, c, activityID)
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
	create: func(ctx context.Context, c *asc.Client, activityID string) (string, error) {
		var body gen.GameCenterActivityVersionCreateRequest
		body.Data.Type = gen.GameCenterActivityVersionCreateRequestDataTypeGameCenterActivityVersions
		d := &body.Data.Relationships.Activity.Data
		d.Id, d.Type = activityID, gen.GameCenterActivityVersionCreateRequestDataRelationshipsActivityDataTypeGameCenterActivities
		out, err := c.GameCenterActivityVersionsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

// ---- apple_game_center_activity ----

var (
	_ resource.ResourceWithConfigure        = &activityResource{}
	_ resource.ResourceWithImportState      = &activityResource{}
	_ resource.ResourceWithIdentity         = &activityResource{}
	_ resource.ResourceWithConfigValidators = &activityResource{}
)

type activityResource struct {
	resourceIdentity
	client *asc.Client
}

type activityModel struct {
	DetailID          types.String `tfsdk:"game_center_detail_id"`
	GroupID           types.String `tfsdk:"game_center_group_id"`
	ReferenceName     types.String `tfsdk:"reference_name"`
	VendorIdentifier  types.String `tfsdk:"vendor_identifier"`
	PlayStyle         types.String `tfsdk:"play_style"`
	MinimumPlayers    types.Int64  `tfsdk:"minimum_players_count"`
	MaximumPlayers    types.Int64  `tfsdk:"maximum_players_count"`
	SupportsPartyCode types.Bool   `tfsdk:"supports_party_code"`
	Archived          types.Bool   `tfsdk:"archived"`
	Properties        types.Map    `tfsdk:"properties"`
	FallbackURL       types.String `tfsdk:"fallback_url"`
	ID                types.String `tfsdk:"id"`
}

func newActivityResource() resource.Resource {
	return &activityResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the Game Center activity.")}}
}

func (r *activityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_activity"
}

func (r *activityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center activity, which links players to a part of the game, for an app or " +
			"for a group of apps. Link achievements and leaderboards with `apple_game_center_activity_achievement` and " +
			"`apple_game_center_activity_leaderboard`, add its name with `apple_game_center_activity_localization` and " +
			"its images with `apple_game_center_activity_default_image` and `apple_game_center_activity_image`, then " +
			"submit it with `apple_submit_game_center_activity`. `fallback_url` belongs to the version being prepared, " +
			"so changing it once the current version has been reviewed starts the next one. Destroying the resource " +
			"deletes the activity; one that has been live can't be deleted, so it is archived instead. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"game_center_detail_id": replacedOptionalString("App Store Connect ID of the app's Game Center detail, e.g. " +
				"`apple_game_center_detail.example.id`. Set this or `game_center_group_id`."),
			"game_center_group_id": replacedOptionalString("App Store Connect ID of a Game Center group, for an activity " +
				"its apps share. Set this or `game_center_detail_id`."),
			"reference_name":    schema.StringAttribute{MarkdownDescription: "Name of the activity in App Store Connect.", Required: true},
			"vendor_identifier": replacedString("Identifier the game knows the activity by, e.g. `com.example.daily_puzzle`. Changing it creates a new activity."),
			"play_style": optionalComputedString("How players take part: "+docValues(activityPlayStyles)+". Unset keeps App Store Connect's value.",
				oneOf(activityPlayStyles)),
			"minimum_players_count": optionalInt("Fewest players the activity takes.", int64validator.AtLeast(1)),
			"maximum_players_count": optionalInt("Most players the activity takes.", int64validator.AtLeast(1)),
			"supports_party_code":   optionalBool("Whether players can join with a party code. Unset keeps App Store Connect's value."),
			"archived": schema.BoolAttribute{
				MarkdownDescription: "Whether the activity is archived, which hides it from players. Defaults to `false`.",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"properties": schema.MapAttribute{
				MarkdownDescription: "Properties passed to the game when a player opens the activity.", Optional: true, ElementType: types.StringType,
			},
			"fallback_url": optionalString("URL that opens when the game isn't installed or doesn't handle the activity."),
			"id":           idAttribute("App Store Connect ID of the Game Center activity."),
		},
	}
}

func (r *activityResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id"))}
}

func (r *activityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *activityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// apply sets everything but the fallback URL, which belongs to a version.
func (m *activityModel) apply(activity *gen.GameCenterActivity) {
	a := asc.Value(activity.Attributes)
	m.ID, m.ReferenceName, m.VendorIdentifier = types.StringValue(activity.Id), stringValue(a.ReferenceName), stringValue(a.VendorIdentifier)
	m.PlayStyle, m.MinimumPlayers, m.MaximumPlayers = stringValue(a.PlayStyle), intValue(a.MinimumPlayersCount), intValue(a.MaximumPlayersCount)
	m.SupportsPartyCode, m.Archived, m.Properties = types.BoolPointerValue(a.SupportsPartyCode), types.BoolValue(asc.Value(a.Archived)), stringMapValue(a.Properties)
	rel := activity.Relationships
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

// attributes returns the attributes a create or an update sets. An update
// names the unset player counts and properties as null, which removes them.
func (m *activityModel) attributes(ctx context.Context, update bool, diags *diag.Diagnostics) map[string]any {
	attributes := map[string]any{"referenceName": m.ReferenceName.ValueString()}
	if update {
		attributes["archived"] = m.Archived.ValueBool()
	} else {
		attributes["vendorIdentifier"] = m.VendorIdentifier.ValueString()
	}
	if style := knownString(m.PlayStyle); style != nil {
		attributes["playStyle"] = style
	}
	if !m.SupportsPartyCode.IsNull() && !m.SupportsPartyCode.IsUnknown() {
		attributes["supportsPartyCode"] = m.SupportsPartyCode.ValueBool()
	}
	for name, count := range map[string]types.Int64{"minimumPlayersCount": m.MinimumPlayers, "maximumPlayersCount": m.MaximumPlayers} {
		if v := knownInt64(count); v != nil || update {
			attributes[name] = v
		}
	}
	if !m.Properties.IsNull() {
		properties := map[string]string{}
		diags.Append(m.Properties.ElementsAs(ctx, &properties, false)...)
		attributes["properties"] = properties
	} else if update {
		attributes["properties"] = nil
	}
	return attributes
}

// setFallbackURL sets the fallback URL of the version being prepared, as raw
// JSON so that a removed URL is sent as null.
func (r *activityResource) setFallbackURL(ctx context.Context, activityID string, url *string) error {
	versionID, err := activityMetadata.editable(ctx, r.client, activityID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "gameCenterActivityVersions", "id": versionID, "attributes": map[string]any{"fallbackUrl": url},
	}})
	if err != nil {
		return err
	}
	out, err := r.client.GameCenterActivityVersionsUpdateInstanceWithBodyWithResponse(ctx, versionID, "application/json", bytes.NewReader(body))
	return asc.Err(out, err)
}

// fallbackURL returns the fallback URL of the version to read.
func (r *activityResource) fallbackURL(ctx context.Context, activityID string) (types.String, error) {
	all, err := activityVersions(ctx, r.client, activityID)
	if err != nil {
		return types.StringNull(), err
	}
	infos := make([]versionInfo, len(all))
	for i, v := range all {
		a := asc.Value(v.Attributes)
		infos[i] = versionInfo{id: v.Id, number: asc.Value(a.Version), state: string(asc.Value(a.State))}
	}
	_, current, _ := pickVersions(infos)
	for _, v := range all {
		if current != nil && v.Id == current.id {
			return stringValue(asc.Value(v.Attributes).FallbackUrl), nil
		}
	}
	return types.StringNull(), nil
}

// Create creates the activity with its first version, then sets what a
// create can't: the version's fallback URL, and archived.
func (r *activityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan activityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	attributes := plan.attributes(ctx, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	const version = "${version}"
	relationships := map[string]any{"versions": map[string]any{"data": []any{map[string]any{"type": "gameCenterActivityVersions", "id": version}}}}
	if id := plan.DetailID.ValueString(); id != "" {
		relationships["gameCenterDetail"] = linkage("gameCenterDetails", id)
	} else {
		relationships["gameCenterGroup"] = linkage("gameCenterGroups", plan.GroupID.ValueString())
	}
	body, err := json.Marshal(map[string]any{
		"data":     map[string]any{"type": "gameCenterActivities", "attributes": attributes, "relationships": relationships},
		"included": []any{map[string]any{"type": "gameCenterActivityVersions", "id": version}},
	})
	var activity *gen.GameCenterActivity
	if err == nil {
		out, e := r.client.GameCenterActivitiesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			activity = &out.JSON201.Data
		}
	}
	state := plan
	if err == nil {
		state.ID = types.StringValue(activity.Id)
		if url := knownString(plan.FallbackURL); url != nil {
			err = r.setFallbackURL(ctx, activity.Id, url)
		}
	}
	if err == nil && plan.Archived.ValueBool() {
		activity, err = r.update(ctx, &state, &resp.Diagnostics)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create Game Center activity", err)
		// The activity may exist: record it so that the next apply finishes it.
		if activity != nil {
			state.apply(activity)
			state.Properties, state.FallbackURL = plan.Properties, types.StringNull()
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		}
		return
	}
	state.apply(activity)
	state.Properties = plan.Properties
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *activityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state activityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterActivitiesGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.GameCenterActivitiesGetInstanceParams{
		Include: &[]gen.GameCenterActivitiesGetInstanceParamsInclude{
			gen.GameCenterActivitiesGetInstanceParamsIncludeGameCenterDetail, gen.GameCenterActivitiesGetInstanceParamsIncludeGameCenterGroup,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		state.apply(&out.JSON200.Data)
		state.FallbackURL, err = r.fallbackURL(ctx, state.ID.ValueString())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center activity", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// update sends the activity's attributes as raw JSON, so that removed ones
// are sent as null.
func (r *activityResource) update(ctx context.Context, m *activityModel, diags *diag.Diagnostics) (*gen.GameCenterActivity, error) {
	id := m.ID.ValueString()
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "gameCenterActivities", "id": id, "attributes": m.attributes(ctx, true, diags),
	}})
	if err != nil {
		return nil, err
	}
	out, err := r.client.GameCenterActivitiesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *activityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, prior activityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	activity, err := r.update(ctx, &plan, &resp.Diagnostics)
	if err == nil && !plan.FallbackURL.Equal(prior.FallbackURL) {
		err = r.setFallbackURL(ctx, plan.ID.ValueString(), knownString(plan.FallbackURL))
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update Game Center activity", err)
		return
	}
	state := plan
	state.apply(activity)
	state.Properties = plan.Properties
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete archives an activity that has been live, which App Store Connect
// refuses to delete.
func (r *activityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state activityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterActivitiesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	err = asc.Err(out, err)
	if asc.HasCode(err, "STATE_ERROR") {
		state.Archived = types.BoolValue(true)
		if _, err = r.update(ctx, &state, &resp.Diagnostics); err == nil {
			resp.Diagnostics.AddWarning("Game Center activity archived",
				"The activity "+state.ID.ValueString()+" has been live and can't be deleted, so it was archived.")
		}
	}
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Game Center activity", err)
	}
}

// ---- apple_game_center_activity_localization ----

func activityLocalization(l *gen.GameCenterActivityLocalization) localization {
	a := asc.Value(l.Attributes)
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: map[string]*string{"name": a.Name, "description": a.Description}}
}

var activityLocalizationAPI = localizationAPI{
	typeName: "game_center_activity_localization",
	description: "Sets the name and description of a Game Center activity for one locale; " +
		"`apple_game_center_activity_image` adds the locale's image.",
	parent:     "activity_id",
	parentDesc: "App Store Connect ID of the activity, e.g. `apple_game_center_activity.example.id`.",
	versions:   activityMetadata,
	fields: []localizationField{
		{attr: "name", json: "name", desc: "Name of the activity.", required: true, min: 1, max: 255},
		{attr: "description", json: "description", desc: "Description of the activity.", min: 1, max: 255},
	},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.GameCenterActivityVersionsLocalizationsGetToManyRelatedWithResponse(ctx, versionID,
			&gen.GameCenterActivityVersionsLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterActivityLocalizationsResponse) []gen.GameCenterActivityLocalization {
				return p.Data
			},
			func(p *gen.GameCenterActivityLocalizationsResponse) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		localizations := make([]localization, len(all))
		for i := range all {
			localizations[i] = activityLocalization(&all[i])
		}
		return localizations, nil
	},
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		var body gen.GameCenterActivityLocalizationCreateRequest
		body.Data.Type = gen.GameCenterActivityLocalizationCreateRequestDataTypeGameCenterActivityLocalizations
		body.Data.Attributes.Locale, body.Data.Attributes.Name, body.Data.Attributes.Description = locale, asc.Value(values["name"]), values["description"]
		v := &body.Data.Relationships.Version.Data
		v.Id, v.Type = versionID, gen.GameCenterActivityLocalizationCreateRequestDataRelationshipsVersionDataTypeGameCenterActivityVersions
		out, err := c.GameCenterActivityLocalizationsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(activityLocalization(&out.JSON201.Data)), nil
	},
	// update sends raw JSON, so that a removed description is sent as null.
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		body, err := json.Marshal(map[string]any{"data": map[string]any{"type": "gameCenterActivityLocalizations", "id": id, "attributes": values}})
		if err != nil {
			return nil, err
		}
		out, err := c.GameCenterActivityLocalizationsUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(activityLocalization(&out.JSON200.Data)), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterActivityLocalizationsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var newActivityLocalizationResource = newVersionedLocalizationResource(activityLocalizationAPI)

// ---- apple_game_center_activity_image and apple_game_center_activity_default_image ----

// activityImageAPI returns the image API of an activity's localizations, or
// of its versions for the default image.
func activityImageAPI(versionLevel bool) localizationImageAPI {
	api := localizationImageAPI{
		typeName:      "game_center_activity_image",
		noun:          "Game Center activity image",
		description:   "Uploads the image of a Game Center activity for one locale. A localization has one image; uploading another replaces it.",
		localizations: activityLocalizationAPI,
		versionLevel:  versionLevel,
		existing: func(ctx context.Context, c *asc.Client, localizationID string) (string, error) {
			out, err := c.GameCenterActivityLocalizationsImageGetToOneRelatedWithResponse(ctx, localizationID, nil)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			return out.JSON200.Data.Id, nil
		},
		reserve: func(ctx context.Context, c *asc.Client, ownerID, name string, size int) (string, []gen.UploadOperation, error) {
			var body gen.GameCenterActivityImageCreateRequest
			body.Data.Type = gen.GameCenterActivityImageCreateRequestDataTypeGameCenterActivityImages
			body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
			rel := asc.Alloc(&body.Data.Relationships)
			if versionLevel {
				v := asc.Alloc(&asc.Alloc(&rel.Version).Data)
				v.Id, v.Type = ownerID, gen.GameCenterActivityImageCreateRequestDataRelationshipsVersionDataTypeGameCenterActivityVersions
			} else {
				l := asc.Alloc(&asc.Alloc(&rel.Localization).Data)
				l.Id, l.Type = ownerID, gen.GameCenterActivityImageCreateRequestDataRelationshipsLocalizationDataTypeGameCenterActivityLocalizations
			}
			out, err := c.GameCenterActivityImagesCreateInstanceWithResponse(ctx, body)
			if err := asc.Err(out, err); err != nil {
				return "", nil, err
			}
			return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
		},
		commit: func(ctx context.Context, c *asc.Client, id string) (string, error) {
			var body gen.GameCenterActivityImageUpdateRequest
			body.Data.Type, body.Data.Id = gen.GameCenterActivityImageUpdateRequestDataTypeGameCenterActivityImages, id
			asc.Alloc(&body.Data.Attributes).Uploaded = new(true)
			out, err := c.GameCenterActivityImagesUpdateInstanceWithResponse(ctx, id, body)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
		},
		remove: func(ctx context.Context, c *asc.Client, id string) error {
			out, err := c.GameCenterActivityImagesDeleteInstanceWithResponse(ctx, id)
			return asc.Err(out, err)
		},
	}
	if versionLevel {
		api.typeName, api.noun = "game_center_activity_default_image", "Game Center activity default image"
		api.description = "Uploads the default image of a Game Center activity, shown for locales without an image of their own. " +
			"An activity has one default image; uploading another replaces it."
		api.existing = func(ctx context.Context, c *asc.Client, versionID string) (string, error) {
			out, err := c.GameCenterActivityVersionsDefaultImageGetToOneRelatedWithResponse(ctx, versionID, nil)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			return out.JSON200.Data.Id, nil
		}
	}
	return api
}

var (
	newActivityImageResource        = newLocalizationImageResource(activityImageAPI(false))
	newActivityDefaultImageResource = newLocalizationImageResource(activityImageAPI(true))
)

// ---- apple_game_center_activity_achievement and apple_game_center_activity_leaderboard ----

// activityLinkAPI is the part of the API that links an activity to its
// achievements or its leaderboards.
type activityLinkAPI struct {
	typeName, noun, attr, example string
	set                           func(ctx context.Context, c *asc.Client, activityID, id string, add bool) error
	linked                        func(a *gen.GameCenterActivity) []string
}

var (
	_ resource.ResourceWithConfigure   = &activityLinkResource{}
	_ resource.ResourceWithImportState = &activityLinkResource{}
	_ resource.ResourceWithIdentity    = &activityLinkResource{}
)

type activityLinkResource struct {
	resourceIdentity
	api    activityLinkAPI
	client *asc.Client
}

func newActivityLinkResource(api activityLinkAPI) func() resource.Resource {
	return func() resource.Resource {
		return &activityLinkResource{api: api, resourceIdentity: resourceIdentity{attrs: []identityAttribute{
			{name: "activity_id", description: "App Store Connect ID of the activity.", state: "activity_id"},
			{name: api.attr, description: "App Store Connect ID of the " + api.noun + ".", state: api.attr},
		}}}
	}
}

func (r *activityLinkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *activityLinkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Links a Game Center activity to " + indefinite(r.api.noun) + " of the same app or group, which players then " +
			"see with the activity. Import it with `<activity_id>/<" + r.api.attr + ">`.",
		Attributes: map[string]schema.Attribute{
			"activity_id": replacedString("App Store Connect ID of the activity, e.g. `apple_game_center_activity.example.id`."),
			r.api.attr:    replacedString("App Store Connect ID of the " + r.api.noun + ", e.g. `" + r.api.example + "`."),
		},
	}
}

// indefinite puts "a" or "an" before a noun.
func indefinite(noun string) string {
	if slices.Contains([]byte("aeiou"), noun[0]) {
		return "an " + noun
	}
	return "a " + noun
}

func (r *activityLinkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *activityLinkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ids reads the activity's and the linked item's IDs from a plan or state.
func (r *activityLinkResource) ids(ctx context.Context, get func(context.Context, path.Path, any) diag.Diagnostics, diags *diag.Diagnostics) (activityID, id string) {
	var a, i types.String
	diags.Append(get(ctx, path.Root("activity_id"), &a)...)
	diags.Append(get(ctx, path.Root(r.api.attr), &i)...)
	return a.ValueString(), i.ValueString()
}

func (r *activityLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	activityID, id := r.ids(ctx, req.Plan.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.api.set(ctx, r.client, activityID, id, true); err != nil {
		apiError(&resp.Diagnostics, "link Game Center activity to "+r.api.noun, err)
		return
	}
	resp.State.Raw = req.Plan.Raw
}

func (r *activityLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	activityID, id := r.ids(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterActivitiesGetInstanceWithResponse(ctx, activityID, &gen.GameCenterActivitiesGetInstanceParams{
		Include: &[]gen.GameCenterActivitiesGetInstanceParamsInclude{
			gen.GameCenterActivitiesGetInstanceParamsIncludeAchievementsV2, gen.GameCenterActivitiesGetInstanceParamsIncludeLeaderboardsV2,
		},
		LimitAchievementsV2: new(50), LimitLeaderboardsV2: new(50),
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) || err == nil && !slices.Contains(r.api.linked(&out.JSON200.Data), id) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center activity", err)
	}
}

// Update is never called: both attributes replace the resource.
func (r *activityLinkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *activityLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	activityID, id := r.ids(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.api.set(ctx, r.client, activityID, id, false); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "unlink Game Center activity from "+r.api.noun, err)
	}
}

var newActivityAchievementResource = newActivityLinkResource(activityLinkAPI{
	typeName: "game_center_activity_achievement", noun: "achievement", attr: "achievement_id", example: "apple_game_center_achievement.example.id",
	set: func(ctx context.Context, c *asc.Client, activityID, id string, add bool) error {
		var body gen.GameCenterActivityAchievementsV2LinkagesRequest
		d := asc.Push(&body.Data)
		d.Id, d.Type = id, gen.GameCenterActivityAchievementsV2LinkagesRequestDataTypeGameCenterAchievements
		if add {
			out, err := c.GameCenterActivitiesAchievementsV2CreateToManyRelationshipWithResponse(ctx, activityID, body)
			return asc.Err(out, err)
		}
		out, err := c.GameCenterActivitiesAchievementsV2DeleteToManyRelationshipWithResponse(ctx, activityID, body)
		return asc.Err(out, err)
	},
	linked: func(a *gen.GameCenterActivity) []string {
		var ids []string
		if rel := a.Relationships; rel != nil && rel.AchievementsV2 != nil {
			for _, d := range asc.Value(rel.AchievementsV2.Data) {
				ids = append(ids, d.Id)
			}
		}
		return ids
	},
})

var newActivityLeaderboardResource = newActivityLinkResource(activityLinkAPI{
	typeName: "game_center_activity_leaderboard", noun: "leaderboard", attr: "leaderboard_id", example: "apple_game_center_leaderboard.example.id",
	set: func(ctx context.Context, c *asc.Client, activityID, id string, add bool) error {
		var body gen.GameCenterActivityLeaderboardsV2LinkagesRequest
		d := asc.Push(&body.Data)
		d.Id, d.Type = id, gen.GameCenterActivityLeaderboardsV2LinkagesRequestDataTypeGameCenterLeaderboards
		if add {
			out, err := c.GameCenterActivitiesLeaderboardsV2CreateToManyRelationshipWithResponse(ctx, activityID, body)
			return asc.Err(out, err)
		}
		out, err := c.GameCenterActivitiesLeaderboardsV2DeleteToManyRelationshipWithResponse(ctx, activityID, body)
		return asc.Err(out, err)
	},
	linked: func(a *gen.GameCenterActivity) []string {
		var ids []string
		if rel := a.Relationships; rel != nil && rel.LeaderboardsV2 != nil {
			for _, d := range asc.Value(rel.LeaderboardsV2.Data) {
				ids = append(ids, d.Id)
			}
		}
		return ids
	},
})

// ---- action.apple_submit_game_center_activity ----

var newSubmitActivityAction = newSubmitVersionedAction("submit_game_center_activity", "Game Center activity",
	"apple_game_center_activity.example.id", activityMetadata,
	func(item *gen.ReviewSubmissionItemCreateRequest, versionID string) {
		v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.GameCenterActivityVersion).Data)
		v.Id, v.Type = versionID, gen.ReviewSubmissionItemCreateRequestDataRelationshipsGameCenterActivityVersionDataTypeGameCenterActivityVersions
	})

// ---- data.apple_game_center_activities ----

type activityData struct {
	ID               types.String `tfsdk:"id"`
	ReferenceName    types.String `tfsdk:"reference_name"`
	VendorIdentifier types.String `tfsdk:"vendor_identifier"`
	PlayStyle        types.String `tfsdk:"play_style"`
	Archived         types.Bool   `tfsdk:"archived"`
}

type activitiesData struct {
	DetailID   types.String   `tfsdk:"game_center_detail_id"`
	GroupID    types.String   `tfsdk:"game_center_group_id"`
	Activities []activityData `tfsdk:"activities"`
}

// ownerAttributes are the arguments of a data source that lists the Game
// Center items of an app or of a group.
func ownerAttributes(results string, items dsschema.ListNestedAttribute) map[string]dsschema.Attribute {
	return map[string]dsschema.Attribute{
		"game_center_detail_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of an app's Game Center detail. Set this or `game_center_group_id`.", Optional: true,
		},
		"game_center_group_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of a Game Center group. Set this or `game_center_detail_id`.", Optional: true,
		},
		results: items,
	}
}

var newActivitiesDataSource = listDataSource("game_center_activities",
	"Lists the Game Center activities of an app or of a group.",
	ownerAttributes("activities", listOf("The activities.", map[string]dsschema.Attribute{
		"id":                dsComputed("App Store Connect ID of the activity."),
		"reference_name":    dsComputed("Name of the activity in App Store Connect."),
		"vendor_identifier": dsComputed("Identifier the game knows the activity by."),
		"play_style":        dsComputed("How players take part: " + docValues(activityPlayStyles) + "."),
		"archived":          dsBool("Whether the activity is archived."),
	})),
	func(ctx context.Context, c *asc.Client, m *activitiesData, diags *diag.Diagnostics) {
		var first *gen.GameCenterActivitiesResponse
		var err error
		if id := m.DetailID.ValueString(); id != "" {
			out, e := c.GameCenterDetailsGameCenterActivitiesGetToManyRelatedWithResponse(ctx, id, &gen.GameCenterDetailsGameCenterActivitiesGetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		} else {
			out, e := c.GameCenterGroupsGameCenterActivitiesGetToManyRelatedWithResponse(ctx, m.GroupID.ValueString(), &gen.GameCenterGroupsGameCenterActivitiesGetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		}
		var all []gen.GameCenterActivity
		if err == nil {
			all, err = asc.All(ctx, c, first,
				func(p *gen.GameCenterActivitiesResponse) []gen.GameCenterActivity { return p.Data },
				func(p *gen.GameCenterActivitiesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "Game Center detail or group", err)
			return
		}
		m.Activities = []activityData{}
		for i := range all {
			var model activityModel
			model.apply(&all[i])
			m.Activities = append(m.Activities, activityData{
				ID: model.ID, ReferenceName: model.ReferenceName, VendorIdentifier: model.VendorIdentifier, PlayStyle: model.PlayStyle, Archived: model.Archived,
			})
		}
	}, datasourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id")))
