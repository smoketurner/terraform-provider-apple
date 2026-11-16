package provider

import (
	"context"
	"strings"

	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Alternative marketplaces and web distribution in the EU: distribution
// domains and keys, an alternative marketplace's search detail, and the
// alternative distribution package of an App Store version
// (https://developer.apple.com/documentation/appstoreconnectapi/alternative-marketplaces-and-web-distribution).

// ---- apple_alternative_distribution_domain ----

var (
	_ resource.ResourceWithConfigure   = &distributionDomainResource{}
	_ resource.ResourceWithImportState = &distributionDomainResource{}
	_ resource.ResourceWithIdentity    = &distributionDomainResource{}
)

type distributionDomainResource struct {
	resourceIdentity
	client *asc.Client
}

type distributionDomainModel struct {
	Domain        types.String `tfsdk:"domain"`
	ReferenceName types.String `tfsdk:"reference_name"`
	CreatedDate   types.String `tfsdk:"created_date"`
	ID            types.String `tfsdk:"id"`
}

func newDistributionDomainResource() resource.Resource {
	return &distributionDomainResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the alternative distribution domain.")}}
}

func (r *distributionDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alternative_distribution_domain"
}

func (r *distributionDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a domain to the account for web distribution in the EU. A domain can't change: " +
			"changing an attribute replaces it. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"domain":         replacedString("The domain, e.g. `example.com`. Changing it creates a new domain."),
			"reference_name": replacedString("Name of the domain in App Store Connect. Changing it creates a new domain."),
			"created_date":   computedString("When the domain was added."),
			"id":             idAttribute("App Store Connect ID of the alternative distribution domain."),
		},
	}
}

func (r *distributionDomainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *distributionDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *distributionDomainModel) apply(d *gen.AlternativeDistributionDomain) {
	a := asc.Value(d.Attributes)
	m.ID, m.Domain, m.ReferenceName, m.CreatedDate = types.StringValue(d.Id), stringValue(a.Domain), stringValue(a.ReferenceName), timeValue(a.CreatedDate)
}

func (r *distributionDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan distributionDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AlternativeDistributionDomainCreateRequest
	body.Data.Type = gen.AlternativeDistributionDomainCreateRequestDataTypeAlternativeDistributionDomains
	body.Data.Attributes.Domain, body.Data.Attributes.ReferenceName = plan.Domain.ValueString(), plan.ReferenceName.ValueString()
	out, err := r.client.AlternativeDistributionDomainsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "add alternative distribution domain", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *distributionDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state distributionDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AlternativeDistributionDomainsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read alternative distribution domain", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every attribute replaces the domain.
func (r *distributionDomainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *distributionDomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state distributionDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AlternativeDistributionDomainsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete alternative distribution domain", err)
	}
}

// ---- apple_alternative_distribution_key ----

var (
	_ resource.ResourceWithConfigure   = &distributionKeyResource{}
	_ resource.ResourceWithImportState = &distributionKeyResource{}
	_ resource.ResourceWithIdentity    = &distributionKeyResource{}
)

type distributionKeyResource struct {
	resourceIdentity
	client *asc.Client
}

type distributionKeyModel struct {
	PublicKey types.String `tfsdk:"public_key"`
	AppID     types.String `tfsdk:"app_id"`
	ID        types.String `tfsdk:"id"`
}

func newDistributionKeyResource() resource.Resource {
	return &distributionKeyResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the alternative distribution key.")}}
}

func (r *distributionKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alternative_distribution_key"
}

func (r *distributionKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds the public half of an alternative distribution key, which an alternative marketplace " +
			"or web distribution uses to verify installs. Without `app_id` the key applies to every alternative " +
			"distribution app of the account; with it, to one app. A key can't change: changing an attribute replaces " +
			"it. Import it by ID, or with `<app_id>/<id>` for an app's key, because App Store Connect doesn't report " +
			"the app of a key.",
		Attributes: map[string]schema.Attribute{
			"public_key": replacedString("The public key. Changing it creates a new key."),
			"app_id": replacedOptionalString("App Store Connect ID of the app the key is for. Unset means every app of " +
				"the account. Changing it creates a new key."),
			"id": idAttribute("App Store Connect ID of the alternative distribution key, which install verification tokens name."),
		},
	}
}

