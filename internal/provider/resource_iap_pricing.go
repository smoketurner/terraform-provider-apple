package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// In-app purchase pricing and availability, and sandbox testers.

var iapPriceScheduleAPI = priceScheduleAPI{
	typeName: "in_app_purchase_price_schedule",
	description: "Sets an in-app purchase's price: a price point in a base territory, from which App Store Connect " +
		"sets equivalent prices in the other territories, plus optional manual prices for other territories and " +
		"scheduled price changes. Find price points with `data.apple_in_app_purchase_price_points`.",
	parent:     "in_app_purchase_id",
	parentDesc: "App Store Connect ID of the in-app purchase, e.g. `apple_in_app_purchase.example.id`.",
	write: func(ctx context.Context, c *asc.Client, iapID, base string, prices []manualPriceModel) (string, error) {
		var body gen.InAppPurchasePriceScheduleCreateRequest
		body.Data.Type = gen.InAppPurchasePriceScheduleCreateRequestDataTypeInAppPurchasePriceSchedules
		rel := &body.Data.Relationships
		rel.InAppPurchase.Data.Id, rel.InAppPurchase.Data.Type = iapID, gen.InAppPurchasePriceScheduleCreateRequestDataRelationshipsInAppPurchaseDataTypeInAppPurchases
		rel.BaseTerritory.Data.Id, rel.BaseTerritory.Data.Type = base, gen.InAppPurchasePriceScheduleCreateRequestDataRelationshipsBaseTerritoryDataTypeTerritories
		rel.ManualPrices.Data = nil
		for i, p := range prices {
			local := localPriceID(i)
			ref := asc.Push(&rel.ManualPrices.Data)
			ref.Id, ref.Type = local, gen.InAppPurchasePriceScheduleCreateRequestDataRelationshipsManualPricesDataTypeInAppPurchasePrices
			inline := gen.InAppPurchasePriceInlineCreate{Id: new(local), Type: gen.InAppPurchasePriceInlineCreateTypeInAppPurchasePrices}
			a := asc.Alloc(&inline.Attributes)
			a.StartDate, a.EndDate = parseDate(p.StartDate), parseDate(p.EndDate)
			point := asc.Alloc(&asc.Alloc(&asc.Alloc(&inline.Relationships).InAppPurchasePricePoint).Data)
			point.Id, point.Type = p.PricePointID.ValueString(), gen.InAppPurchasePriceInlineCreateRelationshipsInAppPurchasePricePointDataTypeInAppPurchasePricePoints
			var item gen.InAppPurchasePriceScheduleCreateRequest_Included_Item
			if err := item.FromInAppPurchasePriceInlineCreate(inline); err != nil {
				return "", err
			}
			*asc.Push(asc.Alloc(&body.Included)) = item
		}
		out, err := c.InAppPurchasePriceSchedulesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
	read: func(ctx context.Context, c *asc.Client, iapID string) (string, string, []manualPriceModel, error) {
		sched, err := c.InAppPurchasesV2IapPriceScheduleGetToOneRelatedWithResponse(ctx, iapID, nil)
		if err := asc.Err(sched, err); err != nil {
			return "", "", nil, err
		}
		id := sched.JSON200.Data.Id
		base, err := c.InAppPurchasePriceSchedulesBaseTerritoryGetToOneRelatedWithResponse(ctx, id, nil)
		if err := asc.Err(base, err); err != nil {
			return "", "", nil, err
		}
		out, err := c.InAppPurchasePriceSchedulesManualPricesGetToManyRelatedWithResponse(ctx, id, &gen.InAppPurchasePriceSchedulesManualPricesGetToManyRelatedParams{
			Include: &[]gen.InAppPurchasePriceSchedulesManualPricesGetToManyRelatedParamsInclude{gen.InAppPurchasePriceSchedulesManualPricesGetToManyRelatedParamsIncludeInAppPurchasePricePoint},
			Limit:   new(listAll),
		})
		if err := asc.Err(out, err); err != nil {
			return "", "", nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.InAppPurchasePricesResponse) []gen.InAppPurchasePrice { return p.Data },
			func(p *gen.InAppPurchasePricesResponse) *string { return p.Links.Next })
		if err != nil {
			return "", "", nil, err
		}
		prices := []manualPriceModel{}
		for _, p := range all {
			a := asc.Value(p.Attributes)
			if rel := p.Relationships; rel != nil && rel.InAppPurchasePricePoint != nil && rel.InAppPurchasePricePoint.Data != nil {
				prices = append(prices, manualPriceModel{PricePointID: types.StringValue(rel.InAppPurchasePricePoint.Data.Id), StartDate: dateValue(a.StartDate), EndDate: dateValue(a.EndDate)})
			}
		}
		return id, base.JSON200.Data.Id, prices, nil
	},
}

