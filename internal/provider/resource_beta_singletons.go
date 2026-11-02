package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// App Store Connect creates one beta app review detail and one beta license
// agreement per app, and one build beta detail per build. They cannot be
// created or deleted, so these resources adopt the existing object on
// create and only forget it on destroy.

const singletonNote = " App Store Connect creates it with the %s and it cannot be deleted, so creating this resource " +
	"manages the existing one and destroying the resource leaves the current settings in place."

// ---- apple_beta_app_review_detail ----

var (
	_ resource.ResourceWithConfigure   = &betaAppReviewDetailResource{}
	_ resource.ResourceWithImportState = &betaAppReviewDetailResource{}
	_ resource.ResourceWithIdentity    = &betaAppReviewDetailResource{}
)

type betaAppReviewDetailResource struct {
	resourceIdentity
	client *asc.Client
}

type betaAppReviewDetailModel struct {
	ID                  types.String `tfsdk:"id"`
	AppID               types.String `tfsdk:"app_id"`
	ContactEmail        types.String `tfsdk:"contact_email"`
	ContactFirstName    types.String `tfsdk:"contact_first_name"`
	ContactLastName     types.String `tfsdk:"contact_last_name"`
	ContactPhone        types.String `tfsdk:"contact_phone"`
	DemoAccountName     types.String `tfsdk:"demo_account_name"`
	DemoAccountPassword types.String `tfsdk:"demo_account_password"`
	DemoAccountRequired types.Bool   `tfsdk:"demo_account_required"`
	Notes               types.String `tfsdk:"notes"`
}

func newBetaAppReviewDetailResource() resource.Resource {
	return &betaAppReviewDetailResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
	}}}
}

func (r *betaAppReviewDetailResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_app_review_detail"
}

func (r *betaAppReviewDetailResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the contact and demo account information TestFlight App Review uses for an app's " +
			"external testing." + fmt.Sprintf(singletonNote, "app") + " Import it by app ID.",
		Attributes: map[string]schema.Attribute{
			"id":                 idAttribute("App Store Connect ID of the review detail."),
			"app_id":             replacedString("App Store Connect ID of the app."),
			"contact_email":      optionalString("Email address App Review contacts."),
			"contact_first_name": optionalString("First name of the contact."),
			"contact_last_name":  optionalString("Last name of the contact."),
			"contact_phone":      optionalString("Phone number of the contact."),
			"demo_account_name":  optionalString("User name of a demo account for App Review."),
			"demo_account_password": schema.StringAttribute{
				MarkdownDescription: "Password of the demo account.",
				Optional:            true,
				Sensitive:           true,
			},
			"demo_account_required": optionalBool("Whether App Review needs the demo account to use the app."),
			"notes":                 optionalString("Notes for App Review."),
		},
	}
}

func (r *betaAppReviewDetailResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *betaAppReviewDetailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *betaAppReviewDetailModel) apply(d *gen.BetaAppReviewDetail) {
	m.ID = types.StringValue(d.Id)
	a := asc.Value(d.Attributes)
	m.ContactEmail, m.ContactFirstName, m.ContactLastName = stringValue(a.ContactEmail), stringValue(a.ContactFirstName), stringValue(a.ContactLastName)
	m.ContactPhone, m.DemoAccountName, m.Notes = stringValue(a.ContactPhone), stringValue(a.DemoAccountName), stringValue(a.Notes)
	m.DemoAccountRequired = types.BoolPointerValue(a.DemoAccountRequired)
	// App Store Connect may not return the password; keep the configured one.
	if a.DemoAccountPassword != nil {
		m.DemoAccountPassword = types.StringValue(*a.DemoAccountPassword)
	}
}

// find returns the app's review detail.
func (r *betaAppReviewDetailResource) find(ctx context.Context, appID string) (*gen.BetaAppReviewDetail, error) {
	out, err := r.client.BetaAppReviewDetailsGetCollectionWithResponse(ctx, &gen.BetaAppReviewDetailsGetCollectionParams{FilterApp: []string{appID}})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	if len(out.JSON200.Data) == 0 {
		return nil, errNoMatch
	}
	return &out.JSON200.Data[0], nil
}

