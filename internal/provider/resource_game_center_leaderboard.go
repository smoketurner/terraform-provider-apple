package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// Game Center leaderboards
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center-leaderboards).
// A leaderboard belongs to an app's Game Center detail or to a group. Its
// localizations and their images belong to versions, which go through App
// Review; the provider hides the versions, as it does for in-app purchases.

var leaderboardMetadata = metadataVersions{
	noun: "Game Center leaderboard",
	list: func(ctx context.Context, c *asc.Client, leaderboardID string) ([]versionInfo, error) {
		out, err := c.GameCenterLeaderboardsV2VersionsGetToManyRelatedWithResponse(ctx, leaderboardID,
			&gen.GameCenterLeaderboardsV2VersionsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterLeaderboardVersionsV2Response) []gen.GameCenterLeaderboardVersionV2 {
				return p.Data
			},
			func(p *gen.GameCenterLeaderboardVersionsV2Response) *string { return p.Links.Next })
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
	create: func(ctx context.Context, c *asc.Client, leaderboardID string) (string, error) {
		var body gen.GameCenterLeaderboardVersionV2CreateRequest
		body.Data.Type = gen.GameCenterLeaderboardVersionV2CreateRequestDataTypeGameCenterLeaderboardVersions
		d := &body.Data.Relationships.Leaderboard.Data
		d.Id, d.Type = leaderboardID, gen.GameCenterLeaderboardVersionV2CreateRequestDataRelationshipsLeaderboardDataTypeGameCenterLeaderboards
		out, err := c.GameCenterLeaderboardVersionsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

// ---- apple_game_center_leaderboard ----

var (
	leaderboardFormatters = strings.Fields("INTEGER DECIMAL_POINT_1_PLACE DECIMAL_POINT_2_PLACE DECIMAL_POINT_3_PLACE " +
		"ELAPSED_TIME_CENTISECOND ELAPSED_TIME_MINUTE ELAPSED_TIME_SECOND MONEY_POUND_DECIMAL MONEY_POUND MONEY_DOLLAR_DECIMAL " +
		"MONEY_DOLLAR MONEY_EURO_DECIMAL MONEY_EURO MONEY_FRANC_DECIMAL MONEY_FRANC MONEY_KRONER_DECIMAL MONEY_KRONER MONEY_YEN")
	leaderboardSubmissionTypes = enum(gen.GameCenterLeaderboardV2AttributesSubmissionTypeBESTSCORE, gen.GameCenterLeaderboardV2AttributesSubmissionTypeMOSTRECENTSCORE)
	leaderboardSortTypes       = enum(gen.GameCenterLeaderboardV2AttributesScoreSortTypeDESC, gen.GameCenterLeaderboardV2AttributesScoreSortTypeASC)
	leaderboardVisibilities    = enum(gen.GameCenterLeaderboardV2AttributesVisibilitySHOWFORALL, gen.GameCenterLeaderboardV2AttributesVisibilityHIDEFORALL)
)

var (
	_ resource.ResourceWithConfigure        = &leaderboardResource{}
	_ resource.ResourceWithImportState      = &leaderboardResource{}
	_ resource.ResourceWithIdentity         = &leaderboardResource{}
	_ resource.ResourceWithConfigValidators = &leaderboardResource{}
)

type leaderboardResource struct {
	resourceIdentity
	client *asc.Client
}

type leaderboardModel struct {
	DetailID            types.String `tfsdk:"game_center_detail_id"`
	GroupID             types.String `tfsdk:"game_center_group_id"`
	ReferenceName       types.String `tfsdk:"reference_name"`
	VendorIdentifier    types.String `tfsdk:"vendor_identifier"`
	DefaultFormatter    types.String `tfsdk:"default_formatter"`
	SubmissionType      types.String `tfsdk:"submission_type"`
	ScoreSortType       types.String `tfsdk:"score_sort_type"`
	ScoreRangeStart     types.String `tfsdk:"score_range_start"`
	ScoreRangeEnd       types.String `tfsdk:"score_range_end"`
	RecurrenceStartDate types.String `tfsdk:"recurrence_start_date"`
	RecurrenceDuration  types.String `tfsdk:"recurrence_duration"`
	RecurrenceRule      types.String `tfsdk:"recurrence_rule"`
	Visibility          types.String `tfsdk:"visibility"`
	Archived            types.Bool   `tfsdk:"archived"`
	ActivityProperties  types.Map    `tfsdk:"activity_properties"`
	ID                  types.String `tfsdk:"id"`
}

func newLeaderboardResource() resource.Resource {
	return &leaderboardResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the Game Center leaderboard.")}}
}

func (r *leaderboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_leaderboard"
}

func (r *leaderboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center leaderboard for an app, or for a group of apps: a classic one, or a " +
			"recurring one with `recurrence_start_date`, `recurrence_duration` and `recurrence_rule`. Add its name with " +
			"`apple_game_center_leaderboard_localization` and its image with `apple_game_center_leaderboard_image`, " +
			"then submit it with `apple_submit_game_center_leaderboard`. Destroying the resource deletes the " +
			"leaderboard; one that has been live can't be deleted, so it is archived instead. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"game_center_detail_id": replacedOptionalString("App Store Connect ID of the app's Game Center detail, e.g. " +
				"`apple_game_center_detail.example.id`. Set this or `game_center_group_id`."),
			"game_center_group_id": replacedOptionalString("App Store Connect ID of a Game Center group, for a leaderboard " +
				"its apps share. Set this or `game_center_detail_id`."),
			"reference_name":    schema.StringAttribute{MarkdownDescription: "Name of the leaderboard in App Store Connect.", Required: true},
			"vendor_identifier": replacedString("Identifier the game reports scores with, e.g. `com.example.high_scores`. Changing it creates a new leaderboard."),
			"default_formatter": schema.StringAttribute{
				MarkdownDescription: "How scores are shown: " + docValues(leaderboardFormatters) + ".", Required: true,
				Validators: []validator.String{oneOf(leaderboardFormatters)},
			},
			"submission_type": schema.StringAttribute{
				MarkdownDescription: "Which of a player's scores counts: " + docValues(leaderboardSubmissionTypes) + ".", Required: true,
				Validators: []validator.String{oneOf(leaderboardSubmissionTypes)},
			},
			"score_sort_type": schema.StringAttribute{
				MarkdownDescription: "Whether high (`DESC`) or low (`ASC`) scores rank first.", Required: true,
				Validators: []validator.String{oneOf(leaderboardSortTypes)},
			},
			"score_range_start": optionalString("Lowest score the leaderboard accepts, as a number in a string."),
			"score_range_end":   optionalString("Highest score the leaderboard accepts, as a number in a string."),
			"recurrence_start_date": optionalString("When a recurring leaderboard first starts, e.g. `2026-12-01T16:00:00Z`.",
				stringvalidator.RegexMatches(utcTimeRegexp, "must be an RFC 3339 time in UTC, e.g. `2026-12-01T16:00:00Z`")),
			"recurrence_duration": optionalString("How long each occurrence of a recurring leaderboard lasts, as an ISO 8601 duration, e.g. `P1D`."),
			"recurrence_rule":     optionalString("How often a recurring leaderboard starts over, as an iCalendar recurrence rule, e.g. `FREQ=WEEKLY;INTERVAL=1`."),
			"visibility": optionalComputedString("Whether players see the leaderboard: "+docValues(leaderboardVisibilities)+". Unset keeps App Store Connect's value.",
				oneOf(leaderboardVisibilities)),
			"archived": schema.BoolAttribute{
				MarkdownDescription: "Whether the leaderboard is archived, which hides it from players. Defaults to `false`.",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"activity_properties": schema.MapAttribute{
				MarkdownDescription: "Properties passed to the game when the leaderboard is opened from a Game Center activity.",
				Optional:            true, ElementType: types.StringType,
			},
			"id": idAttribute("App Store Connect ID of the Game Center leaderboard."),
		},
	}
}

