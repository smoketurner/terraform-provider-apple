package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center achievements
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center-achievements).
// An achievement belongs to an app's Game Center detail or to a group. Its
// localizations and their images belong to versions, which go through App
// Review; the provider hides the versions, as it does for in-app purchases.

var achievementMetadata = metadataVersions{
	noun: "Game Center achievement",
	list: func(ctx context.Context, c *asc.Client, achievementID string) ([]versionInfo, error) {
		out, err := c.GameCenterAchievementsV2VersionsGetToManyRelatedWithResponse(ctx, achievementID,
			&gen.GameCenterAchievementsV2VersionsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterAchievementVersionsV2Response) []gen.GameCenterAchievementVersionV2 {
				return p.Data
			},
			func(p *gen.GameCenterAchievementVersionsV2Response) *string { return p.Links.Next })
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
	create: func(ctx context.Context, c *asc.Client, achievementID string) (string, error) {
		var body gen.GameCenterAchievementVersionV2CreateRequest
		body.Data.Type = gen.GameCenterAchievementVersionV2CreateRequestDataTypeGameCenterAchievementVersions
		d := &body.Data.Relationships.Achievement.Data
		d.Id, d.Type = achievementID, gen.GameCenterAchievementVersionV2CreateRequestDataRelationshipsAchievementDataTypeGameCenterAchievements
		out, err := c.GameCenterAchievementVersionsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

// ---- apple_game_center_achievement ----

var (
	_ resource.ResourceWithConfigure        = &achievementResource{}
	_ resource.ResourceWithImportState      = &achievementResource{}
	_ resource.ResourceWithIdentity         = &achievementResource{}
	_ resource.ResourceWithConfigValidators = &achievementResource{}
)

type achievementResource struct {
	resourceIdentity
	client *asc.Client
}

type achievementModel struct {
	DetailID           types.String `tfsdk:"game_center_detail_id"`
	GroupID            types.String `tfsdk:"game_center_group_id"`
	ReferenceName      types.String `tfsdk:"reference_name"`
	VendorIdentifier   types.String `tfsdk:"vendor_identifier"`
	Points             types.Int64  `tfsdk:"points"`
	ShowBeforeEarned   types.Bool   `tfsdk:"show_before_earned"`
	Repeatable         types.Bool   `tfsdk:"repeatable"`
	Archived           types.Bool   `tfsdk:"archived"`
	ActivityProperties types.Map    `tfsdk:"activity_properties"`
	ID                 types.String `tfsdk:"id"`
}

func newAchievementResource() resource.Resource {
	return &achievementResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the Game Center achievement.")}}
}

func (r *achievementResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_achievement"
}

func (r *achievementResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center achievement for an app, or for a group of apps. Add its name and " +
			"descriptions with `apple_game_center_achievement_localization` and its image with " +
			"`apple_game_center_achievement_image`, then submit it with `apple_submit_game_center_achievement`. " +
			"Destroying the resource deletes the achievement; one that has been live can't be deleted, so it is " +
			"archived instead. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"game_center_detail_id": replacedOptionalString("App Store Connect ID of the app's Game Center detail, e.g. " +
				"`apple_game_center_detail.example.id`. Set this or `game_center_group_id`."),
			"game_center_group_id": replacedOptionalString("App Store Connect ID of a Game Center group, for an achievement " +
				"its apps share. Set this or `game_center_detail_id`."),
			"reference_name":    schema.StringAttribute{MarkdownDescription: "Name of the achievement in App Store Connect.", Required: true},
			"vendor_identifier": replacedString("Identifier the game reports the achievement with, e.g. `com.example.first_win`. Changing it creates a new achievement."),
			"points": schema.Int64Attribute{
				MarkdownDescription: "Points the achievement is worth, 1 to 100. An app's achievements can total 1,000 points.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 100)},
			},
			"show_before_earned": schema.BoolAttribute{MarkdownDescription: "Whether players see the achievement before they earn it.", Required: true},
			"repeatable":         schema.BoolAttribute{MarkdownDescription: "Whether players can earn the achievement more than once.", Required: true},
			"archived": schema.BoolAttribute{
				MarkdownDescription: "Whether the achievement is archived, which hides it from players. Defaults to `false`.",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"activity_properties": schema.MapAttribute{
				MarkdownDescription: "Properties passed to the game when the achievement is opened from a Game Center activity.",
				Optional:            true, ElementType: types.StringType,
			},
			"id": idAttribute("App Store Connect ID of the Game Center achievement."),
		},
	}
}

func (r *achievementResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id"))}
}

