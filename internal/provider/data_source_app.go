package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Apps cannot be created through the API, so they are data sources only.

type appData struct {
	ID            types.String `tfsdk:"id"`
	BundleID      types.String `tfsdk:"bundle_id"`
	Name          types.String `tfsdk:"name"`
	SKU           types.String `tfsdk:"sku"`
	PrimaryLocale types.String `tfsdk:"primary_locale"`
}

func (m *appData) apply(a *gen.App) {
	attrs := asc.Value(a.Attributes)
	m.ID = types.StringValue(a.Id)
	m.BundleID, m.Name, m.SKU = stringValue(attrs.BundleId), stringValue(attrs.Name), stringValue(attrs.Sku)
	m.PrimaryLocale = stringValue(attrs.PrimaryLocale)
}

var appDataAttributes = map[string]schema.Attribute{
	"id":             dsComputed("App Store Connect ID (Apple ID) of the app."),
	"bundle_id":      dsComputed("Bundle identifier of the app, e.g. `com.example.app`."),
	"name":           dsComputed("Name of the app."),
	"sku":            dsComputed("SKU of the app."),
	"primary_locale": dsComputed("Primary locale of the app, e.g. `en-US`."),
}

// findApps lists the apps matching params, following pagination.
func findApps(ctx context.Context, c *asc.Client, params gen.AppsGetCollectionParams) ([]gen.App, error) {
	params.Limit = new(listAll)
	out, err := c.AppsGetCollectionWithResponse(ctx, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.AppsResponse) []gen.App { return p.Data },
		func(p *gen.AppsResponse) *string { return p.Links.Next })
}

var newAppDataSource = lookupDataSource("app",
	"Looks up an app record by its App Store Connect ID or bundle identifier. Apps cannot be created through the "+
		"App Store Connect API.", "bundle_id",
	withAttributes(appDataAttributes, map[string]schema.Attribute{
		"id":        dsLookup("App Store Connect ID (Apple ID) of the app. Set this or `bundle_id`."),
		"bundle_id": dsLookup("Bundle identifier of the app, e.g. `com.example.app`. Set this or `id`."),
	}),
	func(ctx context.Context, c *asc.Client, m *appData, diags *diag.Diagnostics) {
		if id := m.ID.ValueString(); id != "" {
			out, err := c.AppsGetInstanceWithResponse(ctx, id, nil)
			if err := asc.Err(out, err); err != nil {
				lookupError(diags, "app", err)
				return
			}
			m.apply(&out.JSON200.Data)
			return
		}
		bundleID := m.BundleID.ValueString()
		all, err := findApps(ctx, c, gen.AppsGetCollectionParams{FilterBundleId: &[]string{bundleID}})
		if err != nil {
			apiError(diags, "look up app", err)
			return
		}
		for i := range all {
			if asc.Value(all[i].Attributes.BundleId) == bundleID {
				m.apply(&all[i])
				return
			}
		}
		lookupError(diags, "app", errNoMatch)
	})

type appsData struct {
	Names     types.Set `tfsdk:"names"`
	BundleIDs types.Set `tfsdk:"bundle_ids"`
	SKUs      types.Set `tfsdk:"skus"`
	Apps      []appData `tfsdk:"apps"`
}

var newAppsDataSource = listDataSource("apps", "Lists the app records that match every given filter.",
	map[string]schema.Attribute{
		"names":      dsFilter("Names to match."),
		"bundle_ids": dsFilter("Bundle identifiers to match."),
		"skus":       dsFilter("SKUs to match."),
		"apps":       listOf("The matching apps, ordered by ID.", appDataAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *appsData, diags *diag.Diagnostics) {
		all, err := findApps(ctx, c, gen.AppsGetCollectionParams{
			FilterName:     filterParam[string](ctx, m.Names, diags),
			FilterBundleId: filterParam[string](ctx, m.BundleIDs, diags),
			FilterSku:      filterParam[string](ctx, m.SKUs, diags),
		})
		if err != nil {
			apiError(diags, "list apps", err)
			return
		}
		m.Apps = []appData{}
		for i := range all {
			a := asc.Value(all[i].Attributes)
			if passes(ctx, m.Names, asc.Value(a.Name), diags) && passes(ctx, m.BundleIDs, asc.Value(a.BundleId), diags) &&
				passes(ctx, m.SKUs, asc.Value(a.Sku), diags) {
				var item appData
				item.apply(&all[i])
				m.Apps = append(m.Apps, item)
			}
		}
	})
