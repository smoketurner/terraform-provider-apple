package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Price schedules: a price point in a base territory, from which App Store
// Connect sets equivalent prices elsewhere, plus manual prices for other
// territories and scheduled changes. The API only replaces a schedule as a
// whole, so the manual prices are nested. Apps and in-app purchases have
// the same schedules.

type manualPriceModel struct {
	PricePointID types.String `tfsdk:"price_point_id"`
	StartDate    types.String `tfsdk:"start_date"`
	EndDate      types.String `tfsdk:"end_date"`
}

// priceScheduleAPI is the part of the API a kind of price schedule needs.
type priceScheduleAPI struct {
	typeName    string
	description string
	parent      string // attribute name of the owner's ID, e.g. "app_id"
	parentDesc  string
	write       func(ctx context.Context, c *asc.Client, parentID, base string, prices []manualPriceModel) (string, error)
	read        func(ctx context.Context, c *asc.Client, parentID string) (id, base string, prices []manualPriceModel, err error)
}

var (
	_ resource.ResourceWithConfigure   = &priceScheduleResource{}
	_ resource.ResourceWithImportState = &priceScheduleResource{}
	_ resource.ResourceWithIdentity    = &priceScheduleResource{}
)

type priceScheduleResource struct {
	resourceIdentity
	api    priceScheduleAPI
	client *asc.Client
}

func newPriceScheduleResource(api priceScheduleAPI) func() resource.Resource {
	return func() resource.Resource {
		return &priceScheduleResource{api: api, resourceIdentity: resourceIdentity{attrs: []identityAttribute{
			{name: api.parent, description: api.parentDesc, state: api.parent},
		}}}
	}
}

func (r *priceScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *priceScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: r.api.description + " Every change replaces the whole schedule. App Store Connect can't " +
			"delete a schedule, so destroying the resource leaves the prices as they are. Import it by the owner's ID.",
		Attributes: map[string]schema.Attribute{
			r.api.parent:     replacedString(r.api.parentDesc),
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

// write sends the planned schedule and records it in state. The owner's
// attribute has a kind-specific name, so attributes are read by path.
func (r *priceScheduleResource) write(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State) error {
	var parent, base types.String
	var prices []manualPriceModel
	if d := plan.GetAttribute(ctx, path.Root(r.api.parent), &parent); d.HasError() {
		return fmt.Errorf("reading the plan: %v", d)
	}
	if d := plan.GetAttribute(ctx, path.Root("base_territory"), &base); d.HasError() {
		return fmt.Errorf("reading the plan: %v", d)
	}
	if d := plan.GetAttribute(ctx, path.Root("manual_prices"), &prices); d.HasError() {
		return fmt.Errorf("reading the plan: %v", d)
	}
	id, err := r.api.write(ctx, r.client, parent.ValueString(), base.ValueString(), prices)
	if err != nil {
		return err
	}
	state.Raw = plan.Raw
	if d := state.SetAttribute(ctx, path.Root("id"), id); d.HasError() {
		return fmt.Errorf("writing the state: %v", d)
	}
	return nil
}

func (r *priceScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	if err := r.write(ctx, req.Plan, &resp.State); err != nil {
		apiError(&resp.Diagnostics, "set price schedule", err)
	}
}

func (r *priceScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var parent types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(r.api.parent), &parent)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, base, prices, err := r.api.read(ctx, r.client, parent.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read price schedule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("base_territory"), base)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("manual_prices"), prices)...)
}

func (r *priceScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	if err := r.write(ctx, req.Plan, &resp.State); err != nil {
		apiError(&resp.Diagnostics, "set price schedule", err)
	}
}