func (r *distributionKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

// ImportState also takes "<app_id>/<id>", which records the app of an
// app's key.
func (r *distributionKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	appID, id, ok := strings.Cut(req.ID, "/")
	if ok {
		req.ID = id
	}
	r.importState(ctx, r.client, req, resp)
	if ok && !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), appID)...)
	}
}

func (r *distributionKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan distributionKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AlternativeDistributionKeyCreateRequest
	body.Data.Type, body.Data.Attributes.PublicKey = gen.AlternativeDistributionKeyCreateRequestDataTypeAlternativeDistributionKeys, plan.PublicKey.ValueString()
	if appID := plan.AppID.ValueString(); appID != "" {
		app := asc.Alloc(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).App).Data)
		app.Id, app.Type = appID, gen.AlternativeDistributionKeyCreateRequestDataRelationshipsAppDataTypeApps
	}
	out, err := r.client.AlternativeDistributionKeysCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "add alternative distribution key", err)
		return
	}
	plan.ID = types.StringValue(out.JSON201.Data.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read keeps the configured public key, which can't change, so that a key
// App Store Connect returns in another form isn't read as a change.
func (r *distributionKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state distributionKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AlternativeDistributionKeysGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read alternative distribution key", err)
		return
	}
	if state.PublicKey.IsNull() {
		state.PublicKey = stringValue(asc.Value(out.JSON200.Data.Attributes).PublicKey)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every attribute replaces the key.
func (r *distributionKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *distributionKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state distributionKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AlternativeDistributionKeysDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete alternative distribution key", err)
	}
}

// ---- apple_marketplace_search_detail ----

var (
	_ resource.ResourceWithConfigure   = &searchDetailResource{}
	_ resource.ResourceWithImportState = &searchDetailResource{}
	_ resource.ResourceWithIdentity    = &searchDetailResource{}
)

type searchDetailResource struct {
	resourceIdentity
	client *asc.Client
}

type searchDetailModel struct {
	AppID      types.String `tfsdk:"app_id"`
	CatalogURL types.String `tfsdk:"catalog_url"`
	ID         types.String `tfsdk:"id"`
}

func newSearchDetailResource() resource.Resource {
	return &searchDetailResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the alternative marketplace app.", state: "app_id"},
	}}}
}

func (r *searchDetailResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_marketplace_search_detail"
}

func (r *searchDetailResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the catalog URL of an alternative marketplace app, which marketplace search uses to " +
			"find the apps the marketplace offers. An app has one search detail. Import it by the app's ID.",
		Attributes: map[string]schema.Attribute{
			"app_id":      replacedString("App Store Connect ID of the alternative marketplace app."),
			"catalog_url": schema.StringAttribute{MarkdownDescription: "URL of the marketplace's catalog, e.g. `https://example.com/catalog`.", Required: true},
			"id":          idAttribute("App Store Connect ID of the marketplace search detail."),
		},
	}
}

func (r *searchDetailResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *searchDetailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *searchDetailModel) apply(d *gen.MarketplaceSearchDetail) {
	m.ID, m.CatalogURL = types.StringValue(d.Id), stringValue(asc.Value(d.Attributes).CatalogUrl)
}

