package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Version metadata belongs to one App Store version and is edited while that
// version is editable. Each new version copies the localizations of the
// previous one, with new IDs, so these resources are keyed by version.

func versionIdentity(extra ...identityAttribute) resourceIdentity {
	return resourceIdentity{attrs: append([]identityAttribute{
		{name: "version_id", description: "App Store Connect ID of the App Store version.", state: "version_id"},
	}, extra...)}
}

var versionIDAttribute = replacedString("App Store Connect ID of the App Store version, e.g. `apple_app_store_version.example.id`.")

// ---- apple_app_store_version_localization ----

var (
	_ resource.ResourceWithConfigure   = &versionLocalizationResource{}
	_ resource.ResourceWithImportState = &versionLocalizationResource{}
	_ resource.ResourceWithIdentity    = &versionLocalizationResource{}
)

type versionLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type versionLocalizationModel struct {
	VersionID       types.String `tfsdk:"version_id"`
	Locale          types.String `tfsdk:"locale"`
	Description     types.String `tfsdk:"description"`
	Keywords        types.String `tfsdk:"keywords"`
	MarketingURL    types.String `tfsdk:"marketing_url"`
	PromotionalText types.String `tfsdk:"promotional_text"`
	SupportURL      types.String `tfsdk:"support_url"`
	WhatsNew        types.String `tfsdk:"whats_new"`
	ID              types.String `tfsdk:"id"`
}

func newVersionLocalizationResource() resource.Resource {
	return &versionLocalizationResource{resourceIdentity: versionIdentity(identityAttribute{name: "locale", description: "Locale, e.g. `en-US`.", state: "locale"})}
}

func (r *versionLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_version_localization"
}

func (r *versionLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an App Store version's localized description, keywords, URLs and release notes for one " +
			"locale. A new version starts with copies of the previous version's localizations, and the primary locale " +
			"always has one: creating this resource for an existing locale manages it. Destroying the resource deletes " +
			"the localization while the version is editable, and otherwise leaves it. Import it with `<version_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"version_id":       versionIDAttribute,
			"locale":           replacedString("Locale, e.g. `en-US`.", localeValidator),
			"description":      optionalString("Description of the app, up to 4,000 characters.", stringvalidator.LengthAtMost(4000)),
			"keywords":         optionalString("Comma-separated search keywords, up to 100 characters.", stringvalidator.LengthAtMost(100)),
			"marketing_url":    optionalString("URL of the app's marketing website."),
			"promotional_text": optionalString("Promotional text shown above the description, up to 170 characters.", stringvalidator.LengthAtMost(170)),
			"support_url":      optionalString("URL of the app's support website; App Review requires one."),
			"whats_new":        optionalString("Release notes for the version, up to 4,000 characters. The first version has none.", stringvalidator.LengthAtMost(4000)),
			"id":               schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the localization.", Computed: true},
		},
	}
}

func (r *versionLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *versionLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *versionLocalizationModel) apply(l *gen.AppStoreVersionLocalization) {
	a := asc.Value(l.Attributes)
	m.ID, m.Description, m.Keywords = types.StringValue(l.Id), stringValue(a.Description), stringValue(a.Keywords)
	m.MarketingURL, m.PromotionalText = stringValue(a.MarketingUrl), stringValue(a.PromotionalText)
	m.SupportURL, m.WhatsNew = stringValue(a.SupportUrl), stringValue(a.WhatsNew)
}

func (r *versionLocalizationResource) find(ctx context.Context, versionID, locale string) (*gen.AppStoreVersionLocalization, error) {
	out, err := r.client.AppStoreVersionsAppStoreVersionLocalizationsGetToManyRelatedWithResponse(ctx, versionID,
		&gen.AppStoreVersionsAppStoreVersionLocalizationsGetToManyRelatedParams{FilterLocale: &[]string{locale}})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	for i := range out.JSON200.Data {
		if asc.Value(out.JSON200.Data[i].Attributes.Locale) == locale {
			return &out.JSON200.Data[i], nil
		}
	}
	return nil, errNoMatch
}

