package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Pricing and availability. A price schedule is replaced as a whole, so its
// manual prices are nested. Availability is created once with every
// territory, after which each territory changes on its own, so territories
// are separate resources.

var dateRegexp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

var dateValidator = stringvalidator.RegexMatches(dateRegexp, "must be a date such as `2026-12-01`")

func dateValue(d *openapi_types.Date) types.String {
	if d == nil {
		return types.StringNull()
	}
	return types.StringValue(d.Format(openapi_types.DateFormat))
}

func parseDate(v types.String) *openapi_types.Date {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	t, err := time.Parse(openapi_types.DateFormat, v.ValueString())
	if err != nil {
		return nil
	}
	return &openapi_types.Date{Time: t}
}

// ---- apple_app_price_schedule ----

var (
	_ resource.ResourceWithConfigure   = &priceScheduleResource{}
	_ resource.ResourceWithImportState = &priceScheduleResource{}
	_ resource.ResourceWithIdentity    = &priceScheduleResource{}
)

type priceScheduleResource struct {
	resourceIdentity
	client *asc.Client
}

type manualPriceModel struct {
	PricePointID types.String `tfsdk:"price_point_id"`
	StartDate    types.String `tfsdk:"start_date"`
	EndDate      types.String `tfsdk:"end_date"`
}

type priceScheduleModel struct {
	AppID         types.String       `tfsdk:"app_id"`
	BaseTerritory types.String       `tfsdk:"base_territory"`
	ManualPrices  []manualPriceModel `tfsdk:"manual_prices"`
	ID            types.String       `tfsdk:"id"`
}

func newPriceScheduleResource() resource.Resource {
	return &priceScheduleResource{resourceIdentity: appIdentity()}
}

func (r *priceScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_price_schedule"
}

func (r *priceScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an app's price: a price point in a base territory, from which App Store Connect " +
			"sets equivalent prices in the other territories, plus optional manual prices for other territories and " +
			"scheduled price changes. Find price points with `data.apple_app_price_points`. Every change replaces the " +
			"whole schedule. App Store Connect can't delete a schedule, so destroying the resource leaves the prices " +
			"as they are. Import it by app ID.",
		Attributes: map[string]schema.Attribute{
			"app_id":         replacedString("App Store Connect ID of the app."),
			"base_territory": schema.StringAttribute{MarkdownDescription: "Territory whose price the others are equalized from, e.g. `USA`.", Required: true, Validators: []validator.String{territoryValidator}},
			"manual_prices": schema.SetNestedAttribute{
				MarkdownDescription: "Manual prices. The base territory needs one; other territories without one get " +
					"the equivalent of the base price. A territory can have one price without dates, and dated prices " +
					"schedule changes.",
				Required:   true,
				Validators: []validator.Set{setvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"price_point_id": schema.StringAttribute{MarkdownDescription: "ID of the price point, which sets the territory and price.", Required: true},
					"start_date":     optionalString("First day of the price, e.g. `2026-12-01`. Unset means now.", dateValidator),
					"end_date":       optionalString("Last day of the price. Unset means no end.", dateValidator),
				}},
			},
			"id": idAttribute("App Store Connect ID of the price schedule."),
		},
	}
}

func (r *priceScheduleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *priceScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *priceScheduleResource) write(ctx context.Context, m *priceScheduleModel) error {
	var body gen.AppPriceScheduleCreateRequest
	body.Data.Type = gen.AppPriceScheduleCreateRequestDataTypeAppPriceSchedules
	rel := &body.Data.Relationships
	rel.App.Data.Id, rel.App.Data.Type = m.AppID.ValueString(), gen.AppPriceScheduleCreateRequestDataRelationshipsAppDataTypeApps
	rel.BaseTerritory.Data.Id, rel.BaseTerritory.Data.Type = m.BaseTerritory.ValueString(), gen.AppPriceScheduleCreateRequestDataRelationshipsBaseTerritoryDataTypeTerritories
	rel.ManualPrices.Data = nil
	for i, p := range m.ManualPrices {
		// Inline objects are referenced by local IDs of the form "${id}".
		local := fmt.Sprintf("${price-%d}", i)
		ref := asc.Push(&rel.ManualPrices.Data)
		ref.Id, ref.Type = local, gen.AppPriceScheduleCreateRequestDataRelationshipsManualPricesDataTypeAppPrices
		inline := gen.AppPriceV2InlineCreate{Id: new(local), Type: gen.AppPriceV2InlineCreateTypeAppPrices}
		a := asc.Alloc(&inline.Attributes)
		a.StartDate, a.EndDate = parseDate(p.StartDate), parseDate(p.EndDate)
		point := asc.Alloc(&asc.Alloc(&asc.Alloc(&inline.Relationships).AppPricePoint).Data)
		point.Id, point.Type = p.PricePointID.ValueString(), gen.AppPriceV2InlineCreateRelationshipsAppPricePointDataTypeAppPricePoints
		var item gen.AppPriceScheduleCreateRequest_Included_Item
		if err := item.FromAppPriceV2InlineCreate(inline); err != nil {
			return err
		}
		*asc.Push(asc.Alloc(&body.Included)) = item
	}
	out, err := r.client.AppPriceSchedulesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	m.ID = types.StringValue(out.JSON201.Data.Id)
	return nil
}

