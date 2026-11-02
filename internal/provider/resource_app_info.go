package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// App info is the app metadata that applies across versions. An app has one
// app info for its live version and, while a new version is being prepared,
// a second one that takes effect when that version is released. These
// resources are keyed by app, and change whichever app info is editable.

// editableAppInfoStates are the states in which app metadata can be edited
// ("App and submission statuses",
// https://developer.apple.com/help/app-store-connect/reference/app-information/app-and-submission-statuses).
var editableAppInfoStates = enum(gen.AppInfoAttributesStatePREPAREFORSUBMISSION, gen.AppInfoAttributesStateREADYFORREVIEW,
	gen.AppInfoAttributesStateWAITINGFORREVIEW, gen.AppInfoAttributesStateREJECTED, gen.AppInfoAttributesStateDEVELOPERREJECTED)

// errNotEditable reports that no app info of an app can be edited.
var errNotEditable = errors.New("no version of the app is in an editable state; create a new App Store version first, " +
	"or wait for review to finish")

// appInfos returns an app's app info that can be edited, if any, and the
// one to read: the editable one, or else the live one.
func appInfos(ctx context.Context, c *asc.Client, appID string) (editable, current *gen.AppInfo, err error) {
	out, err := c.AppsAppInfosGetToManyRelatedWithResponse(ctx, appID, &gen.AppsAppInfosGetToManyRelatedParams{
		Include: &[]gen.AppsAppInfosGetToManyRelatedParamsInclude{
			gen.AppsAppInfosGetToManyRelatedParamsIncludePrimaryCategory, gen.AppsAppInfosGetToManyRelatedParamsIncludeAgeRatingDeclaration,
		},
	})
	if err := asc.Err(out, err); err != nil {
		return nil, nil, err
	}
	for i := range out.JSON200.Data {
		info := &out.JSON200.Data[i]
		state := string(asc.Value(asc.Value(info.Attributes).State))
		switch {
		case slices.Contains(editableAppInfoStates, state):
			editable = info
		case state != string(gen.AppInfoAttributesStateREPLACEDWITHNEWINFO) && current == nil:
			current = info
		}
	}
	if editable != nil {
		current = editable
	}
	if current == nil {
		return nil, nil, errNoMatch
	}
	return editable, current, nil
}

func editableAppInfo(ctx context.Context, c *asc.Client, appID string) (*gen.AppInfo, error) {
	editable, _, err := appInfos(ctx, c, appID)
	if err != nil {
		return nil, err
	}
	if editable == nil {
		return nil, errNotEditable
	}
	return editable, nil
}

// ---- apple_app_info ----

// appCategorySlots are the app info's category relationships, with their
// attribute names.
var appCategorySlots = []struct{ attr, rel string }{
	{"primary_category", "primaryCategory"},
	{"primary_subcategory_one", "primarySubcategoryOne"},
	{"primary_subcategory_two", "primarySubcategoryTwo"},
	{"secondary_category", "secondaryCategory"},
	{"secondary_subcategory_one", "secondarySubcategoryOne"},
	{"secondary_subcategory_two", "secondarySubcategoryTwo"},
}

var (
	_ resource.ResourceWithConfigure   = &appInfoResource{}
	_ resource.ResourceWithImportState = &appInfoResource{}
	_ resource.ResourceWithIdentity    = &appInfoResource{}
)

type appInfoResource struct {
	resourceIdentity
	client *asc.Client
}

type appInfoModel struct {
	AppID                   types.String `tfsdk:"app_id"`
	PrimaryCategory         types.String `tfsdk:"primary_category"`
	PrimarySubcategoryOne   types.String `tfsdk:"primary_subcategory_one"`
	PrimarySubcategoryTwo   types.String `tfsdk:"primary_subcategory_two"`
	SecondaryCategory       types.String `tfsdk:"secondary_category"`
	SecondarySubcategoryOne types.String `tfsdk:"secondary_subcategory_one"`
	SecondarySubcategoryTwo types.String `tfsdk:"secondary_subcategory_two"`
	AppInfoID               types.String `tfsdk:"app_info_id"`
	State                   types.String `tfsdk:"state"`
	AppStoreAgeRating       types.String `tfsdk:"app_store_age_rating"`
}

func (m *appInfoModel) slots() []*types.String {
	return []*types.String{&m.PrimaryCategory, &m.PrimarySubcategoryOne, &m.PrimarySubcategoryTwo,
		&m.SecondaryCategory, &m.SecondarySubcategoryOne, &m.SecondarySubcategoryTwo}
}

