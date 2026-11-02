package provider

import (
	"context"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var certificateTypes = []string{
	"APPLE_PAY", "APPLE_PAY_MERCHANT_IDENTITY", "APPLE_PAY_PSP_IDENTITY", "APPLE_PAY_RSA", "DEVELOPER_ID_APPLICATION",
	"DEVELOPER_ID_APPLICATION_G2", "DEVELOPER_ID_KEXT", "DEVELOPER_ID_KEXT_G2", "DEVELOPMENT", "DISTRIBUTION",
	"IDENTITY_ACCESS", "IOS_DEVELOPMENT", "IOS_DISTRIBUTION", "MAC_APP_DEVELOPMENT", "MAC_APP_DISTRIBUTION",
	"MAC_INSTALLER_DISTRIBUTION", "PASS_TYPE_ID", "PASS_TYPE_ID_WITH_NFC",
}

var (
	_ resource.ResourceWithConfigure   = &certificateResource{}
	_ resource.ResourceWithImportState = &certificateResource{}
	_ resource.ResourceWithIdentity    = &certificateResource{}
	_ resource.ResourceWithModifyPlan  = &certificateResource{}
)

type certificateResource struct {
	resourceIdentity
	client *asc.Client
	now    func() time.Time
}

type certificateModel struct {
	ID                 types.String `tfsdk:"id"`
	CertificateType    types.String `tfsdk:"certificate_type"`
	CSRContent         types.String `tfsdk:"csr_content"`
	MerchantID         types.String `tfsdk:"merchant_id"`
	PassTypeID         types.String `tfsdk:"pass_type_id"`
	Activated          types.Bool   `tfsdk:"activated"`
	EarlyRenewalHours  types.Int64  `tfsdk:"early_renewal_hours"`
	ReadyForRenewal    types.Bool   `tfsdk:"ready_for_renewal"`
	Name               types.String `tfsdk:"name"`
	DisplayName        types.String `tfsdk:"display_name"`
	SerialNumber       types.String `tfsdk:"serial_number"`
	Platform           types.String `tfsdk:"platform"`
	ExpirationDate     types.String `tfsdk:"expiration_date"`
	CertificateContent types.String `tfsdk:"certificate_content"`
}

func newCertificateResource() resource.Resource {
	return &certificateResource{
		resourceIdentity: resourceIdentity{
			attrs:   idIdentity("App Store Connect ID of the certificate."),
			resolve: resolveCertificate,
			keyName: "serial number",
		},
		now: time.Now,
	}
}

func (r *certificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate"
}

// replaceUnlessImported forces replacement when the value changes, except
// from null: imported certificates have no CSR or merchant ID in state,
// because App Store Connect does not return them.
func replaceUnlessImported() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull()
		},
		"Changing the value creates a new certificate, unless the previous value is unknown because the certificate was imported.",
		"Changing the value creates a new certificate, unless the previous value is unknown because the certificate was imported.",
	)
}