// read fills m from the app's price schedule.
func (r *priceScheduleResource) read(ctx context.Context, m *priceScheduleModel) error {
	sched, err := r.client.AppsAppPriceScheduleGetToOneRelatedWithResponse(ctx, m.AppID.ValueString(), nil)
	if err := asc.Err(sched, err); err != nil {
		return err
	}
	id := sched.JSON200.Data.Id
	base, err := r.client.AppPriceSchedulesBaseTerritoryGetToOneRelatedWithResponse(ctx, id, nil)
	if err := asc.Err(base, err); err != nil {
		return err
	}
	out, err := r.client.AppPriceSchedulesManualPricesGetToManyRelatedWithResponse(ctx, id, &gen.AppPriceSchedulesManualPricesGetToManyRelatedParams{
		Include: &[]gen.AppPriceSchedulesManualPricesGetToManyRelatedParamsInclude{gen.AppPriceSchedulesManualPricesGetToManyRelatedParamsIncludeAppPricePoint},
		Limit:   new(listAll),
	})
	if err := asc.Err(out, err); err != nil {
		return err
	}
	prices, err := asc.All(ctx, r.client, out.JSON200,
		func(p *gen.AppPricesV2Response) []gen.AppPriceV2 { return p.Data },
		func(p *gen.AppPricesV2Response) *string { return p.Links.Next })
	if err != nil {
		return err
	}
	m.ID, m.BaseTerritory = types.StringValue(id), types.StringValue(base.JSON200.Data.Id)
	m.ManualPrices = []manualPriceModel{}
	for _, p := range prices {
		a := asc.Value(p.Attributes)
		if rel := p.Relationships; rel != nil && rel.AppPricePoint != nil && rel.AppPricePoint.Data != nil {
			m.ManualPrices = append(m.ManualPrices, manualPriceModel{
				PricePointID: types.StringValue(rel.AppPricePoint.Data.Id), StartDate: dateValue(a.StartDate), EndDate: dateValue(a.EndDate),
			})
		}
	}
	return nil
}

func (r *priceScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan priceScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set app price schedule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *priceScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state priceScheduleModel
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
		apiError(&resp.Diagnostics, "read app price schedule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *priceScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan priceScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set app price schedule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the prices, which the API cannot delete.
func (r *priceScheduleResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_app_availability ----

var (
	_ resource.ResourceWithConfigure   = &availabilityResource{}
	_ resource.ResourceWithImportState = &availabilityResource{}
	_ resource.ResourceWithIdentity    = &availabilityResource{}
)

type availabilityResource struct {
	resourceIdentity
	client *asc.Client
}

type availabilityModel struct {
	AppID                     types.String `tfsdk:"app_id"`
	AvailableInNewTerritories types.Bool   `tfsdk:"available_in_new_territories"`
	ID                        types.String `tfsdk:"id"`
}

func newAvailabilityResource() resource.Resource {
	return &availabilityResource{resourceIdentity: appIdentity()}
}

func (r *availabilityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_availability"
}

func (r *availabilityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets up where an app is sold. An app isn't available anywhere until it has an " +
			"availability; creating this resource creates one with every territory unavailable, or manages the existing " +
			"one. Make the app available in a territory with `apple_app_territory_availability`. App Store Connect can't " +
			"delete an availability, so destroying the resource leaves it. Import it by app ID.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app."),
			"available_in_new_territories": schema.BoolAttribute{
				MarkdownDescription: "Whether the app becomes available in territories the App Store adds later.",
				Required:            true,
			},
			"id": idAttribute("App Store Connect ID of the availability."),
		},
	}
}

