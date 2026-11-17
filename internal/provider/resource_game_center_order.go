package provider

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// The order of Game Center items: the achievements, leaderboards and
// leaderboard sets of an app or of a group, and the leaderboards of a set.
// The API sets an order by replacing the relationship with the same items
// in another order.

// orderOwner is one of the things whose items a resource can order.
type orderOwner struct {
	attr, desc string
	// prefix names the owner in the resource's ID, e.g. "detail".
	prefix string
}

var detailOrGroup = []orderOwner{
	{attr: "game_center_detail_id", prefix: "detail", desc: "App Store Connect ID of the app's Game Center detail. Set this or `game_center_group_id`."},
	{attr: "game_center_group_id", prefix: "group", desc: "App Store Connect ID of a Game Center group. Set this or `game_center_detail_id`."},
}

// orderAPI is the part of the API a kind of order needs. owner indexes
// owners.
type orderAPI struct {
	typeName, description string
	owners                []orderOwner
	items, itemsDesc      string // the list attribute, e.g. "achievement_ids"
	list                  func(ctx context.Context, c *asc.Client, owner int, ownerID string) ([]string, error)
	replace               func(ctx context.Context, c *asc.Client, owner int, ownerID string, ids []string) error
}

var (
	_ resource.ResourceWithConfigure        = &orderResource{}
	_ resource.ResourceWithImportState      = &orderResource{}
	_ resource.ResourceWithIdentity         = &orderResource{}
	_ resource.ResourceWithConfigValidators = &orderResource{}
)

type orderResource struct {
	resourceIdentity
	api    orderAPI
	client *asc.Client
}

func newOrderResource(api orderAPI) func() resource.Resource {
	return func() resource.Resource {
		return &orderResource{api: api, resourceIdentity: resourceIdentity{attrs: idIdentity("The owner's kind and App Store Connect ID, e.g. `" + api.owners[0].prefix + "/<id>`.")}}
	}
}

func (r *orderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *orderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	var keys []string
	attributes := map[string]schema.Attribute{
		r.api.items: schema.ListAttribute{
			MarkdownDescription: r.api.itemsDesc, Required: true, ElementType: types.StringType,
			Validators: []validator.List{listvalidator.SizeAtLeast(1), listvalidator.UniqueValues()},
		},
		"id": idAttribute("The owner's kind and App Store Connect ID."),
	}
	for _, owner := range r.api.owners {
		keys = append(keys, "`"+owner.prefix+"/<"+owner.attr+">`")
		if len(r.api.owners) == 1 {
			attributes[owner.attr] = replacedString(owner.desc)
		} else {
			attributes[owner.attr] = replacedOptionalString(owner.desc)
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: r.api.description + " App Store Connect has no way to remove an order, so destroying the resource " +
			"leaves the items as they are. Import it with " + strings.Join(keys, " or ") + ".",
		Attributes: attributes,
	}
}

func (r *orderResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	if len(r.api.owners) == 1 {
		return nil
	}
	var owners path.Expressions
	for _, owner := range r.api.owners {
		owners = append(owners, path.MatchRoot(owner.attr))
	}
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(owners...)}
}

