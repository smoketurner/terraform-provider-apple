package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

// ageRatingField is one answer of the age rating questionnaire: a boolean,
// or a string limited to values (any string when values is nil).
type ageRatingField struct {
	json   string
	bool   bool
	values []string
}

var frequencies = []string{"NONE", "INFREQUENT_OR_MILD", "FREQUENT_OR_INTENSE", "INFREQUENT", "FREQUENT"}

// ageRatingFields are the attributes of AgeRatingDeclarationUpdateRequest.
// TestAgeRatingFields checks them against the generated type.
var ageRatingFields = []ageRatingField{
	{json: "advertising", bool: true},
	{json: "ageAssurance", bool: true},
	{json: "ageRatingOverride", values: []string{"NONE", "NINE_PLUS", "THIRTEEN_PLUS", "SIXTEEN_PLUS", "SEVENTEEN_PLUS", "UNRATED"}},
	{json: "ageRatingOverrideV2", values: []string{"NONE", "NINE_PLUS", "THIRTEEN_PLUS", "SIXTEEN_PLUS", "EIGHTEEN_PLUS", "UNRATED"}},
	{json: "alcoholTobaccoOrDrugUseOrReferences", values: frequencies},
	{json: "contests", values: frequencies},
	{json: "developerAgeRatingInfoUrl"},
	{json: "gambling", bool: true},
	{json: "gamblingSimulated", values: frequencies},
	{json: "gracRatingClassificationNumber"},
	{json: "gunsOrOtherWeapons", values: frequencies},
	{json: "healthOrWellnessTopics", bool: true},
	{json: "horrorOrFearThemes", values: frequencies},
	{json: "kidsAgeBand", values: []string{"FIVE_AND_UNDER", "SIX_TO_EIGHT", "NINE_TO_ELEVEN"}},
	{json: "koreaAgeRatingOverride", values: []string{"NONE", "ALL", "TWELVE_PLUS", "FIFTEEN_PLUS", "NINETEEN_PLUS"}},
	{json: "lootBox", bool: true},
	{json: "matureOrSuggestiveThemes", values: frequencies},
	{json: "medicalOrTreatmentInformation", values: frequencies},
	{json: "messagingAndChat", bool: true},
	{json: "parentalControls", bool: true},
	{json: "profanityOrCrudeHumor", values: frequencies},
	{json: "sexualContentGraphicAndNudity", values: frequencies},
	{json: "sexualContentOrNudity", values: frequencies},
	{json: "socialMedia", bool: true},
	{json: "socialMediaAgeRestricted", bool: true},
	{json: "unrestrictedWebAccess", bool: true},
	{json: "userGeneratedContent", bool: true},
	{json: "violenceCartoonOrFantasy", values: frequencies},
	{json: "violenceRealistic", values: frequencies},
	{json: "violenceRealisticProlongedGraphicOrSadistic", values: frequencies},
}

var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// attr is the field's attribute name, e.g. kids_age_band.
func (f ageRatingField) attr() string {
	return strings.ToLower(camelBoundary.ReplaceAllString(f.json, "${1}_${2}"))
}

var (
	_ resource.ResourceWithConfigure   = &ageRatingResource{}
	_ resource.ResourceWithImportState = &ageRatingResource{}
	_ resource.ResourceWithIdentity    = &ageRatingResource{}
)

type ageRatingResource struct {
	resourceIdentity
	client *asc.Client
}

func newAgeRatingResource() resource.Resource {
	return &ageRatingResource{resourceIdentity: appIdentity()}
}

func (r *ageRatingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_age_rating_declaration"
}

func (r *ageRatingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"app_id": replacedString("App Store Connect ID of the app."),
		"id":     schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the declaration, which changes with each version.", Computed: true},
	}
	for _, f := range ageRatingFields {
		desc := "Answer for `" + f.json + "`. Unset answers keep their current value."
		if f.bool {
			attrs[f.attr()] = schema.BoolAttribute{MarkdownDescription: desc, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}}
			continue
		}
		a := schema.StringAttribute{MarkdownDescription: desc, Optional: true, Computed: true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
		if f.values != nil {
			a.MarkdownDescription = "Answer for `" + f.json + "`: " + docValues(f.values) + ". Unset answers keep their current value."
			a.Validators = []validator.String{oneOf(f.values)}
		}
		attrs[f.attr()] = a
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Answers the age rating questionnaire, from which App Store Connect derives the app's age " +
			"ratings. Only the answers set here are sent. Changes apply to the declaration of the version being prepared " +
			"and need a version in an editable state. Destroying the resource leaves the answers as they are. Import it by app ID.",
		Attributes: attrs,
	}
}