func (r *versionLocalizationResource) update(ctx context.Context, id string, plan, state *versionLocalizationModel) (*gen.AppStoreVersionLocalization, error) {
	var body gen.AppStoreVersionLocalizationUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppStoreVersionLocalizationUpdateRequestDataTypeAppStoreVersionLocalizations, id
	a := asc.Alloc(&body.Data.Attributes)
	a.Description = changedString(plan.Description, state.Description)
	a.Keywords = changedString(plan.Keywords, state.Keywords)
	a.MarketingUrl = changedString(plan.MarketingURL, state.MarketingURL)
	a.PromotionalText = changedString(plan.PromotionalText, state.PromotionalText)
	a.SupportUrl = changedString(plan.SupportURL, state.SupportURL)
	a.WhatsNew = changedString(plan.WhatsNew, state.WhatsNew)
	out, err := r.client.AppStoreVersionLocalizationsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *versionLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan versionLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	existing, err := r.find(ctx, plan.VersionID.ValueString(), plan.Locale.ValueString())
	var l *gen.AppStoreVersionLocalization
	switch {
	case err == nil:
		var current versionLocalizationModel
		current.apply(existing)
		l, err = r.update(ctx, existing.Id, &plan, &current)
	case errors.Is(err, errNoMatch):
		var body gen.AppStoreVersionLocalizationCreateRequest
		body.Data.Type = gen.AppStoreVersionLocalizationCreateRequestDataTypeAppStoreVersionLocalizations
		a := &body.Data.Attributes
		a.Locale, a.Description, a.Keywords = plan.Locale.ValueString(), knownString(plan.Description), knownString(plan.Keywords)
		a.MarketingUrl, a.PromotionalText = knownString(plan.MarketingURL), knownString(plan.PromotionalText)
		a.SupportUrl, a.WhatsNew = knownString(plan.SupportURL), knownString(plan.WhatsNew)
		rel := &body.Data.Relationships.AppStoreVersion.Data
		rel.Id, rel.Type = plan.VersionID.ValueString(), gen.AppStoreVersionLocalizationCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		out, e := r.client.AppStoreVersionLocalizationsCreateInstanceWithResponse(ctx, body)
		if err = asc.Err(out, e); err == nil {
			l = &out.JSON201.Data
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "add App Store version localization", err)
		return
	}
	plan.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *versionLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state versionLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.find(ctx, state.VersionID.ValueString(), state.Locale.ValueString())
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read App Store version localization", err)
		return
	}
	state.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *versionLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state versionLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.update(ctx, state.ID.ValueString(), &plan, &state)
	if err != nil {
		apiError(&resp.Diagnostics, "update App Store version localization", err)
		return
	}
	plan.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the localization while its version is editable; a
// released version's localizations stay with it.
func (r *versionLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state versionLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	editable, err := versionEditable(ctx, r.client, state.VersionID.ValueString())
	if err == nil && editable {
		out, e := r.client.AppStoreVersionLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
		if err = asc.Err(out, e); asc.IsNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "delete App Store version localization", err)
	}
}

// ---- apple_app_store_review_detail ----

var (
	_ resource.ResourceWithConfigure   = &reviewDetailResource{}
	_ resource.ResourceWithImportState = &reviewDetailResource{}
	_ resource.ResourceWithIdentity    = &reviewDetailResource{}
)

type reviewDetailResource struct {
	resourceIdentity
	client *asc.Client
}

type reviewDetailModel struct {
	VersionID           types.String `tfsdk:"version_id"`
	ContactFirstName    types.String `tfsdk:"contact_first_name"`
	ContactLastName     types.String `tfsdk:"contact_last_name"`
	ContactPhone        types.String `tfsdk:"contact_phone"`
	ContactEmail        types.String `tfsdk:"contact_email"`
	DemoAccountRequired types.Bool   `tfsdk:"demo_account_required"`
	DemoAccountName     types.String `tfsdk:"demo_account_name"`
	DemoAccountPassword types.String `tfsdk:"demo_account_password"`
	Notes               types.String `tfsdk:"notes"`
	ID                  types.String `tfsdk:"id"`
}

