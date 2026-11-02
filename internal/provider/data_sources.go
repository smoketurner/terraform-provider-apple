package provider

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

// genericDataSource holds the framework plumbing shared by every data
// source: M is the model, read fills it from the configuration.
type genericDataSource[M any] struct {
	typeName    string
	description string
	attributes  map[string]schema.Attribute
	validators  []datasource.ConfigValidator
	read        func(ctx context.Context, c *asc.Client, m *M, diags *diag.Diagnostics)
	client      *asc.Client
}

var (
	_ datasource.DataSourceWithConfigure        = &genericDataSource[struct{}]{}
	_ datasource.DataSourceWithConfigValidators = &genericDataSource[struct{}]{}
)

func (d *genericDataSource[M]) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.typeName
}

func (d *genericDataSource[M]) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: d.description, Attributes: d.attributes}
}

func (d *genericDataSource[M]) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return d.validators
}

func (d *genericDataSource[M]) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromDataSource(req, resp)
}

func (d *genericDataSource[M]) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m M
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d.read(ctx, d.client, &m, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// lookupDataSource returns a singular data source that finds one object by
// "id" or by the natural key attribute key.
func lookupDataSource[M any](typeName, description, key string, attributes map[string]schema.Attribute,
	read func(ctx context.Context, c *asc.Client, m *M, diags *diag.Diagnostics),
) func() datasource.DataSource {
	return func() datasource.DataSource {
		return &genericDataSource[M]{
			typeName:    typeName,
			description: description,
			attributes:  attributes,
			validators:  []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot(key))},
			read:        read,
		}
	}
}

// listDataSource returns a plural data source.
func listDataSource[M any](typeName, description string, attributes map[string]schema.Attribute,
	read func(ctx context.Context, c *asc.Client, m *M, diags *diag.Diagnostics), validators ...datasource.ConfigValidator,
) func() datasource.DataSource {
	return func() datasource.DataSource {
		return &genericDataSource[M]{typeName: typeName, description: description, attributes: attributes, read: read, validators: validators}
	}
}

// lookupError reports a failed lookup by ID or natural key.
func lookupError(diags *diag.Diagnostics, noun string, err error) {
	if errors.Is(err, errNoMatch) || asc.IsNotFound(err) {
		diags.AddError("Not found", "No "+noun+" matches the given arguments.")
		return
	}
	apiError(diags, "look up "+noun, err)
}

// Schema building blocks.

func dsComputed(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
}

func dsSensitive(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Computed: true, Sensitive: true}
}

func dsComputedSet(desc string) schema.SetAttribute {
	return schema.SetAttribute{MarkdownDescription: desc, Computed: true, ElementType: types.StringType}
}

// dsLookup is an Optional+Computed lookup argument.
func dsLookup(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Optional: true, Computed: true}
}

// dsFilter is a filter argument: the results must match one of the values.
func dsFilter(desc string, values ...string) schema.SetAttribute {
	a := schema.SetAttribute{MarkdownDescription: desc, Optional: true, ElementType: types.StringType}
	if len(values) > 0 {
		a.MarkdownDescription += " Values: " + docValues(values) + "."
		a.Validators = []validator.Set{setvalidator.ValueStringsAre(oneOf(values))}
	}
	return a
}

// withAttributes returns base plus extra, which wins on conflicts.
func withAttributes(base map[string]schema.Attribute, extra map[string]schema.Attribute) map[string]schema.Attribute {
	out := maps.Clone(base)
	maps.Copy(out, extra)
	return out
}

// listOf is the computed list of results of a plural data source.
func listOf(desc string, item map[string]schema.Attribute) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: desc,
		Computed:            true,
		NestedObject:        schema.NestedAttributeObject{Attributes: item},
	}
}

// filterParam converts a filter set to a query parameter; an unset or empty
// filter is omitted.
func filterParam[T ~string](ctx context.Context, set types.Set, diags *diag.Diagnostics) *[]T {
	values := setStrings(ctx, set, diags)
	if len(values) == 0 {
		return nil
	}
	out := make([]T, len(values))
	for i, v := range values {
		out[i] = T(v)
	}
	return &out
}

// passes reports whether value passes a filter set. The API's filters are
// applied again here, so results match exactly whatever the API does.
func passes(ctx context.Context, set types.Set, value string, diags *diag.Diagnostics) bool {
	values := setStrings(ctx, set, diags)
	return len(values) == 0 || slices.Contains(values, value)
}