var newIAPPriceScheduleResource = newPriceScheduleResource(iapPriceScheduleAPI)

// ---- price point data sources ----

func iapPricePointItems(data []gen.InAppPurchasePricePoint) []pricePointData {
	var items []pricePointData
	for _, p := range data {
		a := asc.Value(p.Attributes)
		item := pricePointData{ID: types.StringValue(p.Id), Territory: types.StringNull(), CustomerPrice: stringValue(a.CustomerPrice), Proceeds: stringValue(a.Proceeds)}
		if rel := p.Relationships; rel != nil && rel.Territory != nil && rel.Territory.Data != nil {
			item.Territory = types.StringValue(rel.Territory.Data.Id)
		}
		items = append(items, item)
	}
	return items
}

func allIAPPricePoints(ctx context.Context, c *asc.Client, first *gen.InAppPurchasePricePointsResponse) ([]gen.InAppPurchasePricePoint, error) {
	return asc.All(ctx, c, first,
		func(p *gen.InAppPurchasePricePointsResponse) []gen.InAppPurchasePricePoint { return p.Data },
		func(p *gen.InAppPurchasePricePointsResponse) *string { return p.Links.Next })
}

type iapPricePointsData struct {
	IAPID          types.String     `tfsdk:"in_app_purchase_id"`
	Territories    types.Set        `tfsdk:"territories"`
	CustomerPrices types.Set        `tfsdk:"customer_prices"`
	PricePoints    []pricePointData `tfsdk:"price_points"`
}

var newIAPPricePointsDataSource = listDataSource("in_app_purchase_price_points",
	"Lists the price points an in-app purchase can be sold at, per territory, for `apple_in_app_purchase_price_schedule`.",
	map[string]dsschema.Attribute{
		"in_app_purchase_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the in-app purchase.", Required: true},
		"territories":        dsFilter("Only price points in these territories, e.g. `USA`."),
		"customer_prices":    dsFilter("Only price points with these customer prices, e.g. `0.99`."),
		"price_points":       listOf("The price points.", pricePointAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *iapPricePointsData, diags *diag.Diagnostics) {
		out, err := c.InAppPurchasesV2PricePointsGetToManyRelatedWithResponse(ctx, m.IAPID.ValueString(), &gen.InAppPurchasesV2PricePointsGetToManyRelatedParams{
			FilterTerritory: filterParam[string](ctx, m.Territories, diags),
			Include:         &[]gen.InAppPurchasesV2PricePointsGetToManyRelatedParamsInclude{gen.InAppPurchasesV2PricePointsGetToManyRelatedParamsIncludeTerritory},
			Limit:           new(listAll),
		})
		var all []gen.InAppPurchasePricePoint
		if err = asc.Err(out, err); err == nil {
			all, err = allIAPPricePoints(ctx, c, out.JSON200)
		}
		if err != nil {
			apiError(diags, "list in-app purchase price points", err)
			return
		}
		filterPricePoints(ctx, &m.PricePoints, m.CustomerPrices, iapPricePointItems(all), diags)
	})