func newReviewDetailResource() resource.Resource {
	return &reviewDetailResource{resourceIdentity: versionIdentity()}
}

func (r *reviewDetailResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_review_detail"
}

func (r *reviewDetailResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the contact and sign-in information App Review uses for an App Store version. Changes " +
			"need the version to be editable. App Store Connect has no way to delete review details, so destroying the " +
			"resource leaves them. Import it by version ID.",
		Attributes: map[string]schema.Attribute{
			"version_id":         versionIDAttribute,
			"contact_first_name": optionalString("First name of App Review's contact."),
			"contact_last_name":  optionalString("Last name of App Review's contact."),
			"contact_phone":      optionalString("Phone number of App Review's contact, with the country code, e.g. `+1 555 555 0100`."),
			"contact_email":      optionalString("Email address of App Review's contact.", stringvalidator.RegexMatches(emailRegexp, "must be an email address")),
			"demo_account_required": schema.BoolAttribute{
				MarkdownDescription: "Whether App Review needs to sign in to review the app. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"demo_account_name": optionalString("User name of the demo account."),
			"demo_account_password": schema.StringAttribute{
				MarkdownDescription: "Password of the demo account.",
				Optional:            true,
				Sensitive:           true,
			},
			"notes": optionalString("Notes for App Review, up to 4,000 characters.", stringvalidator.LengthAtMost(4000)),
			"id":    idAttribute("App Store Connect ID of the review details."),
		},
	}
}

func (r *reviewDetailResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *reviewDetailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *reviewDetailModel) apply(d *gen.AppStoreReviewDetail) {
	a := asc.Value(d.Attributes)
	m.ID = types.StringValue(d.Id)
	m.ContactFirstName, m.ContactLastName = stringValue(a.ContactFirstName), stringValue(a.ContactLastName)
	m.ContactPhone, m.ContactEmail = stringValue(a.ContactPhone), stringValue(a.ContactEmail)
	m.DemoAccountRequired = types.BoolValue(asc.Value(a.DemoAccountRequired))
	m.DemoAccountName, m.Notes = stringValue(a.DemoAccountName), stringValue(a.Notes)
	// Keep the configured password if the API doesn't return it.
	if a.DemoAccountPassword != nil {
		m.DemoAccountPassword = types.StringValue(*a.DemoAccountPassword)
	}
}

