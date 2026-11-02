package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var (
	_ resource.ResourceWithConfigure   = &bundleIDResource{}
	_ resource.ResourceWithImportState = &bundleIDResource{}
	_ resource.ResourceWithIdentity    = &bundleIDResource{}
)

type bundleIDResource struct {
	resourceIdentity
	client *asc.Client
}

type bundleIDModel struct {
	ID         types.String `tfsdk:"id"`
	Identifier types.String `tfsdk:"identifier"`
	Name       types.String `tfsdk:"name"`
	Platform   types.String `tfsdk:"platform"`
	SeedID     types.String `tfsdk:"seed_id"`
}

func newBundleIDResource() resource.Resource {
	return &bundleIDResource{resourceIdentity: resourceIdentity{
		attrs:   idIdentity("App Store Connect ID of the bundle ID."),
		resolve: resolveBundleID,
		keyName: "bundle identifier",
	}}
}

func (r *bundleIDResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bundle_id"
}

func (r *bundleIDResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Registers a bundle ID (App ID) that identifies an app in the developer account. Import it by its " +
			"App Store Connect ID or its bundle identifier.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("App Store Connect ID of the bundle ID."),
			"identifier": replacedString("Bundle identifier, e.g. `com.example.app`, or a wildcard such as `com.example.*`. "+
				"Changing it creates a new bundle ID.", stringvalidator.LengthAtLeast(1)),
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the bundle ID in the developer account.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"platform": platformAttribute(),
			"seed_id": schema.StringAttribute{
				MarkdownDescription: "App ID prefix. Defaults to the team ID. Changing it creates a new bundle ID.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *bundleIDResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *bundleIDResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *bundleIDModel) apply(b *gen.BundleId) {
	m.ID = types.StringValue(b.Id)
	if a := b.Attributes; a != nil {
		m.Identifier = stringValue(a.Identifier)
		m.Name = stringValue(a.Name)
		m.Platform = stringValue(a.Platform)
		m.SeedID = stringValue(a.SeedId)
	}
}

func (r *bundleIDResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan bundleIDModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BundleIdCreateRequest
	body.Data.Type = gen.BundleIdCreateRequestDataTypeBundleIds
	a := &body.Data.Attributes
	a.Identifier, a.Name = plan.Identifier.ValueString(), plan.Name.ValueString()
	a.Platform = gen.BundleIdPlatform(plan.Platform.ValueString())
	if !plan.SeedID.IsUnknown() && !plan.SeedID.IsNull() {
		a.SeedId = new(plan.SeedID.ValueString())
	}
	out, err := r.client.BundleIdsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create bundle ID", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bundleIDResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state bundleIDModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BundleIdsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read bundle ID", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bundleIDResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan bundleIDModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BundleIdUpdateRequest
	body.Data.Type, body.Data.Id = gen.BundleIdUpdateRequestDataTypeBundleIds, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).Name = new(plan.Name.ValueString())
	out, err := r.client.BundleIdsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update bundle ID", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bundleIDResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bundleIDModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BundleIdsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete bundle ID", err)
	}
}

// findBundleIDs lists the bundle IDs matching params, following pagination.
func findBundleIDs(ctx context.Context, c *asc.Client, params gen.BundleIdsGetCollectionParams) ([]gen.BundleId, error) {
	params.Limit = new(listAll)
	out, err := c.BundleIdsGetCollectionWithResponse(ctx, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.BundleIdsResponse) []gen.BundleId { return p.Data },
		func(p *gen.BundleIdsResponse) *string { return p.Links.Next })
}

// bundleIDByIdentifier finds a bundle ID by its exact identifier.
func bundleIDByIdentifier(ctx context.Context, c *asc.Client, identifier string) (*gen.BundleId, error) {
	all, err := findBundleIDs(ctx, c, gen.BundleIdsGetCollectionParams{FilterIdentifier: &[]string{identifier}})
	if err != nil {
		return nil, err
	}
	for i := range all {
		if asc.Value(all[i].Attributes.Identifier) == identifier {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

func resolveBundleID(ctx context.Context, c *asc.Client, key string) (string, error) {
	return resolveByID(ctx, key,
		func(ctx context.Context, id string) error {
			out, err := c.BundleIdsGetInstanceWithResponse(ctx, id, nil)
			return asc.Err(out, err)
		},
		func(ctx context.Context, key string) (string, error) {
			b, err := bundleIDByIdentifier(ctx, c, key)
			if err != nil {
				return "", err
			}
			return b.Id, nil
		})
}
