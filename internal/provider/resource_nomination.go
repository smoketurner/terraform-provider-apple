package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Featuring nominations tell Apple's editorial team about an upcoming
// launch, update or new content
// (https://developer.apple.com/documentation/appstoreconnectapi/nominations).

var (
	nominationTypes  = enum(gen.NominationAttributesTypeAPPLAUNCH, gen.NominationAttributesTypeAPPENHANCEMENTS, gen.NominationAttributesTypeNEWCONTENT)
	nominationStates = enum(gen.NominationAttributesStateDRAFT, gen.NominationAttributesStateSUBMITTED, gen.NominationAttributesStateARCHIVED)
)

var (
	_ resource.ResourceWithConfigure   = &nominationResource{}
	_ resource.ResourceWithImportState = &nominationResource{}
	_ resource.ResourceWithIdentity    = &nominationResource{}
)

type nominationResource struct {
	resourceIdentity
	client *asc.Client
}

type nominationModel struct {
	Name                       types.String `tfsdk:"name"`
	Type                       types.String `tfsdk:"type"`
	Description                types.String `tfsdk:"description"`
	AppIDs                     types.Set    `tfsdk:"app_ids"`
	PublishStartDate           types.String `tfsdk:"publish_start_date"`
	PublishEndDate             types.String `tfsdk:"publish_end_date"`
	DeviceFamilies             types.Set    `tfsdk:"device_families"`
	Locales                    types.Set    `tfsdk:"locales"`
	TerritoryIDs               types.Set    `tfsdk:"territory_ids"`
	SupplementalMaterialsURIs  types.List   `tfsdk:"supplemental_materials_uris"`
	LaunchInSelectMarketsFirst types.Bool   `tfsdk:"launch_in_select_markets_first"`
	PreOrderEnabled            types.Bool   `tfsdk:"pre_order_enabled"`
	Notes                      types.String `tfsdk:"notes"`
	Submitted                  types.Bool   `tfsdk:"submitted"`
	ID                         types.String `tfsdk:"id"`
	State                      types.String `tfsdk:"state"`
	SubmittedDate              types.String `tfsdk:"submitted_date"`
}

func newNominationResource() resource.Resource {
	return &nominationResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the nomination.")}}
}

func (r *nominationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nomination"
}

func optionalSet(desc string, validators ...validator.Set) schema.SetAttribute {
	return schema.SetAttribute{MarkdownDescription: desc, Optional: true, ElementType: types.StringType, Validators: validators}
}

// unsubmitReplaces replaces a submitted nomination when submitted changes to
// false: a submitted nomination can't return to draft.
type unsubmitReplaces struct{}

func (unsubmitReplaces) Description(context.Context) string {
	return "Replaces a submitted nomination when submitted is set to false."
}

func (m unsubmitReplaces) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (unsubmitReplaces) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	if req.StateValue.ValueBool() && !req.PlanValue.IsUnknown() && !req.PlanValue.ValueBool() {
		resp.RequiresReplace = true
	}
}

func (r *nominationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Nominates apps for featuring on the App Store: a launch, an update or new content. A " +
			"nomination is a draft until `submitted` is `true`. A submitted nomination can't return to draft, so " +
			"setting `submitted = false` then replaces it with a new draft. Destroying the resource deletes the " +
			"nomination. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"name":        schema.StringAttribute{MarkdownDescription: "Name of the nomination.", Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"type":        schema.StringAttribute{MarkdownDescription: "What is nominated: " + docValues(nominationTypes) + ".", Required: true, Validators: []validator.String{oneOf(nominationTypes)}},
			"description": schema.StringAttribute{MarkdownDescription: "What makes the launch, update or content notable.", Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"app_ids": schema.SetAttribute{
				MarkdownDescription: "App Store Connect IDs of the apps nominated.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"publish_start_date": schema.StringAttribute{
				MarkdownDescription: "When the launch, update or content becomes available, in UTC, e.g. `2026-12-01T16:00:00Z`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(utcTimeRegexp, "must be an RFC 3339 time in UTC, e.g. `2026-12-01T16:00:00Z`")},
			},
			"publish_end_date": optionalString("When it stops being available, for time-limited content, in UTC.",
				stringvalidator.RegexMatches(utcTimeRegexp, "must be an RFC 3339 time in UTC, e.g. `2026-12-01T16:00:00Z`")),
			"device_families": optionalSet("Device families: "+docValues(deviceFamilies)+".", setvalidator.ValueStringsAre(oneOf(deviceFamilies))),
			"locales":         optionalSet("Locales of the launch, e.g. `en-US`.", setvalidator.ValueStringsAre(localeValidator)),
			"territory_ids":   optionalSet("Territories of the launch, e.g. `USA`.", setvalidator.ValueStringsAre(territoryValidator)),
			"supplemental_materials_uris": schema.ListAttribute{
				MarkdownDescription: "Links to supporting material, such as press kits or videos.",
				Optional:            true,
				ElementType:         types.StringType,
			},
			"launch_in_select_markets_first": optionalBool("Whether the app launches in some markets before the others."),
			"pre_order_enabled":              optionalBool("Whether the app is available for pre-order."),
			"notes":                          optionalString("Other information for the editorial team."),
			"submitted": schema.BoolAttribute{
				MarkdownDescription: "Whether the nomination is submitted to Apple. Defaults to `false`, a draft.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				PlanModifiers:       []planmodifier.Bool{unsubmitReplaces{}, boolplanmodifier.UseStateForUnknown()},
			},
			"id":             idAttribute("App Store Connect ID of the nomination."),
			"state":          computedOnly("State of the nomination: " + docValues(nominationStates) + "."),
			"submitted_date": computedOnly("When the nomination was submitted."),
		},
	}
}

