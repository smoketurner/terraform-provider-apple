package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Product page optimization: an experiment shows treatments, which are
// alternative versions of an app's product page, to a share of visitors
// (https://developer.apple.com/documentation/appstoreconnectapi/app-store-version-experiments).
// The provider uses version 2 of the experiments API; version 1 is
// deprecated. A treatment's screenshots and previews are App Asset Library
// placements on a treatment localization.

var experimentStates = enum(gen.AppStoreVersionExperimentV2AttributesStatePREPAREFORSUBMISSION,
	gen.AppStoreVersionExperimentV2AttributesStateREADYFORREVIEW, gen.AppStoreVersionExperimentV2AttributesStateWAITINGFORREVIEW,
	gen.AppStoreVersionExperimentV2AttributesStateINREVIEW, gen.AppStoreVersionExperimentV2AttributesStateACCEPTED,
	gen.AppStoreVersionExperimentV2AttributesStateAPPROVED, gen.AppStoreVersionExperimentV2AttributesStateREJECTED,
	gen.AppStoreVersionExperimentV2AttributesStateCOMPLETED, gen.AppStoreVersionExperimentV2AttributesStateSTOPPED)

// ---- apple_app_store_version_experiment ----

var (
	_ resource.ResourceWithConfigure   = &experimentResource{}
	_ resource.ResourceWithImportState = &experimentResource{}
	_ resource.ResourceWithIdentity    = &experimentResource{}
)

type experimentResource struct {
	resourceIdentity
	client *asc.Client
}

type experimentModel struct {
	AppID             types.String `tfsdk:"app_id"`
	Platform          types.String `tfsdk:"platform"`
	Name              types.String `tfsdk:"name"`
	TrafficProportion types.Int64  `tfsdk:"traffic_proportion"`
	State             types.String `tfsdk:"state"`
	ReviewRequired    types.Bool   `tfsdk:"review_required"`
	StartDate         types.String `tfsdk:"start_date"`
	EndDate           types.String `tfsdk:"end_date"`
	ID                types.String `tfsdk:"id"`
}

func newExperimentResource() resource.Resource {
	return &experimentResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the experiment.")}}
}

func (r *experimentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_version_experiment"
}

func (r *experimentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines a product page optimization test: an experiment that shows up to three treatments, " +
			"created with `apple_app_store_version_experiment_treatment`, to a share of the people who visit an app's " +
			"product page. An app has one experiment at a time per platform. Submit it with the " +
			"`apple_submit_app_store_version_experiment` action when it needs review, start and stop it with the " +
			"`apple_start_app_store_version_experiment` and `apple_stop_app_store_version_experiment` actions, and apply " +
			"a treatment to the product page with `apple_promote_app_store_version_experiment_treatment`. An experiment " +
			"can't change once it has started. Destroying the resource deletes the experiment. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"app_id":   replacedString("App Store Connect ID of the app."),
			"platform": replacedString("Platform of the product page: "+docValues(storePlatforms)+".", oneOf(storePlatforms)),
			"name":     schema.StringAttribute{MarkdownDescription: "Reference name of the test, up to 64 characters.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 64)}},
			"traffic_proportion": requiredInt("Percentage of visitors who see a treatment instead of the original product page, from 1 to 100. "+
				"The treatments share it equally.", int64validator.Between(1, 100)),
			"state":           computedOnly("State of the experiment: " + docValues(experimentStates) + "."),
			"review_required": schema.BoolAttribute{MarkdownDescription: "Whether App Review must review the experiment before it can start.", Computed: true},
			"start_date":      computedOnly("When the experiment started."),
			"end_date":        computedOnly("When the experiment ended."),
			"id":              idAttribute("App Store Connect ID of the experiment."),
		},
	}
}

