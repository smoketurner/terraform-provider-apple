package provider

import (
	"context"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var profileTypes = []string{
	"IOS_APP_ADHOC", "IOS_APP_DEVELOPMENT", "IOS_APP_INHOUSE", "IOS_APP_STORE",
	"MAC_APP_DEVELOPMENT", "MAC_APP_DIRECT", "MAC_APP_STORE",
	"MAC_CATALYST_APP_DEVELOPMENT", "MAC_CATALYST_APP_DIRECT", "MAC_CATALYST_APP_STORE",
	"TVOS_APP_ADHOC", "TVOS_APP_DEVELOPMENT", "TVOS_APP_INHOUSE", "TVOS_APP_STORE",
}

var (
	_ resource.ResourceWithConfigure   = &profileResource{}
	_ resource.ResourceWithImportState = &profileResource{}
	_ resource.ResourceWithIdentity    = &profileResource{}
	_ resource.ResourceWithModifyPlan  = &profileResource{}
)

type profileResource struct {
	resourceIdentity
	client *asc.Client
	now    func() time.Time
}

type profileModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	ProfileType    types.String `tfsdk:"profile_type"`
	BundleID       types.String `tfsdk:"bundle_id"`
	CertificateIDs types.Set    `tfsdk:"certificate_ids"`
	DeviceIDs      types.Set    `tfsdk:"device_ids"`
	Platform       types.String `tfsdk:"platform"`
	ProfileState   types.String `tfsdk:"profile_state"`
	ProfileContent types.String `tfsdk:"profile_content"`
	UUID           types.String `tfsdk:"uuid"`
	CreatedDate    types.String `tfsdk:"created_date"`
	ExpirationDate types.String `tfsdk:"expiration_date"`
}

func newProfileResource() resource.Resource {
	return &profileResource{
		resourceIdentity: resourceIdentity{
			attrs:   idIdentity("App Store Connect ID of the profile."),
			resolve: resolveProfile,
			keyName: "name",
		},
		now: time.Now,
	}
}

func (r *profileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_profile"
}

func (r *profileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replaceSet := []planmodifier.Set{setplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a provisioning profile. Profiles cannot be changed, so changing any argument creates a " +
			"new one.\n\nA profile becomes `INVALID` when one of its certificates is revoked or expires, one of its devices " +
			"is disabled, or its bundle ID's capabilities change. The next plan after that, or after the profile expires, " +
			"replaces it. Import it by its App Store Connect ID or name.",
		Attributes: map[string]schema.Attribute{
			"id":           idAttribute("App Store Connect ID of the profile."),
			"name":         replacedString("Name of the profile.", stringvalidator.LengthAtLeast(1)),
			"profile_type": replacedString("Profile type: "+docValues(profileTypes)+".", oneOf(profileTypes)),
			"bundle_id":    replacedString("App Store Connect ID of the bundle ID, e.g. `apple_bundle_id.app.id`."),
			"certificate_ids": schema.SetAttribute{
				MarkdownDescription: "App Store Connect IDs of the certificates the profile allows to sign the app.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
				PlanModifiers:       replaceSet,
			},
			"device_ids": schema.SetAttribute{
				MarkdownDescription: "App Store Connect IDs of the devices, for development and ad hoc profiles.",
				Optional:            true,
				ElementType:         types.StringType,
				PlanModifiers:       replaceSet,
			},
			"platform": schema.StringAttribute{MarkdownDescription: "Platform of the profile.", Computed: true, PlanModifiers: keep},
			"profile_state": schema.StringAttribute{
				MarkdownDescription: "State of the profile: `ACTIVE` or `INVALID`.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"profile_content": schema.StringAttribute{
				MarkdownDescription: "The profile, base64-encoded. Write it to a `.mobileprovision` or `.provisionprofile` " +
					"file with the `content_base64` argument of `local_sensitive_file`.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"uuid":            schema.StringAttribute{MarkdownDescription: "UUID of the profile.", Computed: true, PlanModifiers: keep},
			"created_date":    schema.StringAttribute{MarkdownDescription: "Creation date, in RFC 3339 format.", Computed: true, PlanModifiers: keep},
			"expiration_date": schema.StringAttribute{MarkdownDescription: "Expiration date, in RFC 3339 format.", Computed: true, PlanModifiers: keep},
		},
	}
}

func (r *profileResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *profileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ModifyPlan replaces a profile that is INVALID or past its expiration date,
// because App Store Connect cannot regenerate one through the API.
func (r *profileResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state profileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	expired := false
	if exp, err := time.Parse(time.RFC3339, state.ExpirationDate.ValueString()); err == nil {
		expired = !r.now().Before(exp)
	}
	if state.ProfileState.ValueString() != string(gen.ProfileAttributesProfileStateINVALID) && !expired {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("profile_state"), types.StringUnknown())...)
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("profile_state"))
}

func (m *profileModel) apply(p *gen.Profile) {
	m.ID = types.StringValue(p.Id)
	if a := p.Attributes; a != nil {
		m.Name = stringValue(a.Name)
		m.ProfileType = stringValue(a.ProfileType)
		m.Platform = stringValue(a.Platform)
		m.ProfileState = stringValue(a.ProfileState)
		m.ProfileContent = stringValue(a.ProfileContent)
		m.UUID = stringValue(a.Uuid)
		m.CreatedDate = timeValue(a.CreatedDate)
		m.ExpirationDate = timeValue(a.ExpirationDate)
	}
}

