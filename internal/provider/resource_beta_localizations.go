package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// optionalString is a plain optional string attribute.
func optionalString(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Optional: true, Validators: validators}
}

// changedString returns the value to send for an optional string in an
// update: the planned value, "" to clear a value removed from the
// configuration, or nil to leave it alone.
func changedString(plan, state types.String) *string {
	switch {
	case plan.IsUnknown():
		return nil
	case !plan.IsNull():
		return new(plan.ValueString())
	case !state.IsNull():
		return new("")
	}
	return nil
}

func knownString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return new(v.ValueString())
}

var localeValidator = stringvalidator.RegexMatches(localeRegexp, "must be a locale such as `en-US`")

// ---- apple_beta_app_localization ----

var (
	_ resource.ResourceWithConfigure   = &betaAppLocalizationResource{}
	_ resource.ResourceWithImportState = &betaAppLocalizationResource{}
	_ resource.ResourceWithIdentity    = &betaAppLocalizationResource{}
)

type betaAppLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type betaAppLocalizationModel struct {
	ID                types.String `tfsdk:"id"`
	AppID             types.String `tfsdk:"app_id"`
	Locale            types.String `tfsdk:"locale"`
	Description       types.String `tfsdk:"description"`
	FeedbackEmail     types.String `tfsdk:"feedback_email"`
	MarketingURL      types.String `tfsdk:"marketing_url"`
	PrivacyPolicyURL  types.String `tfsdk:"privacy_policy_url"`
	TVOSPrivacyPolicy types.String `tfsdk:"tvos_privacy_policy"`
}

func newBetaAppLocalizationResource() resource.Resource {
	return &betaAppLocalizationResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the localization.")}}
}

func (r *betaAppLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_app_localization"
}

func (r *betaAppLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the TestFlight test information of an app for one locale: the beta app description and " +
			"the links and email testers see. An app has one localization per locale.",
		Attributes: map[string]schema.Attribute{
			"id":                  idAttribute("App Store Connect ID of the localization."),
			"app_id":              replacedString("App Store Connect ID of the app. Changing it creates a new localization."),
			"locale":              replacedString("Locale, e.g. `en-US`. Changing it creates a new localization.", localeValidator),
			"description":         optionalString("Beta app description: what the app does and what to test."),
			"feedback_email":      optionalString("Email address testers send feedback to.", stringvalidator.RegexMatches(emailRegexp, "must be an email address")),
			"marketing_url":       optionalString("Marketing URL of the app."),
			"privacy_policy_url":  optionalString("URL of the app's privacy policy."),
			"tvos_privacy_policy": optionalString("Privacy policy text for tvOS, which cannot open URLs."),
		},
	}
}

func (r *betaAppLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *betaAppLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *betaAppLocalizationModel) apply(l *gen.BetaAppLocalization) {
	m.ID = types.StringValue(l.Id)
	a := asc.Value(l.Attributes)
	m.Locale, m.Description, m.FeedbackEmail = stringValue(a.Locale), stringValue(a.Description), stringValue(a.FeedbackEmail)
	m.MarketingURL, m.PrivacyPolicyURL, m.TVOSPrivacyPolicy = stringValue(a.MarketingUrl), stringValue(a.PrivacyPolicyUrl), stringValue(a.TvOsPrivacyPolicy)
	if l.Relationships != nil && l.Relationships.App != nil && l.Relationships.App.Data != nil {
		m.AppID = types.StringValue(l.Relationships.App.Data.Id)
	}
}