func (r *experimentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *experimentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *experimentModel) apply(e *gen.AppStoreVersionExperimentV2) {
	m.ID = types.StringValue(e.Id)
	a := asc.Value(e.Attributes)
	m.Platform, m.Name, m.TrafficProportion = stringValue(a.Platform), stringValue(a.Name), intValue(a.TrafficProportion)
	m.State, m.ReviewRequired = stringValue(a.State), types.BoolPointerValue(a.ReviewRequired)
	m.StartDate, m.EndDate = timeValue(a.StartDate), timeValue(a.EndDate)
	if rel := e.Relationships; rel != nil && rel.App != nil && rel.App.Data != nil {
		m.AppID = types.StringValue(rel.App.Data.Id)
	}
}

// readExperiment gets an experiment with the relationship that holds its
// app.
func readExperiment(ctx context.Context, c *asc.Client, id string) (*gen.AppStoreVersionExperimentV2, error) {
	out, err := c.AppStoreVersionExperimentsV2GetInstanceWithResponse(ctx, id, &gen.AppStoreVersionExperimentsV2GetInstanceParams{
		Include: &[]gen.AppStoreVersionExperimentsV2GetInstanceParamsInclude{gen.AppStoreVersionExperimentsV2GetInstanceParamsIncludeApp},
	})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *experimentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan experimentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppStoreVersionExperimentV2CreateRequest
	body.Data.Type = gen.AppStoreVersionExperimentV2CreateRequestDataTypeAppStoreVersionExperiments
	a := &body.Data.Attributes
	a.Name, a.Platform, a.TrafficProportion = plan.Name.ValueString(), gen.Platform(plan.Platform.ValueString()), int(plan.TrafficProportion.ValueInt64())
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.AppStoreVersionExperimentV2CreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.AppStoreVersionExperimentsV2CreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create experiment", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *experimentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state experimentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := readExperiment(ctx, r.client, state.ID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read experiment", err)
		return
	}
	state.apply(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *experimentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan experimentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppStoreVersionExperimentV2UpdateRequest
	body.Data.Type, body.Data.Id = gen.AppStoreVersionExperimentV2UpdateRequestDataTypeAppStoreVersionExperiments, plan.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.Name, a.TrafficProportion = plan.Name.ValueStringPointer(), new(int(plan.TrafficProportion.ValueInt64()))
	out, err := r.client.AppStoreVersionExperimentsV2UpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update experiment", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *experimentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state experimentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionExperimentsV2DeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete experiment", err)
	}
}

// ---- data.apple_app_store_version_experiments ----

type experimentData struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Platform          types.String `tfsdk:"platform"`
	TrafficProportion types.Int64  `tfsdk:"traffic_proportion"`
	State             types.String `tfsdk:"state"`
}

type experimentsData struct {
	AppID       types.String     `tfsdk:"app_id"`
	States      types.Set        `tfsdk:"states"`
	Experiments []experimentData `tfsdk:"experiments"`
}

var newExperimentsDataSource = listDataSource("app_store_version_experiments",
	"Lists an app's product page optimization experiments.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"states": dsFilter("States to match: "+docValues(experimentStates)+".", experimentStates...),
		"experiments": listOf("The experiments.", map[string]dsschema.Attribute{
			"id":                 dsComputed("App Store Connect ID of the experiment."),
			"name":               dsComputed("Reference name of the test."),
			"platform":           dsComputed("Platform of the product page."),
			"traffic_proportion": dsschema.Int64Attribute{MarkdownDescription: "Percentage of visitors who see a treatment.", Computed: true},
			"state":              dsComputed("State of the experiment."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *experimentsData, diags *diag.Diagnostics) {
		params := &gen.AppsAppStoreVersionExperimentsV2GetToManyRelatedParams{
			Limit:       new(listAll),
			FilterState: filterParam[gen.AppsAppStoreVersionExperimentsV2GetToManyRelatedParamsFilterState](ctx, m.States, diags),
		}
		if diags.HasError() {
			return
		}
		out, err := c.AppsAppStoreVersionExperimentsV2GetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), params)
		var all []gen.AppStoreVersionExperimentV2
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AppStoreVersionExperimentsV2Response) []gen.AppStoreVersionExperimentV2 { return p.Data },
				func(p *gen.AppStoreVersionExperimentsV2Response) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list experiments", err)
			return
		}
		m.Experiments = []experimentData{}
		for _, e := range all {
			a := asc.Value(e.Attributes)
			m.Experiments = append(m.Experiments, experimentData{
				ID: types.StringValue(e.Id), Name: stringValue(a.Name), Platform: stringValue(a.Platform),
				TrafficProportion: intValue(a.TrafficProportion), State: stringValue(a.State),
			})
		}
	})