func appIdentity() resourceIdentity {
	return resourceIdentity{attrs: []identityAttribute{{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"}}}
}

func newAppInfoResource() resource.Resource { return &appInfoResource{resourceIdentity: appIdentity()} }

func (r *appInfoResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_info"
}

func (r *appInfoResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"app_id":               replacedString("App Store Connect ID of the app."),
		"app_info_id":          schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app info this reads, which changes with each version.", Computed: true},
		"state":                schema.StringAttribute{MarkdownDescription: "State of that app info, e.g. `PREPARE_FOR_SUBMISSION`.", Computed: true},
		"app_store_age_rating": schema.StringAttribute{MarkdownDescription: "Age rating App Store Connect derived from the age rating declaration.", Computed: true},
	}
	for _, s := range appCategorySlots {
		attrs[s.attr] = optionalString("Category ID, e.g. `GAMES` or `GAMES_PUZZLE`; see `apple_app_categories`.")
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an app's App Store categories. Changes apply to the app info of the version being " +
			"prepared, and take effect when it is released; they need a version in an editable state. Destroying the " +
			"resource leaves the categories as they are. Import it by app ID.",
		Attributes: attrs,
	}
}

func (r *appInfoResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *appInfoResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *appInfoModel) apply(info *gen.AppInfo) {
	a := asc.Value(info.Attributes)
	m.AppInfoID, m.State, m.AppStoreAgeRating = types.StringValue(info.Id), stringValue(a.State), stringValue(a.AppStoreAgeRating)
	// The category relationships have distinct generated types; read them
	// through JSON.
	var rel map[string]struct {
		Data *struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	b, _ := json.Marshal(info.Relationships)
	_ = json.Unmarshal(b, &rel)
	for i, s := range appCategorySlots {
		*m.slots()[i] = types.StringNull()
		if d := rel[s.rel].Data; d != nil {
			*m.slots()[i] = types.StringValue(d.ID)
		}
	}
}

// set sends the categories in m to the editable app info. Unset categories
// are cleared, which the generated request type cannot express.
func (r *appInfoResource) set(ctx context.Context, m *appInfoModel) error {
	info, err := editableAppInfo(ctx, r.client, m.AppID.ValueString())
	if err != nil {
		return err
	}
	rels := map[string]any{}
	for i, s := range appCategorySlots {
		var data any
		if v := *m.slots()[i]; !v.IsNull() && !v.IsUnknown() {
			data = map[string]string{"type": "appCategories", "id": v.ValueString()}
		}
		rels[s.rel] = map[string]any{"data": data}
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{"type": "appInfos", "id": info.Id, "relationships": rels}})
	if err != nil {
		return err
	}
	out, err := r.client.AppInfosUpdateInstanceWithBodyWithResponse(ctx, info.Id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return err
	}
	m.apply(&out.JSON200.Data)
	return nil
}

func (r *appInfoResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appInfoModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set app categories", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appInfoResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state appInfoModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, current, err := appInfos(ctx, r.client, state.AppID.ValueString())
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read app info", err)
		return
	}
	state.apply(current)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appInfoResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appInfoModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set app categories", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the categories, which every app needs.
func (r *appInfoResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

// ---- apple_app_info_localization ----

var (
	_ resource.ResourceWithConfigure   = &appInfoLocalizationResource{}
	_ resource.ResourceWithImportState = &appInfoLocalizationResource{}
	_ resource.ResourceWithIdentity    = &appInfoLocalizationResource{}
)

type appInfoLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type appInfoLocalizationModel struct {
	AppID             types.String `tfsdk:"app_id"`
	Locale            types.String `tfsdk:"locale"`
	Name              types.String `tfsdk:"name"`
	Subtitle          types.String `tfsdk:"subtitle"`
	PrivacyPolicyURL  types.String `tfsdk:"privacy_policy_url"`
	PrivacyChoicesURL types.String `tfsdk:"privacy_choices_url"`
	PrivacyPolicyText types.String `tfsdk:"privacy_policy_text"`
	ID                types.String `tfsdk:"id"`
}

func newAppInfoLocalizationResource() resource.Resource {
	return &appInfoLocalizationResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "locale", description: "Locale, e.g. `en-US`.", state: "locale"},
	}}}
}

func (r *appInfoLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_info_localization"
}

func (r *appInfoLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an app's localized App Store name, subtitle and privacy information for one locale. " +
			"Changes apply to the app info of the version being prepared and need a version in an editable state. The " +
			"app's primary locale always has a localization: creating this resource for it manages the existing one. " +
			"Add the same locale to each version with the version localization resource. Import it with `<app_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app."),
			"locale": replacedString("Locale, e.g. `en-US`.", localeValidator),
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the app on the App Store, up to 30 characters.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(2, 30)},
			},
			"subtitle":            optionalString("Subtitle, up to 30 characters.", stringvalidator.LengthAtMost(30)),
			"privacy_policy_url":  optionalString("URL of the app's privacy policy."),
			"privacy_choices_url": optionalString("URL where users can manage their privacy choices."),
			"privacy_policy_text": optionalString("Privacy policy text, for tvOS apps."),
			"id":                  schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the localization, which changes with each version.", Computed: true},
		},
	}
}

