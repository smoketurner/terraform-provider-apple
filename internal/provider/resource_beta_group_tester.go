package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Testers are account-wide: one tester can be in groups of many apps,
// managed by different configurations. apple_beta_group_tester therefore
// manages a single membership, never the tester as a whole.

var (
	_ resource.ResourceWithConfigure   = &betaGroupTesterResource{}
	_ resource.ResourceWithImportState = &betaGroupTesterResource{}
	_ resource.ResourceWithIdentity    = &betaGroupTesterResource{}
)

type betaGroupTesterResource struct {
	resourceIdentity
	client *asc.Client
}

type betaGroupTesterModel struct {
	BetaGroupID  types.String `tfsdk:"beta_group_id"`
	Email        types.String `tfsdk:"email"`
	FirstName    types.String `tfsdk:"first_name"`
	LastName     types.String `tfsdk:"last_name"`
	BetaTesterID types.String `tfsdk:"beta_tester_id"`
	InviteType   types.String `tfsdk:"invite_type"`
	State        types.String `tfsdk:"state"`
}

func newBetaGroupTesterResource() resource.Resource {
	return &betaGroupTesterResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "beta_group_id", description: "App Store Connect ID of the beta group.", state: "beta_group_id"},
		{name: "beta_tester_id", description: "App Store Connect ID of the beta tester.", state: "beta_tester_id"},
	}}}
}

func (r *betaGroupTesterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_group_tester"
}

func (r *betaGroupTesterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a TestFlight beta tester to one beta group. Testers belong to the whole account, so " +
			"this resource manages only the membership: if no tester has the email, it creates one in the group, and " +
			"otherwise it adds the existing tester to the group. Destroying it removes the tester from this group only; " +
			"the tester and their other groups, including other apps' groups, are left alone. To delete a tester " +
			"everywhere, use the `apple_delete_beta_tester` action. Import it with `<beta_group_id>/<email or beta tester ID>`.",
		Attributes: map[string]schema.Attribute{
			"beta_group_id": replacedString("App Store Connect ID of the beta group. Changing it moves the tester to another group."),
			"email": replacedString("Email address of the tester, compared case-insensitively. Changing it adds another tester.",
				stringvalidator.RegexMatches(emailRegexp, "must be an email address")),
			"first_name": optionalString("First name, used only when the tester is created. An existing tester keeps their " +
				"name, and changing this argument later changes nothing in App Store Connect."),
			"last_name":      optionalString("Last name, used only when the tester is created, like `first_name`."),
			"beta_tester_id": schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the tester.", Computed: true, PlanModifiers: keep},
			"invite_type":    schema.StringAttribute{MarkdownDescription: "How the tester was invited: `EMAIL` or `PUBLIC_LINK`.", Computed: true, PlanModifiers: keep},
			"state":          schema.StringAttribute{MarkdownDescription: "State of the tester's invitation, e.g. `INVITED` or `ACCEPTED`.", Computed: true, PlanModifiers: keep},
		},
	}
}