func (r *achievementResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *achievementResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func stringMapValue(m *gen.StringToStringMap) types.Map {
	if m == nil {
		return types.MapNull(types.StringType)
	}
	elements := map[string]attr.Value{}
	for key, value := range *m {
		elements[key] = types.StringValue(value)
	}
	return types.MapValueMust(types.StringType, elements)
}

func (m *achievementModel) apply(a *gen.GameCenterAchievementV2) {
	attributes := asc.Value(a.Attributes)
	m.ID, m.ReferenceName, m.VendorIdentifier = types.StringValue(a.Id), stringValue(attributes.ReferenceName), stringValue(attributes.VendorIdentifier)
	m.Points, m.ShowBeforeEarned, m.Repeatable = intValue(attributes.Points), types.BoolPointerValue(attributes.ShowBeforeEarned), types.BoolPointerValue(attributes.Repeatable)
	m.Archived, m.ActivityProperties = types.BoolValue(asc.Value(attributes.Archived)), stringMapValue(attributes.ActivityProperties)
	rel := a.Relationships
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
// names removed activity properties as null.
func (m *achievementModel) attributes(ctx context.Context, update bool, diags *diag.Diagnostics) map[string]any {
	attributes := map[string]any{
		"referenceName": m.ReferenceName.ValueString(), "points": m.Points.ValueInt64(),
		"showBeforeEarned": m.ShowBeforeEarned.ValueBool(), "repeatable": m.Repeatable.ValueBool(),
	}
	if update {
		attributes["archived"] = m.Archived.ValueBool()
	} else {
		attributes["vendorIdentifier"] = m.VendorIdentifier.ValueString()
	}
	if !m.ActivityProperties.IsNull() {
		properties := map[string]string{}
		diags.Append(m.ActivityProperties.ElementsAs(ctx, &properties, false)...)
		attributes["activityProperties"] = properties
	} else if update {
		attributes["activityProperties"] = nil
	}
	return attributes
}

// Create creates the achievement with its first version, then archives it
// when planned: an achievement can't be created archived.
func (r *achievementResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan achievementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	attributes := plan.attributes(ctx, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	const version = "${version}"
	relationships := map[string]any{"versions": map[string]any{"data": []any{map[string]any{"type": "gameCenterAchievementVersions", "id": version}}}}
	if id := plan.DetailID.ValueString(); id != "" {
		relationships["gameCenterDetail"] = linkage("gameCenterDetails", id)
	} else {
		relationships["gameCenterGroup"] = linkage("gameCenterGroups", plan.GroupID.ValueString())
	}
	body, err := json.Marshal(map[string]any{
		"data":     map[string]any{"type": "gameCenterAchievements", "attributes": attributes, "relationships": relationships},
		"included": []any{map[string]any{"type": "gameCenterAchievementVersions", "id": version}},
	})
	var achievement *gen.GameCenterAchievementV2
	if err == nil {
		out, e := r.client.GameCenterAchievementsV2CreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			achievement = &out.JSON201.Data
		}
	}
	if err == nil && plan.Archived.ValueBool() {
		plan.ID = types.StringValue(achievement.Id)
		achievement, err = r.update(ctx, &plan, &resp.Diagnostics)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create Game Center achievement", err)
		return
	}
	properties := plan.ActivityProperties
	plan.apply(achievement)
	plan.ActivityProperties = properties
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *achievementResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state achievementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterAchievementsV2GetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.GameCenterAchievementsV2GetInstanceParams{
		Include: &[]gen.GameCenterAchievementsV2GetInstanceParamsInclude{
			gen.GameCenterAchievementsV2GetInstanceParamsIncludeGameCenterDetail, gen.GameCenterAchievementsV2GetInstanceParamsIncludeGameCenterGroup,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center achievement", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// update sends the achievement's attributes as raw JSON, so that removed
// activity properties are sent as null.
func (r *achievementResource) update(ctx context.Context, m *achievementModel, diags *diag.Diagnostics) (*gen.GameCenterAchievementV2, error) {
	id := m.ID.ValueString()
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "gameCenterAchievements", "id": id, "attributes": m.attributes(ctx, true, diags),
	}})
	if err != nil {
		return nil, err
	}
	out, err := r.client.GameCenterAchievementsV2UpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *achievementResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan achievementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	achievement, err := r.update(ctx, &plan, &resp.Diagnostics)
	if err != nil {
		apiError(&resp.Diagnostics, "update Game Center achievement", err)
		return
	}
	properties := plan.ActivityProperties
	plan.apply(achievement)
	plan.ActivityProperties = properties
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete archives an achievement that has been live, which App Store
// Connect refuses to delete.
func (r *achievementResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state achievementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterAchievementsV2DeleteInstanceWithResponse(ctx, state.ID.ValueString())
	err = asc.Err(out, err)
	if asc.HasCode(err, "STATE_ERROR") {
		state.Archived = types.BoolValue(true)
		if _, err = r.update(ctx, &state, &resp.Diagnostics); err == nil {
			resp.Diagnostics.AddWarning("Game Center achievement archived",
				"The achievement "+state.ID.ValueString()+" has been live and can't be deleted, so it was archived.")
		}
	}
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Game Center achievement", err)
	}
}

// ---- apple_game_center_achievement_localization ----

func achievementLocalization(l *gen.GameCenterAchievementLocalizationV2) localization {
	a := asc.Value(l.Attributes)
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: map[string]*string{
		"name": a.Name, "beforeEarnedDescription": a.BeforeEarnedDescription, "afterEarnedDescription": a.AfterEarnedDescription,
	}}
}

var achievementLocalizationAPI = localizationAPI{
	typeName: "game_center_achievement_localization",
	description: "Sets the name and descriptions of a Game Center achievement for one locale; " +
		"`apple_game_center_achievement_image` adds the locale's image.",
	parent:     "achievement_id",
	parentDesc: "App Store Connect ID of the achievement, e.g. `apple_game_center_achievement.example.id`.",
	versions:   achievementMetadata,
	fields: []localizationField{
		{attr: "name", json: "name", desc: "Name of the achievement.", required: true, min: 1, max: 255},
		{
			attr: "before_earned_description", json: "beforeEarnedDescription", desc: "Description players see before they earn the achievement.",
			required: true, min: 1, max: 255,
		},
		{
			attr: "after_earned_description", json: "afterEarnedDescription", desc: "Description players see once they earned the achievement.",
			required: true, min: 1, max: 255,
		},
	},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.GameCenterAchievementVersionsV2LocalizationsGetToManyRelatedWithResponse(ctx, versionID,
			&gen.GameCenterAchievementVersionsV2LocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterAchievementLocalizationsV2Response) []gen.GameCenterAchievementLocalizationV2 {
				return p.Data
			},
			func(p *gen.GameCenterAchievementLocalizationsV2Response) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		localizations := make([]localization, len(all))
		for i := range all {
			localizations[i] = achievementLocalization(&all[i])
		}
		return localizations, nil
	},
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		var body gen.GameCenterAchievementLocalizationV2CreateRequest
		body.Data.Type = gen.GameCenterAchievementLocalizationV2CreateRequestDataTypeGameCenterAchievementLocalizations
		a := &body.Data.Attributes
		a.Locale, a.Name = locale, asc.Value(values["name"])
		a.BeforeEarnedDescription, a.AfterEarnedDescription = asc.Value(values["beforeEarnedDescription"]), asc.Value(values["afterEarnedDescription"])
		v := &body.Data.Relationships.Version.Data
		v.Id, v.Type = versionID, gen.GameCenterAchievementLocalizationV2CreateRequestDataRelationshipsVersionDataTypeGameCenterAchievementVersions
		out, err := c.GameCenterAchievementLocalizationsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(achievementLocalization(&out.JSON201.Data)), nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		var body gen.GameCenterAchievementLocalizationV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.GameCenterAchievementLocalizationV2UpdateRequestDataTypeGameCenterAchievementLocalizations, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Name, a.BeforeEarnedDescription, a.AfterEarnedDescription = values["name"], values["beforeEarnedDescription"], values["afterEarnedDescription"]
		out, err := c.GameCenterAchievementLocalizationsV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(achievementLocalization(&out.JSON200.Data)), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterAchievementLocalizationsV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var newAchievementLocalizationResource = newVersionedLocalizationResource(achievementLocalizationAPI)

// ---- apple_game_center_achievement_image ----

var newAchievementImageResource = newLocalizationImageResource(localizationImageAPI{
	typeName:      "game_center_achievement_image",
	noun:          "Game Center achievement image",
	description:   "Uploads the image of a Game Center achievement for one locale. A localization has one image; uploading another replaces it.",
	localizations: achievementLocalizationAPI,
	existing: func(ctx context.Context, c *asc.Client, localizationID string) (string, error) {
		out, err := c.GameCenterAchievementLocalizationsV2ImageGetToOneRelatedWithResponse(ctx, localizationID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	reserve: func(ctx context.Context, c *asc.Client, localizationID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.GameCenterAchievementImageV2CreateRequest
		body.Data.Type = gen.GameCenterAchievementImageV2CreateRequestDataTypeGameCenterAchievementImages
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		l := &body.Data.Relationships.Localization.Data
		l.Id, l.Type = localizationID, gen.GameCenterAchievementImageV2CreateRequestDataRelationshipsLocalizationDataTypeGameCenterAchievementLocalizations
		out, err := c.GameCenterAchievementImagesV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		var body gen.GameCenterAchievementImageV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.GameCenterAchievementImageV2UpdateRequestDataTypeGameCenterAchievementImages, id
		asc.Alloc(&body.Data.Attributes).Uploaded = new(true)
		out, err := c.GameCenterAchievementImagesV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterAchievementImagesV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
})

// ---- action.apple_submit_game_center_achievement ----

var newSubmitAchievementAction = newSubmitVersionedAction("submit_game_center_achievement", "Game Center achievement",
	"apple_game_center_achievement.example.id", achievementMetadata,
	func(item *gen.ReviewSubmissionItemCreateRequest, versionID string) {
		v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.GameCenterAchievementVersion).Data)
		v.Id, v.Type = versionID, gen.ReviewSubmissionItemCreateRequestDataRelationshipsGameCenterAchievementVersionDataTypeGameCenterAchievementVersions
	})

// ---- data.apple_game_center_achievements ----

type achievementData struct {
	ID               types.String `tfsdk:"id"`
	ReferenceName    types.String `tfsdk:"reference_name"`
	VendorIdentifier types.String `tfsdk:"vendor_identifier"`
	Points           types.Int64  `tfsdk:"points"`
	ShowBeforeEarned types.Bool   `tfsdk:"show_before_earned"`
	Repeatable       types.Bool   `tfsdk:"repeatable"`
	Archived         types.Bool   `tfsdk:"archived"`
}

type achievementsData struct {
	DetailID     types.String      `tfsdk:"game_center_detail_id"`
	GroupID      types.String      `tfsdk:"game_center_group_id"`
	Archived     types.Bool        `tfsdk:"archived"`
	Achievements []achievementData `tfsdk:"achievements"`
}

var newAchievementsDataSource = listDataSource("game_center_achievements",
	"Lists the Game Center achievements of an app or of a group.",
	map[string]dsschema.Attribute{
		"game_center_detail_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of an app's Game Center detail. Set this or `game_center_group_id`.", Optional: true,
		},
		"game_center_group_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of a Game Center group. Set this or `game_center_detail_id`.", Optional: true,
		},
		"archived": dsschema.BoolAttribute{
			MarkdownDescription: "`true` lists only archived achievements, `false` only the others. Unset lists every achievement.", Optional: true,
		},
		"achievements": listOf("The achievements.", map[string]dsschema.Attribute{
			"id":                 dsComputed("App Store Connect ID of the achievement."),
			"reference_name":     dsComputed("Name of the achievement in App Store Connect."),
			"vendor_identifier":  dsComputed("Identifier the game reports the achievement with."),
			"points":             dsInt("Points the achievement is worth."),
			"show_before_earned": dsBool("Whether players see the achievement before they earn it."),
			"repeatable":         dsBool("Whether players can earn the achievement more than once."),
			"archived":           dsBool("Whether the achievement is archived."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *achievementsData, diags *diag.Diagnostics) {
		var first *gen.GameCenterAchievementsV2Response
		var err error
		if id := m.DetailID.ValueString(); id != "" {
			params := &gen.GameCenterDetailsGameCenterAchievementsV2GetToManyRelatedParams{Limit: new(listAll)}
			if !m.Archived.IsNull() {
				params.FilterArchived = &[]string{strconv.FormatBool(m.Archived.ValueBool())}
			}
			out, e := c.GameCenterDetailsGameCenterAchievementsV2GetToManyRelatedWithResponse(ctx, id, params)
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		} else {
			params := &gen.GameCenterGroupsGameCenterAchievementsV2GetToManyRelatedParams{Limit: new(listAll)}
			if !m.Archived.IsNull() {
				params.FilterArchived = &[]string{strconv.FormatBool(m.Archived.ValueBool())}
			}
			out, e := c.GameCenterGroupsGameCenterAchievementsV2GetToManyRelatedWithResponse(ctx, m.GroupID.ValueString(), params)
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		}
		var all []gen.GameCenterAchievementV2
		if err == nil {
			all, err = asc.All(ctx, c, first,
				func(p *gen.GameCenterAchievementsV2Response) []gen.GameCenterAchievementV2 { return p.Data },
				func(p *gen.GameCenterAchievementsV2Response) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "Game Center detail or group", err)
			return
		}
		m.Achievements = []achievementData{}
		for i := range all {
			var model achievementModel
			model.apply(&all[i])
			if !m.Archived.IsNull() && !m.Archived.Equal(model.Archived) {
				continue
			}
			m.Achievements = append(m.Achievements, achievementData{
				ID: model.ID, ReferenceName: model.ReferenceName, VendorIdentifier: model.VendorIdentifier, Points: model.Points,
				ShowBeforeEarned: model.ShowBeforeEarned, Repeatable: model.Repeatable, Archived: model.Archived,
			})
		}
	}, datasourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id")))