func (r *appInfoLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *appInfoLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *appInfoLocalizationModel) apply(l *gen.AppInfoLocalization) {
	a := asc.Value(l.Attributes)
	m.ID, m.Name, m.Subtitle = types.StringValue(l.Id), stringValue(a.Name), stringValue(a.Subtitle)
	m.PrivacyPolicyURL, m.PrivacyChoicesURL, m.PrivacyPolicyText = stringValue(a.PrivacyPolicyUrl), stringValue(a.PrivacyChoicesUrl), stringValue(a.PrivacyPolicyText)
}

// findLocalization returns the locale's localization of an app info.
func findAppInfoLocalization(ctx context.Context, c *asc.Client, appInfoID, locale string) (*gen.AppInfoLocalization, error) {
	out, err := c.AppInfosAppInfoLocalizationsGetToManyRelatedWithResponse(ctx, appInfoID, &gen.AppInfosAppInfoLocalizationsGetToManyRelatedParams{
		FilterLocale: &[]string{locale},
	})
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

func (r *appInfoLocalizationResource) update(ctx context.Context, id string, plan, state *appInfoLocalizationModel) (*gen.AppInfoLocalization, error) {
	var body gen.AppInfoLocalizationUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppInfoLocalizationUpdateRequestDataTypeAppInfoLocalizations, id
	a := asc.Alloc(&body.Data.Attributes)
	a.Name = new(plan.Name.ValueString())
	a.Subtitle = changedString(plan.Subtitle, state.Subtitle)
	a.PrivacyPolicyUrl = changedString(plan.PrivacyPolicyURL, state.PrivacyPolicyURL)
	a.PrivacyChoicesUrl = changedString(plan.PrivacyChoicesURL, state.PrivacyChoicesURL)
	a.PrivacyPolicyText = changedString(plan.PrivacyPolicyText, state.PrivacyPolicyText)
	out, err := r.client.AppInfoLocalizationsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *appInfoLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appInfoLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	info, err := editableAppInfo(ctx, r.client, plan.AppID.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "add app info localization", err)
		return
	}
	existing, err := findAppInfoLocalization(ctx, r.client, info.Id, plan.Locale.ValueString())
	var l *gen.AppInfoLocalization
	switch {
	case err == nil:
		var current appInfoLocalizationModel
		current.apply(existing)
		l, err = r.update(ctx, existing.Id, &plan, &current)
	case errors.Is(err, errNoMatch):
		var body gen.AppInfoLocalizationCreateRequest
		body.Data.Type = gen.AppInfoLocalizationCreateRequestDataTypeAppInfoLocalizations
		a := &body.Data.Attributes
		a.Locale, a.Name, a.Subtitle = plan.Locale.ValueString(), plan.Name.ValueString(), knownString(plan.Subtitle)
		a.PrivacyPolicyUrl, a.PrivacyChoicesUrl, a.PrivacyPolicyText = knownString(plan.PrivacyPolicyURL), knownString(plan.PrivacyChoicesURL), knownString(plan.PrivacyPolicyText)
		body.Data.Relationships.AppInfo.Data.Id = info.Id
		body.Data.Relationships.AppInfo.Data.Type = gen.AppInfoLocalizationCreateRequestDataRelationshipsAppInfoDataTypeAppInfos
		out, e := r.client.AppInfoLocalizationsCreateInstanceWithResponse(ctx, body)
		if err = asc.Err(out, e); err == nil {
			l = &out.JSON201.Data
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "add app info localization", err)
		return
	}
	plan.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appInfoLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state appInfoLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, current, err := appInfos(ctx, r.client, state.AppID.ValueString())
	var l *gen.AppInfoLocalization
	if err == nil {
		l, err = findAppInfoLocalization(ctx, r.client, current.Id, state.Locale.ValueString())
	}
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read app info localization", err)
		return
	}
	state.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appInfoLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state appInfoLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	info, err := editableAppInfo(ctx, r.client, plan.AppID.ValueString())
	var l *gen.AppInfoLocalization
	if err == nil {
		l, err = findAppInfoLocalization(ctx, r.client, info.Id, plan.Locale.ValueString())
	}
	if err == nil {
		l, err = r.update(ctx, l.Id, &plan, &state)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update app info localization", err)
		return
	}
	plan.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appInfoLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state appInfoLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	info, err := editableAppInfo(ctx, r.client, state.AppID.ValueString())
	var l *gen.AppInfoLocalization
	if err == nil {
		l, err = findAppInfoLocalization(ctx, r.client, info.Id, state.Locale.ValueString())
	}
	if errors.Is(err, errNoMatch) {
		return
	}
	if err == nil {
		out, e := r.client.AppInfoLocalizationsDeleteInstanceWithResponse(ctx, l.Id)
		if err = asc.Err(out, e); asc.IsNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "delete app info localization", err)
	}
}
