package provider

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
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

// Game Center challenges
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center-challenges).
// A challenge lets players compete with friends on a leaderboard. It
// belongs to an app's Game Center detail or to a group. Its default image,
// localizations and their images belong to versions, which go through App
// Review; the provider hides the versions, as it does for in-app purchases.

// challengeVersions lists an challenge's versions.
func challengeVersions(ctx context.Context, c *asc.Client, challengeID string) ([]gen.GameCenterChallengeVersion, error) {
	out, err := c.GameCenterChallengesVersionsGetToManyRelatedWithResponse(ctx, challengeID, &gen.GameCenterChallengesVersionsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.GameCenterChallengeVersionsResponse) []gen.GameCenterChallengeVersion { return p.Data },
		func(p *gen.GameCenterChallengeVersionsResponse) *string { return p.Links.Next })
}

var challengeMetadata = metadataVersions{
	noun: "Game Center challenge",
	list: func(ctx context.Context, c *asc.Client, challengeID string) ([]versionInfo, error) {
		all, err := challengeVersions(ctx, c, challengeID)
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
	create: func(ctx context.Context, c *asc.Client, challengeID string) (string, error) {
		var body gen.GameCenterChallengeVersionCreateRequest
		body.Data.Type = gen.GameCenterChallengeVersionCreateRequestDataTypeGameCenterChallengeVersions
		d := &body.Data.Relationships.Challenge.Data
		d.Id, d.Type = challengeID, gen.GameCenterChallengeVersionCreateRequestDataRelationshipsChallengeDataTypeGameCenterChallenges
		out, err := c.GameCenterChallengeVersionsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

// ---- apple_game_center_challenge ----

var challengeTypes = enum(gen.GameCenterChallengeCreateRequestDataAttributesChallengeTypeLEADERBOARD)

var (
	_ resource.ResourceWithConfigure        = &challengeResource{}
	_ resource.ResourceWithImportState      = &challengeResource{}
	_ resource.ResourceWithIdentity         = &challengeResource{}
	_ resource.ResourceWithConfigValidators = &challengeResource{}
)

type challengeResource struct {
	resourceIdentity
	client *asc.Client
}

type challengeModel struct {
	DetailID         types.String `tfsdk:"game_center_detail_id"`
	GroupID          types.String `tfsdk:"game_center_group_id"`
	ReferenceName    types.String `tfsdk:"reference_name"`
	VendorIdentifier types.String `tfsdk:"vendor_identifier"`
	ChallengeType    types.String `tfsdk:"challenge_type"`
	LeaderboardID    types.String `tfsdk:"leaderboard_id"`
	Repeatable       types.Bool   `tfsdk:"repeatable"`
	Archived         types.Bool   `tfsdk:"archived"`
	ID               types.String `tfsdk:"id"`
}

func newChallengeResource() resource.Resource {
	return &challengeResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the Game Center challenge.")}}
}

func (r *challengeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_challenge"
}

func (r *challengeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center challenge, in which players compete with friends on a leaderboard, for " +
			"an app or for a group of apps. Add its name with `apple_game_center_challenge_localization` and its images " +
			"with `apple_game_center_challenge_default_image` and `apple_game_center_challenge_image`, then submit it " +
			"with `apple_submit_game_center_challenge`. Destroying the resource deletes the challenge; one that has " +
			"been live can't be deleted, so it is archived instead. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"game_center_detail_id": replacedOptionalString("App Store Connect ID of the app's Game Center detail, e.g. " +
				"`apple_game_center_detail.example.id`. Set this or `game_center_group_id`."),
			"game_center_group_id": replacedOptionalString("App Store Connect ID of a Game Center group, for a challenge " +
				"its apps share. Set this or `game_center_detail_id`."),
			"reference_name":    schema.StringAttribute{MarkdownDescription: "Name of the challenge in App Store Connect.", Required: true},
			"vendor_identifier": replacedString("Identifier the game knows the challenge by, e.g. `com.example.weekly_duel`. Changing it creates a new challenge."),
			"challenge_type": replacedString("Kind of challenge: "+docValues(challengeTypes)+". Changing it creates a new challenge.",
				oneOf(challengeTypes)),
			"leaderboard_id": schema.StringAttribute{
				MarkdownDescription: "App Store Connect ID of the leaderboard the challenge is played on, of the same app or group, e.g. " +
					"`apple_game_center_leaderboard.example.id`.",
				Required: true,
			},
			"repeatable": optionalBool("Whether players can take the challenge again. Unset keeps App Store Connect's value."),
			"archived": schema.BoolAttribute{
				MarkdownDescription: "Whether the challenge is archived, which hides it from players. Defaults to `false`.",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"id": idAttribute("App Store Connect ID of the Game Center challenge."),
		},
	}
}

func (r *challengeResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id"))}
}

func (r *challengeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *challengeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *challengeModel) apply(challenge *gen.GameCenterChallenge) {
	a := asc.Value(challenge.Attributes)
	m.ID, m.ReferenceName, m.VendorIdentifier = types.StringValue(challenge.Id), stringValue(a.ReferenceName), stringValue(a.VendorIdentifier)
	m.ChallengeType, m.Repeatable, m.Archived = stringValue(a.ChallengeType), types.BoolPointerValue(a.Repeatable), types.BoolValue(asc.Value(a.Archived))
	rel := challenge.Relationships
	if rel == nil {
		return
	}
	if rel.GameCenterDetail != nil && rel.GameCenterDetail.Data != nil {
		m.DetailID = types.StringValue(rel.GameCenterDetail.Data.Id)
	}
	if rel.GameCenterGroup != nil && rel.GameCenterGroup.Data != nil {
		m.GroupID = types.StringValue(rel.GameCenterGroup.Data.Id)
	}
	if rel.LeaderboardV2 != nil && rel.LeaderboardV2.Data != nil {
		m.LeaderboardID = types.StringValue(rel.LeaderboardV2.Data.Id)
	}
}

// Create creates the challenge with its first version, which the generated
// request type can't express, then archives it when planned.
func (r *challengeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan challengeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const version = "${version}"
	attributes := map[string]any{
		"referenceName": plan.ReferenceName.ValueString(), "vendorIdentifier": plan.VendorIdentifier.ValueString(), "challengeType": plan.ChallengeType.ValueString(),
	}
	if !plan.Repeatable.IsNull() && !plan.Repeatable.IsUnknown() {
		attributes["repeatable"] = plan.Repeatable.ValueBool()
	}
	relationships := map[string]any{
		"versions":      map[string]any{"data": []any{map[string]any{"type": "gameCenterChallengeVersions", "id": version}}},
		"leaderboardV2": linkage("gameCenterLeaderboards", plan.LeaderboardID.ValueString()),
	}
	if id := plan.DetailID.ValueString(); id != "" {
		relationships["gameCenterDetail"] = linkage("gameCenterDetails", id)
	} else {
		relationships["gameCenterGroup"] = linkage("gameCenterGroups", plan.GroupID.ValueString())
	}
	body, err := json.Marshal(map[string]any{
		"data":     map[string]any{"type": "gameCenterChallenges", "attributes": attributes, "relationships": relationships},
		"included": []any{map[string]any{"type": "gameCenterChallengeVersions", "id": version}},
	})
	var challenge *gen.GameCenterChallenge
	if err == nil {
		out, e := r.client.GameCenterChallengesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			challenge = &out.JSON201.Data
		}
	}
	if err == nil && plan.Archived.ValueBool() {
		plan.ID = types.StringValue(challenge.Id)
		challenge, err = r.update(ctx, &plan)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create Game Center challenge", err)
		return
	}
	plan.apply(challenge)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *challengeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state challengeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterChallengesGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.GameCenterChallengesGetInstanceParams{
		Include: &[]gen.GameCenterChallengesGetInstanceParamsInclude{
			gen.GameCenterChallengesGetInstanceParamsIncludeGameCenterDetail, gen.GameCenterChallengesGetInstanceParamsIncludeGameCenterGroup,
			gen.GameCenterChallengesGetInstanceParamsIncludeLeaderboardV2,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Game Center challenge", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// update sets the challenge's attributes and its leaderboard.
func (r *challengeResource) update(ctx context.Context, m *challengeModel) (*gen.GameCenterChallenge, error) {
	var body gen.GameCenterChallengeUpdateRequest
	body.Data.Type, body.Data.Id = gen.GameCenterChallengeUpdateRequestDataTypeGameCenterChallenges, m.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.ReferenceName, a.Archived = new(m.ReferenceName.ValueString()), new(m.Archived.ValueBool())
	if !m.Repeatable.IsNull() && !m.Repeatable.IsUnknown() {
		a.Repeatable = new(m.Repeatable.ValueBool())
	}
	l := asc.Alloc(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).LeaderboardV2).Data)
	l.Id, l.Type = m.LeaderboardID.ValueString(), gen.GameCenterChallengeUpdateRequestDataRelationshipsLeaderboardV2DataTypeGameCenterLeaderboards
	out, err := r.client.GameCenterChallengesUpdateInstanceWithResponse(ctx, m.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *challengeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan challengeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	challenge, err := r.update(ctx, &plan)
	if err != nil {
		apiError(&resp.Diagnostics, "update Game Center challenge", err)
		return
	}
	plan.apply(challenge)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete archives a challenge that has been live, which App Store Connect
// refuses to delete.
func (r *challengeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state challengeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterChallengesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	err = asc.Err(out, err)
	if asc.HasCode(err, "STATE_ERROR") {
		state.Archived = types.BoolValue(true)
		if _, err = r.update(ctx, &state); err == nil {
			resp.Diagnostics.AddWarning("Game Center challenge archived",
				"The challenge "+state.ID.ValueString()+" has been live and can't be deleted, so it was archived.")
		}
	}
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Game Center challenge", err)
	}
}

// ---- apple_game_center_challenge_localization ----

func challengeLocalization(l *gen.GameCenterChallengeLocalization) localization {
	a := asc.Value(l.Attributes)
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: map[string]*string{"name": a.Name, "description": a.Description}}
}

var challengeLocalizationAPI = localizationAPI{
	typeName: "game_center_challenge_localization",
	description: "Sets the name and description of a Game Center challenge for one locale; " +
		"`apple_game_center_challenge_image` adds the locale's image.",
	parent:     "challenge_id",
	parentDesc: "App Store Connect ID of the challenge, e.g. `apple_game_center_challenge.example.id`.",
	versions:   challengeMetadata,
	fields: []localizationField{
		{attr: "name", json: "name", desc: "Name of the challenge.", required: true, min: 1, max: 255},
		{attr: "description", json: "description", desc: "Description of the challenge.", min: 1, max: 255},
	},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.GameCenterChallengeVersionsLocalizationsGetToManyRelatedWithResponse(ctx, versionID,
			&gen.GameCenterChallengeVersionsLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.GameCenterChallengeLocalizationsResponse) []gen.GameCenterChallengeLocalization {
				return p.Data
			},
			func(p *gen.GameCenterChallengeLocalizationsResponse) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		localizations := make([]localization, len(all))
		for i := range all {
			localizations[i] = challengeLocalization(&all[i])
		}
		return localizations, nil
	},
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		var body gen.GameCenterChallengeLocalizationCreateRequest
		body.Data.Type = gen.GameCenterChallengeLocalizationCreateRequestDataTypeGameCenterChallengeLocalizations
		body.Data.Attributes.Locale, body.Data.Attributes.Name, body.Data.Attributes.Description = locale, asc.Value(values["name"]), values["description"]
		v := &body.Data.Relationships.Version.Data
		v.Id, v.Type = versionID, gen.GameCenterChallengeLocalizationCreateRequestDataRelationshipsVersionDataTypeGameCenterChallengeVersions
		out, err := c.GameCenterChallengeLocalizationsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(challengeLocalization(&out.JSON201.Data)), nil
	},
	// update sends raw JSON, so that a removed description is sent as null.
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		body, err := json.Marshal(map[string]any{"data": map[string]any{"type": "gameCenterChallengeLocalizations", "id": id, "attributes": values}})
		if err != nil {
			return nil, err
		}
		out, err := c.GameCenterChallengeLocalizationsUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return new(challengeLocalization(&out.JSON200.Data)), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.GameCenterChallengeLocalizationsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var newChallengeLocalizationResource = newVersionedLocalizationResource(challengeLocalizationAPI)