func (r *orderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *orderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// owner returns which owner a plan or state sets, and its ID. A state that
// was imported has only the resource's ID, which names both.
func (r *orderResource) owner(ctx context.Context, get func(context.Context, path.Path, any) diag.Diagnostics, diags *diag.Diagnostics) (int, string) {
	for i, owner := range r.api.owners {
		var id types.String
		diags.Append(get(ctx, path.Root(owner.attr), &id)...)
		if id.ValueString() != "" {
			return i, id.ValueString()
		}
	}
	var id types.String
	diags.Append(get(ctx, path.Root("id"), &id)...)
	prefix, ownerID, _ := strings.Cut(id.ValueString(), "/")
	for i, owner := range r.api.owners {
		if owner.prefix == prefix && ownerID != "" {
			return i, ownerID
		}
	}
	diags.AddError("Invalid ID", "The ID "+id.ValueString()+" doesn't name the owner of the "+strings.ReplaceAll(r.api.typeName, "_", " ")+".")
	return 0, ""
}

// write sets the planned order and records it.
func (r *orderResource) write(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	owner, ownerID := r.owner(ctx, plan.GetAttribute, diags)
	var items types.List
	diags.Append(plan.GetAttribute(ctx, path.Root(r.api.items), &items)...)
	if diags.HasError() {
		return
	}
	if err := r.api.replace(ctx, r.client, owner, ownerID, listStrings(ctx, items)); err != nil {
		apiError(diags, "set "+strings.ReplaceAll(r.api.typeName, "_", " "), err)
		return
	}
	state.Raw = plan.Raw
	diags.Append(state.SetAttribute(ctx, path.Root("id"), r.api.owners[owner].prefix+"/"+ownerID)...)
}

func (r *orderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	r.write(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *orderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	r.write(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *orderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	owner, ownerID := r.owner(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ids, err := r.api.list(ctx, r.client, owner, ownerID)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+strings.ReplaceAll(r.api.typeName, "_", " "), err)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(r.api.owners[owner].attr), ownerID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(r.api.items), stringsList(ids))...)
}

// Delete only forgets the order: the API has no way to remove one.
func (r *orderResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// listed pages through a list response and returns the IDs of its items.
func listed[P, T any](ctx context.Context, c *asc.Client, first *P, data func(*P) []T, next func(*P) *string, id func(*T) string) ([]string, error) {
	all, err := asc.All(ctx, c, first, data, next)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(all))
	for i := range all {
		ids[i] = id(&all[i])
	}
	return ids, nil
}

// ordered builds a linkage request body: every generated one has the same
// shape, so it is filled through JSON.
func ordered[B any](kind string, ids []string) B {
	data := make([]map[string]string, len(ids))
	for i, id := range ids {
		data[i] = map[string]string{"type": kind, "id": id}
	}
	var body B
	// The shapes match, so this can't fail.
	if encoded, err := json.Marshal(map[string]any{"data": data}); err == nil {
		_ = json.Unmarshal(encoded, &body)
	}
	return body
}

var newAchievementOrderResource = newOrderResource(orderAPI{
	typeName:    "game_center_achievement_order",
	description: "Sets the order in which the Game Center achievements of an app or of a group are shown.",
	owners:      detailOrGroup,
	items:       "achievement_ids",
	itemsDesc:   "App Store Connect IDs of every achievement of the app or group, in order.",
	list: func(ctx context.Context, c *asc.Client, owner int, ownerID string) ([]string, error) {
		data := func(p *gen.GameCenterAchievementsV2Response) []gen.GameCenterAchievementV2 { return p.Data }
		next := func(p *gen.GameCenterAchievementsV2Response) *string { return p.Links.Next }
		id := func(a *gen.GameCenterAchievementV2) string { return a.Id }
		if owner == 0 {
			out, err := c.GameCenterDetailsGameCenterAchievementsV2GetToManyRelatedWithResponse(ctx, ownerID, &gen.GameCenterDetailsGameCenterAchievementsV2GetToManyRelatedParams{Limit: new(listAll)})
			if err := asc.Err(out, err); err != nil {
				return nil, err
			}
			return listed(ctx, c, out.JSON200, data, next, id)
		}
		out, err := c.GameCenterGroupsGameCenterAchievementsV2GetToManyRelatedWithResponse(ctx, ownerID, &gen.GameCenterGroupsGameCenterAchievementsV2GetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return listed(ctx, c, out.JSON200, data, next, id)
	},
	replace: func(ctx context.Context, c *asc.Client, owner int, ownerID string, ids []string) error {
		if owner == 0 {
			out, err := c.GameCenterDetailsGameCenterAchievementsV2ReplaceToManyRelationshipWithResponse(ctx, ownerID,
				ordered[gen.GameCenterDetailGameCenterAchievementsV2LinkagesRequest]("gameCenterAchievements", ids))
			return asc.Err(out, err)
		}
		out, err := c.GameCenterGroupsGameCenterAchievementsV2ReplaceToManyRelationshipWithResponse(ctx, ownerID,
			ordered[gen.GameCenterGroupGameCenterAchievementsV2LinkagesRequest]("gameCenterAchievements", ids))
		return asc.Err(out, err)
	},
})

var newLeaderboardOrderResource = newOrderResource(orderAPI{
	typeName:    "game_center_leaderboard_order",
	description: "Sets the order in which the Game Center leaderboards of an app or of a group are shown.",
	owners:      detailOrGroup,
	items:       "leaderboard_ids",
	itemsDesc:   "App Store Connect IDs of every leaderboard of the app or group, in order.",
	list: func(ctx context.Context, c *asc.Client, owner int, ownerID string) ([]string, error) {
		data := func(p *gen.GameCenterLeaderboardsV2Response) []gen.GameCenterLeaderboardV2 { return p.Data }
		next := func(p *gen.GameCenterLeaderboardsV2Response) *string { return p.Links.Next }
		id := func(l *gen.GameCenterLeaderboardV2) string { return l.Id }
		if owner == 0 {
			out, err := c.GameCenterDetailsGameCenterLeaderboardsV2GetToManyRelatedWithResponse(ctx, ownerID, &gen.GameCenterDetailsGameCenterLeaderboardsV2GetToManyRelatedParams{Limit: new(listAll)})
			if err := asc.Err(out, err); err != nil {
				return nil, err
			}
			return listed(ctx, c, out.JSON200, data, next, id)
		}
		out, err := c.GameCenterGroupsGameCenterLeaderboardsV2GetToManyRelatedWithResponse(ctx, ownerID, &gen.GameCenterGroupsGameCenterLeaderboardsV2GetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return listed(ctx, c, out.JSON200, data, next, id)
	},
	replace: func(ctx context.Context, c *asc.Client, owner int, ownerID string, ids []string) error {
		if owner == 0 {
			out, err := c.GameCenterDetailsGameCenterLeaderboardsV2ReplaceToManyRelationshipWithResponse(ctx, ownerID,
				ordered[gen.GameCenterDetailGameCenterLeaderboardsV2LinkagesRequest]("gameCenterLeaderboards", ids))
			return asc.Err(out, err)
		}
		out, err := c.GameCenterGroupsGameCenterLeaderboardsV2ReplaceToManyRelationshipWithResponse(ctx, ownerID,
			ordered[gen.GameCenterGroupGameCenterLeaderboardsV2LinkagesRequest]("gameCenterLeaderboards", ids))
		return asc.Err(out, err)
	},
})

var newLeaderboardSetOrderResource = newOrderResource(orderAPI{
	typeName:    "game_center_leaderboard_set_order",
	description: "Sets the order in which the Game Center leaderboard sets of an app or of a group are shown.",
	owners:      detailOrGroup,
	items:       "leaderboard_set_ids",
	itemsDesc:   "App Store Connect IDs of every leaderboard set of the app or group, in order.",
	list: func(ctx context.Context, c *asc.Client, owner int, ownerID string) ([]string, error) {
		data := func(p *gen.GameCenterLeaderboardSetsV2Response) []gen.GameCenterLeaderboardSetV2 { return p.Data }
		next := func(p *gen.GameCenterLeaderboardSetsV2Response) *string { return p.Links.Next }
		id := func(s *gen.GameCenterLeaderboardSetV2) string { return s.Id }
		if owner == 0 {
			out, err := c.GameCenterDetailsGameCenterLeaderboardSetsV2GetToManyRelatedWithResponse(ctx, ownerID, &gen.GameCenterDetailsGameCenterLeaderboardSetsV2GetToManyRelatedParams{Limit: new(listAll)})
			if err := asc.Err(out, err); err != nil {
				return nil, err
			}
			return listed(ctx, c, out.JSON200, data, next, id)
		}
		out, err := c.GameCenterGroupsGameCenterLeaderboardSetsV2GetToManyRelatedWithResponse(ctx, ownerID, &gen.GameCenterGroupsGameCenterLeaderboardSetsV2GetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return listed(ctx, c, out.JSON200, data, next, id)
	},
	replace: func(ctx context.Context, c *asc.Client, owner int, ownerID string, ids []string) error {
		if owner == 0 {
			out, err := c.GameCenterDetailsGameCenterLeaderboardSetsV2ReplaceToManyRelationshipWithResponse(ctx, ownerID,
				ordered[gen.GameCenterDetailGameCenterLeaderboardSetsV2LinkagesRequest]("gameCenterLeaderboardSets", ids))
			return asc.Err(out, err)
		}
		out, err := c.GameCenterGroupsGameCenterLeaderboardSetsV2ReplaceToManyRelationshipWithResponse(ctx, ownerID,
			ordered[gen.GameCenterGroupGameCenterLeaderboardSetsV2LinkagesRequest]("gameCenterLeaderboardSets", ids))
		return asc.Err(out, err)
	},
})

var newSetMemberOrderResource = newOrderResource(orderAPI{
	typeName: "game_center_leaderboard_set_member_order",
	description: "Sets the order of the leaderboards in a Game Center leaderboard set. The list replaces the set's " +
		"leaderboards, so name the same ones as the set's `apple_game_center_leaderboard_set_member` resources.",
	owners:    []orderOwner{{attr: "leaderboard_set_id", prefix: "set", desc: "App Store Connect ID of the leaderboard set."}},
	items:     "leaderboard_ids",
	itemsDesc: "App Store Connect IDs of every leaderboard in the set, in order; refer to the member resources' `leaderboard_id`.",
	list: func(ctx context.Context, c *asc.Client, _ int, setID string) ([]string, error) {
		out, err := c.GameCenterLeaderboardSetsV2GameCenterLeaderboardsGetToManyRelatedWithResponse(ctx, setID,
			&gen.GameCenterLeaderboardSetsV2GameCenterLeaderboardsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return listed(ctx, c, out.JSON200,
			func(p *gen.GameCenterLeaderboardsV2Response) []gen.GameCenterLeaderboardV2 { return p.Data },
			func(p *gen.GameCenterLeaderboardsV2Response) *string { return p.Links.Next },
			func(l *gen.GameCenterLeaderboardV2) string { return l.Id })
	},
	replace: func(ctx context.Context, c *asc.Client, _ int, setID string, ids []string) error {
		out, err := c.GameCenterLeaderboardSetsV2GameCenterLeaderboardsReplaceToManyRelationshipWithResponse(ctx, setID,
			ordered[gen.GameCenterLeaderboardSetV2GameCenterLeaderboardsLinkagesRequest]("gameCenterLeaderboards", ids))
		return asc.Err(out, err)
	},
})