func (r *nominationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *nominationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// optionalSetValue reads an API list into an optional set, mapping nil or
// empty to null.
func optionalSetValue(values []string) types.Set {
	if len(values) == 0 {
		return types.SetNull(types.StringType)
	}
	return stringsValue(values)
}

func (m *nominationModel) apply(n *gen.Nomination) {
	a := asc.Value(n.Attributes)
	m.ID, m.State, m.SubmittedDate = types.StringValue(n.Id), stringValue(a.State), timeValue(a.SubmittedDate)
	m.Name, m.Type, m.Description, m.Notes = stringValue(a.Name), stringValue(a.Type), stringValue(a.Description), stringValue(a.Notes)
	m.PublishStartDate, m.PublishEndDate = timeValue(a.PublishStartDate), timeValue(a.PublishEndDate)
	var families []string
	for _, f := range asc.Value(a.DeviceFamilies) {
		families = append(families, string(f))
	}
	m.DeviceFamilies, m.Locales = optionalSetValue(families), optionalSetValue(asc.Value(a.Locales))
	m.SupplementalMaterialsURIs = types.ListNull(types.StringType)
	if uris := asc.Value(a.SupplementalMaterialsUris); len(uris) > 0 {
		m.SupplementalMaterialsURIs = stringsList(uris)
	}
	m.LaunchInSelectMarketsFirst = types.BoolValue(asc.Value(a.LaunchInSelectMarketsFirst))
	m.PreOrderEnabled = types.BoolValue(asc.Value(a.PreOrderEnabled))
	m.Submitted = types.BoolValue(asc.Value(a.State) != gen.NominationAttributesStateDRAFT)
	var apps, territories []string
	if rel := n.Relationships; rel != nil {
		if rel.RelatedApps != nil {
			for _, d := range asc.Value(rel.RelatedApps.Data) {
				apps = append(apps, d.Id)
			}
		}
		if rel.SupportedTerritories != nil {
			for _, d := range asc.Value(rel.SupportedTerritories.Data) {
				territories = append(territories, d.Id)
			}
		}
	}
	m.AppIDs, m.TerritoryIDs = stringsValue(apps), optionalSetValue(territories)
}

func linkages(ctx context.Context, typ string, set types.Set, diags *diag.Diagnostics) []map[string]string {
	out := []map[string]string{}
	for _, id := range setStrings(ctx, set, diags) {
		out = append(out, map[string]string{"type": typ, "id": id})
	}
	return out
}

// body builds the nomination as raw JSON. Every attribute is sent, with
// null for unset ones, which clears them on update; the generated update
// type can't send null.
func (m *nominationModel) body(ctx context.Context, id string, diags *diag.Diagnostics) []byte {
	optional := func(v types.String) any {
		if v.IsNull() || v.IsUnknown() {
			return nil
		}
		return v.ValueString()
	}
	optionalBool := func(v types.Bool) any {
		if v.IsNull() || v.IsUnknown() {
			return nil
		}
		return v.ValueBool()
	}
	list := func(v []string) any {
		if len(v) == 0 {
			return nil
		}
		return v
	}
	var uris []string
	if !m.SupplementalMaterialsURIs.IsNull() && !m.SupplementalMaterialsURIs.IsUnknown() {
		diags.Append(m.SupplementalMaterialsURIs.ElementsAs(ctx, &uris, false)...)
	}
	attrs := map[string]any{
		"name": m.Name.ValueString(), "type": m.Type.ValueString(), "description": m.Description.ValueString(),
		"publishStartDate": m.PublishStartDate.ValueString(), "publishEndDate": optional(m.PublishEndDate),
		"deviceFamilies": list(setStrings(ctx, m.DeviceFamilies, diags)), "locales": list(setStrings(ctx, m.Locales, diags)),
		"supplementalMaterialsUris": list(uris), "notes": optional(m.Notes),
		"launchInSelectMarketsFirst": optionalBool(m.LaunchInSelectMarketsFirst), "preOrderEnabled": optionalBool(m.PreOrderEnabled),
		"submitted": m.Submitted.ValueBool(),
	}
	data := map[string]any{
		"type": "nominations", "attributes": attrs,
		"relationships": map[string]any{
			"relatedApps":          map[string]any{"data": linkages(ctx, "apps", m.AppIDs, diags)},
			"supportedTerritories": map[string]any{"data": linkages(ctx, "territories", m.TerritoryIDs, diags)},
		},
	}
	if id != "" {
		data["id"] = id
	}
	b, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		diags.AddError("Cannot encode nomination", err.Error())
	}
	return b
}