// ---- apple_game_center_challenge_image and apple_game_center_challenge_default_image ----

// challengeImageAPI returns the image API of an challenge's localizations, or
// of its versions for the default image.
func challengeImageAPI(versionLevel bool) localizationImageAPI {
	api := localizationImageAPI{
		typeName:      "game_center_challenge_image",
		noun:          "Game Center challenge image",
		description:   "Uploads the image of a Game Center challenge for one locale. A localization has one image; uploading another replaces it.",
		localizations: challengeLocalizationAPI,
		versionLevel:  versionLevel,
		existing: func(ctx context.Context, c *asc.Client, localizationID string) (string, error) {
			out, err := c.GameCenterChallengeLocalizationsImageGetToOneRelatedWithResponse(ctx, localizationID, nil)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			return out.JSON200.Data.Id, nil
		},
		reserve: func(ctx context.Context, c *asc.Client, ownerID, name string, size int) (string, []gen.UploadOperation, error) {
			var body gen.GameCenterChallengeImageCreateRequest
			body.Data.Type = gen.GameCenterChallengeImageCreateRequestDataTypeGameCenterChallengeImages
			body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
			rel := asc.Alloc(&body.Data.Relationships)
			if versionLevel {
				v := asc.Alloc(&asc.Alloc(&rel.Version).Data)
				v.Id, v.Type = ownerID, gen.GameCenterChallengeImageCreateRequestDataRelationshipsVersionDataTypeGameCenterChallengeVersions
			} else {
				l := asc.Alloc(&asc.Alloc(&rel.Localization).Data)
				l.Id, l.Type = ownerID, gen.GameCenterChallengeImageCreateRequestDataRelationshipsLocalizationDataTypeGameCenterChallengeLocalizations
			}
			out, err := c.GameCenterChallengeImagesCreateInstanceWithResponse(ctx, body)
			if err := asc.Err(out, err); err != nil {
				return "", nil, err
			}
			return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
		},
		commit: func(ctx context.Context, c *asc.Client, id string) (string, error) {
			var body gen.GameCenterChallengeImageUpdateRequest
			body.Data.Type, body.Data.Id = gen.GameCenterChallengeImageUpdateRequestDataTypeGameCenterChallengeImages, id
			asc.Alloc(&body.Data.Attributes).Uploaded = new(true)
			out, err := c.GameCenterChallengeImagesUpdateInstanceWithResponse(ctx, id, body)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
		},
		remove: func(ctx context.Context, c *asc.Client, id string) error {
			out, err := c.GameCenterChallengeImagesDeleteInstanceWithResponse(ctx, id)
			return asc.Err(out, err)
		},
	}
	if versionLevel {
		api.typeName, api.noun = "game_center_challenge_default_image", "Game Center challenge default image"
		api.description = "Uploads the default image of a Game Center challenge, shown for locales without an image of their own. " +
			"An challenge has one default image; uploading another replaces it."
		api.existing = func(ctx context.Context, c *asc.Client, versionID string) (string, error) {
			out, err := c.GameCenterChallengeVersionsDefaultImageGetToOneRelatedWithResponse(ctx, versionID, nil)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			return out.JSON200.Data.Id, nil
		}
	}
	return api
}

