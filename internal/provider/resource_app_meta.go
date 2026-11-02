package provider

import (
	"context"
	"errors"
	"regexp"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// optionalComputedString keeps App Store Connect's value when unset.
func optionalComputedString(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Computed:            true,
		Validators:          validators,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

// ---- apple_app_settings ----

var (
	contentRightsValues = enum(gen.AppUpdateRequestDataAttributesContentRightsDeclarationDOESNOTUSETHIRDPARTYCONTENT,
		gen.AppUpdateRequestDataAttributesContentRightsDeclarationUSESTHIRDPARTYCONTENT)
	statusURLVersions = enum(gen.SubscriptionStatusUrlVersionV1, gen.SubscriptionStatusUrlVersionV2)
)

var (
	_ resource.ResourceWithConfigure   = &appSettingsResource{}
	_ resource.ResourceWithImportState = &appSettingsResource{}
	_ resource.ResourceWithIdentity    = &appSettingsResource{}
)

type appSettingsResource struct {
	resourceIdentity
	client *asc.Client
}

type appSettingsModel struct {
	AppID                        types.String `tfsdk:"app_id"`
	PrimaryLocale                types.String `tfsdk:"primary_locale"`
	BundleID                     types.String `tfsdk:"bundle_id"`
	ContentRightsDeclaration     types.String `tfsdk:"content_rights_declaration"`
	AccessibilityURL             types.String `tfsdk:"accessibility_url"`
	SubscriptionStatusURL        types.String `tfsdk:"subscription_status_url"`
	SubscriptionStatusURLVersion types.String `tfsdk:"subscription_status_url_version"`
	SandboxStatusURL             types.String `tfsdk:"subscription_status_url_for_sandbox"`
	SandboxStatusURLVersion      types.String `tfsdk:"subscription_status_url_version_for_sandbox"`
	StreamlinedPurchasingEnabled types.Bool   `tfsdk:"streamlined_purchasing_enabled"`
}

func newAppSettingsResource() resource.Resource {
	return &appSettingsResource{resourceIdentity: appIdentity()}
}

func (r *appSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_settings"
}

func (r *appSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an app's settings: primary locale, bundle ID, content rights, accessibility URL and " +
			"App Store Server Notifications URLs. Apps cannot be created through the API, so this manages an existing app; " +
			"unset arguments keep their current values, and destroying the resource leaves the settings as they are. " +
			"Import it by app ID.",
		Attributes: map[string]schema.Attribute{
			"app_id":         replacedString("App Store Connect ID of the app."),
			"primary_locale": optionalComputedString("Primary locale, e.g. `en-US`. The app needs an `apple_app_info_localization` for it.", localeValidator),
			"bundle_id":      optionalComputedString("Bundle identifier of the app."),
			"content_rights_declaration": optionalComputedString("Whether the app uses third-party content: "+docValues(contentRightsValues)+".",
				oneOf(contentRightsValues)),
			"accessibility_url":                   optionalComputedString("URL of the app's accessibility information."),
			"subscription_status_url":             optionalComputedString("URL that receives App Store Server Notifications."),
			"subscription_status_url_version":     optionalComputedString("Version of those notifications: "+docValues(statusURLVersions)+".", oneOf(statusURLVersions)),
			"subscription_status_url_for_sandbox": optionalComputedString("URL that receives sandbox App Store Server Notifications."),
			"subscription_status_url_version_for_sandbox": optionalComputedString("Version of the sandbox notifications: "+
				docValues(statusURLVersions)+".", oneOf(statusURLVersions)),
			"streamlined_purchasing_enabled": optionalBool("Whether streamlined purchasing is enabled."),
		},
	}
}

func (r *appSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *appSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *appSettingsModel) apply(a *gen.App) {
	at := asc.Value(a.Attributes)
	m.PrimaryLocale, m.BundleID, m.ContentRightsDeclaration = stringValue(at.PrimaryLocale), stringValue(at.BundleId), stringValue(at.ContentRightsDeclaration)
	m.AccessibilityURL, m.SubscriptionStatusURL = stringValue(at.AccessibilityUrl), stringValue(at.SubscriptionStatusUrl)
	m.SubscriptionStatusURLVersion, m.SandboxStatusURL = stringValue(at.SubscriptionStatusUrlVersion), stringValue(at.SubscriptionStatusUrlForSandbox)
	m.SandboxStatusURLVersion = stringValue(at.SubscriptionStatusUrlVersionForSandbox)
	m.StreamlinedPurchasingEnabled = types.BoolPointerValue(at.StreamlinedPurchasingEnabled)
}

func (r *appSettingsResource) write(ctx context.Context, plan *appSettingsModel) error {
	id := plan.AppID.ValueString()
	var body gen.AppUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppUpdateRequestDataTypeApps, id
	a := asc.Alloc(&body.Data.Attributes)
	a.PrimaryLocale, a.BundleId, a.AccessibilityUrl = knownString(plan.PrimaryLocale), knownString(plan.BundleID), knownString(plan.AccessibilityURL)
	a.SubscriptionStatusUrl, a.SubscriptionStatusUrlForSandbox = knownString(plan.SubscriptionStatusURL), knownString(plan.SandboxStatusURL)
	if v := knownString(plan.ContentRightsDeclaration); v != nil {
		a.ContentRightsDeclaration = new(gen.AppUpdateRequestDataAttributesContentRightsDeclaration(*v))
	}
	if v := knownString(plan.SubscriptionStatusURLVersion); v != nil {
		a.SubscriptionStatusUrlVersion = new(gen.SubscriptionStatusUrlVersion(*v))
	}
	if v := knownString(plan.SandboxStatusURLVersion); v != nil {
		a.SubscriptionStatusUrlVersionForSandbox = new(gen.SubscriptionStatusUrlVersion(*v))
	}
	a.StreamlinedPurchasingEnabled = boolPtr(plan.StreamlinedPurchasingEnabled)
	out, err := r.client.AppsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	plan.apply(&out.JSON200.Data)
	return nil
}

func (r *appSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "update app settings", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state appSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppsGetInstanceWithResponse(ctx, state.AppID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read app", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "update app settings", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the app's settings; apps cannot be deleted through the API.
func (r *appSettingsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_end_user_license_agreement ----

var territoryValidator = stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Z]{3}$`), "must be a territory ID, e.g. `USA`")

var (
	_ resource.ResourceWithConfigure   = &eulaResource{}
	_ resource.ResourceWithImportState = &eulaResource{}
	_ resource.ResourceWithIdentity    = &eulaResource{}
)

type eulaResource struct {
	resourceIdentity
	client *asc.Client
}

type eulaModel struct {
	AppID         types.String `tfsdk:"app_id"`
	AgreementText types.String `tfsdk:"agreement_text"`
	TerritoryIDs  types.Set    `tfsdk:"territory_ids"`
	ID            types.String `tfsdk:"id"`
}

func newEULAResource() resource.Resource { return &eulaResource{resourceIdentity: appIdentity()} }

func (r *eulaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_end_user_license_agreement"
}

func (r *eulaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a custom license agreement for an app in the listed territories, replacing Apple's " +
			"standard agreement there. An app has at most one. Import it by app ID.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app."),
			"agreement_text": schema.StringAttribute{
				MarkdownDescription: "Text of the license agreement.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"territory_ids": schema.SetAttribute{
				MarkdownDescription: "Territories where the agreement applies, as ISO 3166-1 alpha-3 codes, e.g. `USA`.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(territoryValidator)},
			},
			"id": idAttribute("App Store Connect ID of the agreement."),
		},
	}
}

func (r *eulaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *eulaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func eulaTerritories(ctx context.Context, c *asc.Client, id string) ([]string, error) {
	out, err := c.EndUserLicenseAgreementsTerritoriesGetToManyRelationshipWithResponse(ctx, id,
		&gen.EndUserLicenseAgreementsTerritoriesGetToManyRelationshipParams{Limit: new(linkageLimit)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	ids, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.EndUserLicenseAgreementTerritoriesLinkagesResponse) []string {
			var ids []string
			for _, d := range p.Data {
				ids = append(ids, d.Id)
			}
			return ids
		},
		func(p *gen.EndUserLicenseAgreementTerritoriesLinkagesResponse) *string { return p.Links.Next })
	slices.Sort(ids)
	return ids, err
}

func (r *eulaResource) read(ctx context.Context, m *eulaModel) error {
	out, err := r.client.AppsEndUserLicenseAgreementGetToOneRelatedWithResponse(ctx, m.AppID.ValueString(), nil)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	e := out.JSON200.Data
	territories, err := eulaTerritories(ctx, r.client, e.Id)
	if err != nil {
		return err
	}
	m.ID, m.AgreementText = types.StringValue(e.Id), stringValue(asc.Value(e.Attributes).AgreementText)
	m.TerritoryIDs = stringsValue(territories)
	return nil
}

func (r *eulaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan eulaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.EndUserLicenseAgreementCreateRequest
	body.Data.Type = gen.EndUserLicenseAgreementCreateRequestDataTypeEndUserLicenseAgreements
	body.Data.Attributes.AgreementText = plan.AgreementText.ValueString()
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.EndUserLicenseAgreementCreateRequestDataRelationshipsAppDataTypeApps
	for _, t := range ids(ctx, plan.TerritoryIDs) {
		e := asc.Push(&body.Data.Relationships.Territories.Data)
		e.Id, e.Type = t, gen.EndUserLicenseAgreementCreateRequestDataRelationshipsTerritoriesDataTypeTerritories
	}
	out, err := r.client.EndUserLicenseAgreementsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create license agreement", err)
		return
	}
	plan.ID = types.StringValue(out.JSON201.Data.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *eulaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state eulaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.read(ctx, &state)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read license agreement", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *eulaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan eulaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.EndUserLicenseAgreementUpdateRequest
	body.Data.Type, body.Data.Id = gen.EndUserLicenseAgreementUpdateRequestDataTypeEndUserLicenseAgreements, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).AgreementText = new(plan.AgreementText.ValueString())
	rel := asc.Alloc(&asc.Alloc(&body.Data.Relationships).Territories)
	for _, t := range ids(ctx, plan.TerritoryIDs) {
		e := asc.Append(&rel.Data)
		e.Id, e.Type = t, gen.EndUserLicenseAgreementUpdateRequestDataRelationshipsTerritoriesDataTypeTerritories
	}
	out, err := r.client.EndUserLicenseAgreementsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update license agreement", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *eulaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state eulaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.EndUserLicenseAgreementsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete license agreement", err)
	}
}

// ---- apple_accessibility_declaration ----

var (
	_ resource.ResourceWithConfigure   = &accessibilityResource{}
	_ resource.ResourceWithImportState = &accessibilityResource{}
	_ resource.ResourceWithIdentity    = &accessibilityResource{}
)

type accessibilityResource struct {
	resourceIdentity
	client *asc.Client
}

type accessibilityModel struct {
	ID                        types.String `tfsdk:"id"`
	AppID                     types.String `tfsdk:"app_id"`
	DeviceFamily              types.String `tfsdk:"device_family"`
	Publish                   types.Bool   `tfsdk:"publish"`
	State                     types.String `tfsdk:"state"`
	AudioDescriptions         types.Bool   `tfsdk:"supports_audio_descriptions"`
	Captions                  types.Bool   `tfsdk:"supports_captions"`
	DarkInterface             types.Bool   `tfsdk:"supports_dark_interface"`
	DifferentiateWithoutColor types.Bool   `tfsdk:"supports_differentiate_without_color_alone"`
	LargerText                types.Bool   `tfsdk:"supports_larger_text"`
	ReducedMotion             types.Bool   `tfsdk:"supports_reduced_motion"`
	SufficientContrast        types.Bool   `tfsdk:"supports_sufficient_contrast"`
	VoiceControl              types.Bool   `tfsdk:"supports_voice_control"`
	VoiceOver                 types.Bool   `tfsdk:"supports_voiceover"`
}

// features returns pointers to the feature attributes, in the order of
// accessibilityFeatureNames.
func (m *accessibilityModel) features() []*types.Bool {
	return []*types.Bool{&m.AudioDescriptions, &m.Captions, &m.DarkInterface, &m.DifferentiateWithoutColor, &m.LargerText,
		&m.ReducedMotion, &m.SufficientContrast, &m.VoiceControl, &m.VoiceOver}
}

var accessibilityFeatureNames = []string{
	"supports_audio_descriptions", "supports_captions", "supports_dark_interface", "supports_differentiate_without_color_alone",
	"supports_larger_text", "supports_reduced_motion", "supports_sufficient_contrast", "supports_voice_control", "supports_voiceover",
}

func newAccessibilityResource() resource.Resource {
	return &accessibilityResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the declaration.")}}
}

func (r *accessibilityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_accessibility_declaration"
}

func (r *accessibilityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id":            idAttribute("App Store Connect ID of the declaration."),
		"app_id":        replacedString("App Store Connect ID of the app."),
		"device_family": replacedString("Device family the declaration covers: "+docValues(deviceFamilies)+".", oneOf(deviceFamilies)),
		"publish": schema.BoolAttribute{
			MarkdownDescription: "Whether to publish the declaration on the App Store, replacing the device family's " +
				"published declaration. Defaults to `false`, which keeps it a draft. A published declaration cannot be " +
				"unpublished, so setting this back to `false` replaces it with a new draft.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(false),
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplaceIf(
				func(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = req.StateValue.ValueBool() && !req.PlanValue.ValueBool()
				}, "Unpublishing creates a new draft.", "Unpublishing creates a new draft.")},
		},
		"state": schema.StringAttribute{MarkdownDescription: "`DRAFT` or `PUBLISHED`.", Computed: true},
	}
	for _, name := range accessibilityFeatureNames {
		attrs[name] = schema.BoolAttribute{
			MarkdownDescription: "Whether the app supports this accessibility feature. Defaults to `false`.",
			Optional:            true,
			Computed:            true,
			Default:             booldefault.StaticBool(false),
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Declares the accessibility features an app supports on one device family, shown on its " +
			"App Store page. A declaration replaced by a newer published one is created again. Import it by ID.",
		Attributes: attrs,
	}
}

func (r *accessibilityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *accessibilityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *accessibilityModel) apply(d *gen.AccessibilityDeclaration) {
	a := asc.Value(d.Attributes)
	m.ID, m.DeviceFamily, m.State = types.StringValue(d.Id), stringValue(a.DeviceFamily), stringValue(a.State)
	m.Publish = types.BoolValue(asc.Value(a.State) == gen.AccessibilityDeclarationAttributesStatePUBLISHED)
	values := []*bool{a.SupportsAudioDescriptions, a.SupportsCaptions, a.SupportsDarkInterface, a.SupportsDifferentiateWithoutColorAlone,
		a.SupportsLargerText, a.SupportsReducedMotion, a.SupportsSufficientContrast, a.SupportsVoiceControl, a.SupportsVoiceover}
	for i, f := range m.features() {
		*f = types.BoolValue(asc.Value(values[i]))
	}
}

func (r *accessibilityResource) update(ctx context.Context, m *accessibilityModel, publish bool) (*gen.AccessibilityDeclaration, error) {
	var body gen.AccessibilityDeclarationUpdateRequest
	body.Data.Type, body.Data.Id = gen.AccessibilityDeclarationUpdateRequestDataTypeAccessibilityDeclarations, m.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	f := m.features()
	a.SupportsAudioDescriptions, a.SupportsCaptions, a.SupportsDarkInterface = boolPtr(*f[0]), boolPtr(*f[1]), boolPtr(*f[2])
	a.SupportsDifferentiateWithoutColorAlone, a.SupportsLargerText, a.SupportsReducedMotion = boolPtr(*f[3]), boolPtr(*f[4]), boolPtr(*f[5])
	a.SupportsSufficientContrast, a.SupportsVoiceControl, a.SupportsVoiceover = boolPtr(*f[6]), boolPtr(*f[7]), boolPtr(*f[8])
	if publish {
		a.Publish = new(true)
	}
	out, err := r.client.AccessibilityDeclarationsUpdateInstanceWithResponse(ctx, m.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *accessibilityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan accessibilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AccessibilityDeclarationCreateRequest
	body.Data.Type = gen.AccessibilityDeclarationCreateRequestDataTypeAccessibilityDeclarations
	a := &body.Data.Attributes
	a.DeviceFamily = gen.DeviceFamily(plan.DeviceFamily.ValueString())
	f := plan.features()
	a.SupportsAudioDescriptions, a.SupportsCaptions, a.SupportsDarkInterface = boolPtr(*f[0]), boolPtr(*f[1]), boolPtr(*f[2])
	a.SupportsDifferentiateWithoutColorAlone, a.SupportsLargerText, a.SupportsReducedMotion = boolPtr(*f[3]), boolPtr(*f[4]), boolPtr(*f[5])
	a.SupportsSufficientContrast, a.SupportsVoiceControl, a.SupportsVoiceover = boolPtr(*f[6]), boolPtr(*f[7]), boolPtr(*f[8])
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.AccessibilityDeclarationCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.AccessibilityDeclarationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create accessibility declaration", err)
		return
	}
	publish := plan.Publish.ValueBool()
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if publish {
		d, err := r.update(ctx, &plan, true)
		if err != nil {
			apiError(&resp.Diagnostics, "publish accessibility declaration", err)
			return
		}
		plan.apply(d)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *accessibilityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state accessibilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AccessibilityDeclarationsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) || err == nil && asc.Value(asc.Value(out.JSON200.Data.Attributes).State) == gen.AccessibilityDeclarationAttributesStateREPLACED {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read accessibility declaration", err)
		return
	}
	appID := state.AppID
	state.apply(&out.JSON200.Data)
	state.AppID = appID
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *accessibilityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state accessibilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	publish := plan.Publish.ValueBool() && !state.Publish.ValueBool()
	d, err := r.update(ctx, &plan, publish)
	if err != nil {
		apiError(&resp.Diagnostics, "update accessibility declaration", err)
		return
	}
	appID := plan.AppID
	plan.apply(d)
	plan.AppID = appID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accessibilityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state accessibilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AccessibilityDeclarationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete accessibility declaration", err)
	}
}

// ---- apple_android_to_ios_app_mapping ----

var (
	_ resource.ResourceWithConfigure   = &androidMappingResource{}
	_ resource.ResourceWithImportState = &androidMappingResource{}
	_ resource.ResourceWithIdentity    = &androidMappingResource{}
)

type androidMappingResource struct {
	resourceIdentity
	client *asc.Client
}

type androidMappingModel struct {
	ID           types.String `tfsdk:"id"`
	AppID        types.String `tfsdk:"app_id"`
	PackageName  types.String `tfsdk:"package_name"`
	Fingerprints types.List   `tfsdk:"signing_certificate_sha256_fingerprints"`
}

func newAndroidMappingResource() resource.Resource {
	return &androidMappingResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the mapping.")}}
}

func (r *androidMappingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_android_to_ios_app_mapping"
}

func (r *androidMappingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Links an Android app to this iOS app, so the App Store can recognize users moving from " +
			"Android. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"id":     idAttribute("App Store Connect ID of the mapping."),
			"app_id": replacedString("App Store Connect ID of the app."),
			"package_name": schema.StringAttribute{
				MarkdownDescription: "Package name of the Android app, e.g. `com.example.app`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"signing_certificate_sha256_fingerprints": schema.ListAttribute{
				MarkdownDescription: "SHA-256 fingerprints of the Android app's signing certificates.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
			},
		},
	}
}

func (r *androidMappingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *androidMappingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func listStrings(ctx context.Context, l types.List) []string {
	var out []string
	_ = l.ElementsAs(ctx, &out, false)
	return out
}

func (m *androidMappingModel) apply(d *gen.AndroidToIosAppMappingDetail) {
	a := asc.Value(d.Attributes)
	m.ID, m.PackageName = types.StringValue(d.Id), stringValue(a.PackageName)
	m.Fingerprints = stringsList(asc.Value(a.AppSigningKeyPublicCertificateSha256Fingerprints))
}

func (r *androidMappingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan androidMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AndroidToIosAppMappingDetailCreateRequest
	body.Data.Type = gen.AndroidToIosAppMappingDetailCreateRequestDataTypeAndroidToIosAppMappingDetails
	body.Data.Attributes.PackageName = plan.PackageName.ValueString()
	body.Data.Attributes.AppSigningKeyPublicCertificateSha256Fingerprints = listStrings(ctx, plan.Fingerprints)
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.AndroidToIosAppMappingDetailCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.AndroidToIosAppMappingDetailsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create Android app mapping", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *androidMappingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state androidMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AndroidToIosAppMappingDetailsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Android app mapping", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *androidMappingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan androidMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AndroidToIosAppMappingDetailUpdateRequest
	body.Data.Type, body.Data.Id = gen.AndroidToIosAppMappingDetailUpdateRequestDataTypeAndroidToIosAppMappingDetails, plan.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.PackageName = new(plan.PackageName.ValueString())
	a.AppSigningKeyPublicCertificateSha256Fingerprints = new(listStrings(ctx, plan.Fingerprints))
	out, err := r.client.AndroidToIosAppMappingDetailsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update Android app mapping", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *androidMappingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state androidMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AndroidToIosAppMappingDetailsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Android app mapping", err)
	}
}

// ---- apple_app_tag ----

var (
	_ resource.ResourceWithConfigure   = &appTagResource{}
	_ resource.ResourceWithImportState = &appTagResource{}
	_ resource.ResourceWithIdentity    = &appTagResource{}
)

type appTagResource struct {
	resourceIdentity
	client *asc.Client
}

type appTagModel struct {
	AppID             types.String `tfsdk:"app_id"`
	Name              types.String `tfsdk:"name"`
	VisibleInAppStore types.Bool   `tfsdk:"visible_in_app_store"`
	ID                types.String `tfsdk:"id"`
}

func newAppTagResource() resource.Resource {
	return &appTagResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "name", description: "Name of the tag.", state: "name"},
	}}}
}

func (r *appTagResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_tag"
}

func (r *appTagResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Chooses whether a tag Apple assigned to an app is shown on the App Store. Apple creates the " +
			"tags; destroying the resource leaves the tag as it is. Import it with `<app_id>/<name>`.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app."),
			"name":   replacedString("Name of the tag, as Apple assigned it."),
			"visible_in_app_store": schema.BoolAttribute{
				MarkdownDescription: "Whether the tag is shown on the App Store.",
				Required:            true,
			},
			"id": schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the tag.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *appTagResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *appTagResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *appTagResource) find(ctx context.Context, appID, name string) (*gen.AppTag, error) {
	out, err := r.client.AppsAppTagsGetToManyRelatedWithResponse(ctx, appID, &gen.AppsAppTagsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	all, err := asc.All(ctx, r.client, out.JSON200,
		func(p *gen.AppTagsResponse) []gen.AppTag { return p.Data },
		func(p *gen.AppTagsResponse) *string { return p.Links.Next })
	if err != nil {
		return nil, err
	}
	for i := range all {
		if asc.Value(asc.Value(all[i].Attributes).Name) == name {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

func (r *appTagResource) set(ctx context.Context, m *appTagModel) error {
	tag, err := r.find(ctx, m.AppID.ValueString(), m.Name.ValueString())
	if err != nil {
		return err
	}
	var body gen.AppTagUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppTagUpdateRequestDataTypeAppTags, tag.Id
	asc.Alloc(&body.Data.Attributes).VisibleInAppStore = new(m.VisibleInAppStore.ValueBool())
	out, err := r.client.AppTagsUpdateInstanceWithResponse(ctx, tag.Id, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	m.ID, m.VisibleInAppStore = types.StringValue(tag.Id), types.BoolValue(asc.Value(asc.Value(out.JSON200.Data.Attributes).VisibleInAppStore))
	return nil
}

func (r *appTagResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appTagModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		if errors.Is(err, errNoMatch) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "No such tag", "The app has no tag named "+plan.Name.ValueString()+".")
			return
		}
		apiError(&resp.Diagnostics, "update app tag", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appTagResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state appTagModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tag, err := r.find(ctx, state.AppID.ValueString(), state.Name.ValueString())
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read app tag", err)
		return
	}
	state.ID, state.VisibleInAppStore = types.StringValue(tag.Id), types.BoolValue(asc.Value(asc.Value(tag.Attributes).VisibleInAppStore))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appTagResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appTagModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "update app tag", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the tag, which Apple manages.
func (r *appTagResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}