var newIAPPricePointEqualizationsDataSource = listDataSource("in_app_purchase_price_point_equalizations",
	"Lists the price points in other territories that are equivalent to an in-app purchase price point: the prices "+
		"App Store Connect sets automatically in territories without a manual price.",
	map[string]dsschema.Attribute{
		"price_point_id": dsschema.StringAttribute{MarkdownDescription: "ID of the price point.", Required: true},
		"territories":    dsFilter("Only equivalents in these territories, e.g. `GBR`."),
		"price_points":   listOf("The equivalent price points.", pricePointAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *equalizationsData, diags *diag.Diagnostics) {
		out, err := c.InAppPurchasePricePointsEqualizationsGetToManyRelatedWithResponse(ctx, m.PricePointID.ValueString(), &gen.InAppPurchasePricePointsEqualizationsGetToManyRelatedParams{
			FilterTerritory: filterParam[string](ctx, m.Territories, diags),
			Include:         &[]gen.InAppPurchasePricePointsEqualizationsGetToManyRelatedParamsInclude{gen.InAppPurchasePricePointsEqualizationsGetToManyRelatedParamsIncludeTerritory},
			Limit:           new(listAll),
		})
		var all []gen.InAppPurchasePricePoint
		if err = asc.Err(out, err); err == nil {
			all, err = allIAPPricePoints(ctx, c, out.JSON200)
		}
		if err != nil {
			apiError(diags, "list price point equalizations", err)
			return
		}
		filterPricePoints(ctx, &m.PricePoints, types.SetNull(types.StringType), iapPricePointItems(all), diags)
	})

// ---- apple_in_app_purchase_availability ----

var (
	_ resource.ResourceWithConfigure   = &iapAvailabilityResource{}
	_ resource.ResourceWithImportState = &iapAvailabilityResource{}
	_ resource.ResourceWithIdentity    = &iapAvailabilityResource{}
)

type iapAvailabilityResource struct {
	resourceIdentity
	client *asc.Client
}

type iapAvailabilityModel struct {
	IAPID                     types.String `tfsdk:"in_app_purchase_id"`
	AvailableInNewTerritories types.Bool   `tfsdk:"available_in_new_territories"`
	Territories               types.Set    `tfsdk:"territories"`
	ID                        types.String `tfsdk:"id"`
}

func newIAPAvailabilityResource() resource.Resource {
	return &iapAvailabilityResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "in_app_purchase_id", description: "App Store Connect ID of the in-app purchase.", state: "in_app_purchase_id"},
	}}}
}

func (r *iapAvailabilityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_in_app_purchase_availability"
}

func (r *iapAvailabilityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the territories where an in-app purchase is sold. App Store Connect sets the whole " +
			"list at once, so every change replaces it. App Store Connect can't delete an availability, so destroying " +
			"the resource leaves it. Import it by in-app purchase ID.",
		Attributes: map[string]schema.Attribute{
			"in_app_purchase_id": replacedString("App Store Connect ID of the in-app purchase, e.g. `apple_in_app_purchase.example.id`."),
			"available_in_new_territories": schema.BoolAttribute{
				MarkdownDescription: "Whether the purchase becomes available in territories the App Store adds later.",
				Required:            true,
			},
			"territories": schema.SetAttribute{
				MarkdownDescription: "Territories where the purchase is sold, e.g. `USA`; see `data.apple_territories`.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.Set{setvalidator.ValueStringsAre(territoryValidator)},
			},
			"id": idAttribute("App Store Connect ID of the availability."),
		},
	}
}