func (r *nominationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan nominationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	body := plan.body(ctx, "", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.NominationsCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create nomination", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *nominationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state nominationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.NominationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.NominationsGetInstanceParams{
		Include:                   &[]gen.NominationsGetInstanceParamsInclude{gen.NominationsGetInstanceParamsIncludeRelatedApps, gen.NominationsGetInstanceParamsIncludeSupportedTerritories},
		LimitRelatedApps:          new(50),
		LimitSupportedTerritories: new(listAll),
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read nomination", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *nominationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state nominationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	body := plan.body(ctx, state.ID.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.NominationsUpdateInstanceWithBodyWithResponse(ctx, state.ID.ValueString(), "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update nomination", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *nominationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state nominationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.NominationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete nomination", err)
	}
}

// ---- data.apple_nominations ----

type nominationData struct {
	ID    types.String `tfsdk:"id"`
	Name  types.String `tfsdk:"name"`
	Type  types.String `tfsdk:"type"`
	State types.String `tfsdk:"state"`
}

type nominationsData struct {
	States      types.Set        `tfsdk:"states"`
	Types       types.Set        `tfsdk:"types"`
	AppIDs      types.Set        `tfsdk:"app_ids"`
	Nominations []nominationData `tfsdk:"nominations"`
}

var newNominationsDataSource = listDataSource("nominations",
	"Lists the account's featuring nominations.",
	map[string]dsschema.Attribute{
		"states":  dsFilter("Only nominations in these states; all states when unset.", nominationStates...),
		"types":   dsFilter("Only nominations of these types.", nominationTypes...),
		"app_ids": dsFilter("Only nominations of one of these apps."),
		"nominations": listOf("The nominations.", map[string]dsschema.Attribute{
			"id":    dsComputed("App Store Connect ID of the nomination."),
			"name":  dsComputed("Name of the nomination."),
			"type":  dsComputed("Type of the nomination."),
			"state": dsComputed("State of the nomination."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *nominationsData, diags *diag.Diagnostics) {
		states := filterParam[gen.NominationsGetCollectionParamsFilterState](ctx, m.States, diags)
		if states == nil {
			states = new(slicesOf[gen.NominationsGetCollectionParamsFilterState](nominationStates))
		}
		out, err := c.NominationsGetCollectionWithResponse(ctx, &gen.NominationsGetCollectionParams{
			FilterState:       *states,
			FilterType:        filterParam[gen.NominationsGetCollectionParamsFilterType](ctx, m.Types, diags),
			FilterRelatedApps: filterParam[string](ctx, m.AppIDs, diags),
			Limit:             new(listAll),
		})
		var all []gen.Nomination
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.NominationsResponse) []gen.Nomination { return p.Data },
				func(p *gen.NominationsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list nominations", err)
			return
		}
		m.Nominations = []nominationData{}
		for _, n := range all {
			a := asc.Value(n.Attributes)
			m.Nominations = append(m.Nominations, nominationData{ID: types.StringValue(n.Id), Name: stringValue(a.Name), Type: stringValue(a.Type), State: stringValue(a.State)})
		}
	})

// ---- apple_app_store_version_localization_search_keyword ----

var (
	_ resource.ResourceWithConfigure   = &searchKeywordResource{}
	_ resource.ResourceWithImportState = &searchKeywordResource{}
	_ resource.ResourceWithIdentity    = &searchKeywordResource{}
)

type searchKeywordResource struct {
	resourceIdentity
	client *asc.Client
}

type searchKeywordModel struct {
	LocalizationID types.String `tfsdk:"localization_id"`
	KeywordID      types.String `tfsdk:"keyword_id"`
}

func newSearchKeywordResource() resource.Resource {
	return &searchKeywordResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "localization_id", description: "App Store Connect ID of the version localization.", state: "localization_id"},
		{name: "keyword_id", description: "App Store Connect ID of the search keyword.", state: "keyword_id"},
	}}}
}