func (r *availabilityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *availabilityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func appAvailability(ctx context.Context, c *asc.Client, appID string) (*gen.AppAvailabilityV2, error) {
	out, err := c.AppsAppAvailabilityV2GetToOneRelatedWithResponse(ctx, appID, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func territoryAvailabilities(ctx context.Context, c *asc.Client, availabilityID string) ([]gen.TerritoryAvailability, error) {
	out, err := c.AppAvailabilitiesV2TerritoryAvailabilitiesGetToManyRelatedWithResponse(ctx, availabilityID,
		&gen.AppAvailabilitiesV2TerritoryAvailabilitiesGetToManyRelatedParams{
			Include: &[]gen.AppAvailabilitiesV2TerritoryAvailabilitiesGetToManyRelatedParamsInclude{gen.AppAvailabilitiesV2TerritoryAvailabilitiesGetToManyRelatedParamsIncludeTerritory},
			Limit:   new(listAll),
		})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.TerritoryAvailabilitiesResponse) []gen.TerritoryAvailability { return p.Data },
		func(p *gen.TerritoryAvailabilitiesResponse) *string { return p.Links.Next })
}

func territoryOf(ta *gen.TerritoryAvailability) string {
	if rel := ta.Relationships; rel != nil && rel.Territory != nil && rel.Territory.Data != nil {
		return rel.Territory.Data.Id
	}
	return ""
}

// write creates the app's availability, with every territory unavailable,
// or replaces the existing one, keeping its territories.
func (r *availabilityResource) write(ctx context.Context, m *availabilityModel) error {
	var body gen.AppAvailabilityV2CreateRequest
	body.Data.Type = gen.AppAvailabilityV2CreateRequestDataTypeAppAvailabilities
	body.Data.Attributes.AvailableInNewTerritories = m.AvailableInNewTerritories.ValueBool()
	body.Data.Relationships.App.Data.Id = m.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.AppAvailabilityV2CreateRequestDataRelationshipsAppDataTypeApps
	refs := &body.Data.Relationships.TerritoryAvailabilities.Data
	ref := func(id string) {
		e := asc.Push(refs)
		e.Id, e.Type = id, gen.AppAvailabilityV2CreateRequestDataRelationshipsTerritoryAvailabilitiesDataTypeTerritoryAvailabilities
	}
	existing, err := appAvailability(ctx, r.client, m.AppID.ValueString())
	switch {
	case err == nil:
		tas, err := territoryAvailabilities(ctx, r.client, existing.Id)
		if err != nil {
			return err
		}
		for _, ta := range tas {
			ref(ta.Id)
		}
	case asc.IsNotFound(err):
		out, err := r.client.TerritoriesGetCollectionWithResponse(ctx, &gen.TerritoriesGetCollectionParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return err
		}
		all, err := asc.All(ctx, r.client, out.JSON200,
			func(p *gen.TerritoriesResponse) []gen.Territory { return p.Data },
			func(p *gen.TerritoriesResponse) *string { return p.Links.Next })
		if err != nil {
			return err
		}
		included := []gen.TerritoryAvailabilityInlineCreate{}
		for _, t := range all {
			local := "${" + t.Id + "}"
			ref(local)
			item := gen.TerritoryAvailabilityInlineCreate{Id: new(local), Type: gen.TerritoryAvailabilityInlineCreateTypeTerritoryAvailabilities}
			asc.Alloc(&item.Attributes).Available = new(false)
			d := asc.Alloc(&asc.Alloc(&asc.Alloc(&item.Relationships).Territory).Data)
			d.Id, d.Type = t.Id, gen.TerritoryAvailabilityInlineCreateRelationshipsTerritoryDataTypeTerritories
			included = append(included, item)
		}
		body.Included = &included
	default:
		return err
	}
	out, err := r.client.AppAvailabilitiesV2CreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	m.ID = types.StringValue(out.JSON201.Data.Id)
	return nil
}

func (r *availabilityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan availabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set app availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *availabilityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state availabilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	av, err := appAvailability(ctx, r.client, state.AppID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read app availability", err)
		return
	}
	state.ID = types.StringValue(av.Id)
	state.AvailableInNewTerritories = types.BoolValue(asc.Value(asc.Value(av.Attributes).AvailableInNewTerritories))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *availabilityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan availabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set app availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the availability, which the API cannot delete.
func (r *availabilityResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_app_territory_availability ----

var (
	_ resource.ResourceWithConfigure   = &territoryAvailabilityResource{}
	_ resource.ResourceWithImportState = &territoryAvailabilityResource{}
	_ resource.ResourceWithIdentity    = &territoryAvailabilityResource{}
)

type territoryAvailabilityResource struct {
	resourceIdentity
	client *asc.Client
}

type territoryAvailabilityModel struct {
	AppID               types.String `tfsdk:"app_id"`
	Territory           types.String `tfsdk:"territory"`
	Available           types.Bool   `tfsdk:"available"`
	ReleaseDate         types.String `tfsdk:"release_date"`
	PreOrderEnabled     types.Bool   `tfsdk:"pre_order_enabled"`
	ID                  types.String `tfsdk:"id"`
	ContentStatuses     types.Set    `tfsdk:"content_statuses"`
	PreOrderPublishDate types.String `tfsdk:"pre_order_publish_date"`
}

func newTerritoryAvailabilityResource() resource.Resource {
	return &territoryAvailabilityResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "territory", description: "Territory, e.g. `USA`.", state: "territory"},
	}}}
}

