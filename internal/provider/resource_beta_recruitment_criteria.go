package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var deviceFamilies = enum(gen.DeviceFamilyAPPLETV, gen.DeviceFamilyAPPLEWATCH, gen.DeviceFamilyIPAD, gen.DeviceFamilyIPHONE,
	gen.DeviceFamilyMAC, gen.DeviceFamilyVISION)

var (
	_ resource.ResourceWithConfigure   = &betaRecruitmentCriteriaResource{}
	_ resource.ResourceWithImportState = &betaRecruitmentCriteriaResource{}
	_ resource.ResourceWithIdentity    = &betaRecruitmentCriteriaResource{}
)

type betaRecruitmentCriteriaResource struct {
	resourceIdentity
	client *asc.Client
}

type recruitmentFilterModel struct {
	DeviceFamily     types.String `tfsdk:"device_family"`
	MinimumOSVersion types.String `tfsdk:"minimum_os_version"`
	MaximumOSVersion types.String `tfsdk:"maximum_os_version"`
}

type betaRecruitmentCriteriaModel struct {
	ID          types.String             `tfsdk:"id"`
	BetaGroupID types.String             `tfsdk:"beta_group_id"`
	Filters     []recruitmentFilterModel `tfsdk:"filters"`
}

func newBetaRecruitmentCriteriaResource() resource.Resource {
	return &betaRecruitmentCriteriaResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "beta_group_id", description: "App Store Connect ID of the beta group.", state: "beta_group_id"},
	}}}
}

func (r *betaRecruitmentCriteriaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_recruitment_criteria"
}

func (r *betaRecruitmentCriteriaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Limits who can join an external beta group's public link to testers with the listed device " +
			"families and OS versions. The `apple_beta_recruitment_criterion_options` data source lists the values App " +
			"Store Connect accepts. Import it by beta group ID.",
		Attributes: map[string]schema.Attribute{
			"id":            idAttribute("App Store Connect ID of the criteria."),
			"beta_group_id": replacedString("App Store Connect ID of an external beta group. Changing it creates new criteria."),
			"filters": schema.ListNestedAttribute{
				MarkdownDescription: "Device families and OS version ranges testers must match. Changes are made in place.",
				Required:            true,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"device_family": schema.StringAttribute{
						MarkdownDescription: "Device family: " + docValues(deviceFamilies) + ".",
						Required:            true,
						Validators:          []validator.String{oneOf(deviceFamilies)},
					},
					"minimum_os_version": optionalString("Lowest OS version allowed, e.g. `17.0`."),
					"maximum_os_version": optionalString("Highest OS version allowed."),
				}},
			},
		},
	}
}

func (r *betaRecruitmentCriteriaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *betaRecruitmentCriteriaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *betaRecruitmentCriteriaModel) apiFilters() []gen.DeviceFamilyOsVersionFilter {
	out := make([]gen.DeviceFamilyOsVersionFilter, len(m.Filters))
	for i, f := range m.Filters {
		out[i] = gen.DeviceFamilyOsVersionFilter{
			DeviceFamily:       new(gen.DeviceFamily(f.DeviceFamily.ValueString())),
			MinimumOsInclusive: knownString(f.MinimumOSVersion),
			MaximumOsInclusive: knownString(f.MaximumOSVersion),
		}
	}
	return out
}

func (m *betaRecruitmentCriteriaModel) apply(c *gen.BetaRecruitmentCriterion) {
	m.ID = types.StringValue(c.Id)
	m.Filters = []recruitmentFilterModel{}
	for _, f := range asc.Value(asc.Value(c.Attributes).DeviceFamilyOsVersionFilters) {
		m.Filters = append(m.Filters, recruitmentFilterModel{
			DeviceFamily:     stringValue(f.DeviceFamily),
			MinimumOSVersion: stringValue(f.MinimumOsInclusive),
			MaximumOSVersion: stringValue(f.MaximumOsInclusive),
		})
	}
}

func (r *betaRecruitmentCriteriaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaRecruitmentCriteriaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaRecruitmentCriterionCreateRequest
	body.Data.Type = gen.BetaRecruitmentCriterionCreateRequestDataTypeBetaRecruitmentCriteria
	body.Data.Attributes.DeviceFamilyOsVersionFilters = plan.apiFilters()
	body.Data.Relationships.BetaGroup.Data.Id = plan.BetaGroupID.ValueString()
	body.Data.Relationships.BetaGroup.Data.Type = gen.BetaRecruitmentCriterionCreateRequestDataRelationshipsBetaGroupDataTypeBetaGroups
	out, err := r.client.BetaRecruitmentCriteriaCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create recruitment criteria", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaRecruitmentCriteriaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaRecruitmentCriteriaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaGroupsBetaRecruitmentCriteriaGetToOneRelatedWithResponse(ctx, state.BetaGroupID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read recruitment criteria", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *betaRecruitmentCriteriaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaRecruitmentCriteriaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaRecruitmentCriterionUpdateRequest
	body.Data.Type, body.Data.Id = gen.BetaRecruitmentCriterionUpdateRequestDataTypeBetaRecruitmentCriteria, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).DeviceFamilyOsVersionFilters = new(plan.apiFilters())
	out, err := r.client.BetaRecruitmentCriteriaUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update recruitment criteria", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaRecruitmentCriteriaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state betaRecruitmentCriteriaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaRecruitmentCriteriaDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete recruitment criteria", err)
	}
}
