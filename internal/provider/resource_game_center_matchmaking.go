package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Game Center matchmaking
// (https://developer.apple.com/documentation/appstoreconnectapi/game-center-matchmaking).
// A rule set holds the rules and teams that decide which players are
// matched; a queue matches the players who ask for a match with a rule set.

var matchmakingRuleTypes = enum(gen.GameCenterMatchmakingRuleCreateRequestDataAttributesTypeCOMPATIBLE,
	gen.GameCenterMatchmakingRuleCreateRequestDataAttributesTypeDISTANCE, gen.GameCenterMatchmakingRuleCreateRequestDataAttributesTypeMATCH,
	gen.GameCenterMatchmakingRuleCreateRequestDataAttributesTypeTEAM)

func playersAttribute(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{MarkdownDescription: desc, Required: true, Validators: []validator.Int64{int64validator.AtLeast(1)}}
}

// ruleSetChildIdentity is the identity of a rule or a team, which the API
// reads only through its rule set.
func ruleSetChildIdentity(noun string) resourceIdentity {
	return resourceIdentity{attrs: []identityAttribute{
		{name: "rule_set_id", description: "App Store Connect ID of the matchmaking rule set.", state: "rule_set_id"},
		{name: "id", description: "App Store Connect ID of the matchmaking " + noun + ".", state: "id"},
	}}
}

// ---- apple_game_center_matchmaking_rule_set ----

var (
	_ resource.ResourceWithConfigure   = &ruleSetResource{}
	_ resource.ResourceWithImportState = &ruleSetResource{}
	_ resource.ResourceWithIdentity    = &ruleSetResource{}
)

type ruleSetResource struct {
	resourceIdentity
	client *asc.Client
}

type ruleSetModel struct {
	ReferenceName       types.String `tfsdk:"reference_name"`
	RuleLanguageVersion types.Int64  `tfsdk:"rule_language_version"`
	MinPlayers          types.Int64  `tfsdk:"min_players"`
	MaxPlayers          types.Int64  `tfsdk:"max_players"`
	ID                  types.String `tfsdk:"id"`
}

func newRuleSetResource() resource.Resource {
	return &ruleSetResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the matchmaking rule set.")}}
}

func (r *ruleSetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_matchmaking_rule_set"
}

func (r *ruleSetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center matchmaking rule set, which holds the rules and teams that decide which " +
			"players are matched. Add them with `apple_game_center_matchmaking_rule` and " +
			"`apple_game_center_matchmaking_team`, and use the rule set in an `apple_game_center_matchmaking_queue`. " +
			"Destroying the resource deletes the rule set with its rules and teams. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"reference_name": replacedString("Name of the rule set. Changing it creates a new rule set."),
			"rule_language_version": schema.Int64Attribute{
				MarkdownDescription: "Version of the rule expression language, e.g. `1`. Changing it creates a new rule set.", Required: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"min_players": playersAttribute("Fewest players in a match."),
			"max_players": playersAttribute("Most players in a match."),
			"id":          idAttribute("App Store Connect ID of the matchmaking rule set."),
		},
	}
}

func (r *ruleSetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *ruleSetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *ruleSetModel) apply(set *gen.GameCenterMatchmakingRuleSet) {
	a := asc.Value(set.Attributes)
	m.ID, m.ReferenceName, m.RuleLanguageVersion = types.StringValue(set.Id), stringValue(a.ReferenceName), intValue(a.RuleLanguageVersion)
	m.MinPlayers, m.MaxPlayers = intValue(a.MinPlayers), intValue(a.MaxPlayers)
}