// Delete leaves the prices, which the API cannot delete.
func (r *priceScheduleResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// localPriceID is the local ID of an inline price: inline objects are
// referenced by IDs of the form "${id}".
func localPriceID(i int) string { return fmt.Sprintf("${price-%d}", i) }

var appPriceScheduleAPI = priceScheduleAPI{
	typeName: "app_price_schedule",
	description: "Sets an app's price: a price point in a base territory, from which App Store Connect sets " +
		"equivalent prices in the other territories, plus optional manual prices for other territories and scheduled " +
		"price changes. Find price points with `data.apple_app_price_points`.",
	parent:     "app_id",
	parentDesc: "App Store Connect ID of the app.",
	write: func(ctx context.Context, c *asc.Client, appID, base string, prices []manualPriceModel) (string, error) {
		var body gen.AppPriceScheduleCreateRequest
		body.Data.Type = gen.AppPriceScheduleCreateRequestDataTypeAppPriceSchedules
		rel := &body.Data.Relationships
		rel.App.Data.Id, rel.App.Data.Type = appID, gen.AppPriceScheduleCreateRequestDataRelationshipsAppDataTypeApps
		rel.BaseTerritory.Data.Id, rel.BaseTerritory.Data.Type = base, gen.AppPriceScheduleCreateRequestDataRelationshipsBaseTerritoryDataTypeTerritories
		rel.ManualPrices.Data = nil
		for i, p := range prices {
			local := localPriceID(i)
			ref := asc.Push(&rel.ManualPrices.Data)
			ref.Id, ref.Type = local, gen.AppPriceScheduleCreateRequestDataRelationshipsManualPricesDataTypeAppPrices
			inline := gen.AppPriceV2InlineCreate{Id: new(local), Type: gen.AppPriceV2InlineCreateTypeAppPrices}
			a := asc.Alloc(&inline.Attributes)
			a.StartDate, a.EndDate = parseDate(p.StartDate), parseDate(p.EndDate)
			point := asc.Alloc(&asc.Alloc(&asc.Alloc(&inline.Relationships).AppPricePoint).Data)
			point.Id, point.Type = p.PricePointID.ValueString(), gen.AppPriceV2InlineCreateRelationshipsAppPricePointDataTypeAppPricePoints
			var item gen.AppPriceScheduleCreateRequest_Included_Item
			if err := item.FromAppPriceV2InlineCreate(inline); err != nil {
				return "", err
			}
			*asc.Push(asc.Alloc(&body.Included)) = item
		}
		out, err := c.AppPriceSchedulesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
	read: func(ctx context.Context, c *asc.Client, appID string) (string, string, []manualPriceModel, error) {
		sched, err := c.AppsAppPriceScheduleGetToOneRelatedWithResponse(ctx, appID, nil)
		if err := asc.Err(sched, err); err != nil {
			return "", "", nil, err
		}
		id := sched.JSON200.Data.Id
		base, err := c.AppPriceSchedulesBaseTerritoryGetToOneRelatedWithResponse(ctx, id, nil)
		if err := asc.Err(base, err); err != nil {
			return "", "", nil, err
		}
		out, err := c.AppPriceSchedulesManualPricesGetToManyRelatedWithResponse(ctx, id, &gen.AppPriceSchedulesManualPricesGetToManyRelatedParams{
			Include: &[]gen.AppPriceSchedulesManualPricesGetToManyRelatedParamsInclude{gen.AppPriceSchedulesManualPricesGetToManyRelatedParamsIncludeAppPricePoint},
			Limit:   new(listAll),
		})
		if err := asc.Err(out, err); err != nil {
			return "", "", nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.AppPricesV2Response) []gen.AppPriceV2 { return p.Data },
			func(p *gen.AppPricesV2Response) *string { return p.Links.Next })
		if err != nil {
			return "", "", nil, err
		}
		prices := []manualPriceModel{}
		for _, p := range all {
			a := asc.Value(p.Attributes)
			if rel := p.Relationships; rel != nil && rel.AppPricePoint != nil && rel.AppPricePoint.Data != nil {
				prices = append(prices, manualPriceModel{PricePointID: types.StringValue(rel.AppPricePoint.Data.Id), StartDate: dateValue(a.StartDate), EndDate: dateValue(a.EndDate)})
			}
		}
		return id, base.JSON200.Data.Id, prices, nil
	},
}

var newAppPriceScheduleResource = newPriceScheduleResource(appPriceScheduleAPI)