func (r *searchDetailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan searchDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.MarketplaceSearchDetailCreateRequest
	body.Data.Type, body.Data.Attributes.CatalogUrl = gen.MarketplaceSearchDetailCreateRequestDataTypeMarketplaceSearchDetails, plan.CatalogURL.ValueString()
	app := &body.Data.Relationships.App.Data
	app.Id, app.Type = plan.AppID.ValueString(), gen.MarketplaceSearchDetailCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.MarketplaceSearchDetailsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "add marketplace search detail", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read finds the detail through its app: the API can't read one by ID.
func (r *searchDetailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state searchDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppsMarketplaceSearchDetailGetToOneRelatedWithResponse(ctx, state.AppID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read marketplace search detail", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *searchDetailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan searchDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.MarketplaceSearchDetailUpdateRequest
	body.Data.Type, body.Data.Id = gen.MarketplaceSearchDetailUpdateRequestDataTypeMarketplaceSearchDetails, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).CatalogUrl = new(plan.CatalogURL.ValueString())
	out, err := r.client.MarketplaceSearchDetailsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update marketplace search detail", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *searchDetailResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state searchDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.MarketplaceSearchDetailsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete marketplace search detail", err)
	}
}

// ---- apple_alternative_distribution_package ----

var (
	_ resource.ResourceWithConfigure   = &distributionPackageResource{}
	_ resource.ResourceWithImportState = &distributionPackageResource{}
	_ resource.ResourceWithIdentity    = &distributionPackageResource{}
)

type distributionPackageResource struct {
	resourceIdentity
	client *asc.Client
}

type distributionPackageModel struct {
	VersionID types.String `tfsdk:"app_store_version_id"`
	ID        types.String `tfsdk:"id"`
}

func newDistributionPackageResource() resource.Resource {
	return &distributionPackageResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_store_version_id", description: "App Store Connect ID of the App Store version.", state: "app_store_version_id"},
	}}}
}

func (r *distributionPackageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alternative_distribution_package"
}

func (r *distributionPackageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates the alternative distribution package of an App Store version, which an " +
			"alternative marketplace or a website downloads to distribute the app in the EU. A version has one " +
			"package: the resource creates it, or manages the existing one. Read its downloads with " +
			"`data.apple_alternative_distribution_package_versions`. App Store Connect can't delete a package, so " +
			"destroying the resource leaves it. Import it by the App Store version's ID.",
		Attributes: map[string]schema.Attribute{
			"app_store_version_id": replacedString("App Store Connect ID of the App Store version, e.g. " +
				"`apple_app_store_version.example.id`. Changing it creates a package for the other version."),
			"id": idAttribute("App Store Connect ID of the alternative distribution package."),
		},
	}
}