func (r *iapAvailabilityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *iapAvailabilityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *iapAvailabilityResource) write(ctx context.Context, m *iapAvailabilityModel, diags *diag.Diagnostics) error {
	var body gen.InAppPurchaseAvailabilityCreateRequest
	body.Data.Type = gen.InAppPurchaseAvailabilityCreateRequestDataTypeInAppPurchaseAvailabilities
	body.Data.Attributes.AvailableInNewTerritories = m.AvailableInNewTerritories.ValueBool()
	body.Data.Relationships.InAppPurchase.Data.Id = m.IAPID.ValueString()
	body.Data.Relationships.InAppPurchase.Data.Type = gen.InAppPurchaseAvailabilityCreateRequestDataRelationshipsInAppPurchaseDataTypeInAppPurchases
	// The API needs an array, even an empty one.
	terr := &body.Data.Relationships.AvailableTerritories.Data
	asc.Push(terr)
	*terr = (*terr)[:0]
	for _, t := range setStrings(ctx, m.Territories, diags) {
		e := asc.Push(terr)
		e.Id, e.Type = t, gen.InAppPurchaseAvailabilityCreateRequestDataRelationshipsAvailableTerritoriesDataTypeTerritories
	}
	out, err := r.client.InAppPurchaseAvailabilitiesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	m.ID = types.StringValue(out.JSON201.Data.Id)
	return nil
}

func (r *iapAvailabilityResource) read(ctx context.Context, m *iapAvailabilityModel) error {
	av, err := r.client.InAppPurchasesV2InAppPurchaseAvailabilityGetToOneRelatedWithResponse(ctx, m.IAPID.ValueString(), nil)
	if err := asc.Err(av, err); err != nil {
		return err
	}
	out, err := r.client.InAppPurchaseAvailabilitiesAvailableTerritoriesGetToManyRelatedWithResponse(ctx, av.JSON200.Data.Id,
		&gen.InAppPurchaseAvailabilitiesAvailableTerritoriesGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return err
	}
	all, err := asc.All(ctx, r.client, out.JSON200,
		func(p *gen.TerritoriesResponse) []gen.Territory { return p.Data },
		func(p *gen.TerritoriesResponse) *string { return p.Links.Next })
	if err != nil {
		return err
	}
	var territories []string
	for _, t := range all {
		territories = append(territories, t.Id)
	}
	m.ID, m.Territories = types.StringValue(av.JSON200.Data.Id), stringsValue(territories)
	m.AvailableInNewTerritories = types.BoolValue(asc.Value(asc.Value(av.JSON200.Data.Attributes).AvailableInNewTerritories))
	return nil
}

func (r *iapAvailabilityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan iapAvailabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "set in-app purchase availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iapAvailabilityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state iapAvailabilityModel
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
		apiError(&resp.Diagnostics, "read in-app purchase availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *iapAvailabilityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan iapAvailabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "set in-app purchase availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the availability, which the API cannot delete.
func (r *iapAvailabilityResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_sandbox_tester ----

var renewalRates = enum(gen.SandboxTesterV2AttributesSubscriptionRenewalRateMONTHLYRENEWALEVERYONEHOUR,
	gen.SandboxTesterV2AttributesSubscriptionRenewalRateMONTHLYRENEWALEVERYTHIRTYMINUTES,
	gen.SandboxTesterV2AttributesSubscriptionRenewalRateMONTHLYRENEWALEVERYFIFTEENMINUTES,
	gen.SandboxTesterV2AttributesSubscriptionRenewalRateMONTHLYRENEWALEVERYFIVEMINUTES,
	gen.SandboxTesterV2AttributesSubscriptionRenewalRateMONTHLYRENEWALEVERYTHREEMINUTES)

var (
	_ resource.ResourceWithConfigure   = &sandboxTesterResource{}
	_ resource.ResourceWithImportState = &sandboxTesterResource{}
	_ resource.ResourceWithIdentity    = &sandboxTesterResource{}
)

type sandboxTesterResource struct {
	resourceIdentity
	client *asc.Client
}

type sandboxTesterModel struct {
	Email                   types.String `tfsdk:"email"`
	Territory               types.String `tfsdk:"territory"`
	InterruptPurchases      types.Bool   `tfsdk:"interrupt_purchases"`
	SubscriptionRenewalRate types.String `tfsdk:"subscription_renewal_rate"`
	ID                      types.String `tfsdk:"id"`
	FirstName               types.String `tfsdk:"first_name"`
	LastName                types.String `tfsdk:"last_name"`
	ApplePayCompatible      types.Bool   `tfsdk:"apple_pay_compatible"`
}

func newSandboxTesterResource() resource.Resource {
	return &sandboxTesterResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "email", description: "Email address of the sandbox Apple Account.", state: "email"},
	}}}
}