func (r *territoryAvailabilityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_territory_availability"
}

func (r *territoryAvailabilityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets whether an app is sold in one territory, and when. The app needs an " +
			"`apple_app_availability`; reference it in `app_id` so it's created first. Destroying the resource makes the " +
			"app unavailable in the territory. Import it with `<app_id>/<territory>`.",
		Attributes: map[string]schema.Attribute{
			"app_id":    replacedString("App Store Connect ID of the app, e.g. `apple_app_availability.example.app_id`."),
			"territory": replacedString("Territory, e.g. `USA`; see `data.apple_territories`.", territoryValidator),
			"available": schema.BoolAttribute{MarkdownDescription: "Whether the app is sold in the territory.", Required: true},
			"release_date": optionalString("Date the app is released in the territory, e.g. `2026-12-01`, for a release "+
				"after approval or a pre-order. Ending a pre-order clears it.", dateValidator),
			"pre_order_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether customers can pre-order the app before `release_date`, which must be in " +
					"the future. End pre-orders early with `apple_end_pre_orders`. Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"id":                     schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the territory availability.", Computed: true},
			"content_statuses":       schema.SetAttribute{MarkdownDescription: "Statuses of the app in the territory, e.g. `AVAILABLE` or `CANNOT_SELL_RESTRICTED_RATING`.", Computed: true, ElementType: types.StringType},
			"pre_order_publish_date": computedOnly("Date the pre-order was published on the App Store."),
		},
	}
}