func (r *certificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a signing certificate from a certificate signing request (CSR). Destroying it revokes the " +
			"certificate, which invalidates the provisioning profiles that use it. Certificates cannot be renewed in place; " +
			"use `early_renewal_hours` with `create_before_destroy` to replace one before it expires. Import it by its App " +
			"Store Connect ID or serial number.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("App Store Connect ID of the certificate."),
			"certificate_type": replacedString("Certificate type: "+docValues(certificateTypes)+". Changing it creates a new certificate.",
				oneOf(certificateTypes)),
			"csr_content": schema.StringAttribute{
				MarkdownDescription: "PEM-encoded certificate signing request, e.g. `tls_cert_request.signing.cert_request_pem`. " +
					"Changing it creates a new certificate.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{replaceUnlessImported()},
			},
			"merchant_id": schema.StringAttribute{
				MarkdownDescription: "App Store Connect ID of the merchant ID, for Apple Pay certificates. Changing it creates a new certificate.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("pass_type_id"))},
				PlanModifiers:       []planmodifier.String{replaceUnlessImported()},
			},
			"pass_type_id": schema.StringAttribute{
				MarkdownDescription: "App Store Connect ID of the pass type ID, for `PASS_TYPE_ID` and `PASS_TYPE_ID_WITH_NFC` " +
					"certificates. Changing it creates a new certificate.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"activated": schema.BoolAttribute{
				MarkdownDescription: "Whether the certificate is activated. Changes are made in place.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"early_renewal_hours": schema.Int64Attribute{
				MarkdownDescription: "Replace the certificate when it is this many hours or fewer from its expiration date. The " +
					"check runs during plan, so the certificate is replaced by the first apply after that point.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.AtLeast(0)},
			},
			"ready_for_renewal": schema.BoolAttribute{
				MarkdownDescription: "Whether the certificate is within `early_renewal_hours` of expiring, or has expired, so the next apply replaces it.",
				Computed:            true,
			},
			"name":            schema.StringAttribute{MarkdownDescription: "Name of the certificate.", Computed: true, PlanModifiers: keep},
			"display_name":    schema.StringAttribute{MarkdownDescription: "Display name of the certificate.", Computed: true, PlanModifiers: keep},
			"serial_number":   schema.StringAttribute{MarkdownDescription: "Serial number, in hexadecimal.", Computed: true, PlanModifiers: keep},
			"platform":        schema.StringAttribute{MarkdownDescription: "Platform of the certificate, if it has one.", Computed: true, PlanModifiers: keep},
			"expiration_date": schema.StringAttribute{MarkdownDescription: "Expiration date, in RFC 3339 format.", Computed: true, PlanModifiers: keep},
			"certificate_content": schema.StringAttribute{
				MarkdownDescription: "The certificate, base64-encoded DER.",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       keep,
			},
		},
	}
}

func (r *certificateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *certificateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// readyForRenewal reports whether a certificate expiring at expiration
// (RFC 3339) is within hours of expiring.
func readyForRenewal(now time.Time, expiration types.String, hours types.Int64) bool {
	exp, err := time.Parse(time.RFC3339, expiration.ValueString())
	if err != nil {
		return false
	}
	return !now.Before(exp.Add(-time.Duration(hours.ValueInt64()) * time.Hour))
}

func (r *certificateResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan certificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if readyForRenewal(r.now(), state.ExpirationDate, plan.EarlyRenewalHours) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ready_for_renewal"), types.BoolUnknown())...)
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("ready_for_renewal"))
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ready_for_renewal"), false)...)
}

func (m *certificateModel) apply(c *gen.Certificate, now time.Time) {
	m.ID = types.StringValue(c.Id)
	if a := c.Attributes; a != nil {
		m.CertificateType = stringValue(a.CertificateType)
		m.Activated = types.BoolPointerValue(a.Activated)
		m.Name = stringValue(a.Name)
		m.DisplayName = stringValue(a.DisplayName)
		m.SerialNumber = stringValue(a.SerialNumber)
		m.Platform = stringValue(a.Platform)
		m.ExpirationDate = timeValue(a.ExpirationDate)
		m.CertificateContent = stringValue(a.CertificateContent)
	}
	m.ReadyForRenewal = types.BoolValue(readyForRenewal(now, m.ExpirationDate, m.EarlyRenewalHours))
}

