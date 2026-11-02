package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func clientFromResource(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *asc.Client {
	return clientFrom(req.ProviderData, &resp.Diagnostics)
}

func clientFromDataSource(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *asc.Client {
	return clientFrom(req.ProviderData, &resp.Diagnostics)
}

func clientFrom(data any, diags *diag.Diagnostics) *asc.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*asc.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *asc.Client, got %T", data))
	}
	return c
}

func apiError(diags *diag.Diagnostics, action string, err error) {
	diags.AddError("App Store Connect API error", fmt.Sprintf("Unable to %s: %s", action, err))
}

// idAttribute is the computed "id" attribute every resource exposes.
func idAttribute(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

// computedString is a read-only attribute that keeps its value across plans.
func computedString(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

// replacedString is a required attribute that cannot change in place.
func replacedString(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Required:            true,
		Validators:          validators,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

// enum lists the values of a generated enum type.
func enum[T ~string](values ...T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

func oneOf(values []string) validator.String {
	return stringvalidator.OneOf(values...)
}

// docValues formats values for an attribute description.
func docValues(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "`" + v + "`"
	}
	return strings.Join(quoted, ", ")
}

var platforms = enum(gen.BundleIdPlatformIOS, gen.BundleIdPlatformMACOS, gen.BundleIdPlatformUNIVERSAL)

func platformAttribute() schema.StringAttribute {
	return replacedString("Platform: "+docValues(platforms)+". Changing it creates a new resource.", oneOf(platforms))
}

// stringValue converts an optional API string, mapping nil to null.
func stringValue[T ~string](p *T) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(string(*p))
}

// timeValue formats an API date-time as RFC 3339, mapping nil to null.
func timeValue(t *gen.DateTime) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

func stringSet(ctx context.Context, values []string, diags *diag.Diagnostics) types.Set {
	if values == nil {
		values = []string{}
	}
	set, d := types.SetValueFrom(ctx, types.StringType, values)
	diags.Append(d...)
	return set
}

func setStrings(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	var out []string
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	diags.Append(set.ElementsAs(ctx, &out, false)...)
	return out
}

// listAll is the page size for listing every item of a collection.
const listAll = 200

// linkageLimit is the page size for relationship linkages.
const linkageLimit = 1000