func (r *territoryAvailabilityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *territoryAvailabilityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

var errNoAvailability = errors.New("the app has no availability; create an apple_app_availability first")

// findTerritoryAvailabilities returns the app's availability of each
// territory in territories.
func findTerritoryAvailabilities(ctx context.Context, c *asc.Client, appID string, territories ...string) ([]gen.TerritoryAvailability, error) {
	av, err := appAvailability(ctx, c, appID)
	if asc.IsNotFound(err) {
		return nil, errNoAvailability
	}
	if err != nil {
		return nil, err
	}
	all, err := territoryAvailabilities(ctx, c, av.Id)
	if err != nil {
		return nil, err
	}
	var out []gen.TerritoryAvailability
	for _, t := range territories {
		i := slices.IndexFunc(all, func(ta gen.TerritoryAvailability) bool { return territoryOf(&ta) == t })
		if i < 0 {
			return nil, fmt.Errorf("%w: territory %s", errNoMatch, t)
		}
		out = append(out, all[i])
	}
	return out, nil
}

func (m *territoryAvailabilityModel) apply(ta *gen.TerritoryAvailability) {
	a := asc.Value(ta.Attributes)
	m.ID = types.StringValue(ta.Id)
	m.Available, m.PreOrderEnabled = types.BoolValue(asc.Value(a.Available)), types.BoolValue(asc.Value(a.PreOrderEnabled))
	m.ReleaseDate, m.PreOrderPublishDate = dateValue(a.ReleaseDate), dateValue(a.PreOrderPublishDate)
	var statuses []string
	for _, s := range asc.Value(a.ContentStatuses) {
		statuses = append(statuses, string(s))
	}
	m.ContentStatuses = stringsValue(statuses)
}

// update sets a territory's availability. The request is raw JSON, because
// clearing the release date needs null, which the generated type can't send.
func updateTerritoryAvailability(ctx context.Context, c *asc.Client, id string, available, preOrder bool, releaseDate types.String) (*gen.TerritoryAvailability, error) {
	attrs := map[string]any{"available": available, "preOrderEnabled": preOrder, "releaseDate": nil}
	if !releaseDate.IsNull() && !releaseDate.IsUnknown() {
		attrs["releaseDate"] = releaseDate.ValueString()
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{"type": "territoryAvailabilities", "id": id, "attributes": attrs}})
	if err != nil {
		return nil, err
	}
	out, err := c.TerritoryAvailabilitiesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *territoryAvailabilityResource) write(ctx context.Context, m *territoryAvailabilityModel) error {
	found, err := findTerritoryAvailabilities(ctx, r.client, m.AppID.ValueString(), m.Territory.ValueString())
	if err != nil {
		return err
	}
	ta, err := updateTerritoryAvailability(ctx, r.client, found[0].Id, m.Available.ValueBool(), m.PreOrderEnabled.ValueBool(), m.ReleaseDate)
	if err != nil {
		return err
	}
	m.apply(ta)
	return nil
}

func (r *territoryAvailabilityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan territoryAvailabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set territory availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *territoryAvailabilityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state territoryAvailabilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := findTerritoryAvailabilities(ctx, r.client, state.AppID.ValueString(), state.Territory.ValueString())
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) || errors.Is(err, errNoAvailability) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read territory availability", err)
		return
	}
	state.apply(&found[0])
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *territoryAvailabilityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan territoryAvailabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set territory availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete makes the app unavailable in the territory.
func (r *territoryAvailabilityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state territoryAvailabilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := findTerritoryAvailabilities(ctx, r.client, state.AppID.ValueString(), state.Territory.ValueString())
	if errors.Is(err, errNoMatch) || errors.Is(err, errNoAvailability) {
		return
	}
	if err == nil {
		_, err = updateTerritoryAvailability(ctx, r.client, found[0].Id, false, false, types.StringNull())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "make the app unavailable in "+state.Territory.ValueString(), err)
	}
}

// ---- apple_end_pre_orders ----

type endPreOrdersModel struct {
	AppID       types.String `tfsdk:"app_id"`
	Territories types.Set    `tfsdk:"territories"`
}

var newEndPreOrdersAction = newAction("end_pre_orders",
	"Ends an app's pre-orders in territories, releasing the app there now instead of on its release date. The "+
		"territories' `release_date` is cleared, which `apple_app_territory_availability` then shows as a change.",
	map[string]actionschema.Attribute{
		"app_id":      actionString("App Store Connect ID of the app."),
		"territories": actionSet("Territories whose pre-orders end, e.g. `USA`."),
	},
	func(ctx context.Context, c *asc.Client, m *endPreOrdersModel) (string, error) {
		territories := ids(ctx, m.Territories)
		found, err := findTerritoryAvailabilities(ctx, c, m.AppID.ValueString(), territories...)
		if err != nil {
			return "", err
		}
		var body gen.EndAppAvailabilityPreOrderCreateRequest
		body.Data.Type = gen.EndAppAvailabilityPreOrderCreateRequestDataTypeEndAppAvailabilityPreOrders
		for _, ta := range found {
			e := asc.Push(&body.Data.Relationships.TerritoryAvailabilities.Data)
			e.Id, e.Type = ta.Id, gen.EndAppAvailabilityPreOrderCreateRequestDataRelationshipsTerritoryAvailabilitiesDataTypeTerritoryAvailabilities
		}
		out, err := c.EndAppAvailabilityPreOrdersCreateInstanceWithResponse(ctx, body)
		return fmt.Sprintf("Ended pre-orders in %d territories", len(found)), asc.Err(out, err)
	})