func (r *certificateResource) setActivated(ctx context.Context, id string, activated bool) (*gen.Certificate, error) {
	var body gen.CertificateUpdateRequest
	body.Data.Type, body.Data.Id = gen.CertificateUpdateRequestDataTypeCertificates, id
	asc.Alloc(&body.Data.Attributes).Activated = new(activated)
	out, err := r.client.CertificatesUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *certificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan certificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.CertificateCreateRequest
	body.Data.Type = gen.CertificateCreateRequestDataTypeCertificates
	body.Data.Attributes.CertificateType = gen.CertificateType(plan.CertificateType.ValueString())
	body.Data.Attributes.CsrContent = plan.CSRContent.ValueString()
	if id := plan.MerchantID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).MerchantId).Data)
		d.Id, d.Type = id, gen.CertificateCreateRequestDataRelationshipsMerchantIdDataTypeMerchantIds
	}
	if id := plan.PassTypeID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).PassTypeId).Data)
		d.Id, d.Type = id, gen.CertificateCreateRequestDataRelationshipsPassTypeIdDataTypePassTypeIds
	}
	out, err := r.client.CertificatesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create certificate", err)
		return
	}
	cert := &out.JSON201.Data
	activated := plan.Activated
	// Save the certificate before changing it further, so a failure below
	// leaves it in state instead of orphaning it.
	plan.apply(cert, r.now())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if !activated.IsUnknown() && !activated.IsNull() && activated.ValueBool() != asc.Value(cert.Attributes.Activated) {
		cert, err = r.setActivated(ctx, cert.Id, activated.ValueBool())
		if err != nil {
			apiError(&resp.Diagnostics, "update certificate", err)
			return
		}
		plan.apply(cert, r.now())
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *certificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state certificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	out, err := r.client.CertificatesGetInstanceWithResponse(ctx, id, nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read certificate", err)
		return
	}
	state.apply(&out.JSON200.Data, r.now())

	link, err := r.client.CertificatesPassTypeIdGetToOneRelationshipWithResponse(ctx, id)
	if err := asc.Err(link, err); err != nil {
		apiError(&resp.Diagnostics, "read certificate pass type ID", err)
		return
	}
	state.PassTypeID = types.StringNull()
	if link.JSON200 != nil && link.JSON200.Data.Id != "" {
		state.PassTypeID = types.StringValue(link.JSON200.Data.Id)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *certificateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state certificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.copyComputed(&state)
	if !plan.Activated.Equal(state.Activated) {
		cert, err := r.setActivated(ctx, plan.ID.ValueString(), plan.Activated.ValueBool())
		if err != nil {
			apiError(&resp.Diagnostics, "update certificate", err)
			return
		}
		plan.apply(cert, r.now())
	}
	plan.ReadyForRenewal = types.BoolValue(readyForRenewal(r.now(), plan.ExpirationDate, plan.EarlyRenewalHours))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// copyComputed fills the read-only attributes from state, for updates that
// change only Terraform-side arguments.
func (m *certificateModel) copyComputed(state *certificateModel) {
	m.Name, m.DisplayName, m.SerialNumber = state.Name, state.DisplayName, state.SerialNumber
	m.Platform, m.ExpirationDate, m.CertificateContent = state.Platform, state.ExpirationDate, state.CertificateContent
}

func (r *certificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state certificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CertificatesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "revoke certificate", err)
	}
}

// findCertificates lists the certificates matching params, following
// pagination.
func findCertificates(ctx context.Context, c *asc.Client, params gen.CertificatesGetCollectionParams) ([]gen.Certificate, error) {
	params.Limit = new(listAll)
	out, err := c.CertificatesGetCollectionWithResponse(ctx, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.CertificatesResponse) []gen.Certificate { return p.Data },
		func(p *gen.CertificatesResponse) *string { return p.Links.Next })
}

// certificateBySerial finds a certificate by its serial number. It lists
// every certificate and compares case-insensitively, because serial numbers
// are hexadecimal and may be written in either case.
func certificateBySerial(ctx context.Context, c *asc.Client, serial string) (*gen.Certificate, error) {
	all, err := findCertificates(ctx, c, gen.CertificatesGetCollectionParams{})
	if err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(asc.Value(all[i].Attributes.SerialNumber), serial) {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

func resolveCertificate(ctx context.Context, c *asc.Client, key string) (string, error) {
	return resolveByID(ctx, key,
		func(ctx context.Context, id string) error {
			out, err := c.CertificatesGetInstanceWithResponse(ctx, id, nil)
			return asc.Err(out, err)
		},
		func(ctx context.Context, key string) (string, error) {
			cert, err := certificateBySerial(ctx, c, key)
			if err != nil {
				return "", err
			}
			return cert.Id, nil
		})
}
