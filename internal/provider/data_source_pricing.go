package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// ---- data.apple_territories ----

type territoryData struct {
	ID       types.String `tfsdk:"id"`
	Currency types.String `tfsdk:"currency"`
}

type territoriesData struct {
	IDs         types.Set       `tfsdk:"ids"`
	Territories []territoryData `tfsdk:"territories"`
}

var newTerritoriesDataSource = listDataSource("territories",
	"Lists the territories where the App Store sells apps, for availability and pricing.",
	map[string]schema.Attribute{
		"ids": dsComputedSet("IDs of the territories, e.g. `USA`, for `for_each`."),
		"territories": listOf("The territories, ordered by ID.", map[string]schema.Attribute{
			"id":       dsComputed("ID of the territory: an ISO 3166-1 alpha-3 code, e.g. `USA`."),
			"currency": dsComputed("Currency of the territory, e.g. `USD`."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *territoriesData, diags *diag.Diagnostics) {
		out, err := c.TerritoriesGetCollectionWithResponse(ctx, &gen.TerritoriesGetCollectionParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "list territories", err)
			return
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.TerritoriesResponse) []gen.Territory { return p.Data },
			func(p *gen.TerritoriesResponse) *string { return p.Links.Next })
		if err != nil {
			apiError(diags, "list territories", err)
			return
		}
		m.Territories = []territoryData{}
		var ids []string
		for _, t := range all {
			ids = append(ids, t.Id)
			m.Territories = append(m.Territories, territoryData{ID: types.StringValue(t.Id), Currency: stringValue(asc.Value(t.Attributes).Currency)})
		}
		m.IDs = stringsValue(ids)
	})

// ---- data.apple_app_price_points and data.apple_app_price_point_equalizations ----

type pricePointData struct {
	ID            types.String `tfsdk:"id"`
	Territory     types.String `tfsdk:"territory"`
	CustomerPrice types.String `tfsdk:"customer_price"`
	Proceeds      types.String `tfsdk:"proceeds"`
}

var pricePointAttributes = map[string]schema.Attribute{
	"id":             dsComputed("ID of the price point, for `apple_app_price_schedule`."),
	"territory":      dsComputed("Territory of the price point, e.g. `USA`."),
	"customer_price": dsComputed("Price customers pay, in the territory's currency, e.g. `0.99`."),
	"proceeds":       dsComputed("Proceeds per sale, in the territory's currency."),
}

func pricePoints(ctx context.Context, m *[]pricePointData, prices types.Set, data []gen.AppPricePointV3, diags *diag.Diagnostics) {
	var items []pricePointData
	for _, p := range data {
		a := asc.Value(p.Attributes)
		item := pricePointData{ID: types.StringValue(p.Id), Territory: types.StringNull(), CustomerPrice: stringValue(a.CustomerPrice), Proceeds: stringValue(a.Proceeds)}
		if rel := p.Relationships; rel != nil && rel.Territory != nil && rel.Territory.Data != nil {
			item.Territory = types.StringValue(rel.Territory.Data.Id)
		}
		items = append(items, item)
	}
	filterPricePoints(ctx, m, prices, items, diags)
}

// filterPricePoints keeps the price points whose customer price passes
// prices.
func filterPricePoints(ctx context.Context, m *[]pricePointData, prices types.Set, items []pricePointData, diags *diag.Diagnostics) {
	*m = []pricePointData{}
	for _, item := range items {
		if passes(ctx, prices, item.CustomerPrice.ValueString(), diags) {
			*m = append(*m, item)
		}
	}
}

func allPricePoints(ctx context.Context, c *asc.Client, first *gen.AppPricePointsV3Response) ([]gen.AppPricePointV3, error) {
	return asc.All(ctx, c, first,
		func(p *gen.AppPricePointsV3Response) []gen.AppPricePointV3 { return p.Data },
		func(p *gen.AppPricePointsV3Response) *string { return p.Links.Next })
}

type appPricePointsData struct {
	AppID          types.String     `tfsdk:"app_id"`
	Territories    types.Set        `tfsdk:"territories"`
	CustomerPrices types.Set        `tfsdk:"customer_prices"`
	PricePoints    []pricePointData `tfsdk:"price_points"`
}

var newAppPricePointsDataSource = listDataSource("app_price_points",
	"Lists the price points an app can be sold at, per territory. Choose one for the base territory of "+
		"`apple_app_price_schedule`.",
	map[string]schema.Attribute{
		"app_id":          schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"territories":     dsFilter("Only price points in these territories, e.g. `USA`."),
		"customer_prices": dsFilter("Only price points with these customer prices, e.g. `0.99`."),
		"price_points":    listOf("The price points.", pricePointAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *appPricePointsData, diags *diag.Diagnostics) {
		out, err := c.AppsAppPricePointsGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsAppPricePointsGetToManyRelatedParams{
			FilterTerritory: filterParam[string](ctx, m.Territories, diags),
			Include:         &[]gen.AppsAppPricePointsGetToManyRelatedParamsInclude{gen.AppsAppPricePointsGetToManyRelatedParamsIncludeTerritory},
			Limit:           new(listAll),
		})
		var all []gen.AppPricePointV3
		if err = asc.Err(out, err); err == nil {
			all, err = allPricePoints(ctx, c, out.JSON200)
		}
		if err != nil {
			apiError(diags, "list app price points", err)
			return
		}
		pricePoints(ctx, &m.PricePoints, m.CustomerPrices, all, diags)
	})

type equalizationsData struct {
	PricePointID types.String     `tfsdk:"price_point_id"`
	Territories  types.Set        `tfsdk:"territories"`
	PricePoints  []pricePointData `tfsdk:"price_points"`
}

var newPricePointEqualizationsDataSource = listDataSource("app_price_point_equalizations",
	"Lists the price points in other territories that are equivalent to a price point: the prices App Store "+
		"Connect sets automatically in territories without a manual price.",
	map[string]schema.Attribute{
		"price_point_id": schema.StringAttribute{MarkdownDescription: "ID of the price point.", Required: true},
		"territories":    dsFilter("Only equivalents in these territories, e.g. `GBR`."),
		"price_points":   listOf("The equivalent price points.", pricePointAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *equalizationsData, diags *diag.Diagnostics) {
		out, err := c.AppPricePointsV3EqualizationsGetToManyRelatedWithResponse(ctx, m.PricePointID.ValueString(), &gen.AppPricePointsV3EqualizationsGetToManyRelatedParams{
			FilterTerritory: filterParam[string](ctx, m.Territories, diags),
			Include:         &[]gen.AppPricePointsV3EqualizationsGetToManyRelatedParamsInclude{gen.AppPricePointsV3EqualizationsGetToManyRelatedParamsIncludeTerritory},
			Limit:           new(listAll),
		})
		var all []gen.AppPricePointV3
		if err = asc.Err(out, err); err == nil {
			all, err = allPricePoints(ctx, c, out.JSON200)
		}
		if err != nil {
			apiError(diags, "list price point equalizations", err)
			return
		}
		pricePoints(ctx, &m.PricePoints, types.SetNull(types.StringType), all, diags)
	})
