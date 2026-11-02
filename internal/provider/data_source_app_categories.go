package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

type appCategoryData struct {
	ID        types.String `tfsdk:"id"`
	ParentID  types.String `tfsdk:"parent_id"`
	Platforms types.Set    `tfsdk:"platforms"`
}

type appCategoriesData struct {
	Platforms  types.Set         `tfsdk:"platforms"`
	Categories []appCategoryData `tfsdk:"categories"`
}

var categoryPlatforms = enum(gen.PlatformIOS, gen.PlatformMACOS, gen.PlatformTVOS, gen.PlatformVISIONOS)

var newAppCategoriesDataSource = listDataSource("app_categories",
	"Lists the App Store categories and subcategories, for `apple_app_info`.",
	map[string]schema.Attribute{
		"platforms": dsFilter("Only categories available on one of these platforms.", categoryPlatforms...),
		"categories": listOf("The categories, then their subcategories, ordered by ID.", map[string]schema.Attribute{
			"id":        dsComputed("ID of the category, e.g. `GAMES` or `GAMES_PUZZLE`."),
			"parent_id": dsComputed("ID of the parent category, for subcategories."),
			"platforms": dsComputedSet("Platforms the category is available on."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *appCategoriesData, diags *diag.Diagnostics) {
		out, err := c.AppCategoriesGetCollectionWithResponse(ctx, &gen.AppCategoriesGetCollectionParams{
			FilterPlatforms: filterParam[gen.AppCategoriesGetCollectionParamsFilterPlatforms](ctx, m.Platforms, diags),
			Limit:           new(listAll),
		})
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "list app categories", err)
			return
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.AppCategoriesResponse) []gen.AppCategory { return p.Data },
			func(p *gen.AppCategoriesResponse) *string { return p.Links.Next })
		if err != nil {
			apiError(diags, "list app categories", err)
			return
		}
		m.Categories = []appCategoryData{}
		for _, cat := range all {
			var platforms []string
			for _, p := range asc.Value(asc.Value(cat.Attributes).Platforms) {
				platforms = append(platforms, string(p))
			}
			item := appCategoryData{ID: types.StringValue(cat.Id), ParentID: types.StringNull(), Platforms: stringsValue(platforms)}
			if rel := cat.Relationships; rel != nil && rel.Parent != nil && rel.Parent.Data != nil {
				item.ParentID = types.StringValue(rel.Parent.Data.Id)
			}
			m.Categories = append(m.Categories, item)
		}
	})