func (r *leaderboardResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id"))}
}

func (r *leaderboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *leaderboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *leaderboardModel) apply(l *gen.GameCenterLeaderboardV2) {
	a := asc.Value(l.Attributes)
	m.ID, m.ReferenceName, m.VendorIdentifier = types.StringValue(l.Id), stringValue(a.ReferenceName), stringValue(a.VendorIdentifier)
	m.DefaultFormatter, m.SubmissionType, m.ScoreSortType = stringValue(a.DefaultFormatter), stringValue(a.SubmissionType), stringValue(a.ScoreSortType)
	m.ScoreRangeStart, m.ScoreRangeEnd = stringValue(a.ScoreRangeStart), stringValue(a.ScoreRangeEnd)
	m.RecurrenceStartDate, m.RecurrenceRule, m.Visibility = timeValue(a.RecurrenceStartDate), stringValue(a.RecurrenceRule), stringValue(a.Visibility)
	m.RecurrenceDuration = types.StringNull()
	if a.RecurrenceDuration != nil {
		m.RecurrenceDuration = types.StringValue(a.RecurrenceDuration.String())
	}
	m.Archived, m.ActivityProperties = types.BoolValue(asc.Value(a.Archived)), stringMapValue(a.ActivityProperties)
	rel := l.Relationships
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
// names the optional attributes that are unset as null, which removes them.
func (m *leaderboardModel) attributes(ctx context.Context, update bool, diags *diag.Diagnostics) map[string]any {
	attributes := map[string]any{
		"referenceName": m.ReferenceName.ValueString(), "defaultFormatter": m.DefaultFormatter.ValueString(),
		"submissionType": m.SubmissionType.ValueString(), "scoreSortType": m.ScoreSortType.ValueString(),
	}
	if update {
		attributes["archived"] = m.Archived.ValueBool()
	} else {
		attributes["vendorIdentifier"] = m.VendorIdentifier.ValueString()
	}
	if visibility := knownString(m.Visibility); visibility != nil {
		attributes["visibility"] = visibility
	}
	for name, value := range map[string]types.String{
		"scoreRangeStart": m.ScoreRangeStart, "scoreRangeEnd": m.ScoreRangeEnd, "recurrenceStartDate": m.RecurrenceStartDate,
		"recurrenceDuration": m.RecurrenceDuration, "recurrenceRule": m.RecurrenceRule,
	} {
		if v := knownString(value); v != nil || update {
			attributes[name] = v
		}
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

// keep restores the planned values of what App Store Connect may return in
// another form.
func (m *leaderboardModel) keep(plan *leaderboardModel) {
	m.ActivityProperties, m.RecurrenceDuration, m.RecurrenceStartDate = plan.ActivityProperties, plan.RecurrenceDuration, plan.RecurrenceStartDate
}

// Create creates the leaderboard with its first version, then archives it
// when planned: a leaderboard can't be created archived.
func (r *leaderboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan leaderboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	attributes := plan.attributes(ctx, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	const version = "${version}"
	relationships := map[string]any{"versions": map[string]any{"data": []any{map[string]any{"type": "gameCenterLeaderboardVersions", "id": version}}}}
	if id := plan.DetailID.ValueString(); id != "" {
		relationships["gameCenterDetail"] = linkage("gameCenterDetails", id)
	} else {
		relationships["gameCenterGroup"] = linkage("gameCenterGroups", plan.GroupID.ValueString())
	}
	body, err := json.Marshal(map[string]any{
		"data":     map[string]any{"type": "gameCenterLeaderboards", "attributes": attributes, "relationships": relationships},
		"included": []any{map[string]any{"type": "gameCenterLeaderboardVersions", "id": version}},
	})
	var leaderboard *gen.GameCenterLeaderboardV2
	if err == nil {
		out, e := r.client.GameCenterLeaderboardsV2CreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			leaderboard = &out.JSON201.Data
		}
	}
	state := plan
	if err == nil && plan.Archived.ValueBool() {
		state.ID = types.StringValue(leaderboard.Id)
		leaderboard, err = r.update(ctx, &state, &resp.Diagnostics)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create Game Center leaderboard", err)
		return
	}
	state.apply(leaderboard)
	state.keep(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *leaderboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state leaderboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardsV2GetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.GameCenterLeaderboardsV2GetInstanceParams{
		Include: &[]gen.GameCenterLeaderboardsV2GetInstanceParamsInclude{
			gen.GameCenterLeaderboardsV2GetInstanceParamsIncludeGameCenterDetail, gen.GameCenterLeaderboardsV2GetInstanceParamsIncludeGameCenterGroup,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center leaderboard", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// update sends the leaderboard's attributes as raw JSON, so that removed
// ones are sent as null.
func (r *leaderboardResource) update(ctx context.Context, m *leaderboardModel, diags *diag.Diagnostics) (*gen.GameCenterLeaderboardV2, error) {
	id := m.ID.ValueString()
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "gameCenterLeaderboards", "id": id, "attributes": m.attributes(ctx, true, diags),
	}})
	if err != nil {
		return nil, err
	}
	out, err := r.client.GameCenterLeaderboardsV2UpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *leaderboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan leaderboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	leaderboard, err := r.update(ctx, &plan, &resp.Diagnostics)
	if err != nil {
		apiError(&resp.Diagnostics, "update Game Center leaderboard", err)
		return
	}
	state := plan
	state.apply(leaderboard)
	state.keep(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete archives a leaderboard that has been live, which App Store Connect
// refuses to delete.
func (r *leaderboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state leaderboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterLeaderboardsV2DeleteInstanceWithResponse(ctx, state.ID.ValueString())
	err = asc.Err(out, err)
	if asc.HasCode(err, "STATE_ERROR") {
		state.Archived = types.BoolValue(true)
		if _, err = r.update(ctx, &state, &resp.Diagnostics); err == nil {
			resp.Diagnostics.AddWarning("Game Center leaderboard archived",
				"The leaderboard "+state.ID.ValueString()+" has been live and can't be deleted, so it was archived.")
		}
	}
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Game Center leaderboard", err)
	}
}

// ---- apple_game_center_leaderboard_localization ----

func leaderboardLocalization(l *gen.GameCenterLeaderboardLocalizationV2) localization {
	a := asc.Value(l.Attributes)
	values := map[string]*string{
		"name": a.Name, "description": a.Description, "formatterSuffix": a.FormatterSuffix, "formatterSuffixSingular": a.FormatterSuffixSingular,
	}
	if a.FormatterOverride != nil {
		values["formatterOverride"] = new(string(*a.FormatterOverride))
	}
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: values}
}

var leaderboardLocalizationAPI = localizationAPI{
	typeName: "game_center_leaderboard_localization",
	description: "Sets the name of a Game Center leaderboard, and how its scores are shown, for one locale; " +
		"`apple_game_center_leaderboard_image` adds the locale's image.",
	parent:     "leaderboard_id",
	parentDesc: "App Store Connect ID of the leaderboard, e.g. `apple_game_center_leaderboard.example.id`.",
	versions:   leaderboardMetadata,
	fields: []localizationField{
		{attr: "name", json: "name", desc: "Name of the leaderboard.", required: true, min: 1, max: 255},
		{attr: "description", json: "description", desc: "Description of the leaderboard.", min: 1, max: 255},
		{
			attr: "formatter_override", json: "formatterOverride", min: 1, max: 40,
			desc: "How scores are shown in this locale, instead of the leaderboard's `default_formatter`, e.g. `MONEY_EURO`.",
		},
		{attr: "formatter_suffix", json: "formatterSuffix", desc: "Text after a score, e.g. ` points`.", min: 1, max: 255},
		{attr: "formatter_suffix_singular", json: "formatterSuffixSingular", desc: "Text after a score of one, e.g. ` point`.", min: 1, max: 255},
	},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.GameCenterLeaderboardVersionsV2LocalizationsGetToManyRelatedWithResponse(ctx, versionID,
			&gen.GameCenterLeaderboardVersionsV2LocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterLeaderboardLocalizationsV2Response) []gen.GameCenterLeaderboardLocalizationV2 {
				return p.Data
			},
			func(p *gen.GameCenterLeaderboardLocalizationsV2Response) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		localizations := make([]localization, len(all))
		for i := range all {
			localizations[i] = leaderboardLocalization(&all[i])
		}
		return localizations, nil
	},
	// create and update send raw JSON: a create leaves unset values out, and
	// an update sends them as null, which removes them.
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		attributes := map[string]any{"locale": locale}
		for name, value := range values {
			if value != nil {
				attributes[name] = value
			}
		}
		body, err := json.Marshal(map[string]any{"data": map[string]any{
			"type": "gameCenterLeaderboardLocalizations", "attributes": attributes,
			"relationships": map[string]any{"version": linkage("gameCenterLeaderboardVersions", versionID)},
		}})
		if err != nil {
			return nil, err
		}
		out, err := c.GameCenterLeaderboardLocalizationsV2CreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(leaderboardLocalization(&out.JSON201.Data)), nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		body, err := json.Marshal(map[string]any{"data": map[string]any{"type": "gameCenterLeaderboardLocalizations", "id": id, "attributes": values}})
		if err != nil {
			return nil, err
		}
		out, err := c.GameCenterLeaderboardLocalizationsV2UpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(leaderboardLocalization(&out.JSON200.Data)), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterLeaderboardLocalizationsV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var newLeaderboardLocalizationResource = newVersionedLocalizationResource(leaderboardLocalizationAPI)

// ---- apple_game_center_leaderboard_image ----

var newLeaderboardImageResource = newLocalizationImageResource(localizationImageAPI{
	typeName:      "game_center_leaderboard_image",
	noun:          "Game Center leaderboard image",
	description:   "Uploads the image of a Game Center leaderboard for one locale. A localization has one image; uploading another replaces it.",
	localizations: leaderboardLocalizationAPI,
	existing: func(ctx context.Context, c *asc.Client, localizationID string) (string, error) {
		out, err := c.GameCenterLeaderboardLocalizationsV2ImageGetToOneRelatedWithResponse(ctx, localizationID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	reserve: func(ctx context.Context, c *asc.Client, localizationID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.GameCenterLeaderboardImageV2CreateRequest
		body.Data.Type = gen.GameCenterLeaderboardImageV2CreateRequestDataTypeGameCenterLeaderboardImages
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		l := &body.Data.Relationships.Localization.Data
		l.Id, l.Type = localizationID, gen.GameCenterLeaderboardImageV2CreateRequestDataRelationshipsLocalizationDataTypeGameCenterLeaderboardLocalizations
		out, err := c.GameCenterLeaderboardImagesV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		var body gen.GameCenterLeaderboardImageV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.GameCenterLeaderboardImageV2UpdateRequestDataTypeGameCenterLeaderboardImages, id
		asc.Alloc(&body.Data.Attributes).Uploaded = new(true)
		out, err := c.GameCenterLeaderboardImagesV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterLeaderboardImagesV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
})

// ---- action.apple_submit_game_center_leaderboard ----

var newSubmitLeaderboardAction = newSubmitVersionedAction("submit_game_center_leaderboard", "Game Center leaderboard",
	"apple_game_center_leaderboard.example.id", leaderboardMetadata,
	func(item *gen.ReviewSubmissionItemCreateRequest, versionID string) {
		v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.GameCenterLeaderboardVersion).Data)
		v.Id, v.Type = versionID, gen.ReviewSubmissionItemCreateRequestDataRelationshipsGameCenterLeaderboardVersionDataTypeGameCenterLeaderboardVersions
	})

// ---- data.apple_game_center_leaderboards ----

type leaderboardData struct {
	ID               types.String `tfsdk:"id"`
	ReferenceName    types.String `tfsdk:"reference_name"`
	VendorIdentifier types.String `tfsdk:"vendor_identifier"`
	DefaultFormatter types.String `tfsdk:"default_formatter"`
	SubmissionType   types.String `tfsdk:"submission_type"`
	ScoreSortType    types.String `tfsdk:"score_sort_type"`
	RecurrenceRule   types.String `tfsdk:"recurrence_rule"`
	Archived         types.Bool   `tfsdk:"archived"`
}

type leaderboardsData struct {
	DetailID     types.String      `tfsdk:"game_center_detail_id"`
	GroupID      types.String      `tfsdk:"game_center_group_id"`
	Archived     types.Bool        `tfsdk:"archived"`
	Leaderboards []leaderboardData `tfsdk:"leaderboards"`
}

var newLeaderboardsDataSource = listDataSource("game_center_leaderboards",
	"Lists the Game Center leaderboards of an app or of a group.",
	map[string]dsschema.Attribute{
		"game_center_detail_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of an app's Game Center detail. Set this or `game_center_group_id`.", Optional: true,
		},
		"game_center_group_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of a Game Center group. Set this or `game_center_detail_id`.", Optional: true,
		},
		"archived": dsschema.BoolAttribute{
			MarkdownDescription: "`true` lists only archived leaderboards, `false` only the others. Unset lists every leaderboard.", Optional: true,
		},
		"leaderboards": listOf("The leaderboards.", map[string]dsschema.Attribute{
			"id":                dsComputed("App Store Connect ID of the leaderboard."),
			"reference_name":    dsComputed("Name of the leaderboard in App Store Connect."),
			"vendor_identifier": dsComputed("Identifier the game reports scores with."),
			"default_formatter": dsComputed("How scores are shown."),
			"submission_type":   dsComputed("Which of a player's scores counts: " + docValues(leaderboardSubmissionTypes) + "."),
			"score_sort_type":   dsComputed("Whether high (`DESC`) or low (`ASC`) scores rank first."),
			"recurrence_rule":   dsComputed("How often a recurring leaderboard starts over. Null for a classic leaderboard."),
			"archived":          dsBool("Whether the leaderboard is archived."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *leaderboardsData, diags *diag.Diagnostics) {
		var first *gen.GameCenterLeaderboardsV2Response
		var err error
		var archived *[]string
		if !m.Archived.IsNull() {
			archived = &[]string{strconv.FormatBool(m.Archived.ValueBool())}
		}
		if id := m.DetailID.ValueString(); id != "" {
			out, e := c.GameCenterDetailsGameCenterLeaderboardsV2GetToManyRelatedWithResponse(ctx, id,
				&gen.GameCenterDetailsGameCenterLeaderboardsV2GetToManyRelatedParams{FilterArchived: archived, Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		} else {
			out, e := c.GameCenterGroupsGameCenterLeaderboardsV2GetToManyRelatedWithResponse(ctx, m.GroupID.ValueString(),
				&gen.GameCenterGroupsGameCenterLeaderboardsV2GetToManyRelatedParams{FilterArchived: archived, Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		}
		var all []gen.GameCenterLeaderboardV2
		if err == nil {
			all, err = asc.All(ctx, c, first,
				func(p *gen.GameCenterLeaderboardsV2Response) []gen.GameCenterLeaderboardV2 { return p.Data },
				func(p *gen.GameCenterLeaderboardsV2Response) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "Game Center detail or group", err)
			return
		}
		m.Leaderboards = []leaderboardData{}
		for i := range all {
			var model leaderboardModel
			model.apply(&all[i])
			if !m.Archived.IsNull() && !m.Archived.Equal(model.Archived) {
				continue
			}
			m.Leaderboards = append(m.Leaderboards, leaderboardData{
				ID: model.ID, ReferenceName: model.ReferenceName, VendorIdentifier: model.VendorIdentifier, DefaultFormatter: model.DefaultFormatter,
				SubmissionType: model.SubmissionType, ScoreSortType: model.ScoreSortType, RecurrenceRule: model.RecurrenceRule, Archived: model.Archived,
			})
		}
	}, datasourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id")))
