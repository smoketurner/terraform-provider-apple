package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

func identifierDataAttributes(kind identifierKind) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":         dsComputed("App Store Connect ID of the " + kind.noun + "."),
		"identifier": dsComputed("Identifier, e.g. `" + kind.example + "`."),
		"name":       dsComputed("Description of the " + kind.noun + "."),
	}
}

func newIdentifierDataSource(kind identifierKind) func() datasource.DataSource {
	return lookupDataSource(kind.typeName,
		"Looks up a "+kind.noun+" by its App Store Connect ID or identifier.", "identifier",
		withAttributes(identifierDataAttributes(kind), map[string]schema.Attribute{
			"id":         dsLookup("App Store Connect ID of the " + kind.noun + ". Set this or `identifier`."),
			"identifier": dsLookup("Identifier, e.g. `" + kind.example + "`. Set this or `id`."),
		}),
		func(ctx context.Context, c *asc.Client, m *identifierModel, diags *diag.Diagnostics) {
			var found *identifier
			var err error
			if id := m.ID.ValueString(); id != "" {
				found, err = kind.get(ctx, c, id)
			} else {
				found, err = kind.byIdentifier(ctx, c, m.Identifier.ValueString())
			}
			if err != nil {
				lookupError(diags, kind.noun, err)
				return
			}
			m.apply(found)
		})
}

type merchantIDsData struct {
	Identifiers types.Set         `tfsdk:"identifiers"`
	Names       types.Set         `tfsdk:"names"`
	Results     []identifierModel `tfsdk:"merchant_ids"`
}

type passTypeIDsData struct {
	Identifiers types.Set         `tfsdk:"identifiers"`
	Names       types.Set         `tfsdk:"names"`
	Results     []identifierModel `tfsdk:"pass_type_ids"`
}

// identifiersFields gives the plural data sources' models a common view.
type identifiersFields interface {
	fields() (identifiers, names types.Set, results *[]identifierModel)
}

func (m *merchantIDsData) fields() (types.Set, types.Set, *[]identifierModel) {
	return m.Identifiers, m.Names, &m.Results
}

func (m *passTypeIDsData) fields() (types.Set, types.Set, *[]identifierModel) {
	return m.Identifiers, m.Names, &m.Results
}

func newIdentifiersDataSource[M any, P interface {
	*M
	identifiersFields
}](kind identifierKind) func() datasource.DataSource {
	return listDataSource(kind.typeName+"s", "Lists the "+kind.noun+"s that match every given filter.",
		map[string]schema.Attribute{
			"identifiers":       dsFilter("Identifiers to match."),
			"names":             dsFilter("Names to match."),
			kind.typeName + "s": listOf("The matching "+kind.noun+"s, ordered by ID.", identifierDataAttributes(kind)),
		},
		func(ctx context.Context, c *asc.Client, m *M, diags *diag.Diagnostics) {
			identifiers, names, results := P(m).fields()
			all, err := kind.list(ctx, c, filterParam[string](ctx, identifiers, diags), filterParam[string](ctx, names, diags))
			if err != nil {
				apiError(diags, "list "+kind.noun+"s", err)
				return
			}
			*results = []identifierModel{}
			for i := range all {
				if passes(ctx, identifiers, all[i].Identifier, diags) && passes(ctx, names, all[i].Name, diags) {
					var item identifierModel
					item.apply(&all[i])
					*results = append(*results, item)
				}
			}
		})
}