func (r *ageRatingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *ageRatingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ageRatingDeclaration returns the declaration of the app's editable app
// info, or with editableOnly false, of the app info to read.
func ageRatingDeclaration(ctx context.Context, c *asc.Client, appID string, editableOnly bool) (id string, attrs map[string]any, err error) {
	editable, current, err := appInfos(ctx, c, appID)
	if err != nil {
		return "", nil, err
	}
	info := current
	if editableOnly {
		if editable == nil {
			return "", nil, errNotEditable
		}
		info = editable
	}
	out, err := c.AppInfosAgeRatingDeclarationGetToOneRelatedWithResponse(ctx, info.Id, nil)
	if err := asc.Err(out, err); err != nil {
		return "", nil, err
	}
	b, err := json.Marshal(out.JSON200.Data.Attributes)
	if err != nil {
		return "", nil, err
	}
	attrs = map[string]any{}
	if err := json.Unmarshal(b, &attrs); err != nil {
		return "", nil, err
	}
	return out.JSON200.Data.Id, attrs, nil
}

// configured returns the answers set in data, which is a plan or a config.
func configured(ctx context.Context, data interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}, diags *diag.Diagnostics) map[string]any {
	out := map[string]any{}
	for _, f := range ageRatingFields {
		if f.bool {
			var v types.Bool
			diags.Append(data.GetAttribute(ctx, path.Root(f.attr()), &v)...)
			if !v.IsNull() && !v.IsUnknown() {
				out[f.json] = v.ValueBool()
			}
			continue
		}
		var v types.String
		diags.Append(data.GetAttribute(ctx, path.Root(f.attr()), &v)...)
		if !v.IsNull() && !v.IsUnknown() {
			out[f.json] = v.ValueString()
		}
	}
	return out
}

// setState writes the declaration's answers to state.
func setAgeRatingState(ctx context.Context, state *tfsdk.State, appID, id string, attrs map[string]any, diags *diag.Diagnostics) {
	diags.Append(state.SetAttribute(ctx, path.Root("app_id"), appID)...)
	diags.Append(state.SetAttribute(ctx, path.Root("id"), id)...)
	for _, f := range ageRatingFields {
		var v attr.Value
		switch x := attrs[f.json].(type) {
		case bool:
			v = types.BoolValue(x)
		case string:
			v = types.StringValue(x)
		default:
			if f.bool {
				v = types.BoolNull()
			} else {
				v = types.StringNull()
			}
		}
		diags.Append(state.SetAttribute(ctx, path.Root(f.attr()), v)...)
	}
}

func (r *ageRatingResource) write(ctx context.Context, appID string, answers map[string]any) (string, map[string]any, error) {
	id, _, err := ageRatingDeclaration(ctx, r.client, appID, true)
	if err != nil {
		return "", nil, err
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{"type": "ageRatingDeclarations", "id": id, "attributes": answers}})
	if err != nil {
		return "", nil, err
	}
	out, err := r.client.AgeRatingDeclarationsUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return "", nil, err
	}
	b, _ := json.Marshal(out.JSON200.Data.Attributes)
	attrs := map[string]any{}
	err = json.Unmarshal(b, &attrs)
	return id, attrs, err
}

func (r *ageRatingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var appID types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("app_id"), &appID)...)
	answers := configured(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, attrs, err := r.write(ctx, appID.ValueString(), answers)
	if err != nil {
		apiError(&resp.Diagnostics, "update age rating declaration", err)
		return
	}
	setAgeRatingState(ctx, &resp.State, appID.ValueString(), id, attrs, &resp.Diagnostics)
}

func (r *ageRatingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var appID types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("app_id"), &appID)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, attrs, err := ageRatingDeclaration(ctx, r.client, appID.ValueString(), false)
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read age rating declaration", err)
		return
	}
	setAgeRatingState(ctx, &resp.State, appID.ValueString(), id, attrs, &resp.Diagnostics)
}

func (r *ageRatingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var appID types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("app_id"), &appID)...)
	answers := configured(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, attrs, err := r.write(ctx, appID.ValueString(), answers)
	if err != nil {
		apiError(&resp.Diagnostics, "update age rating declaration", err)
		return
	}
	setAgeRatingState(ctx, &resp.State, appID.ValueString(), id, attrs, &resp.Diagnostics)
}

// Delete leaves the answers, which every version needs.
func (r *ageRatingResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