func (r *betaAppReviewDetailResource) update(ctx context.Context, id string, plan, state *betaAppReviewDetailModel) (*gen.BetaAppReviewDetail, error) {
	var body gen.BetaAppReviewDetailUpdateRequest
	body.Data.Type, body.Data.Id = gen.BetaAppReviewDetailUpdateRequestDataTypeBetaAppReviewDetails, id
	a := asc.Alloc(&body.Data.Attributes)
	a.ContactEmail = changedString(plan.ContactEmail, state.ContactEmail)
	a.ContactFirstName = changedString(plan.ContactFirstName, state.ContactFirstName)
	a.ContactLastName = changedString(plan.ContactLastName, state.ContactLastName)
	a.ContactPhone = changedString(plan.ContactPhone, state.ContactPhone)
	a.DemoAccountName = changedString(plan.DemoAccountName, state.DemoAccountName)
	a.DemoAccountPassword = changedString(plan.DemoAccountPassword, state.DemoAccountPassword)
	a.Notes = changedString(plan.Notes, state.Notes)
	a.DemoAccountRequired = boolPtr(plan.DemoAccountRequired)
	out, err := r.client.BetaAppReviewDetailsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *betaAppReviewDetailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaAppReviewDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	existing, err := r.find(ctx, plan.AppID.ValueString())
	if err != nil {
		singletonLookupError(&resp.Diagnostics, "beta app review detail", "app", err)
		return
	}
	var current betaAppReviewDetailModel
	current.apply(existing)
	updated, err := r.update(ctx, existing.Id, &plan, &current)
	if err != nil {
		apiError(&resp.Diagnostics, "update beta app review detail", err)
		return
	}
	password := plan.DemoAccountPassword
	plan.apply(updated)
	plan.DemoAccountPassword = password
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaAppReviewDetailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaAppReviewDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.find(ctx, state.AppID.ValueString())
	if errors.Is(err, errNoMatch) || asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read beta app review detail", err)
		return
	}
	state.apply(found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *betaAppReviewDetailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state betaAppReviewDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.update(ctx, state.ID.ValueString(), &plan, &state)
	if err != nil {
		apiError(&resp.Diagnostics, "update beta app review detail", err)
		return
	}
	password := plan.DemoAccountPassword
	plan.apply(updated)
	plan.DemoAccountPassword = password
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the review detail, which App Store Connect cannot delete.
func (r *betaAppReviewDetailResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_beta_license_agreement ----

var (
	_ resource.ResourceWithConfigure   = &betaLicenseAgreementResource{}
	_ resource.ResourceWithImportState = &betaLicenseAgreementResource{}
	_ resource.ResourceWithIdentity    = &betaLicenseAgreementResource{}
)

type betaLicenseAgreementResource struct {
	resourceIdentity
	client *asc.Client
}

type betaLicenseAgreementModel struct {
	ID            types.String `tfsdk:"id"`
	AppID         types.String `tfsdk:"app_id"`
	AgreementText types.String `tfsdk:"agreement_text"`
}

func newBetaLicenseAgreementResource() resource.Resource {
	return &betaLicenseAgreementResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
	}}}
}

func (r *betaLicenseAgreementResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_license_agreement"
}

func (r *betaLicenseAgreementResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the license agreement TestFlight testers accept before testing an app." +
			fmt.Sprintf(singletonNote, "app") + " Import it by app ID.",
		Attributes: map[string]schema.Attribute{
			"id":     idAttribute("App Store Connect ID of the license agreement."),
			"app_id": replacedString("App Store Connect ID of the app."),
			"agreement_text": schema.StringAttribute{
				MarkdownDescription: "Text of the license agreement.",
				Required:            true,
			},
		},
	}
}

func (r *betaLicenseAgreementResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *betaLicenseAgreementResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *betaLicenseAgreementResource) find(ctx context.Context, appID string) (*gen.BetaLicenseAgreement, error) {
	out, err := r.client.BetaLicenseAgreementsGetCollectionWithResponse(ctx, &gen.BetaLicenseAgreementsGetCollectionParams{FilterApp: &[]string{appID}})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	if len(out.JSON200.Data) == 0 {
		return nil, errNoMatch
	}
	return &out.JSON200.Data[0], nil
}