func (r *sandboxTesterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox_tester"
}

func (r *sandboxTesterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a sandbox tester's storefront and purchase testing options. Sandbox Apple Accounts " +
			"are created in App Store Connect, so this resource manages an existing tester by email and fails if there " +
			"is none; destroying it leaves the tester. Clear its purchase history with " +
			"`apple_clear_sandbox_purchase_history`. Import it by email.",
		Attributes: map[string]schema.Attribute{
			"email":                     replacedString("Email address of the sandbox Apple Account.", stringvalidator.RegexMatches(emailRegexp, "must be an email address")),
			"territory":                 optionalComputedString("Storefront of the tester, e.g. `USA`.", territoryValidator),
			"interrupt_purchases":       optionalBool("Whether purchases are interrupted, to test interrupted purchase flows."),
			"subscription_renewal_rate": optionalComputedString("How fast monthly subscriptions renew: "+docValues(renewalRates)+".", oneOf(renewalRates)),
			"id":                        idAttribute("App Store Connect ID of the sandbox tester."),
			"first_name":                computedString("First name of the tester."),
			"last_name":                 computedString("Last name of the tester."),
			"apple_pay_compatible":      schema.BoolAttribute{MarkdownDescription: "Whether the tester can test Apple Pay.", Computed: true},
		},
	}
}

func (r *sandboxTesterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *sandboxTesterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func sandboxTesters(ctx context.Context, c *asc.Client) ([]gen.SandboxTesterV2, error) {
	out, err := c.SandboxTestersV2GetCollectionWithResponse(ctx, &gen.SandboxTestersV2GetCollectionParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.SandboxTestersV2Response) []gen.SandboxTesterV2 { return p.Data },
		func(p *gen.SandboxTestersV2Response) *string { return p.Links.Next })
}

var errNoSandboxTester = errors.New("no sandbox tester has this email; create it in App Store Connect under Users and Access")

func findSandboxTester(ctx context.Context, c *asc.Client, email string) (*gen.SandboxTesterV2, error) {
	all, err := sandboxTesters(ctx, c)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if asc.Value(asc.Value(all[i].Attributes).AcAccountName) == email {
			return &all[i], nil
		}
	}
	return nil, errNoSandboxTester
}

func (m *sandboxTesterModel) apply(t *gen.SandboxTesterV2) {
	a := asc.Value(t.Attributes)
	m.ID, m.Email, m.Territory = types.StringValue(t.Id), stringValue(a.AcAccountName), stringValue(a.Territory)
	m.InterruptPurchases, m.SubscriptionRenewalRate = types.BoolValue(asc.Value(a.InterruptPurchases)), stringValue(a.SubscriptionRenewalRate)
	m.FirstName, m.LastName, m.ApplePayCompatible = stringValue(a.FirstName), stringValue(a.LastName), types.BoolValue(asc.Value(a.ApplePayCompatible))
}