func (r *betaGroupTesterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

// ImportState accepts "<beta_group_id>/<email or tester ID>" or the
// identity.
func (r *betaGroupTesterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := r.importParts(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.ID != "" {
		id, err := resolveBetaTester(ctx, r.client, parts[1])
		switch {
		case errors.Is(err, errNoMatch):
			resp.Diagnostics.AddError("Cannot import", fmt.Sprintf("No beta tester has the ID or email %q.", parts[1]))
			return
		case err != nil:
			apiError(&resp.Diagnostics, "look up "+parts[1], err)
			return
		}
		parts[1] = id
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("beta_group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("beta_tester_id"), parts[1])...)
}

func (m *betaGroupTesterModel) apply(t *gen.BetaTester) {
	a := asc.Value(t.Attributes)
	m.BetaTesterID = types.StringValue(t.Id)
	if email := string(asc.Value(a.Email)); email != "" && !strings.EqualFold(email, m.Email.ValueString()) {
		m.Email = types.StringValue(email)
	}
	m.InviteType, m.State = stringValue(a.InviteType), stringValue(a.State)
}

func groupLinkage(groupID string) gen.BetaTesterBetaGroupsLinkagesRequest {
	var body gen.BetaTesterBetaGroupsLinkagesRequest
	e := asc.Push(&body.Data)
	e.Id, e.Type = groupID, gen.BetaTesterBetaGroupsLinkagesRequestDataTypeBetaGroups
	return body
}

func (r *betaGroupTesterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaGroupTesterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := plan.BetaGroupID.ValueString()
	tester, err := betaTesterByEmail(ctx, r.client, plan.Email.ValueString())
	switch {
	case err == nil:
		out, err := r.client.BetaTestersBetaGroupsCreateToManyRelationshipWithResponse(ctx, tester.Id, groupLinkage(groupID))
		if err := asc.Err(out, err); err != nil {
			apiError(&resp.Diagnostics, "add beta tester to group", err)
			return
		}
	case errors.Is(err, errNoMatch):
		var body gen.BetaTesterCreateRequest
		body.Data.Type = gen.BetaTesterCreateRequestDataTypeBetaTesters
		a := &body.Data.Attributes
		a.Email, a.FirstName, a.LastName = openapi_types.Email(plan.Email.ValueString()), knownString(plan.FirstName), knownString(plan.LastName)
		e := asc.Append(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).BetaGroups).Data)
		e.Id, e.Type = groupID, gen.BetaTesterCreateRequestDataRelationshipsBetaGroupsDataTypeBetaGroups
		out, err := r.client.BetaTestersCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			apiError(&resp.Diagnostics, "create beta tester", err)
			return
		}
		tester = &out.JSON201.Data
	default:
		apiError(&resp.Diagnostics, "look up beta tester", err)
		return
	}
	plan.apply(tester)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaGroupTesterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaGroupTesterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.BetaTesterID.ValueString()
	out, err := r.client.BetaTestersGetInstanceWithResponse(ctx, id, nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read beta tester", err)
		return
	}
	groups, err := betaTesterGroupIDs(ctx, r.client, id)
	if err != nil {
		apiError(&resp.Diagnostics, "read beta tester groups", err)
		return
	}
	if !slices.Contains(groups, state.BetaGroupID.ValueString()) {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update records name changes, which only apply when a tester is created.
func (r *betaGroupTesterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaGroupTesterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the tester from the group, leaving the tester and their
// other groups.
func (r *betaGroupTesterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state betaGroupTesterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaTestersBetaGroupsDeleteToManyRelationshipWithResponse(ctx, state.BetaTesterID.ValueString(),
		groupLinkage(state.BetaGroupID.ValueString()))
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "remove beta tester from group", err)
	}
}

func betaTesterGroupIDs(ctx context.Context, c *asc.Client, id string) ([]string, error) {
	out, err := c.BetaTestersBetaGroupsGetToManyRelationshipWithResponse(ctx, id,
		&gen.BetaTestersBetaGroupsGetToManyRelationshipParams{Limit: new(linkageLimit)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	ids, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.BetaTesterBetaGroupsLinkagesResponse) []string {
			var ids []string
			for _, d := range p.Data {
				ids = append(ids, d.Id)
			}
			return ids
		},
		func(p *gen.BetaTesterBetaGroupsLinkagesResponse) *string { return p.Links.Next })
	slices.Sort(ids)
	return ids, err
}

// findBetaTesters lists the testers matching params, following pagination.
func findBetaTesters(ctx context.Context, c *asc.Client, params gen.BetaTestersGetCollectionParams) ([]gen.BetaTester, error) {
	params.Limit = new(listAll)
	out, err := c.BetaTestersGetCollectionWithResponse(ctx, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.BetaTestersResponse) []gen.BetaTester { return p.Data },
		func(p *gen.BetaTestersResponse) *string { return p.Links.Next })
}

// betaTesterByEmail finds a tester by email, compared case-insensitively.
// The API's email filter may not be, so the lowercase address is tried too.
func betaTesterByEmail(ctx context.Context, c *asc.Client, email string) (*gen.BetaTester, error) {
	filters := []string{email}
	if lower := strings.ToLower(email); lower != email {
		filters = append(filters, lower)
	}
	all, err := findBetaTesters(ctx, c, gen.BetaTestersGetCollectionParams{FilterEmail: &filters})
	if err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(string(asc.Value(asc.Value(all[i].Attributes).Email)), email) {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

func resolveBetaTester(ctx context.Context, c *asc.Client, key string) (string, error) {
	return resolveByID(ctx, key,
		func(ctx context.Context, id string) error {
			out, err := c.BetaTestersGetInstanceWithResponse(ctx, id, nil)
			return asc.Err(out, err)
		},
		func(ctx context.Context, key string) (string, error) {
			t, err := betaTesterByEmail(ctx, c, key)
			if err != nil {
				return "", err
			}
			return t.Id, nil
		})
}