var (
	newChallengeImageResource        = newLocalizationImageResource(challengeImageAPI(false))
	newChallengeDefaultImageResource = newLocalizationImageResource(challengeImageAPI(true))
)

// ---- action.apple_submit_game_center_challenge ----

var newSubmitChallengeAction = newSubmitVersionedAction("submit_game_center_challenge", "Game Center challenge",
	"apple_game_center_challenge.example.id", challengeMetadata,
	func(item *gen.ReviewSubmissionItemCreateRequest, versionID string) {
		v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.GameCenterChallengeVersion).Data)
		v.Id, v.Type = versionID, gen.ReviewSubmissionItemCreateRequestDataRelationshipsGameCenterChallengeVersionDataTypeGameCenterChallengeVersions
	})

// ---- data.apple_game_center_challenges ----

type challengeData struct {
	ID               types.String `tfsdk:"id"`
	ReferenceName    types.String `tfsdk:"reference_name"`
	VendorIdentifier types.String `tfsdk:"vendor_identifier"`
	ChallengeType    types.String `tfsdk:"challenge_type"`
	Archived         types.Bool   `tfsdk:"archived"`
}

type challengesData struct {
	DetailID   types.String    `tfsdk:"game_center_detail_id"`
	GroupID    types.String    `tfsdk:"game_center_group_id"`
	Challenges []challengeData `tfsdk:"challenges"`
}