// ---- apple_app_store_version_experiment_treatment ----

var (
	_ resource.ResourceWithConfigure   = &treatmentResource{}
	_ resource.ResourceWithImportState = &treatmentResource{}
	_ resource.ResourceWithIdentity    = &treatmentResource{}
)

type treatmentResource struct {
	resourceIdentity
	client *asc.Client
}

type treatmentModel struct {
	ExperimentID types.String `tfsdk:"experiment_id"`
	Name         types.String `tfsdk:"name"`
	AppIconName  types.String `tfsdk:"app_icon_name"`
	PromotedDate types.String `tfsdk:"promoted_date"`
	ID           types.String `tfsdk:"id"`
}

func newTreatmentResource() resource.Resource {
	return &treatmentResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the treatment.")}}
}

func (r *treatmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_version_experiment_treatment"
}

func (r *treatmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a treatment to a product page optimization experiment: an alternative version of the " +
			"product page. An experiment has up to three. A treatment shows the original icon, screenshots and previews " +
			"unless it has its own: an alternate app icon from the app's binary, or placements on an " +
			"`apple_app_store_version_experiment_treatment_localization`. A treatment can't change once its experiment " +
			"has started. Destroying the resource deletes the treatment. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"experiment_id": replacedString("App Store Connect ID of the experiment, e.g. `apple_app_store_version_experiment.example.id`."),
			"name":          schema.StringAttribute{MarkdownDescription: "Name of the treatment.", Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"app_icon_name": optionalString("Name of an alternate app icon in the app's binary that the treatment shows.", stringvalidator.LengthAtLeast(1)),
			"promoted_date": computedOnly("When the treatment was applied to the product page."),
			"id":            idAttribute("App Store Connect ID of the treatment."),
		},
	}
}

func (r *treatmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *treatmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *treatmentModel) apply(t *gen.AppStoreVersionExperimentTreatment) {
	m.ID = types.StringValue(t.Id)
	a := asc.Value(t.Attributes)
	m.Name, m.AppIconName, m.PromotedDate = stringValue(a.Name), stringValue(a.AppIconName), timeValue(a.PromotedDate)
	if rel := t.Relationships; rel != nil && rel.AppStoreVersionExperimentV2 != nil && rel.AppStoreVersionExperimentV2.Data != nil {
		m.ExperimentID = types.StringValue(rel.AppStoreVersionExperimentV2.Data.Id)
	}
}

func (r *treatmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan treatmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppStoreVersionExperimentTreatmentCreateRequest
	body.Data.Type = gen.AppStoreVersionExperimentTreatmentCreateRequestDataTypeAppStoreVersionExperimentTreatments
	body.Data.Attributes.Name, body.Data.Attributes.AppIconName = plan.Name.ValueString(), knownString(plan.AppIconName)
	e := asc.Alloc(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).AppStoreVersionExperimentV2).Data)
	e.Id, e.Type = plan.ExperimentID.ValueString(), gen.AppStoreVersionExperimentTreatmentCreateRequestDataRelationshipsAppStoreVersionExperimentV2DataTypeAppStoreVersionExperiments
	out, err := r.client.AppStoreVersionExperimentTreatmentsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create treatment", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *treatmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state treatmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionExperimentTreatmentsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppStoreVersionExperimentTreatmentsGetInstanceParams{
		Include: &[]gen.AppStoreVersionExperimentTreatmentsGetInstanceParamsInclude{gen.AppStoreVersionExperimentTreatmentsGetInstanceParamsIncludeAppStoreVersionExperimentV2},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read treatment", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sends raw JSON so that a removed icon name is sent as null.