func (r *distributionPackageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *distributionPackageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// existing returns the ID of the version's package.
func (r *distributionPackageResource) existing(ctx context.Context, versionID string) (string, error) {
	out, err := r.client.AppStoreVersionsAlternativeDistributionPackageGetToOneRelatedWithResponse(ctx, versionID, nil)
	if err := asc.Err(out, err); err != nil {
		return "", err
	}
	return out.JSON200.Data.Id, nil
}

// Create creates the version's package unless it has one.
func (r *distributionPackageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan distributionPackageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.existing(ctx, plan.VersionID.ValueString())
	if asc.IsNotFound(err) {
		var body gen.AlternativeDistributionPackageCreateRequest
		body.Data.Type = gen.AlternativeDistributionPackageCreateRequestDataTypeAlternativeDistributionPackages
		v := &body.Data.Relationships.AppStoreVersion.Data
		v.Id, v.Type = plan.VersionID.ValueString(), gen.AlternativeDistributionPackageCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		out, cerr := r.client.AlternativeDistributionPackagesCreateInstanceWithResponse(ctx, body)
		if err = asc.Err(out, cerr); err == nil {
			id = out.JSON201.Data.Id
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create alternative distribution package", err)
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *distributionPackageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state distributionPackageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.existing(ctx, state.VersionID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read alternative distribution package", err)
		return
	}
	state.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: the version replaces the package.
func (r *distributionPackageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

// Delete only forgets the package: the API has no way to delete one.
func (r *distributionPackageResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- data.apple_alternative_distribution_domains ----

type distributionDomainsData struct {
	Domains []distributionDomainModel `tfsdk:"domains"`
}

var newDistributionDomainsDataSource = listDataSource("alternative_distribution_domains",
	"Lists the account's alternative distribution domains.",
	map[string]dsschema.Attribute{
		"domains": listOf("The domains.", map[string]dsschema.Attribute{
			"id":             dsComputed("App Store Connect ID of the domain."),
			"domain":         dsComputed("The domain."),
			"reference_name": dsComputed("Name of the domain in App Store Connect."),
			"created_date":   dsComputed("When the domain was added."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *distributionDomainsData, diags *diag.Diagnostics) {
		out, err := c.AlternativeDistributionDomainsGetCollectionWithResponse(ctx, &gen.AlternativeDistributionDomainsGetCollectionParams{Limit: new(listAll)})
		var all []gen.AlternativeDistributionDomain
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AlternativeDistributionDomainsResponse) []gen.AlternativeDistributionDomain { return p.Data },
				func(p *gen.AlternativeDistributionDomainsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list alternative distribution domains", err)
			return
		}
		m.Domains = []distributionDomainModel{}
		for _, d := range all {
			var item distributionDomainModel
			item.apply(&d)
			m.Domains = append(m.Domains, item)
		}
	})

// ---- data.apple_alternative_distribution_keys and data.apple_alternative_distribution_key ----

type distributionKeyData struct {
	ID        types.String `tfsdk:"id"`
	PublicKey types.String `tfsdk:"public_key"`
}

type distributionKeysData struct {
	AppSpecific types.Bool            `tfsdk:"app_specific"`
	Keys        []distributionKeyData `tfsdk:"keys"`
}

func distributionKeyValue(k *gen.AlternativeDistributionKey) distributionKeyData {
	return distributionKeyData{ID: types.StringValue(k.Id), PublicKey: stringValue(asc.Value(k.Attributes).PublicKey)}
}

var newDistributionKeysDataSource = listDataSource("alternative_distribution_keys",
	"Lists the account's alternative distribution keys.",
	map[string]dsschema.Attribute{
		"app_specific": dsschema.BoolAttribute{
			MarkdownDescription: "`true` lists only the keys of single apps, `false` only the account's key. Unset lists every key.",
			Optional:            true,
		},
		"keys": listOf("The keys.", map[string]dsschema.Attribute{
			"id":         dsComputed("App Store Connect ID of the key."),
			"public_key": dsComputed("The public key."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *distributionKeysData, diags *diag.Diagnostics) {
		out, err := c.AlternativeDistributionKeysGetCollectionWithResponse(ctx, &gen.AlternativeDistributionKeysGetCollectionParams{
			ExistsApp: m.AppSpecific.ValueBoolPointer(), Limit: new(listAll),
		})
		var all []gen.AlternativeDistributionKey
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AlternativeDistributionKeysResponse) []gen.AlternativeDistributionKey { return p.Data },
				func(p *gen.AlternativeDistributionKeysResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list alternative distribution keys", err)
			return
		}
		m.Keys = []distributionKeyData{}
		for _, k := range all {
			m.Keys = append(m.Keys, distributionKeyValue(&k))
		}
	})

type appDistributionKeyData struct {
	AppID types.String `tfsdk:"app_id"`
	distributionKeyData
}

var newDistributionKeyDataSource = listDataSource("alternative_distribution_key",
	"Reads the alternative distribution key of an app. The key's ID is part of install verification tokens.",
	map[string]dsschema.Attribute{
		"app_id":     dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"id":         dsComputed("App Store Connect ID of the key."),
		"public_key": dsComputed("The public key."),
	},
	func(ctx context.Context, c *asc.Client, m *appDistributionKeyData, diags *diag.Diagnostics) {
		out, err := c.AppsAlternativeDistributionKeyGetToOneRelatedWithResponse(ctx, m.AppID.ValueString(), nil)
		if err := asc.Err(out, err); err != nil {
			lookupError(diags, "alternative distribution key", err)
			return
		}
		m.distributionKeyData = distributionKeyValue(&out.JSON200.Data)
	})

// ---- data.apple_alternative_distribution_package_versions ----

type packageDownloadData struct {
	ID                types.String `tfsdk:"id"`
	URL               types.String `tfsdk:"url"`
	URLExpirationDate types.String `tfsdk:"url_expiration_date"`
	FileChecksum      types.String `tfsdk:"file_checksum"`
	KeyBlob           types.String `tfsdk:"alternative_distribution_key_blob"`
}

type packageVersionData struct {
	ID                types.String `tfsdk:"id"`
	Version           types.String `tfsdk:"version"`
	State             types.String `tfsdk:"state"`
	URL               types.String `tfsdk:"url"`
	URLExpirationDate types.String `tfsdk:"url_expiration_date"`
	FileChecksum      types.String `tfsdk:"file_checksum"`
}

func (m *packageVersionData) apply(v *gen.AlternativeDistributionPackageVersion) {
	a := asc.Value(v.Attributes)
	m.ID, m.Version, m.State = types.StringValue(v.Id), stringValue(a.Version), stringValue(a.State)
	m.URL, m.URLExpirationDate, m.FileChecksum = stringValue(a.Url), timeValue(a.UrlExpirationDate), stringValue(a.FileChecksum)
}

var packageVersionStates = enum(gen.AlternativeDistributionPackageVersionAttributesStateCOMPLETED, gen.AlternativeDistributionPackageVersionAttributesStateREPLACED)

func packageVersionAttributes(id dsschema.Attribute) map[string]dsschema.Attribute {
	return map[string]dsschema.Attribute{
		"id":                  id,
		"version":             dsComputed("Version of the package."),
		"state":               dsComputed("State of the version: " + docValues(packageVersionStates) + "."),
		"url":                 dsSensitive("URL the package downloads from until `url_expiration_date`."),
		"url_expiration_date": dsComputed("When `url` stops working. Read the data source again for a new URL."),
		"file_checksum":       dsComputed("SHA-256 checksum of the package."),
	}
}

type packageVersionsData struct {
	PackageID types.String         `tfsdk:"package_id"`
	States    types.Set            `tfsdk:"states"`
	Versions  []packageVersionData `tfsdk:"versions"`
}

var newPackageVersionsDataSource = listDataSource("alternative_distribution_package_versions",
	"Lists the versions of an alternative distribution package, with the URLs they download from. Apple adds a "+
		"version after it builds the package; the earlier ones become `REPLACED`.",
	map[string]dsschema.Attribute{
		"package_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of the package, e.g. `apple_alternative_distribution_package.example.id`.", Required: true,
		},
		"states":   dsFilter("States to list.", packageVersionStates...),
		"versions": listOf("The versions.", packageVersionAttributes(dsComputed("App Store Connect ID of the package version."))),
	},
	func(ctx context.Context, c *asc.Client, m *packageVersionsData, diags *diag.Diagnostics) {
		params := &gen.AlternativeDistributionPackagesVersionsGetToManyRelatedParams{
			FilterState: filterParam[gen.AlternativeDistributionPackagesVersionsGetToManyRelatedParamsFilterState](ctx, m.States, diags), Limit: new(listAll),
		}
		out, err := c.AlternativeDistributionPackagesVersionsGetToManyRelatedWithResponse(ctx, m.PackageID.ValueString(), params)
		var all []gen.AlternativeDistributionPackageVersion
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AlternativeDistributionPackageVersionsResponse) []gen.AlternativeDistributionPackageVersion {
					return p.Data
				},
				func(p *gen.AlternativeDistributionPackageVersionsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list alternative distribution package versions", err)
			return
		}
		m.Versions = []packageVersionData{}
		for _, v := range all {
			if !passes(ctx, m.States, string(asc.Value(asc.Value(v.Attributes).State)), diags) {
				continue
			}
			var item packageVersionData
			item.apply(&v)
			m.Versions = append(m.Versions, item)
		}
	})

// ---- data.apple_alternative_distribution_package_version ----

type packageVersionDetailData struct {
	packageVersionData
	Variants []packageDownloadData `tfsdk:"variants"`
	Deltas   []packageDownloadData `tfsdk:"deltas"`
}

func packageDownloads(desc string) dsschema.ListNestedAttribute {
	return listOf(desc, map[string]dsschema.Attribute{
		"id":                                dsComputed("App Store Connect ID of the download."),
		"url":                               dsSensitive("URL the file downloads from until `url_expiration_date`."),
		"url_expiration_date":               dsComputed("When `url` stops working."),
		"file_checksum":                     dsComputed("SHA-256 checksum of the file."),
		"alternative_distribution_key_blob": dsSensitive("Key blob of the file, encrypted for the alternative distribution key."),
	})
}

var newPackageVersionDataSource = listDataSource("alternative_distribution_package_version",
	"Reads one version of an alternative distribution package with its variants, which are the package thinned for "+
		"device types, and its deltas, which update an earlier version.",
	withAttributes(packageVersionAttributes(dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the package version.", Required: true}),
		map[string]dsschema.Attribute{
			"variants": packageDownloads("The variants of the version."),
			"deltas":   packageDownloads("The deltas of the version."),
		}),
	func(ctx context.Context, c *asc.Client, m *packageVersionDetailData, diags *diag.Diagnostics) {
		id := m.ID.ValueString()
		out, err := c.AlternativeDistributionPackageVersionsGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			lookupError(diags, "alternative distribution package version", err)
			return
		}
		m.apply(&out.JSON200.Data)
		variants, err := c.AlternativeDistributionPackageVersionsVariantsGetToManyRelatedWithResponse(ctx, id,
			&gen.AlternativeDistributionPackageVersionsVariantsGetToManyRelatedParams{Limit: new(listAll)})
		var allVariants []gen.AlternativeDistributionPackageVariant
		if err = asc.Err(variants, err); err == nil {
			allVariants, err = asc.All(ctx, c, variants.JSON200,
				func(p *gen.AlternativeDistributionPackageVariantsResponse) []gen.AlternativeDistributionPackageVariant {
					return p.Data
				},
				func(p *gen.AlternativeDistributionPackageVariantsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list alternative distribution package variants", err)
			return
		}
		deltas, err := c.AlternativeDistributionPackageVersionsDeltasGetToManyRelatedWithResponse(ctx, id,
			&gen.AlternativeDistributionPackageVersionsDeltasGetToManyRelatedParams{Limit: new(listAll)})
		var allDeltas []gen.AlternativeDistributionPackageDelta
		if err = asc.Err(deltas, err); err == nil {
			allDeltas, err = asc.All(ctx, c, deltas.JSON200,
				func(p *gen.AlternativeDistributionPackageDeltasResponse) []gen.AlternativeDistributionPackageDelta {
					return p.Data
				},
				func(p *gen.AlternativeDistributionPackageDeltasResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list alternative distribution package deltas", err)
			return
		}
		m.Variants, m.Deltas = []packageDownloadData{}, []packageDownloadData{}
		for _, v := range allVariants {
			a := asc.Value(v.Attributes)
			m.Variants = append(m.Variants, packageDownloadData{
				ID: types.StringValue(v.Id), URL: stringValue(a.Url), URLExpirationDate: timeValue(a.UrlExpirationDate),
				FileChecksum: stringValue(a.FileChecksum), KeyBlob: stringValue(a.AlternativeDistributionKeyBlob),
			})
		}
		for _, d := range allDeltas {
			a := asc.Value(d.Attributes)
			m.Deltas = append(m.Deltas, packageDownloadData{
				ID: types.StringValue(d.Id), URL: stringValue(a.Url), URLExpirationDate: timeValue(a.UrlExpirationDate),
				FileChecksum: stringValue(a.FileChecksum), KeyBlob: stringValue(a.AlternativeDistributionKeyBlob),
			})
		}
	})