var newChallengesDataSource = listDataSource("game_center_challenges",
	"Lists the Game Center challenges of an app or of a group.",
	ownerAttributes("challenges", listOf("The challenges.", map[string]dsschema.Attribute{
		"id":                dsComputed("App Store Connect ID of the challenge."),
		"reference_name":    dsComputed("Name of the challenge in App Store Connect."),
		"vendor_identifier": dsComputed("Identifier the game knows the challenge by."),
		"challenge_type":    dsComputed("Kind of challenge: " + docValues(challengeTypes) + "."),
		"archived":          dsBool("Whether the challenge is archived."),
	})),
	func(ctx context.Context, c *asc.Client, m *challengesData, diags *diag.Diagnostics) {
		var first *gen.GameCenterChallengesResponse
		var err error
		if id := m.DetailID.ValueString(); id != "" {
			out, e := c.GameCenterDetailsGameCenterChallengesGetToManyRelatedWithResponse(ctx, id, &gen.GameCenterDetailsGameCenterChallengesGetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		} else {
			out, e := c.GameCenterGroupsGameCenterChallengesGetToManyRelatedWithResponse(ctx, m.GroupID.ValueString(), &gen.GameCenterGroupsGameCenterChallengesGetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				first = out.JSON200
			}
		}
		var all []gen.GameCenterChallenge
		if err == nil {
			all, err = asc.All(ctx, c, first,
				func(p *gen.GameCenterChallengesResponse) []gen.GameCenterChallenge { return p.Data },
				func(p *gen.GameCenterChallengesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "Game Center detail or group", err)
			return
		}
		m.Challenges = []challengeData{}
		for i := range all {
			var model challengeModel
			model.apply(&all[i])
			m.Challenges = append(m.Challenges, challengeData{
				ID: model.ID, ReferenceName: model.ReferenceName, VendorIdentifier: model.VendorIdentifier, ChallengeType: model.ChallengeType, Archived: model.Archived,
			})
		}
	}, datasourcevalidator.ExactlyOneOf(path.MatchRoot("game_center_detail_id"), path.MatchRoot("game_center_group_id")))
