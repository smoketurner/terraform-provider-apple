package provider

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var (
	_ resource.ResourceWithConfigure   = &betaTesterResource{}
	_ resource.ResourceWithImportState = &betaTesterResource{}
	_ resource.ResourceWithIdentity    = &betaTesterResource{}
)

type betaTesterResource struct {
	resourceIdentity
	client *asc.Client
}

type betaTesterModel struct {
	ID           types.String `tfsdk:"id"`
	Email        types.String `tfsdk:"email"`
	FirstName    types.String `tfsdk:"first_name"`
	LastName     types.String `tfsdk:"last_name"`
	BetaGroupIDs types.Set    `tfsdk:"beta_group_ids"`
	InviteType   types.String `tfsdk:"invite_type"`
	State        types.String `tfsdk:"state"`
}

func newBetaTesterResource() resource.Resource {
	return &betaTesterResource{resourceIdentity: resourceIdentity{
		attrs:   idIdentity("App Store Connect ID of the beta tester."),
		resolve: resolveBetaTester,
		keyName: "email",
	}}
}

func (r *betaTesterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_tester"
}

func (r *betaTesterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a TestFlight beta tester to beta groups. `beta_group_ids` is authoritative: groups the " +
			"tester belongs to that are not listed, including other apps' groups, are removed. A tester whose email is " +
			"already registered is adopted. Destroying the resource deletes the tester, which removes their access to " +
			"every app. Import it by its App Store Connect ID or email.",
		Attributes: map[string]schema.Attribute{
			"id":         idAttribute("App Store Connect ID of the beta tester."),
			"email":      replacedString("Email address of the tester. Changing it creates a new tester.", stringvalidator.RegexMatches(emailRegexp, "must be an email address")),
			"first_name": optionalReplacedString("First name of the tester. Changing it creates a new tester."),
			"last_name":  optionalReplacedString("Last name of the tester. Changing it creates a new tester."),
			"beta_group_ids": schema.SetAttribute{
				MarkdownDescription: "App Store Connect IDs of the beta groups the tester belongs to. Changes are made in place.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"invite_type": schema.StringAttribute{MarkdownDescription: "How the tester was invited: `EMAIL` or `PUBLIC_LINK`.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"state": schema.StringAttribute{MarkdownDescription: "State of the tester's invitation, e.g. `INVITED` or `ACCEPTED`.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *betaTesterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *betaTesterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *betaTesterModel) apply(t *gen.BetaTester) {
	m.ID = types.StringValue(t.Id)
	a := asc.Value(t.Attributes)
	if a.Email != nil {
		m.Email = types.StringValue(string(*a.Email))
	}
	m.FirstName, m.LastName = stringValue(a.FirstName), stringValue(a.LastName)
	m.InviteType, m.State = stringValue(a.InviteType), stringValue(a.State)
}

// setGroups makes the tester's groups exactly want, given they are now have.
func (r *betaTesterResource) setGroups(ctx context.Context, id string, have, want []string) error {
	var add, remove gen.BetaTesterBetaGroupsLinkagesRequest
	for _, g := range want {
		if !slices.Contains(have, g) {
			e := asc.Push(&add.Data)
			e.Id, e.Type = g, gen.BetaTesterBetaGroupsLinkagesRequestDataTypeBetaGroups
		}
	}
	for _, g := range have {
		if !slices.Contains(want, g) {
			e := asc.Push(&remove.Data)
			e.Id, e.Type = g, gen.BetaTesterBetaGroupsLinkagesRequestDataTypeBetaGroups
		}
	}
	if len(add.Data) > 0 {
		out, err := r.client.BetaTestersBetaGroupsCreateToManyRelationshipWithResponse(ctx, id, add)
		if err := asc.Err(out, err); err != nil {
			return err
		}
	}
	if len(remove.Data) > 0 {
		out, err := r.client.BetaTestersBetaGroupsDeleteToManyRelationshipWithResponse(ctx, id, remove)
		if err := asc.Err(out, err); err != nil {
			return err
		}
	}
	return nil
}

func (r *betaTesterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaTesterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groups := setStrings(ctx, plan.BetaGroupIDs, &resp.Diagnostics)
	existing, err := betaTesterByEmail(ctx, r.client, plan.Email.ValueString())
	if err != nil && !errors.Is(err, errNoMatch) {
		apiError(&resp.Diagnostics, "look up beta tester", err)
		return
	}
	var tester *gen.BetaTester
	if existing != nil {
		have, err := betaTesterGroupIDs(ctx, r.client, existing.Id)
		if err == nil {
			err = r.setGroups(ctx, existing.Id, have, groups)
		}
		if err != nil {
			apiError(&resp.Diagnostics, "adopt beta tester", err)
			return
		}
		tester = existing
	} else {
		var body gen.BetaTesterCreateRequest
		body.Data.Type = gen.BetaTesterCreateRequestDataTypeBetaTesters
		a := &body.Data.Attributes
		a.Email = openapi_types.Email(plan.Email.ValueString())
		if !plan.FirstName.IsNull() {
			a.FirstName = new(plan.FirstName.ValueString())
		}
		if !plan.LastName.IsNull() {
			a.LastName = new(plan.LastName.ValueString())
		}
		for _, g := range groups {
			e := asc.Append(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).BetaGroups).Data)
			e.Id, e.Type = g, gen.BetaTesterCreateRequestDataRelationshipsBetaGroupsDataTypeBetaGroups
		}
		out, err := r.client.BetaTestersCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			apiError(&resp.Diagnostics, "create beta tester", err)
			return
		}
		tester = &out.JSON201.Data
	}
	// Keep the configured values: App Store Connect compares emails
	// case-insensitively, and an adopted tester's names cannot be changed in
	// place, so the next plan replaces the tester if they differ.
	email, first, last := plan.Email, plan.FirstName, plan.LastName
	plan.apply(tester)
	plan.Email, plan.FirstName, plan.LastName = email, first, last
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaTesterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaTesterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
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
	email := state.Email
	state.apply(&out.JSON200.Data)
	if strings.EqualFold(email.ValueString(), state.Email.ValueString()) {
		state.Email = email
	}
	groups, err := betaTesterGroupIDs(ctx, r.client, id)
	if err != nil {
		apiError(&resp.Diagnostics, "read beta tester groups", err)
		return
	}
	state.BetaGroupIDs = stringSet(ctx, groups, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *betaTesterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state betaTesterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	have := setStrings(ctx, state.BetaGroupIDs, &resp.Diagnostics)
	want := setStrings(ctx, plan.BetaGroupIDs, &resp.Diagnostics)
	if err := r.setGroups(ctx, plan.ID.ValueString(), have, want); err != nil {
		apiError(&resp.Diagnostics, "update beta tester groups", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaTesterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state betaTesterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaTestersDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete beta tester", err)
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