func (r *profileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan profileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.ProfileCreateRequest
	body.Data.Type = gen.ProfileCreateRequestDataTypeProfiles
	body.Data.Attributes.Name = plan.Name.ValueString()
	body.Data.Attributes.ProfileType = gen.ProfileCreateRequestDataAttributesProfileType(plan.ProfileType.ValueString())
	rel := &body.Data.Relationships
	rel.BundleId.Data.Id = plan.BundleID.ValueString()
	rel.BundleId.Data.Type = gen.ProfileCreateRequestDataRelationshipsBundleIdDataTypeBundleIds
	for _, id := range setStrings(ctx, plan.CertificateIDs, &resp.Diagnostics) {
		e := asc.Push(&rel.Certificates.Data)
		e.Id, e.Type = id, gen.ProfileCreateRequestDataRelationshipsCertificatesDataTypeCertificates
	}
	for _, id := range setStrings(ctx, plan.DeviceIDs, &resp.Diagnostics) {
		e := asc.Append(&asc.Alloc(&rel.Devices).Data)
		e.Id, e.Type = id, gen.ProfileCreateRequestDataRelationshipsDevicesDataTypeDevices
	}
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.ProfilesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create profile", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *profileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state profileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	out, err := r.client.ProfilesGetInstanceWithResponse(ctx, id, nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read profile", err)
		return
	}
	state.apply(&out.JSON200.Data)

	bundle, err := r.client.ProfilesBundleIdGetToOneRelationshipWithResponse(ctx, id)
	if err := asc.Err(bundle, err); err != nil {
		apiError(&resp.Diagnostics, "read profile bundle ID", err)
		return
	}
	state.BundleID = types.StringValue(bundle.JSON200.Data.Id)

	certs, err := profileCertificateIDs(ctx, r.client, id)
	if err != nil {
		apiError(&resp.Diagnostics, "read profile certificates", err)
		return
	}
	state.CertificateIDs = stringSet(ctx, certs, &resp.Diagnostics)

	devices, err := profileDeviceIDs(ctx, r.client, id)
	if err != nil {
		apiError(&resp.Diagnostics, "read profile devices", err)
		return
	}
	// A profile without devices reads back as null, matching an unset
	// device_ids.
	if len(devices) > 0 || !state.DeviceIDs.IsNull() {
		state.DeviceIDs = stringSet(ctx, devices, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func profileCertificateIDs(ctx context.Context, c *asc.Client, id string) ([]string, error) {
	out, err := c.ProfilesCertificatesGetToManyRelationshipWithResponse(ctx, id,
		&gen.ProfilesCertificatesGetToManyRelationshipParams{Limit: new(linkageLimit)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	ids, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.ProfileCertificatesLinkagesResponse) []string {
			var ids []string
			for _, d := range p.Data {
				ids = append(ids, d.Id)
			}
			return ids
		},
		func(p *gen.ProfileCertificatesLinkagesResponse) *string { return p.Links.Next })
	slices.Sort(ids)
	return ids, err
}

func profileDeviceIDs(ctx context.Context, c *asc.Client, id string) ([]string, error) {
	out, err := c.ProfilesDevicesGetToManyRelationshipWithResponse(ctx, id,
		&gen.ProfilesDevicesGetToManyRelationshipParams{Limit: new(linkageLimit)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	ids, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.ProfileDevicesLinkagesResponse) []string {
			var ids []string
			for _, d := range p.Data {
				ids = append(ids, d.Id)
			}
			return ids
		},
		func(p *gen.ProfileDevicesLinkagesResponse) *string { return p.Links.Next })
	slices.Sort(ids)
	return ids, err
}

// Update only records the plan: every argument forces replacement, so no
// change reaches App Store Connect in place.
func (r *profileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.Append(resp.State.Set(ctx, req.Plan.Raw)...)
}

func (r *profileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state profileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.ProfilesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete profile", err)
	}
}

// findProfiles lists the profiles matching params, following pagination.
func findProfiles(ctx context.Context, c *asc.Client, params gen.ProfilesGetCollectionParams) ([]gen.Profile, error) {
	params.Limit = new(listAll)
	out, err := c.ProfilesGetCollectionWithResponse(ctx, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.ProfilesResponse) []gen.Profile { return p.Data },
		func(p *gen.ProfilesResponse) *string { return p.Links.Next })
}

// profileByName finds a profile by its exact name.
func profileByName(ctx context.Context, c *asc.Client, name string) (*gen.Profile, error) {
	all, err := findProfiles(ctx, c, gen.ProfilesGetCollectionParams{FilterName: &[]string{name}})
	if err != nil {
		return nil, err
	}
	for i := range all {
		if asc.Value(all[i].Attributes.Name) == name {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

func resolveProfile(ctx context.Context, c *asc.Client, key string) (string, error) {
	return resolveByID(ctx, key,
		func(ctx context.Context, id string) error {
			out, err := c.ProfilesGetInstanceWithResponse(ctx, id, nil)
			return asc.Err(out, err)
		},
		func(ctx context.Context, key string) (string, error) {
			p, err := profileByName(ctx, c, key)
			if err != nil {
				return "", err
			}
			return p.Id, nil
		})
}