func (r *ruleSetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan ruleSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterMatchmakingRuleSetCreateRequest
	body.Data.Type = gen.GameCenterMatchmakingRuleSetCreateRequestDataTypeGameCenterMatchmakingRuleSets
	a := &body.Data.Attributes
	a.ReferenceName, a.RuleLanguageVersion = plan.ReferenceName.ValueString(), int(plan.RuleLanguageVersion.ValueInt64())
	a.MinPlayers, a.MaxPlayers = int(plan.MinPlayers.ValueInt64()), int(plan.MaxPlayers.ValueInt64())
	out, err := r.client.GameCenterMatchmakingRuleSetsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create matchmaking rule set", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleSetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state ruleSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingRuleSetsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read matchmaking rule set", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ruleSetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan ruleSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterMatchmakingRuleSetUpdateRequest
	body.Data.Type, body.Data.Id = gen.GameCenterMatchmakingRuleSetUpdateRequestDataTypeGameCenterMatchmakingRuleSets, plan.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.MinPlayers, a.MaxPlayers = new(int(plan.MinPlayers.ValueInt64())), new(int(plan.MaxPlayers.ValueInt64()))
	out, err := r.client.GameCenterMatchmakingRuleSetsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update matchmaking rule set", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleSetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ruleSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingRuleSetsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete matchmaking rule set", err)
	}
}

// ---- apple_game_center_matchmaking_rule ----

var (
	_ resource.ResourceWithConfigure   = &matchmakingRuleResource{}
	_ resource.ResourceWithImportState = &matchmakingRuleResource{}
	_ resource.ResourceWithIdentity    = &matchmakingRuleResource{}
)

type matchmakingRuleResource struct {
	resourceIdentity
	client *asc.Client
}

type matchmakingRuleModel struct {
	RuleSetID     types.String  `tfsdk:"rule_set_id"`
	ReferenceName types.String  `tfsdk:"reference_name"`
	Type          types.String  `tfsdk:"type"`
	Description   types.String  `tfsdk:"description"`
	Expression    types.String  `tfsdk:"expression"`
	Weight        types.Float64 `tfsdk:"weight"`
	ID            types.String  `tfsdk:"id"`
}

func newMatchmakingRuleResource() resource.Resource {
	return &matchmakingRuleResource{resourceIdentity: ruleSetChildIdentity("rule")}
}

func (r *matchmakingRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_matchmaking_rule"
}

func (r *matchmakingRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a rule to a Game Center matchmaking rule set: an expression over the players' match " +
			"requests that decides who can be matched. Import it with `<rule_set_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"rule_set_id":    replacedString("App Store Connect ID of the rule set, e.g. `apple_game_center_matchmaking_rule_set.example.id`."),
			"reference_name": replacedString("Name of the rule. Changing it creates a new rule."),
			"type": replacedString("Kind of rule: "+docValues(matchmakingRuleTypes)+". Changing it creates a new rule.",
				oneOf(matchmakingRuleTypes)),
			"description": schema.StringAttribute{MarkdownDescription: "Description of the rule.", Required: true},
			"expression": schema.StringAttribute{
				MarkdownDescription: "Expression in the rule language, e.g. `requests[0].properties.mode == requests[1].properties.mode`.", Required: true,
			},
			"weight": schema.Float64Attribute{MarkdownDescription: "Weight of a `DISTANCE` rule against the others.", Optional: true},
			"id":     idAttribute("App Store Connect ID of the matchmaking rule."),
		},
	}
}