func (r *reviewDetailResource) get(ctx context.Context, versionID string) (*gen.AppStoreReviewDetail, error) {
	out, err := r.client.AppStoreVersionsAppStoreReviewDetailGetToOneRelatedWithResponse(ctx, versionID, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *reviewDetailResource) update(ctx context.Context, id string, plan, state *reviewDetailModel) (*gen.AppStoreReviewDetail, error) {
	var body gen.AppStoreReviewDetailUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppStoreReviewDetailUpdateRequestDataTypeAppStoreReviewDetails, id
	a := asc.Alloc(&body.Data.Attributes)
	a.ContactFirstName = changedString(plan.ContactFirstName, state.ContactFirstName)
	a.ContactLastName = changedString(plan.ContactLastName, state.ContactLastName)
	a.ContactPhone = changedString(plan.ContactPhone, state.ContactPhone)
	a.ContactEmail = changedString(plan.ContactEmail, state.ContactEmail)
	a.DemoAccountRequired = new(plan.DemoAccountRequired.ValueBool())
	a.DemoAccountName = changedString(plan.DemoAccountName, state.DemoAccountName)
	a.DemoAccountPassword = changedString(plan.DemoAccountPassword, state.DemoAccountPassword)
	a.Notes = changedString(plan.Notes, state.Notes)
	out, err := r.client.AppStoreReviewDetailsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *reviewDetailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan reviewDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	existing, err := r.get(ctx, plan.VersionID.ValueString())
	var d *gen.AppStoreReviewDetail
	switch {
	case err == nil:
		var current reviewDetailModel
		current.apply(existing)
		d, err = r.update(ctx, existing.Id, &plan, &current)
	case asc.IsNotFound(err):
		var body gen.AppStoreReviewDetailCreateRequest
		body.Data.Type = gen.AppStoreReviewDetailCreateRequestDataTypeAppStoreReviewDetails
		a := asc.Alloc(&body.Data.Attributes)
		a.ContactFirstName, a.ContactLastName = knownString(plan.ContactFirstName), knownString(plan.ContactLastName)
		a.ContactPhone, a.ContactEmail = knownString(plan.ContactPhone), knownString(plan.ContactEmail)
		a.DemoAccountRequired = new(plan.DemoAccountRequired.ValueBool())
		a.DemoAccountName, a.DemoAccountPassword, a.Notes = knownString(plan.DemoAccountName), knownString(plan.DemoAccountPassword), knownString(plan.Notes)
		rel := &body.Data.Relationships.AppStoreVersion.Data
		rel.Id, rel.Type = plan.VersionID.ValueString(), gen.AppStoreReviewDetailCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		out, e := r.client.AppStoreReviewDetailsCreateInstanceWithResponse(ctx, body)
		if err = asc.Err(out, e); err == nil {
			d = &out.JSON201.Data
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "set App Store review details", err)
		return
	}
	plan.apply(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *reviewDetailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state reviewDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.get(ctx, state.VersionID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read App Store review details", err)
		return
	}
	state.apply(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *reviewDetailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state reviewDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.update(ctx, state.ID.ValueString(), &plan, &state)
	if err != nil {
		apiError(&resp.Diagnostics, "update App Store review details", err)
		return
	}
	plan.apply(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the review details, which the API cannot delete.
func (r *reviewDetailResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_app_store_version_phased_release ----

var (
	_ resource.ResourceWithConfigure   = &phasedReleaseResource{}
	_ resource.ResourceWithImportState = &phasedReleaseResource{}
	_ resource.ResourceWithIdentity    = &phasedReleaseResource{}
)

type phasedReleaseResource struct {
	resourceIdentity
	client *asc.Client
}

type phasedReleaseModel struct {
	VersionID types.String `tfsdk:"version_id"`
	Paused    types.Bool   `tfsdk:"paused"`
	ID        types.String `tfsdk:"id"`
	State     types.String `tfsdk:"state"`
}

func newPhasedReleaseResource() resource.Resource {
	return &phasedReleaseResource{resourceIdentity: versionIdentity()}
}

func (r *phasedReleaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_version_phased_release"
}

func (r *phasedReleaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Releases an App Store version's automatic update in phases over seven days. Create it " +
			"before the version is released; the phased release starts when the version is. Destroying the resource " +
			"removes the phased release before it starts, and otherwise leaves it. Import it by version ID.",
		Attributes: map[string]schema.Attribute{
			"version_id": versionIDAttribute,
			"paused": schema.BoolAttribute{
				MarkdownDescription: "Whether the phased release is paused. It takes effect once the release has started, " +
					"and a release can be paused for up to 30 days in total. Defaults to `false`.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"id":    idAttribute("App Store Connect ID of the phased release."),
			"state": computedOnly("State of the phased release: `INACTIVE` until the version is released, then `ACTIVE`, `PAUSED` or `COMPLETE`."),
		},
	}
}

func (r *phasedReleaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *phasedReleaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// apply records p; paused follows the state while the release is running,
// and keeps its configured value before and after.
func (m *phasedReleaseModel) apply(p *gen.AppStoreVersionPhasedRelease) {
	state := asc.Value(asc.Value(p.Attributes).PhasedReleaseState)
	m.ID, m.State = types.StringValue(p.Id), types.StringValue(string(state))
	switch state {
	case gen.PhasedReleaseStateACTIVE:
		m.Paused = types.BoolValue(false)
	case gen.PhasedReleaseStatePAUSED:
		m.Paused = types.BoolValue(true)
	}
	if m.Paused.IsNull() || m.Paused.IsUnknown() {
		m.Paused = types.BoolValue(false)
	}
}

func (r *phasedReleaseResource) get(ctx context.Context, versionID string) (*gen.AppStoreVersionPhasedRelease, error) {
	out, err := r.client.AppStoreVersionsAppStoreVersionPhasedReleaseGetToOneRelatedWithResponse(ctx, versionID, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// converge pauses or resumes a running phased release to match paused.
func (r *phasedReleaseResource) converge(ctx context.Context, p *gen.AppStoreVersionPhasedRelease, paused bool) (*gen.AppStoreVersionPhasedRelease, error) {
	state := asc.Value(asc.Value(p.Attributes).PhasedReleaseState)
	want := state
	switch {
	case state == gen.PhasedReleaseStateACTIVE && paused:
		want = gen.PhasedReleaseStatePAUSED
	case state == gen.PhasedReleaseStatePAUSED && !paused:
		want = gen.PhasedReleaseStateACTIVE
	}
	if want == state {
		return p, nil
	}
	var body gen.AppStoreVersionPhasedReleaseUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppStoreVersionPhasedReleaseUpdateRequestDataTypeAppStoreVersionPhasedReleases, p.Id
	asc.Alloc(&body.Data.Attributes).PhasedReleaseState = new(want)
	out, err := r.client.AppStoreVersionPhasedReleasesUpdateInstanceWithResponse(ctx, p.Id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *phasedReleaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan phasedReleaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppStoreVersionPhasedReleaseCreateRequest
	body.Data.Type = gen.AppStoreVersionPhasedReleaseCreateRequestDataTypeAppStoreVersionPhasedReleases
	rel := &body.Data.Relationships.AppStoreVersion.Data
	rel.Id, rel.Type = plan.VersionID.ValueString(), gen.AppStoreVersionPhasedReleaseCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
	out, err := r.client.AppStoreVersionPhasedReleasesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create phased release", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *phasedReleaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state phasedReleaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.get(ctx, state.VersionID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read phased release", err)
		return
	}
	state.apply(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *phasedReleaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan phasedReleaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.get(ctx, plan.VersionID.ValueString())
	if err == nil {
		p, err = r.converge(ctx, p, plan.Paused.ValueBool())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update phased release", err)
		return
	}
	plan.apply(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a phased release that has not started; one that has can't
// be removed.
func (r *phasedReleaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state phasedReleaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.get(ctx, state.VersionID.ValueString())
	if asc.IsNotFound(err) {
		return
	}
	if err == nil && asc.Value(asc.Value(p.Attributes).PhasedReleaseState) == gen.PhasedReleaseStateINACTIVE {
		out, e := r.client.AppStoreVersionPhasedReleasesDeleteInstanceWithResponse(ctx, p.Id)
		if err = asc.Err(out, e); asc.IsNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "delete phased release", err)
	}
}

// ---- apple_app_encryption_declaration ----

var (
	_ resource.ResourceWithConfigure   = &encryptionDeclarationResource{}
	_ resource.ResourceWithImportState = &encryptionDeclarationResource{}
	_ resource.ResourceWithIdentity    = &encryptionDeclarationResource{}
)

type encryptionDeclarationResource struct {
	resourceIdentity
	client *asc.Client
}

type encryptionDeclarationModel struct {
	AppID                           types.String `tfsdk:"app_id"`
	AppDescription                  types.String `tfsdk:"app_description"`
	ContainsProprietaryCryptography types.Bool   `tfsdk:"contains_proprietary_cryptography"`
	ContainsThirdPartyCryptography  types.Bool   `tfsdk:"contains_third_party_cryptography"`
	AvailableOnFrenchStore          types.Bool   `tfsdk:"available_on_french_store"`
	ID                              types.String `tfsdk:"id"`
	State                           types.String `tfsdk:"state"`
	Exempt                          types.Bool   `tfsdk:"exempt"`
	CodeValue                       types.String `tfsdk:"code_value"`
	CreatedDate                     types.String `tfsdk:"created_date"`
}

func newEncryptionDeclarationResource() resource.Resource {
	return &encryptionDeclarationResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the declaration.")}}
}

func (r *encryptionDeclarationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_encryption_declaration"
}

func replacedBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{MarkdownDescription: desc, Required: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}}
}

func (r *encryptionDeclarationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Declares how an app uses encryption, for export compliance. Assign it to builds with " +
			"`apple_assign_encryption_declaration`. A declaration can't be changed, so changing any argument creates a new " +
			"one, and App Store Connect can't delete one, so destroying the resource leaves it. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"app_id":                            replacedString("App Store Connect ID of the app."),
			"app_description":                   replacedString("How the app uses encryption.", stringvalidator.LengthAtLeast(1)),
			"contains_proprietary_cryptography": replacedBool("Whether the app contains proprietary cryptography."),
			"contains_third_party_cryptography": replacedBool("Whether the app contains third-party cryptography."),
			"available_on_french_store":         replacedBool("Whether the app is available on the App Store in France."),
			"id":                                idAttribute("App Store Connect ID of the declaration."),
			"state":                             computedOnly("State of the declaration, e.g. `CREATED` or `APPROVED`."),
			"exempt": schema.BoolAttribute{
				MarkdownDescription: "Whether the app is exempt from export compliance documentation.",
				Computed:            true,
			},
			"code_value":   computedOnly("Code to add to the app's `Info.plist` as `ITSEncryptionExportComplianceCode`, once approved."),
			"created_date": computedString("When the declaration was created."),
		},
	}
}

func (r *encryptionDeclarationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *encryptionDeclarationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *encryptionDeclarationModel) apply(d *gen.AppEncryptionDeclaration) {
	a := asc.Value(d.Attributes)
	m.ID, m.State, m.CodeValue, m.CreatedDate = types.StringValue(d.Id), stringValue(a.AppEncryptionDeclarationState), stringValue(a.CodeValue), timeValue(a.CreatedDate)
	m.AppDescription = stringValue(a.AppDescription)
	m.ContainsProprietaryCryptography = types.BoolValue(asc.Value(a.ContainsProprietaryCryptography))
	m.ContainsThirdPartyCryptography = types.BoolValue(asc.Value(a.ContainsThirdPartyCryptography))
	m.AvailableOnFrenchStore = types.BoolValue(asc.Value(a.AvailableOnFrenchStore))
	m.Exempt = types.BoolValue(asc.Value(a.Exempt))
	if rel := d.Relationships; rel != nil && rel.App != nil && rel.App.Data != nil { //nolint:staticcheck // The spec deprecates the relationship, but it is the only link to the app.
		m.AppID = types.StringValue(rel.App.Data.Id) //nolint:staticcheck // As above.
	}
}

func (r *encryptionDeclarationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan encryptionDeclarationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppEncryptionDeclarationCreateRequest
	body.Data.Type = gen.AppEncryptionDeclarationCreateRequestDataTypeAppEncryptionDeclarations
	a := &body.Data.Attributes
	a.AppDescription = plan.AppDescription.ValueString()
	a.ContainsProprietaryCryptography, a.ContainsThirdPartyCryptography = plan.ContainsProprietaryCryptography.ValueBool(), plan.ContainsThirdPartyCryptography.ValueBool()
	a.AvailableOnFrenchStore = plan.AvailableOnFrenchStore.ValueBool()
	app := &body.Data.Relationships.App.Data //nolint:staticcheck // The spec deprecates the relationship but still requires it.
	app.Id, app.Type = plan.AppID.ValueString(), gen.AppEncryptionDeclarationCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.AppEncryptionDeclarationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create encryption declaration", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *encryptionDeclarationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state encryptionDeclarationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppEncryptionDeclarationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppEncryptionDeclarationsGetInstanceParams{
		Include: &[]gen.AppEncryptionDeclarationsGetInstanceParamsInclude{gen.AppEncryptionDeclarationsGetInstanceParamsIncludeApp},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read encryption declaration", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument requires replacement.
func (r *encryptionDeclarationResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

// Delete leaves the declaration, which the API cannot delete.
func (r *encryptionDeclarationResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