func (r *treatmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan treatmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "appStoreVersionExperimentTreatments", "id": plan.ID.ValueString(),
		"attributes": map[string]any{"name": plan.Name.ValueString(), "appIconName": knownString(plan.AppIconName)},
	}})
	if err == nil {
		out, e := r.client.AppStoreVersionExperimentTreatmentsUpdateInstanceWithBodyWithResponse(ctx, plan.ID.ValueString(), "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON200.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update treatment", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *treatmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state treatmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionExperimentTreatmentsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete treatment", err)
	}
}

// ---- apple_app_store_version_experiment_treatment_localization ----

var (
	_ resource.ResourceWithConfigure   = &treatmentLocalizationResource{}
	_ resource.ResourceWithImportState = &treatmentLocalizationResource{}
	_ resource.ResourceWithIdentity    = &treatmentLocalizationResource{}
)

type treatmentLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type treatmentLocalizationModel struct {
	TreatmentID types.String `tfsdk:"treatment_id"`
	Locale      types.String `tfsdk:"locale"`
	ID          types.String `tfsdk:"id"`
}

// resolveTreatmentLocalization maps "<treatment_id>/<locale>" to the
// localization's ID, and returns any other key as the ID.
func resolveTreatmentLocalization(ctx context.Context, c *asc.Client, key string) (string, error) {
	treatmentID, locale, ok := strings.Cut(key, "/")
	if !ok {
		return key, nil
	}
	out, err := c.AppStoreVersionExperimentTreatmentsAppStoreVersionExperimentTreatmentLocalizationsGetToManyRelatedWithResponse(ctx, treatmentID,
		&gen.AppStoreVersionExperimentTreatmentsAppStoreVersionExperimentTreatmentLocalizationsGetToManyRelatedParams{
			FilterLocale: &[]string{locale}, Limit: new(listAll),
		})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		return "", errNoMatch
	}
	if err != nil {
		return "", err
	}
	for _, l := range out.JSON200.Data {
		if asc.Value(asc.Value(l.Attributes).Locale) == locale {
			return l.Id, nil
		}
	}
	return "", errNoMatch
}

func newTreatmentLocalizationResource() resource.Resource {
	return &treatmentLocalizationResource{resourceIdentity: resourceIdentity{
		attrs:   idIdentity("App Store Connect ID of the treatment localization."),
		resolve: resolveTreatmentLocalization,
		keyName: "treatment ID and locale",
	}}
}

func (r *treatmentLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_version_experiment_treatment_localization"
}

func (r *treatmentLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a locale to a product page optimization treatment, to give it screenshots and previews " +
			"of its own: use the resource's `id` as the `experiment_treatment_localization_id` of " +
			"`apple_app_asset_library_placement`. A locale without a localization shows the original product page's " +
			"media. Destroying the resource deletes the localization with its placements. Import it by ID or with " +
			"`<treatment_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"treatment_id": replacedString("App Store Connect ID of the treatment, e.g. `apple_app_store_version_experiment_treatment.example.id`."),
			"locale":       replacedString("Locale, e.g. `en-US`.", localeValidator),
			"id":           idAttribute("App Store Connect ID of the treatment localization."),
		},
	}
}

func (r *treatmentLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *treatmentLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *treatmentLocalizationModel) apply(l *gen.AppStoreVersionExperimentTreatmentLocalization) {
	m.ID, m.Locale = types.StringValue(l.Id), stringValue(asc.Value(l.Attributes).Locale)
	if rel := l.Relationships; rel != nil && rel.AppStoreVersionExperimentTreatment != nil && rel.AppStoreVersionExperimentTreatment.Data != nil {
		m.TreatmentID = types.StringValue(rel.AppStoreVersionExperimentTreatment.Data.Id)
	}
}