func (r *matchmakingRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *matchmakingRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// float32Value converts an API number held as float32 without adding
// digits it doesn't have.
func float32Value(p *float32) types.Float64 {
	if p == nil {
		return types.Float64Null()
	}
	v, err := strconv.ParseFloat(strconv.FormatFloat(float64(*p), 'g', -1, 32), 64)
	if err != nil {
		return types.Float64Value(float64(*p))
	}
	return types.Float64Value(v)
}

func (m *matchmakingRuleModel) apply(rule *gen.GameCenterMatchmakingRule) {
	a := asc.Value(rule.Attributes)
	m.ID, m.ReferenceName, m.Type = types.StringValue(rule.Id), stringValue(a.ReferenceName), stringValue(a.Type)
	m.Description, m.Expression, m.Weight = stringValue(a.Description), stringValue(a.Expression), float32Value(a.Weight)
}

// send creates or updates the rule as raw JSON, so that the weight keeps its
// digits and a removed weight is sent as null.
func (r *matchmakingRuleResource) send(ctx context.Context, m *matchmakingRuleModel, create bool) (*gen.GameCenterMatchmakingRule, error) {
	attributes := map[string]any{"description": m.Description.ValueString(), "expression": m.Expression.ValueString()}
	data := map[string]any{"type": "gameCenterMatchmakingRules", "attributes": attributes}
	if weight := m.Weight.ValueFloat64Pointer(); weight != nil || !create {
		attributes["weight"] = weight
	}
	if create {
		attributes["referenceName"], attributes["type"] = m.ReferenceName.ValueString(), m.Type.ValueString()
		data["relationships"] = map[string]any{"ruleSet": linkage("gameCenterMatchmakingRuleSets", m.RuleSetID.ValueString())}
	} else {
		data["id"] = m.ID.ValueString()
	}
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return nil, err
	}
	if create {
		out, err := r.client.GameCenterMatchmakingRulesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return &out.JSON201.Data, nil
	}
	out, err := r.client.GameCenterMatchmakingRulesUpdateInstanceWithBodyWithResponse(ctx, m.ID.ValueString(), "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *matchmakingRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan matchmakingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.send(ctx, &plan, true)
	if err != nil {
		apiError(&resp.Diagnostics, "create matchmaking rule", err)
		return
	}
	plan.apply(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read finds the rule among its rule set's: the API can't read one by ID.
func (r *matchmakingRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state matchmakingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingRuleSetsRulesGetToManyRelatedWithResponse(ctx, state.RuleSetID.ValueString(),
		&gen.GameCenterMatchmakingRuleSetsRulesGetToManyRelatedParams{Limit: new(listAll)})
	var all []gen.GameCenterMatchmakingRule
	if err = asc.Err(out, err); err == nil {
		all, err = asc.All(ctx, r.client, out.JSON200,
			func(p *gen.GameCenterMatchmakingRulesResponse) []gen.GameCenterMatchmakingRule { return p.Data },
			func(p *gen.GameCenterMatchmakingRulesResponse) *string { return p.Links.Next })
	}
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "read matchmaking rule", err)
		return
	}
	for i := range all {
		if all[i].Id == state.ID.ValueString() {
			state.apply(&all[i])
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *matchmakingRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan matchmakingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.send(ctx, &plan, false)
	if err != nil {
		apiError(&resp.Diagnostics, "update matchmaking rule", err)
		return
	}
	plan.apply(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *matchmakingRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state matchmakingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingRulesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete matchmaking rule", err)
	}
}

// ---- apple_game_center_matchmaking_team ----

var (
	_ resource.ResourceWithConfigure   = &matchmakingTeamResource{}
	_ resource.ResourceWithImportState = &matchmakingTeamResource{}
	_ resource.ResourceWithIdentity    = &matchmakingTeamResource{}
)

type matchmakingTeamResource struct {
	resourceIdentity
	client *asc.Client
}

type matchmakingTeamModel struct {
	RuleSetID     types.String `tfsdk:"rule_set_id"`
	ReferenceName types.String `tfsdk:"reference_name"`
	MinPlayers    types.Int64  `tfsdk:"min_players"`
	MaxPlayers    types.Int64  `tfsdk:"max_players"`
	ID            types.String `tfsdk:"id"`
}

func newMatchmakingTeamResource() resource.Resource {
	return &matchmakingTeamResource{resourceIdentity: ruleSetChildIdentity("team")}
}

func (r *matchmakingTeamResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_matchmaking_team"
}

func (r *matchmakingTeamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a team to a Game Center matchmaking rule set, for matches that put players in teams. " +
			"Import it with `<rule_set_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"rule_set_id":    replacedString("App Store Connect ID of the rule set, e.g. `apple_game_center_matchmaking_rule_set.example.id`."),
			"reference_name": replacedString("Name of the team, which rules refer to. Changing it creates a new team."),
			"min_players":    playersAttribute("Fewest players in the team."),
			"max_players":    playersAttribute("Most players in the team."),
			"id":             idAttribute("App Store Connect ID of the matchmaking team."),
		},
	}
}

func (r *matchmakingTeamResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *matchmakingTeamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *matchmakingTeamModel) apply(team *gen.GameCenterMatchmakingTeam) {
	a := asc.Value(team.Attributes)
	m.ID, m.ReferenceName, m.MinPlayers, m.MaxPlayers = types.StringValue(team.Id), stringValue(a.ReferenceName), intValue(a.MinPlayers), intValue(a.MaxPlayers)
}

func (r *matchmakingTeamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan matchmakingTeamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterMatchmakingTeamCreateRequest
	body.Data.Type = gen.GameCenterMatchmakingTeamCreateRequestDataTypeGameCenterMatchmakingTeams
	a := &body.Data.Attributes
	a.ReferenceName, a.MinPlayers, a.MaxPlayers = plan.ReferenceName.ValueString(), int(plan.MinPlayers.ValueInt64()), int(plan.MaxPlayers.ValueInt64())
	set := &body.Data.Relationships.RuleSet.Data
	set.Id, set.Type = plan.RuleSetID.ValueString(), gen.GameCenterMatchmakingTeamCreateRequestDataRelationshipsRuleSetDataTypeGameCenterMatchmakingRuleSets
	out, err := r.client.GameCenterMatchmakingTeamsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create matchmaking team", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read finds the team among its rule set's: the API can't read one by ID.
func (r *matchmakingTeamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state matchmakingTeamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingRuleSetsTeamsGetToManyRelatedWithResponse(ctx, state.RuleSetID.ValueString(),
		&gen.GameCenterMatchmakingRuleSetsTeamsGetToManyRelatedParams{Limit: new(listAll)})
	var all []gen.GameCenterMatchmakingTeam
	if err = asc.Err(out, err); err == nil {
		all, err = asc.All(ctx, r.client, out.JSON200,
			func(p *gen.GameCenterMatchmakingTeamsResponse) []gen.GameCenterMatchmakingTeam { return p.Data },
			func(p *gen.GameCenterMatchmakingTeamsResponse) *string { return p.Links.Next })
	}
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "read matchmaking team", err)
		return
	}
	for i := range all {
		if all[i].Id == state.ID.ValueString() {
			state.apply(&all[i])
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *matchmakingTeamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan matchmakingTeamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.GameCenterMatchmakingTeamUpdateRequest
	body.Data.Type, body.Data.Id = gen.GameCenterMatchmakingTeamUpdateRequestDataTypeGameCenterMatchmakingTeams, plan.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.MinPlayers, a.MaxPlayers = new(int(plan.MinPlayers.ValueInt64())), new(int(plan.MaxPlayers.ValueInt64()))
	out, err := r.client.GameCenterMatchmakingTeamsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update matchmaking team", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *matchmakingTeamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state matchmakingTeamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingTeamsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete matchmaking team", err)
	}
}

