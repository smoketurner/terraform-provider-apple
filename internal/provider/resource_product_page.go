package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Custom product pages: alternative App Store pages of an app, each with its
// own URL. A page's deep link and localizations belong to versions that go
// through App Review, which the provider hides as it does for in-app
// purchases (see versioned.go)
// (https://developer.apple.com/documentation/appstoreconnectapi/custom-product-pages-and-localizations).
// Their screenshots and previews are App Asset Library placements; the
// screenshot and preview set relationships are deprecated.

// pageVersion is a version of a custom product page.
type pageVersion struct {
	versionInfo
	deepLink *string
}

// listPageVersions lists a page's versions.
func listPageVersions(ctx context.Context, c *asc.Client, pageID string) ([]pageVersion, error) {
	out, err := c.AppCustomProductPagesAppCustomProductPageVersionsGetToManyRelatedWithResponse(ctx, pageID,
		&gen.AppCustomProductPagesAppCustomProductPageVersionsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	all, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.AppCustomProductPageVersionsResponse) []gen.AppCustomProductPageVersion { return p.Data },
		func(p *gen.AppCustomProductPageVersionsResponse) *string { return p.Links.Next })
	if err != nil {
		return nil, err
	}
	var vs []pageVersion
	for _, v := range all {
		a := asc.Value(v.Attributes)
		// Version numbers count up from "1"; one that isn't a number sorts first.
		number, _ := strconv.Atoi(asc.Value(a.Version))
		vs = append(vs, pageVersion{versionInfo: versionInfo{id: v.Id, number: number, state: string(asc.Value(a.State))}, deepLink: a.DeepLink})
	}
	return vs, nil
}