func (r *searchKeywordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_version_localization_search_keyword"
}

func (r *searchKeywordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Links one search keyword to an App Store version localization. App Store Connect offers " +
			"the keywords for each platform and locale; list them with `data.apple_app_search_keywords`. Changes need " +
			"the version to be editable. Destroying the resource removes the keyword. Import it with " +
			"`<localization_id>/<keyword_id>`.",
		Attributes: map[string]schema.Attribute{
			"localization_id": replacedString("App Store Connect ID of the version localization, e.g. `apple_app_store_version_localization.en_us.id`."),
			"keyword_id":      replacedString("App Store Connect ID of the search keyword."),
		},
	}
}

func (r *searchKeywordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *searchKeywordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func keywordLinkage(m *searchKeywordModel) gen.AppStoreVersionLocalizationSearchKeywordsLinkagesRequest {
	var body gen.AppStoreVersionLocalizationSearchKeywordsLinkagesRequest
	e := asc.Push(&body.Data)
	e.Id, e.Type = m.KeywordID.ValueString(), gen.AppStoreVersionLocalizationSearchKeywordsLinkagesRequestDataTypeAppKeywords
	return body
}

// localizationKeywords lists the IDs of a localization's search keywords.
func localizationKeywords(ctx context.Context, c *asc.Client, id string) ([]string, error) {
	out, err := c.AppStoreVersionLocalizationsSearchKeywordsGetToManyRelatedWithResponse(ctx, id,
		&gen.AppStoreVersionLocalizationsSearchKeywordsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	all, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.AppKeywordsResponse) []gen.AppKeyword { return p.Data },
		func(p *gen.AppKeywordsResponse) *string { return p.Links.Next })
	var ids []string
	for _, k := range all {
		ids = append(ids, k.Id)
	}
	return ids, err
}

func (r *searchKeywordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan searchKeywordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionLocalizationsSearchKeywordsCreateToManyRelationshipWithResponse(ctx, plan.LocalizationID.ValueString(), keywordLinkage(&plan))
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "add search keyword", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *searchKeywordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state searchKeywordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ids, err := localizationKeywords(ctx, r.client, state.LocalizationID.ValueString())
	if asc.IsNotFound(err) || err == nil && !slices.Contains(ids, state.KeywordID.ValueString()) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read search keywords", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: both arguments require replacement.
func (r *searchKeywordResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

// Delete removes the keyword while the version is editable; a released
// version keeps its keywords.
func (r *searchKeywordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state searchKeywordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionLocalizationsSearchKeywordsDeleteToManyRelationshipWithResponse(ctx, state.LocalizationID.ValueString(), keywordLinkage(&state))
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "remove search keyword", err)
	}
}

// ---- data.apple_app_search_keywords ----

type searchKeywordsData struct {
	AppID    types.String `tfsdk:"app_id"`
	Platform types.String `tfsdk:"platform"`
	Locale   types.String `tfsdk:"locale"`
	IDs      types.Set    `tfsdk:"ids"`
}

var newSearchKeywordsDataSource = listDataSource("app_search_keywords",
	"Lists the search keywords App Store Connect offers for an app, for "+
		"`apple_app_store_version_localization_search_keyword`.",
	map[string]dsschema.Attribute{
		"app_id":   dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"platform": dsschema.StringAttribute{MarkdownDescription: "Only keywords for this platform: " + docValues(storePlatforms) + ".", Optional: true, Validators: []validator.String{oneOf(storePlatforms)}},
		"locale":   dsschema.StringAttribute{MarkdownDescription: "Only keywords for this locale, e.g. `en-US`.", Optional: true, Validators: []validator.String{localeValidator}},
		"ids":      dsComputedSet("App Store Connect IDs of the keywords."),
	},
	func(ctx context.Context, c *asc.Client, m *searchKeywordsData, diags *diag.Diagnostics) {
		params := &gen.AppsSearchKeywordsGetToManyRelatedParams{Limit: new(listAll)}
		if v := knownString(m.Platform); v != nil {
			params.FilterPlatform = &[]string{*v}
		}
		if v := knownString(m.Locale); v != nil {
			params.FilterLocale = &[]string{*v}
		}
		out, err := c.AppsSearchKeywordsGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), params)
		var all []gen.AppKeyword
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AppKeywordsResponse) []gen.AppKeyword { return p.Data },
				func(p *gen.AppKeywordsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list search keywords", err)
			return
		}
		var ids []string
		for _, k := range all {
			ids = append(ids, k.Id)
		}
		m.IDs = stringsValue(ids)
	})