// ---- apple_game_center_matchmaking_queue ----

var (
	_ resource.ResourceWithConfigure   = &matchmakingQueueResource{}
	_ resource.ResourceWithImportState = &matchmakingQueueResource{}
	_ resource.ResourceWithIdentity    = &matchmakingQueueResource{}
)

type matchmakingQueueResource struct {
	resourceIdentity
	client *asc.Client
}

type matchmakingQueueModel struct {
	ReferenceName       types.String `tfsdk:"reference_name"`
	RuleSetID           types.String `tfsdk:"rule_set_id"`
	ExperimentRuleSetID types.String `tfsdk:"experiment_rule_set_id"`
	ClassicBundleIDs    types.Set    `tfsdk:"classic_matchmaking_bundle_ids"`
	ID                  types.String `tfsdk:"id"`
}

func newMatchmakingQueueResource() resource.Resource {
	return &matchmakingQueueResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the matchmaking queue.")}}
}

func (r *matchmakingQueueResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_game_center_matchmaking_queue"
}

func (r *matchmakingQueueResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a Game Center matchmaking queue, which matches the players who ask for a match with a " +
			"rule set, and optionally tries another rule set as an experiment. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"reference_name": replacedString("Name of the queue, which the game names in match requests. Changing it creates a new queue."),
			"rule_set_id": schema.StringAttribute{
				MarkdownDescription: "App Store Connect ID of the rule set the queue matches with, e.g. `apple_game_center_matchmaking_rule_set.example.id`.",
				Required:            true,
			},
			"experiment_rule_set_id": optionalString("App Store Connect ID of a rule set to try on part of the match requests."),
			"classic_matchmaking_bundle_ids": schema.SetAttribute{
				MarkdownDescription: "Bundle IDs of apps whose classic matchmaking requests go to this queue.", Optional: true, ElementType: types.StringType,
			},
			"id": idAttribute("App Store Connect ID of the matchmaking queue."),
		},
	}
}