var productPageMetadata = metadataVersions{
	noun: "custom product page",
	list: func(ctx context.Context, c *asc.Client, pageID string) ([]versionInfo, error) {
		all, err := listPageVersions(ctx, c, pageID)
		vs := make([]versionInfo, len(all))
		for i, v := range all {
			vs[i] = v.versionInfo
		}
		return vs, err
	},
	create: func(ctx context.Context, c *asc.Client, pageID string) (string, error) {
		var body gen.AppCustomProductPageVersionCreateRequest
		body.Data.Type = gen.AppCustomProductPageVersionCreateRequestDataTypeAppCustomProductPageVersions
		body.Data.Relationships.AppCustomProductPage.Data.Id = pageID
		body.Data.Relationships.AppCustomProductPage.Data.Type = gen.AppCustomProductPageVersionCreateRequestDataRelationshipsAppCustomProductPageDataTypeAppCustomProductPages
		out, err := c.AppCustomProductPageVersionsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

// ---- apple_app_custom_product_page ----

var (
	_ resource.ResourceWithConfigure   = &productPageResource{}
	_ resource.ResourceWithImportState = &productPageResource{}
	_ resource.ResourceWithIdentity    = &productPageResource{}
)

type productPageResource struct {
	resourceIdentity
	client *asc.Client
}

type productPageModel struct {
	AppID    types.String `tfsdk:"app_id"`
	Name     types.String `tfsdk:"name"`
	Visible  types.Bool   `tfsdk:"visible"`
	DeepLink types.String `tfsdk:"deep_link"`
	URL      types.String `tfsdk:"url"`
	State    types.String `tfsdk:"state"`
	ID       types.String `tfsdk:"id"`
}

func newProductPageResource() resource.Resource {
	return &productPageResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the custom product page.")}}
}

func (r *productPageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_custom_product_page"
}

func (r *productPageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a custom product page: an alternative App Store page of an app, reached through its own " +
			"URL. Add promotional text with `apple_app_custom_product_page_localization`, screenshots and previews with " +
			"`apple_app_asset_library_placement` on a localization, and search keywords with " +
			"`apple_app_custom_product_page_localization_search_keyword`; then submit the page with the " +
			"`apple_submit_app_custom_product_page` action. An app can have 70 custom product pages. The deep link belongs " +
			"to the version being prepared: once the page is approved, changing it starts the next version, which App " +
			"Review must approve. Destroying the resource deletes the page, after which its URL opens the app's default " +
			"page. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app."),
			"name":   schema.StringAttribute{MarkdownDescription: "Reference name of the page, shown in App Analytics.", Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"visible": schema.BoolAttribute{
				MarkdownDescription: "Whether the page's URL opens the page. A URL that isn't visible opens the app's default product page. Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"deep_link": optionalString("Link that opens specific content in the app from the page, e.g. `https://example.com/summer`.", stringvalidator.LengthAtLeast(1)),
			"url":       computedOnly("URL of the page on the App Store."),
			"state": computedOnly("State of the page's version being prepared, or else of its current version, e.g. " +
				"`PREPARE_FOR_SUBMISSION`, `WAITING_FOR_REVIEW` or `APPROVED`."),
			"id": idAttribute("App Store Connect ID of the custom product page."),
		},
	}
}

func (r *productPageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *productPageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// read sets the model from the page and the version the provider reads: the
// one being prepared, or else the current one.
func (r *productPageResource) read(ctx context.Context, m *productPageModel) error {
	out, err := r.client.AppCustomProductPagesGetInstanceWithResponse(ctx, m.ID.ValueString(), &gen.AppCustomProductPagesGetInstanceParams{
		Include: &[]gen.AppCustomProductPagesGetInstanceParamsInclude{gen.AppCustomProductPagesGetInstanceParamsIncludeApp},
	})
	if err := asc.Err(out, err); err != nil {
		return err
	}
	p := out.JSON200.Data
	a := asc.Value(p.Attributes)
	m.Name, m.URL = stringValue(a.Name), stringValue(a.Url)
	m.Visible = types.BoolPointerValue(a.Visible)
	if rel := p.Relationships; rel != nil && rel.App != nil && rel.App.Data != nil {
		m.AppID = types.StringValue(rel.App.Data.Id)
	}
	versions, err := listPageVersions(ctx, r.client, m.ID.ValueString())
	if err != nil {
		return err
	}
	infos := make([]versionInfo, len(versions))
	for i, v := range versions {
		infos[i] = v.versionInfo
	}
	m.State, m.DeepLink = types.StringNull(), types.StringNull()
	if _, current, _ := pickVersions(infos); current != nil {
		for _, v := range versions {
			if v.id == current.id {
				m.State, m.DeepLink = types.StringValue(v.state), stringValue(v.deepLink)
			}
		}
	}
	return nil
}

// Create sends raw JSON to create the page's first version inline, as the
// API's example does
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appcustomproductpages).
func (r *productPageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan productPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const local = "${version}"
	version := map[string]any{"type": "appCustomProductPageVersions", "id": local}
	if link := knownString(plan.DeepLink); link != nil {
		version["attributes"] = map[string]any{"deepLink": *link}
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "appCustomProductPages", "attributes": map[string]any{"name": plan.Name.ValueString()},
		"relationships": map[string]any{
			"app":                          map[string]any{"data": map[string]string{"type": "apps", "id": plan.AppID.ValueString()}},
			"appCustomProductPageVersions": map[string]any{"data": []map[string]string{{"type": "appCustomProductPageVersions", "id": local}}},
		},
	}, "included": []any{version}})
	if err == nil {
		out, e := r.client.AppCustomProductPagesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.ID = types.StringValue(out.JSON201.Data.Id)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create custom product page", err)
		return
	}
	// A new page is visible; hiding it is a second request. The page is in
	// state from here on, so a failure doesn't orphan it.
	resp.Diagnostics.Append(resp.State.Set(ctx, &productPageModel{
		AppID: plan.AppID, Name: plan.Name, Visible: types.BoolValue(true), DeepLink: plan.DeepLink,
		URL: types.StringUnknown(), State: types.StringUnknown(), ID: plan.ID,
	})...)
	if !plan.Visible.IsUnknown() && !plan.Visible.ValueBool() {
		err = r.updatePage(ctx, plan.ID.ValueString(), nil, plan.Visible.ValueBoolPointer())
	}
	if err == nil {
		err = r.read(ctx, &plan)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create custom product page", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *productPageResource) updatePage(ctx context.Context, id string, name *string, visible *bool) error {
	var body gen.AppCustomProductPageUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppCustomProductPageUpdateRequestDataTypeAppCustomProductPages, id
	a := asc.Alloc(&body.Data.Attributes)
	a.Name, a.Visible = name, visible
	out, err := r.client.AppCustomProductPagesUpdateInstanceWithResponse(ctx, id, body)
	return asc.Err(out, err)
}

