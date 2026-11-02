package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var bundleIDModelAttributes = map[string]schema.Attribute{
	"id":         dsComputed("App Store Connect ID of the bundle ID."),
	"identifier": dsComputed("Bundle identifier."),
	"name":       dsComputed("Name of the bundle ID."),
	"platform":   dsComputed("Platform of the bundle ID."),
	"seed_id":    dsComputed("App ID prefix."),
}

var newBundleIDDataSource = lookupDataSource("bundle_id",
	"Looks up a bundle ID by its App Store Connect ID or its bundle identifier.", "identifier",
	withAttributes(bundleIDModelAttributes, map[string]schema.Attribute{
		"id":         dsLookup("App Store Connect ID of the bundle ID. Set this or `identifier`."),
		"identifier": dsLookup("Bundle identifier, e.g. `com.example.app`. Set this or `id`."),
	}),
	func(ctx context.Context, c *asc.Client, m *bundleIDModel, diags *diag.Diagnostics) {
		var found *gen.BundleId
		var err error
		if id := m.ID.ValueString(); id != "" {
			out, e := c.BundleIdsGetInstanceWithResponse(ctx, id, nil)
			if err = asc.Err(out, e); err == nil {
				found = &out.JSON200.Data
			}
		} else {
			found, err = bundleIDByIdentifier(ctx, c, m.Identifier.ValueString())
		}
		if err != nil {
			lookupError(diags, "bundle ID", err)
			return
		}
		m.apply(found)
	})

type bundleIDsData struct {
	Identifiers types.Set       `tfsdk:"identifiers"`
	Names       types.Set       `tfsdk:"names"`
	Platforms   types.Set       `tfsdk:"platforms"`
	SeedIDs     types.Set       `tfsdk:"seed_ids"`
	BundleIDs   []bundleIDModel `tfsdk:"bundle_ids"`
}

var newBundleIDsDataSource = listDataSource("bundle_ids", "Lists the bundle IDs that match every given filter.",
	map[string]schema.Attribute{
		"identifiers": dsFilter("Bundle identifiers to match."),
		"names":       dsFilter("Names to match."),
		"platforms":   dsFilter("Platforms to match.", platforms...),
		"seed_ids":    dsFilter("App ID prefixes to match."),
		"bundle_ids":  listOf("The matching bundle IDs, ordered by ID.", bundleIDModelAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *bundleIDsData, diags *diag.Diagnostics) {
		all, err := findBundleIDs(ctx, c, gen.BundleIdsGetCollectionParams{
			FilterIdentifier: filterParam[string](ctx, m.Identifiers, diags),
			FilterName:       filterParam[string](ctx, m.Names, diags),
			FilterPlatform:   filterParam[gen.BundleIdsGetCollectionParamsFilterPlatform](ctx, m.Platforms, diags),
			FilterSeedId:     filterParam[string](ctx, m.SeedIDs, diags),
		})
		if err != nil {
			apiError(diags, "list bundle IDs", err)
			return
		}
		m.BundleIDs = []bundleIDModel{}
		for i := range all {
			a := asc.Value(all[i].Attributes)
			if passes(ctx, m.Identifiers, asc.Value(a.Identifier), diags) && passes(ctx, m.Names, asc.Value(a.Name), diags) &&
				passes(ctx, m.Platforms, string(asc.Value(a.Platform)), diags) && passes(ctx, m.SeedIDs, asc.Value(a.SeedId), diags) {
				var item bundleIDModel
				item.apply(&all[i])
				m.BundleIDs = append(m.BundleIDs, item)
			}
		}
	})