func (r *betaAppLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaAppLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaAppLocalizationCreateRequest
	body.Data.Type = gen.BetaAppLocalizationCreateRequestDataTypeBetaAppLocalizations
	a := &body.Data.Attributes
	a.Locale, a.Description, a.FeedbackEmail = plan.Locale.ValueString(), knownString(plan.Description), knownString(plan.FeedbackEmail)
	a.MarketingUrl, a.PrivacyPolicyUrl, a.TvOsPrivacyPolicy = knownString(plan.MarketingURL), knownString(plan.PrivacyPolicyURL), knownString(plan.TVOSPrivacyPolicy)
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.BetaAppLocalizationCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.BetaAppLocalizationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create beta app localization", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaAppLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaAppLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaAppLocalizationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.BetaAppLocalizationsGetInstanceParams{
		Include: &[]gen.BetaAppLocalizationsGetInstanceParamsInclude{gen.BetaAppLocalizationsGetInstanceParamsIncludeApp},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read beta app localization", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *betaAppLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state betaAppLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaAppLocalizationUpdateRequest
	body.Data.Type, body.Data.Id = gen.BetaAppLocalizationUpdateRequestDataTypeBetaAppLocalizations, plan.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.Description = changedString(plan.Description, state.Description)
	a.FeedbackEmail = changedString(plan.FeedbackEmail, state.FeedbackEmail)
	a.MarketingUrl = changedString(plan.MarketingURL, state.MarketingURL)
	a.PrivacyPolicyUrl = changedString(plan.PrivacyPolicyURL, state.PrivacyPolicyURL)
	a.TvOsPrivacyPolicy = changedString(plan.TVOSPrivacyPolicy, state.TVOSPrivacyPolicy)
	out, err := r.client.BetaAppLocalizationsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update beta app localization", err)
		return
	}
	appID := plan.AppID
	plan.apply(&out.JSON200.Data)
	plan.AppID = appID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaAppLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state betaAppLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaAppLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete beta app localization", err)
	}
}

// ---- apple_beta_build_localization ----

var (
	_ resource.ResourceWithConfigure   = &betaBuildLocalizationResource{}
	_ resource.ResourceWithImportState = &betaBuildLocalizationResource{}
	_ resource.ResourceWithIdentity    = &betaBuildLocalizationResource{}
)

type betaBuildLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type betaBuildLocalizationModel struct {
	ID       types.String `tfsdk:"id"`
	BuildID  types.String `tfsdk:"build_id"`
	Locale   types.String `tfsdk:"locale"`
	WhatsNew types.String `tfsdk:"whats_new"`
}

func newBetaBuildLocalizationResource() resource.Resource {
	return &betaBuildLocalizationResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the localization.")}}
}

func (r *betaBuildLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_build_localization"
}

func (r *betaBuildLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a build's \"What to Test\" text for one locale. A build has one localization per locale.",
		Attributes: map[string]schema.Attribute{
			"id":        idAttribute("App Store Connect ID of the localization."),
			"build_id":  replacedString("App Store Connect ID of the build, e.g. `data.apple_build.latest.id`. Changing it creates a new localization."),
			"locale":    replacedString("Locale, e.g. `en-US`. Changing it creates a new localization.", localeValidator),
			"whats_new": optionalString("What testers should test in this build."),
		},
	}
}

func (r *betaBuildLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *betaBuildLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *betaBuildLocalizationModel) apply(l *gen.BetaBuildLocalization) {
	m.ID = types.StringValue(l.Id)
	a := asc.Value(l.Attributes)
	m.Locale, m.WhatsNew = stringValue(a.Locale), stringValue(a.WhatsNew)
	if l.Relationships != nil && l.Relationships.Build != nil && l.Relationships.Build.Data != nil {
		m.BuildID = types.StringValue(l.Relationships.Build.Data.Id)
	}
}

func (r *betaBuildLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan betaBuildLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaBuildLocalizationCreateRequest
	body.Data.Type = gen.BetaBuildLocalizationCreateRequestDataTypeBetaBuildLocalizations
	body.Data.Attributes.Locale, body.Data.Attributes.WhatsNew = plan.Locale.ValueString(), knownString(plan.WhatsNew)
	body.Data.Relationships.Build.Data.Id = plan.BuildID.ValueString()
	body.Data.Relationships.Build.Data.Type = gen.BetaBuildLocalizationCreateRequestDataRelationshipsBuildDataTypeBuilds
	out, err := r.client.BetaBuildLocalizationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create beta build localization", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaBuildLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state betaBuildLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaBuildLocalizationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.BetaBuildLocalizationsGetInstanceParams{
		Include: &[]gen.BetaBuildLocalizationsGetInstanceParamsInclude{gen.BetaBuildLocalizationsGetInstanceParamsIncludeBuild},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read beta build localization", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *betaBuildLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state betaBuildLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaBuildLocalizationUpdateRequest
	body.Data.Type, body.Data.Id = gen.BetaBuildLocalizationUpdateRequestDataTypeBetaBuildLocalizations, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).WhatsNew = changedString(plan.WhatsNew, state.WhatsNew)
	out, err := r.client.BetaBuildLocalizationsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update beta build localization", err)
		return
	}
	buildID := plan.BuildID
	plan.apply(&out.JSON200.Data)
	plan.BuildID = buildID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *betaBuildLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state betaBuildLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaBuildLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete beta build localization", err)
	}
}