// setDeepLink sets or clears the deep link of the version being prepared,
// starting the next version when the current one has been reviewed. It sends
// raw JSON so that a removed link is sent as null.
func (r *productPageResource) setDeepLink(ctx context.Context, pageID string, link *string) error {
	versionID, err := productPageMetadata.editable(ctx, r.client, pageID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "appCustomProductPageVersions", "id": versionID, "attributes": map[string]any{"deepLink": link},
	}})
	if err != nil {
		return err
	}
	out, err := r.client.AppCustomProductPageVersionsUpdateInstanceWithBodyWithResponse(ctx, versionID, "application/json", bytes.NewReader(body))
	return asc.Err(out, err)
}

func (r *productPageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state productPageModel
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
		apiError(&resp.Diagnostics, "read custom product page", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *productPageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state productPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := plan.ID.ValueString()
	var err error
	if !plan.Name.Equal(state.Name) || !plan.Visible.IsUnknown() && !plan.Visible.Equal(state.Visible) {
		var visible *bool
		if !plan.Visible.IsUnknown() {
			visible = plan.Visible.ValueBoolPointer()
		}
		err = r.updatePage(ctx, id, plan.Name.ValueStringPointer(), visible)
	}
	if err == nil && !plan.DeepLink.Equal(state.DeepLink) {
		err = r.setDeepLink(ctx, id, knownString(plan.DeepLink))
	}
	if err == nil {
		err = r.read(ctx, &plan)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update custom product page", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *productPageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state productPageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppCustomProductPagesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete custom product page", err)
	}
}

// ---- apple_app_custom_product_page_localization ----

func pageLocalizationValues(l gen.AppCustomProductPageLocalization) localization {
	a := asc.Value(l.Attributes)
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: map[string]*string{"promotionalText": a.PromotionalText}}
}

var newProductPageLocalizationResource = newVersionedLocalizationResource(localizationAPI{
	typeName: "app_custom_product_page_localization",
	description: "Adds a locale to a custom product page, with its promotional text. Use the resource's `id` as the " +
		"`custom_product_page_localization_id` of `apple_app_asset_library_placement` to show screenshots and previews.",
	parent:        "custom_product_page_id",
	parentDesc:    "App Store Connect ID of the custom product page, e.g. `apple_app_custom_product_page.example.id`.",
	versions:      productPageMetadata,
	updateInPlace: true,
	fields: []localizationField{
		{attr: "promotional_text", json: "promotionalText", desc: "Promotional text shown above the description, up to 170 characters.", min: 1, max: 170},
	},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.AppCustomProductPageVersionsAppCustomProductPageLocalizationsGetToManyRelatedWithResponse(ctx, versionID,
			&gen.AppCustomProductPageVersionsAppCustomProductPageLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.AppCustomProductPageLocalizationsResponse) []gen.AppCustomProductPageLocalization {
				return p.Data
			},
			func(p *gen.AppCustomProductPageLocalizationsResponse) *string { return p.Links.Next })
		var ls []localization
		for _, l := range all {
			ls = append(ls, pageLocalizationValues(l))
		}
		return ls, err
	},
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		var body gen.AppCustomProductPageLocalizationCreateRequest
		body.Data.Type = gen.AppCustomProductPageLocalizationCreateRequestDataTypeAppCustomProductPageLocalizations
		body.Data.Attributes.Locale, body.Data.Attributes.PromotionalText = locale, values["promotionalText"]
		body.Data.Relationships.AppCustomProductPageVersion.Data.Id = versionID
		body.Data.Relationships.AppCustomProductPageVersion.Data.Type = gen.AppCustomProductPageLocalizationCreateRequestDataRelationshipsAppCustomProductPageVersionDataTypeAppCustomProductPageVersions
		out, err := c.AppCustomProductPageLocalizationsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		l := pageLocalizationValues(out.JSON201.Data)
		return &l, nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		var body gen.AppCustomProductPageLocalizationUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppCustomProductPageLocalizationUpdateRequestDataTypeAppCustomProductPageLocalizations, id
		asc.Alloc(&body.Data.Attributes).PromotionalText = clearable(values["promotionalText"])
		out, err := c.AppCustomProductPageLocalizationsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		l := pageLocalizationValues(out.JSON200.Data)
		return &l, nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppCustomProductPageLocalizationsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
})