func (r *treatmentLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan treatmentLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppStoreVersionExperimentTreatmentLocalizationCreateRequest
	body.Data.Type = gen.AppStoreVersionExperimentTreatmentLocalizationCreateRequestDataTypeAppStoreVersionExperimentTreatmentLocalizations
	body.Data.Attributes.Locale = plan.Locale.ValueString()
	t := &body.Data.Relationships.AppStoreVersionExperimentTreatment.Data
	t.Id, t.Type = plan.TreatmentID.ValueString(), gen.AppStoreVersionExperimentTreatmentLocalizationCreateRequestDataRelationshipsAppStoreVersionExperimentTreatmentDataTypeAppStoreVersionExperimentTreatments
	out, err := r.client.AppStoreVersionExperimentTreatmentLocalizationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create treatment localization", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *treatmentLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state treatmentLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionExperimentTreatmentLocalizationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppStoreVersionExperimentTreatmentLocalizationsGetInstanceParams{
		Include: &[]gen.AppStoreVersionExperimentTreatmentLocalizationsGetInstanceParamsInclude{
			gen.AppStoreVersionExperimentTreatmentLocalizationsGetInstanceParamsIncludeAppStoreVersionExperimentTreatment,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read treatment localization", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument replaces the resource.
func (r *treatmentLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *treatmentLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state treatmentLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionExperimentTreatmentLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete treatment localization", err)
	}
}

// ---- experiment actions ----

type experimentActionModel struct {
	ExperimentID types.String `tfsdk:"experiment_id"`
}

var experimentActionAttribute = actionString("App Store Connect ID of the experiment, e.g. `apple_app_store_version_experiment.example.id`.")

// setExperimentStarted starts or stops an experiment.
func setExperimentStarted(ctx context.Context, c *asc.Client, id string, started bool) error {
	var body gen.AppStoreVersionExperimentV2UpdateRequest
	body.Data.Type, body.Data.Id = gen.AppStoreVersionExperimentV2UpdateRequestDataTypeAppStoreVersionExperiments, id
	asc.Alloc(&body.Data.Attributes).Started = new(started)
	out, err := c.AppStoreVersionExperimentsV2UpdateInstanceWithResponse(ctx, id, body)
	return asc.Err(out, err)
}

var newStartExperimentAction = newAction("start_app_store_version_experiment",
	"Starts a product page optimization experiment, which then shows its treatments to visitors for up to 90 days. "+
		"An experiment that needs review must be approved first, and it can't change once it has started.",
	map[string]actionschema.Attribute{"experiment_id": experimentActionAttribute},
	func(ctx context.Context, c *asc.Client, m *experimentActionModel) (string, error) {
		return "Started experiment " + m.ExperimentID.ValueString(), setExperimentStarted(ctx, c, m.ExperimentID.ValueString(), true)
	})

var newStopExperimentAction = newAction("stop_app_store_version_experiment",
	"Stops a running product page optimization experiment. Everyone then sees the original product page.",
	map[string]actionschema.Attribute{"experiment_id": experimentActionAttribute},
	func(ctx context.Context, c *asc.Client, m *experimentActionModel) (string, error) {
		return "Stopped experiment " + m.ExperimentID.ValueString(), setExperimentStarted(ctx, c, m.ExperimentID.ValueString(), false)
	})

type submitExperimentModel struct {
	ExperimentID types.String `tfsdk:"experiment_id"`
	Submit       types.Bool   `tfsdk:"submit"`
}

var newSubmitExperimentAction = newAction("submit_app_store_version_experiment",
	"Submits a product page optimization experiment to App Review, by adding it to the review submission of its app "+
		"and platform that is being prepared, or a new one. An experiment needs review when its treatments have "+
		"metadata of their own.",
	map[string]actionschema.Attribute{
		"experiment_id": experimentActionAttribute,
		"submit": actionschema.BoolAttribute{
			MarkdownDescription: "Whether to submit the review submission. Set it to `false` to add other items to the same submission. Defaults to `true`.",
			Optional:            true,
		},
	},
	func(ctx context.Context, c *asc.Client, m *submitExperimentModel) (string, error) {
		id := m.ExperimentID.ValueString()
		e, err := readExperiment(ctx, c, id)
		if err != nil {
			return "", err
		}
		var em experimentModel
		em.apply(e)
		if em.AppID.IsNull() {
			return "", errors.New("the API did not return the experiment's app")
		}
		if state := em.State.ValueString(); state != string(gen.AppStoreVersionExperimentV2AttributesStatePREPAREFORSUBMISSION) &&
			state != string(gen.AppStoreVersionExperimentV2AttributesStateREJECTED) {
			return "", fmt.Errorf("experiment %s is %s, and can't be submitted", id, state)
		}
		submissionID, err := openSubmission(ctx, c, em.AppID.ValueString(), gen.Platform(em.Platform.ValueString()))
		if err != nil {
			return "", err
		}
		var item gen.ReviewSubmissionItemCreateRequest
		item.Data.Type = gen.ReviewSubmissionItemCreateRequestDataTypeReviewSubmissionItems
		item.Data.Relationships.ReviewSubmission.Data.Id = submissionID
		item.Data.Relationships.ReviewSubmission.Data.Type = gen.ReviewSubmissionItemCreateRequestDataRelationshipsReviewSubmissionDataTypeReviewSubmissions
		d := asc.Alloc(&asc.Alloc(&item.Data.Relationships.AppStoreVersionExperimentV2).Data)
		d.Id, d.Type = id, gen.ReviewSubmissionItemCreateRequestDataRelationshipsAppStoreVersionExperimentV2DataTypeAppStoreVersionExperiments
		out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		if !m.Submit.IsNull() && !m.Submit.ValueBool() {
			return "Added experiment " + id + " to review submission " + submissionID, nil
		}
		return "Submitted experiment " + id + " for review", updateSubmission(ctx, c, submissionID, true)
	})

type promoteTreatmentModel struct {
	VersionID   types.String `tfsdk:"version_id"`
	TreatmentID types.String `tfsdk:"treatment_id"`
}

var newPromoteTreatmentAction = newAction("promote_app_store_version_experiment_treatment",
	"Applies a product page optimization treatment to an App Store version, so that everyone sees it on the product "+
		"page. Applying a treatment while its experiment is running ends the experiment.",
	map[string]actionschema.Attribute{
		"version_id":   versionActionAttribute,
		"treatment_id": actionString("App Store Connect ID of the treatment, e.g. `apple_app_store_version_experiment_treatment.example.id`."),
	},
	func(ctx context.Context, c *asc.Client, m *promoteTreatmentModel) (string, error) {
		var body gen.AppStoreVersionPromotionCreateRequest
		body.Data.Type = gen.AppStoreVersionPromotionCreateRequestDataTypeAppStoreVersionPromotions
		rel := &body.Data.Relationships
		rel.AppStoreVersion.Data.Id = m.VersionID.ValueString()
		rel.AppStoreVersion.Data.Type = gen.AppStoreVersionPromotionCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		rel.AppStoreVersionExperimentTreatment.Data.Id = m.TreatmentID.ValueString()
		rel.AppStoreVersionExperimentTreatment.Data.Type = gen.AppStoreVersionPromotionCreateRequestDataRelationshipsAppStoreVersionExperimentTreatmentDataTypeAppStoreVersionExperimentTreatments
		out, err := c.AppStoreVersionPromotionsCreateInstanceWithResponse(ctx, body)
		return "Applied treatment " + m.TreatmentID.ValueString() + " to version " + m.VersionID.ValueString(), asc.Err(out, err)
	})