func (r *sandboxTesterResource) write(ctx context.Context, plan *sandboxTesterModel) error {
	t, err := findSandboxTester(ctx, r.client, plan.Email.ValueString())
	if err != nil {
		return err
	}
	var body gen.SandboxTesterV2UpdateRequest
	body.Data.Type, body.Data.Id = gen.SandboxTesterV2UpdateRequestDataTypeSandboxTesters, t.Id
	a := asc.Alloc(&body.Data.Attributes)
	if v := knownString(plan.Territory); v != nil {
		a.Territory = new(gen.TerritoryCode(*v))
	}
	if !plan.InterruptPurchases.IsUnknown() && !plan.InterruptPurchases.IsNull() {
		a.InterruptPurchases = new(plan.InterruptPurchases.ValueBool())
	}
	if v := knownString(plan.SubscriptionRenewalRate); v != nil {
		a.SubscriptionRenewalRate = new(gen.SandboxTesterV2UpdateRequestDataAttributesSubscriptionRenewalRate(*v))
	}
	out, err := r.client.SandboxTestersV2UpdateInstanceWithResponse(ctx, t.Id, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	plan.apply(&out.JSON200.Data)
	return nil
}

func (r *sandboxTesterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan sandboxTesterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set sandbox tester", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sandboxTesterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state sandboxTesterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := findSandboxTester(ctx, r.client, state.Email.ValueString())
	if errors.Is(err, errNoSandboxTester) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read sandbox tester", err)
		return
	}
	state.apply(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sandboxTesterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan sandboxTesterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set sandbox tester", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the tester, which the API can't delete.
func (r *sandboxTesterResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- data.apple_sandbox_testers ----

type sandboxTesterData struct {
	ID        types.String `tfsdk:"id"`
	Email     types.String `tfsdk:"email"`
	FirstName types.String `tfsdk:"first_name"`
	LastName  types.String `tfsdk:"last_name"`
	Territory types.String `tfsdk:"territory"`
}

type sandboxTestersData struct {
	Testers []sandboxTesterData `tfsdk:"sandbox_testers"`
}

var newSandboxTestersDataSource = listDataSource("sandbox_testers",
	"Lists the account's sandbox testers.",
	map[string]dsschema.Attribute{
		"sandbox_testers": listOf("The sandbox testers.", map[string]dsschema.Attribute{
			"id":         dsComputed("App Store Connect ID of the tester."),
			"email":      dsComputed("Email address of the sandbox Apple Account."),
			"first_name": dsComputed("First name."),
			"last_name":  dsComputed("Last name."),
			"territory":  dsComputed("Storefront, e.g. `USA`."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *sandboxTestersData, diags *diag.Diagnostics) {
		all, err := sandboxTesters(ctx, c)
		if err != nil {
			apiError(diags, "list sandbox testers", err)
			return
		}
		m.Testers = []sandboxTesterData{}
		for _, t := range all {
			var tm sandboxTesterModel
			tm.apply(&t)
			m.Testers = append(m.Testers, sandboxTesterData{ID: tm.ID, Email: tm.Email, FirstName: tm.FirstName, LastName: tm.LastName, Territory: tm.Territory})
		}
	})

// ---- apple_clear_sandbox_purchase_history ----

type clearHistoryModel struct {
	TesterIDs types.Set `tfsdk:"sandbox_tester_ids"`
}

var newClearSandboxHistoryAction = newAction("clear_sandbox_purchase_history",
	"Clears the purchase history of sandbox testers, so they can buy non-consumables and subscriptions again.",
	map[string]actionschema.Attribute{"sandbox_tester_ids": actionSet("App Store Connect IDs of the sandbox testers.")},
	func(ctx context.Context, c *asc.Client, m *clearHistoryModel) (string, error) {
		var body gen.SandboxTestersClearPurchaseHistoryRequestV2CreateRequest
		body.Data.Type = gen.SandboxTestersClearPurchaseHistoryRequestV2CreateRequestDataTypeSandboxTestersClearPurchaseHistoryRequest
		for _, id := range ids(ctx, m.TesterIDs) {
			e := asc.Push(&body.Data.Relationships.SandboxTesters.Data)
			e.Id, e.Type = id, gen.SandboxTestersClearPurchaseHistoryRequestV2CreateRequestDataRelationshipsSandboxTestersDataTypeSandboxTesters
		}
		out, err := c.SandboxTestersClearPurchaseHistoryRequestV2CreateInstanceWithResponse(ctx, body)
		return fmt.Sprintf("Cleared the purchase history of %d sandbox testers", len(body.Data.Relationships.SandboxTesters.Data)), asc.Err(out, err)
	})