// ---- apple_app_custom_product_page_localization_search_keyword ----

func pageKeywordLinkage(keywordID string) gen.AppCustomProductPageLocalizationSearchKeywordsLinkagesRequest {
	var body gen.AppCustomProductPageLocalizationSearchKeywordsLinkagesRequest
	e := asc.Push(&body.Data)
	e.Id, e.Type = keywordID, gen.AppCustomProductPageLocalizationSearchKeywordsLinkagesRequestDataTypeAppKeywords
	return body
}

var newProductPageKeywordResource = newKeywordLinkResource(keywordLinkAPI{
	typeName: "app_custom_product_page_localization_search_keyword",
	description: "Assigns one search keyword to a custom product page localization, so that people who search for it " +
		"see the custom product page instead of the default one. The keywords come from the app's App Store version; " +
		"list them with `data.apple_app_search_keywords`. The page is searchable once it is approved and visible. " +
		"The localization's ID changes with each version of the page, which replaces this resource. Destroying the " +
		"resource removes the keyword. Import it with `<localization_id>/<keyword_id>`.",
	localizationDoc: "App Store Connect ID of the custom product page localization, e.g. `apple_app_custom_product_page_localization.en_US.id`.",
	add: func(ctx context.Context, c *asc.Client, localizationID, keywordID string) error {
		out, err := c.AppCustomProductPageLocalizationsSearchKeywordsCreateToManyRelationshipWithResponse(ctx, localizationID, pageKeywordLinkage(keywordID))
		return asc.Err(out, err)
	},
	remove: func(ctx context.Context, c *asc.Client, localizationID, keywordID string) error {
		out, err := c.AppCustomProductPageLocalizationsSearchKeywordsDeleteToManyRelationshipWithResponse(ctx, localizationID, pageKeywordLinkage(keywordID))
		return asc.Err(out, err)
	},
	list: func(ctx context.Context, c *asc.Client, localizationID string) ([]string, error) {
		out, err := c.AppCustomProductPageLocalizationsSearchKeywordsGetToManyRelatedWithResponse(ctx, localizationID,
			&gen.AppCustomProductPageLocalizationsSearchKeywordsGetToManyRelatedParams{Limit: new(listAll)})
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
	},
})

// ---- data.apple_app_custom_product_pages ----

type productPageData struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	URL     types.String `tfsdk:"url"`
	Visible types.Bool   `tfsdk:"visible"`
}

type productPagesData struct {
	AppID types.String      `tfsdk:"app_id"`
	Pages []productPageData `tfsdk:"custom_product_pages"`
}