func (r *betaLicenseAgreementResource) set(ctx context.Context, id, text string) (*gen.BetaLicenseAgreement, error) {
	var body gen.BetaLicenseAgreementUpdateRequest
	body.Data.Type, body.Data.Id = gen.BetaLicenseAgreementUpdateRequestDataTypeBetaLicenseAgreements, id
	asc.Alloc(&body.Data.Attributes).AgreementText = new(text)
	out, err := r.client.BetaLicenseAgreementsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (m *betaLicenseAgreementModel) apply(l *gen.BetaLicenseAgreement) {
	m.ID = types.StringValue(l.Id)
	m.AgreementText = types.StringValue(asc.Value(asc.Value(l.Attributes).AgreementText))
}

func (r *betaLicenseAgreementResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaLicenseAgreementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	existing, err := r.find(ctx, plan.AppID.ValueString())
	if err != nil {
		singletonLookupError(&resp.Diagnostics, "beta license agreement", "app", err)
		return
	}
	updated, err := r.set(ctx, existing.Id, plan.AgreementText.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "update beta license agreement", err)
		return
	}
	plan.apply(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaLicenseAgreementResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaLicenseAgreementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.find(ctx, state.AppID.ValueString())
	if errors.Is(err, errNoMatch) || asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read beta license agreement", err)
		return
	}
	state.apply(found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *betaLicenseAgreementResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaLicenseAgreementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.set(ctx, plan.ID.ValueString(), plan.AgreementText.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "update beta license agreement", err)
		return
	}
	plan.apply(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the license agreement, which App Store Connect cannot
// delete.
func (r *betaLicenseAgreementResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_build_beta_detail ----

var (
	_ resource.ResourceWithConfigure   = &buildBetaDetailResource{}
	_ resource.ResourceWithImportState = &buildBetaDetailResource{}
	_ resource.ResourceWithIdentity    = &buildBetaDetailResource{}
)

type buildBetaDetailResource struct {
	resourceIdentity
	client *asc.Client
}

type buildBetaDetailModel struct {
	ID                types.String `tfsdk:"id"`
	BuildID           types.String `tfsdk:"build_id"`
	AutoNotifyEnabled types.Bool   `tfsdk:"auto_notify_enabled"`
}

func newBuildBetaDetailResource() resource.Resource {
	return &buildBetaDetailResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "build_id", description: "App Store Connect ID of the build.", state: "build_id"},
	}}}
}

func (r *buildBetaDetailResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_build_beta_detail"
}

func (r *buildBetaDetailResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a build's TestFlight settings." + fmt.Sprintf(singletonNote, "build") + " Import it by build ID.",
		Attributes: map[string]schema.Attribute{
			"id":       idAttribute("App Store Connect ID of the build beta detail."),
			"build_id": replacedString("App Store Connect ID of the build."),
			"auto_notify_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether testers are notified automatically when the build becomes available to them.",
				Required:            true,
			},
		},
	}
}

func (r *buildBetaDetailResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *buildBetaDetailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func findBuildBetaDetail(ctx context.Context, c *asc.Client, buildID string) (*gen.BuildBetaDetail, error) {
	out, err := c.BuildBetaDetailsGetCollectionWithResponse(ctx, &gen.BuildBetaDetailsGetCollectionParams{FilterBuild: &[]string{buildID}})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	if len(out.JSON200.Data) == 0 {
		return nil, errNoMatch
	}
	return &out.JSON200.Data[0], nil
}

func (r *buildBetaDetailResource) set(ctx context.Context, id string, autoNotify bool) (*gen.BuildBetaDetail, error) {
	var body gen.BuildBetaDetailUpdateRequest
	body.Data.Type, body.Data.Id = gen.BuildBetaDetailUpdateRequestDataTypeBuildBetaDetails, id
	asc.Alloc(&body.Data.Attributes).AutoNotifyEnabled = new(autoNotify)
	out, err := r.client.BuildBetaDetailsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (m *buildBetaDetailModel) apply(d *gen.BuildBetaDetail) {
	m.ID = types.StringValue(d.Id)
	m.AutoNotifyEnabled = types.BoolValue(asc.Value(asc.Value(d.Attributes).AutoNotifyEnabled))
}

func (r *buildBetaDetailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan buildBetaDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	existing, err := findBuildBetaDetail(ctx, r.client, plan.BuildID.ValueString())
	if err != nil {
		singletonLookupError(&resp.Diagnostics, "build beta detail", "build", err)
		return
	}
	updated, err := r.set(ctx, existing.Id, plan.AutoNotifyEnabled.ValueBool())
	if err != nil {
		apiError(&resp.Diagnostics, "update build beta detail", err)
		return
	}
	plan.apply(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *buildBetaDetailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state buildBetaDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := findBuildBetaDetail(ctx, r.client, state.BuildID.ValueString())
	if errors.Is(err, errNoMatch) || asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read build beta detail", err)
		return
	}
	state.apply(found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *buildBetaDetailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan buildBetaDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.set(ctx, plan.ID.ValueString(), plan.AutoNotifyEnabled.ValueBool())
	if err != nil {
		apiError(&resp.Diagnostics, "update build beta detail", err)
		return
	}
	plan.apply(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the build beta detail, which App Store Connect cannot
// delete.
func (r *buildBetaDetailResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// singletonLookupError reports a failure to find the object App Store
// Connect creates with an app or build.
func singletonLookupError(diags *diag.Diagnostics, noun, owner string, err error) {
	if errors.Is(err, errNoMatch) || asc.IsNotFound(err) {
		diags.AddAttributeError(path.Root(owner+"_id"), "Not found", "The "+owner+" has no "+noun+"; check the "+owner+" ID.")
		return
	}
	diags.AddAttributeError(path.Root(owner+"_id"), "App Store Connect API error", "Unable to look up the "+noun+": "+err.Error())
}