func (r *matchmakingQueueResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *matchmakingQueueResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *matchmakingQueueModel) apply(queue *gen.GameCenterMatchmakingQueue) {
	a := asc.Value(queue.Attributes)
	m.ID, m.ReferenceName = types.StringValue(queue.Id), stringValue(a.ReferenceName)
	m.ClassicBundleIDs = types.SetNull(types.StringType)
	if a.ClassicMatchmakingBundleIds != nil {
		m.ClassicBundleIDs = stringsValue(*a.ClassicMatchmakingBundleIds)
	}
	m.ExperimentRuleSetID = types.StringNull()
	rel := queue.Relationships
	if rel == nil {
		return
	}
	if rel.RuleSet != nil && rel.RuleSet.Data != nil {
		m.RuleSetID = types.StringValue(rel.RuleSet.Data.Id)
	}
	if rel.ExperimentRuleSet != nil && rel.ExperimentRuleSet.Data != nil {
		m.ExperimentRuleSetID = types.StringValue(rel.ExperimentRuleSet.Data.Id)
	}
}

// send creates or updates the queue as raw JSON, so that a removed
// experiment rule set and removed bundle IDs are sent as null.
func (r *matchmakingQueueResource) send(ctx context.Context, m *matchmakingQueueModel, create bool, diags *diag.Diagnostics) (*gen.GameCenterMatchmakingQueue, error) {
	attributes := map[string]any{}
	if !m.ClassicBundleIDs.IsNull() {
		attributes["classicMatchmakingBundleIds"] = setStrings(ctx, m.ClassicBundleIDs, diags)
	} else if !create {
		attributes["classicMatchmakingBundleIds"] = nil
	}
	relationships := map[string]any{"ruleSet": linkage("gameCenterMatchmakingRuleSets", m.RuleSetID.ValueString())}
	if id := m.ExperimentRuleSetID.ValueString(); id != "" {
		relationships["experimentRuleSet"] = linkage("gameCenterMatchmakingRuleSets", id)
	} else if !create {
		relationships["experimentRuleSet"] = map[string]any{"data": nil}
	}
	data := map[string]any{"type": "gameCenterMatchmakingQueues", "attributes": attributes, "relationships": relationships}
	if create {
		attributes["referenceName"] = m.ReferenceName.ValueString()
	} else {
		data["id"] = m.ID.ValueString()
	}
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return nil, err
	}
	if create {
		out, err := r.client.GameCenterMatchmakingQueuesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return &out.JSON201.Data, nil
	}
	out, err := r.client.GameCenterMatchmakingQueuesUpdateInstanceWithBodyWithResponse(ctx, m.ID.ValueString(), "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *matchmakingQueueResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan matchmakingQueueModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	queue, err := r.send(ctx, &plan, true, &resp.Diagnostics)
	if err != nil {
		apiError(&resp.Diagnostics, "create matchmaking queue", err)
		return
	}
	plan.apply(queue)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *matchmakingQueueResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state matchmakingQueueModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingQueuesGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.GameCenterMatchmakingQueuesGetInstanceParams{
		Include: &[]gen.GameCenterMatchmakingQueuesGetInstanceParamsInclude{
			gen.GameCenterMatchmakingQueuesGetInstanceParamsIncludeRuleSet, gen.GameCenterMatchmakingQueuesGetInstanceParamsIncludeExperimentRuleSet,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read matchmaking queue", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *matchmakingQueueResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan matchmakingQueueModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	queue, err := r.send(ctx, &plan, false, &resp.Diagnostics)
	if err != nil {
		apiError(&resp.Diagnostics, "update matchmaking queue", err)
		return
	}
	plan.apply(queue)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *matchmakingQueueResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state matchmakingQueueModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GameCenterMatchmakingQueuesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete matchmaking queue", err)
	}
}

// ---- data sources ----

type ruleSetsData struct {
	RuleSets []ruleSetModel `tfsdk:"rule_sets"`
}

var newRuleSetsDataSource = listDataSource("game_center_matchmaking_rule_sets",
	"Lists the account's Game Center matchmaking rule sets.",
	map[string]dsschema.Attribute{
		"rule_sets": listOf("The rule sets.", map[string]dsschema.Attribute{
			"id":                    dsComputed("App Store Connect ID of the rule set."),
			"reference_name":        dsComputed("Name of the rule set."),
			"rule_language_version": dsInt("Version of the rule expression language."),
			"min_players":           dsInt("Fewest players in a match."),
			"max_players":           dsInt("Most players in a match."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *ruleSetsData, diags *diag.Diagnostics) {
		out, err := c.GameCenterMatchmakingRuleSetsGetCollectionWithResponse(ctx, &gen.GameCenterMatchmakingRuleSetsGetCollectionParams{Limit: new(listAll)})
		var all []gen.GameCenterMatchmakingRuleSet
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.GameCenterMatchmakingRuleSetsResponse) []gen.GameCenterMatchmakingRuleSet { return p.Data },
				func(p *gen.GameCenterMatchmakingRuleSetsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list matchmaking rule sets", err)
			return
		}
		m.RuleSets = make([]ruleSetModel, len(all))
		for i := range all {
			m.RuleSets[i].apply(&all[i])
		}
	})

type matchmakingQueueData struct {
	ID                  types.String `tfsdk:"id"`
	ReferenceName       types.String `tfsdk:"reference_name"`
	RuleSetID           types.String `tfsdk:"rule_set_id"`
	ExperimentRuleSetID types.String `tfsdk:"experiment_rule_set_id"`
}

type matchmakingQueuesData struct {
	Queues []matchmakingQueueData `tfsdk:"queues"`
}

var newMatchmakingQueuesDataSource = listDataSource("game_center_matchmaking_queues",
	"Lists the account's Game Center matchmaking queues.",
	map[string]dsschema.Attribute{
		"queues": listOf("The queues.", map[string]dsschema.Attribute{
			"id":                     dsComputed("App Store Connect ID of the queue."),
			"reference_name":         dsComputed("Name of the queue."),
			"rule_set_id":            dsComputed("App Store Connect ID of the queue's rule set."),
			"experiment_rule_set_id": dsComputed("App Store Connect ID of the queue's experiment rule set. Null without one."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *matchmakingQueuesData, diags *diag.Diagnostics) {
		out, err := c.GameCenterMatchmakingQueuesGetCollectionWithResponse(ctx, &gen.GameCenterMatchmakingQueuesGetCollectionParams{
			Include: &[]gen.GameCenterMatchmakingQueuesGetCollectionParamsInclude{
				gen.GameCenterMatchmakingQueuesGetCollectionParamsIncludeRuleSet, gen.GameCenterMatchmakingQueuesGetCollectionParamsIncludeExperimentRuleSet,
			},
			Limit: new(listAll),
		})
		var all []gen.GameCenterMatchmakingQueue
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.GameCenterMatchmakingQueuesResponse) []gen.GameCenterMatchmakingQueue { return p.Data },
				func(p *gen.GameCenterMatchmakingQueuesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list matchmaking queues", err)
			return
		}
		m.Queues = []matchmakingQueueData{}
		for i := range all {
			var model matchmakingQueueModel
			model.apply(&all[i])
			m.Queues = append(m.Queues, matchmakingQueueData{
				ID: model.ID, ReferenceName: model.ReferenceName, RuleSetID: model.RuleSetID, ExperimentRuleSetID: model.ExperimentRuleSetID,
			})
		}
	})