var newProductPagesDataSource = listDataSource("app_custom_product_pages",
	"Lists an app's custom product pages.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"custom_product_pages": listOf("The custom product pages.", map[string]dsschema.Attribute{
			"id":      dsComputed("App Store Connect ID of the custom product page."),
			"name":    dsComputed("Reference name of the page."),
			"url":     dsComputed("URL of the page on the App Store."),
			"visible": dsschema.BoolAttribute{MarkdownDescription: "Whether the page's URL opens the page.", Computed: true},
		}),
	},
	func(ctx context.Context, c *asc.Client, m *productPagesData, diags *diag.Diagnostics) {
		out, err := c.AppsAppCustomProductPagesGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsAppCustomProductPagesGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.AppCustomProductPage
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AppCustomProductPagesResponse) []gen.AppCustomProductPage { return p.Data },
				func(p *gen.AppCustomProductPagesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list custom product pages", err)
			return
		}
		m.Pages = []productPageData{}
		for _, p := range all {
			a := asc.Value(p.Attributes)
			m.Pages = append(m.Pages, productPageData{ID: types.StringValue(p.Id), Name: stringValue(a.Name), URL: stringValue(a.Url), Visible: types.BoolPointerValue(a.Visible)})
		}
	})

// ---- apple_submit_app_custom_product_page ----

type submitProductPageModel struct {
	AppID    types.String `tfsdk:"app_id"`
	PageID   types.String `tfsdk:"custom_product_page_id"`
	Platform types.String `tfsdk:"platform"`
	Submit   types.Bool   `tfsdk:"submit"`
}

var newSubmitProductPageAction = newAction("submit_app_custom_product_page",
	"Submits a custom product page's version being prepared to App Review, by adding it to the platform's review "+
		"submission that is being prepared, or a new one. The version needs a localization. Once approved, the "+
		"version replaces the page's current one.",
	map[string]actionschema.Attribute{
		"app_id":                 actionString("App Store Connect ID of the app."),
		"custom_product_page_id": actionString("App Store Connect ID of the custom product page, e.g. `apple_app_custom_product_page.example.id`."),
		"platform": actionschema.StringAttribute{
			MarkdownDescription: "Platform of the review submission: " + docValues(storePlatforms) + ". Defaults to `IOS`.",
			Optional:            true,
			Validators:          []validator.String{oneOf(storePlatforms)},
		},
		"submit": actionschema.BoolAttribute{
			MarkdownDescription: "Whether to submit the review submission. Set it to `false` to add other items to the same submission. Defaults to `true`.",
			Optional:            true,
		},
	},
	func(ctx context.Context, c *asc.Client, m *submitProductPageModel) (string, error) {
		pageID := m.PageID.ValueString()
		draft, _, _, err := productPageMetadata.find(ctx, c, pageID)
		if err != nil {
			return "", err
		}
		if draft == nil {
			return "", fmt.Errorf("custom product page %s has no version being prepared", pageID)
		}
		platform := gen.PlatformIOS
		if !m.Platform.IsNull() {
			platform = gen.Platform(m.Platform.ValueString())
		}
		submissionID, err := openSubmission(ctx, c, m.AppID.ValueString(), platform)
		if err != nil {
			return "", err
		}
		var item gen.ReviewSubmissionItemCreateRequest
		item.Data.Type = gen.ReviewSubmissionItemCreateRequestDataTypeReviewSubmissionItems
		item.Data.Relationships.ReviewSubmission.Data.Id = submissionID
		item.Data.Relationships.ReviewSubmission.Data.Type = gen.ReviewSubmissionItemCreateRequestDataRelationshipsReviewSubmissionDataTypeReviewSubmissions
		d := asc.Alloc(&asc.Alloc(&item.Data.Relationships.AppCustomProductPageVersion).Data)
		d.Id, d.Type = draft.id, gen.ReviewSubmissionItemCreateRequestDataRelationshipsAppCustomProductPageVersionDataTypeAppCustomProductPageVersions
		out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		if !m.Submit.IsNull() && !m.Submit.ValueBool() {
			return "Added custom product page " + pageID + " to review submission " + submissionID, nil
		}
		return "Submitted custom product page " + pageID + " for review", updateSubmission(ctx, c, submissionID, true)
	})
