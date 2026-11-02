package provider

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// An App Store version is the metadata and build submitted to App Review.
// A platform has at most one version in preparation; once a version is
// submitted and released it can no longer be changed, so the next version
// is a new resource.

// editableVersionStates are the version states whose metadata can be edited
// ("App and submission statuses",
// https://developer.apple.com/help/app-store-connect/reference/app-information/app-and-submission-statuses).
var editableVersionStates = enum(gen.AppVersionStatePREPAREFORSUBMISSION, gen.AppVersionStateREADYFORREVIEW,
	gen.AppVersionStateWAITINGFORREVIEW, gen.AppVersionStateREJECTED, gen.AppVersionStateMETADATAREJECTED,
	gen.AppVersionStateDEVELOPERREJECTED, gen.AppVersionStateINVALIDBINARY)

var (
	storePlatforms = enum(gen.PlatformIOS, gen.PlatformMACOS, gen.PlatformTVOS, gen.PlatformVISIONOS)
	releaseTypes   = enum(gen.AppStoreVersionAttributesReleaseTypeMANUAL, gen.AppStoreVersionAttributesReleaseTypeAFTERAPPROVAL,
		gen.AppStoreVersionAttributesReleaseTypeSCHEDULED)
	reviewTypes = enum(gen.AppStoreVersionAttributesReviewTypeAPPSTORE, gen.AppStoreVersionAttributesReviewTypeNOTARIZATION)
)

// utcTimeRegexp matches the RFC 3339 UTC times that App Store Connect
// returns, so configured times read back unchanged.
var utcTimeRegexp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

// versionEditable reports whether a version exists and can be edited.
func versionEditable(ctx context.Context, c *asc.Client, id string) (bool, error) {
	out, err := c.AppStoreVersionsGetInstanceWithResponse(ctx, id, nil)
	if err := asc.Err(out, err); err != nil {
		if asc.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return slices.Contains(editableVersionStates, string(asc.Value(asc.Value(out.JSON200.Data.Attributes).AppVersionState))), nil
}

// ---- apple_app_store_version ----

var (
	_ resource.ResourceWithConfigure   = &appStoreVersionResource{}
	_ resource.ResourceWithImportState = &appStoreVersionResource{}
	_ resource.ResourceWithIdentity    = &appStoreVersionResource{}
	_ resource.ResourceWithModifyPlan  = &appStoreVersionResource{}
)

type appStoreVersionResource struct {
	resourceIdentity
	client *asc.Client
}

type appStoreVersionModel struct {
	AppID               types.String `tfsdk:"app_id"`
	Platform            types.String `tfsdk:"platform"`
	VersionString       types.String `tfsdk:"version_string"`
	Copyright           types.String `tfsdk:"copyright"`
	ReleaseType         types.String `tfsdk:"release_type"`
	EarliestReleaseDate types.String `tfsdk:"earliest_release_date"`
	ReviewType          types.String `tfsdk:"review_type"`
	BuildID             types.String `tfsdk:"build_id"`
	ID                  types.String `tfsdk:"id"`
	State               types.String `tfsdk:"state"`
	CreatedDate         types.String `tfsdk:"created_date"`
}

func newAppStoreVersionResource() resource.Resource {
	return &appStoreVersionResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the version.")}}
}

func (r *appStoreVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_store_version"
}

func computedOnly(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
}

// optionalComputedFresh is an optional attribute that App Store Connect
// defaults, read again whenever the resource changes.
func optionalComputedFresh(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Optional: true, Computed: true, Validators: validators}
}

func (r *appStoreVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an App Store version of an app: its version number, release options and build. " +
			"A platform has one version in preparation at a time, and creating this resource while there is one manages " +
			"it. Once a version is submitted and approved it can't be edited, so changing it then creates the next " +
			"version, and destroying the resource deletes the version only if it was never submitted. Submit it with " +
			"`apple_submit_for_review`. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"app_id":         replacedString("App Store Connect ID of the app."),
			"platform":       replacedString("Platform: "+docValues(storePlatforms)+".", oneOf(storePlatforms)),
			"version_string": schema.StringAttribute{MarkdownDescription: "Version number shown on the App Store, e.g. `1.2.0`.", Required: true},
			"copyright":      optionalString("Copyright line, e.g. `2026 Example Inc.`"),
			"release_type": optionalComputedFresh("How the version is released after approval: "+docValues(releaseTypes)+
				". `SCHEDULED` releases it at `earliest_release_date`.", oneOf(releaseTypes)),
			"earliest_release_date": optionalComputedFresh("Release time for a `SCHEDULED` release, in UTC, e.g. "+
				"`2026-12-01T16:00:00Z`. Removing it leaves the time set.",
				stringvalidator.RegexMatches(utcTimeRegexp, "must be an RFC 3339 time in UTC, e.g. `2026-12-01T16:00:00Z`")),
			"review_type": optionalComputedFresh("Review type: "+docValues(reviewTypes)+". `NOTARIZATION` is for apps "+
				"distributed outside the App Store in the EU.", oneOf(reviewTypes)),
			"build_id": optionalComputedFresh("App Store Connect ID of the build to submit, e.g. `data.apple_build.latest.id`. " +
				"It must be a valid build of the app for the platform. Removing it leaves the build attached."),
			"id":           idAttribute("App Store Connect ID of the version."),
			"state":        computedOnly("State of the version, e.g. `PREPARE_FOR_SUBMISSION` or `READY_FOR_DISTRIBUTION`."),
			"created_date": computedOnly("When the version was created."),
		},
	}
}

func (r *appStoreVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *appStoreVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ModifyPlan replaces a version that can no longer be edited when its
// configuration changes: the change goes to a new version.
func (r *appStoreVersionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state appStoreVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || slices.Contains(editableVersionStates, state.State.ValueString()) {
		return
	}
	changed := map[string][2]types.String{
		"version_string": {plan.VersionString, state.VersionString}, "copyright": {plan.Copyright, state.Copyright},
		"release_type": {plan.ReleaseType, state.ReleaseType}, "earliest_release_date": {plan.EarliestReleaseDate, state.EarliestReleaseDate},
		"review_type": {plan.ReviewType, state.ReviewType}, "build_id": {plan.BuildID, state.BuildID},
	}
	var replace []path.Path
	for name, v := range changed {
		if !v[0].IsUnknown() && !v[0].Equal(v[1]) {
			replace = append(replace, path.Root(name))
		}
	}
	if len(replace) == 0 {
		return
	}
	resp.RequiresReplace = append(resp.RequiresReplace, replace...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
}

func (m *appStoreVersionModel) apply(v *gen.AppStoreVersion) {
	a := asc.Value(v.Attributes)
	m.ID, m.Platform, m.VersionString = types.StringValue(v.Id), stringValue(a.Platform), stringValue(a.VersionString)
	m.Copyright, m.ReleaseType, m.ReviewType = stringValue(a.Copyright), stringValue(a.ReleaseType), stringValue(a.ReviewType)
	m.EarliestReleaseDate, m.State, m.CreatedDate = timeValue(a.EarliestReleaseDate), stringValue(a.AppVersionState), timeValue(a.CreatedDate)
}

func parseTime(v types.String) *gen.DateTime {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v.ValueString())
	if err != nil {
		return nil
	}
	return &gen.DateTime{Time: t}
}

// editableVersion returns the app's version in preparation for platform.
func editableVersion(ctx context.Context, c *asc.Client, appID, platform string) (*gen.AppStoreVersion, error) {
	out, err := c.AppsAppStoreVersionsGetToManyRelatedWithResponse(ctx, appID, &gen.AppsAppStoreVersionsGetToManyRelatedParams{
		FilterPlatform:        &[]gen.AppsAppStoreVersionsGetToManyRelatedParamsFilterPlatform{gen.AppsAppStoreVersionsGetToManyRelatedParamsFilterPlatform(platform)},
		FilterAppVersionState: new(slicesOf[gen.AppsAppStoreVersionsGetToManyRelatedParamsFilterAppVersionState](editableVersionStates)),
		Limit:                 new(listAll),
	})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	for i := range out.JSON200.Data {
		v := &out.JSON200.Data[i]
		if slices.Contains(editableVersionStates, string(asc.Value(asc.Value(v.Attributes).AppVersionState))) {
			return v, nil
		}
	}
	return nil, errNoMatch
}

func slicesOf[T ~string](values []string) []T {
	out := make([]T, len(values))
	for i, v := range values {
		out[i] = T(v)
	}
	return out
}

func (r *appStoreVersionResource) update(ctx context.Context, id string, plan, state *appStoreVersionModel) (*gen.AppStoreVersion, error) {
	var body gen.AppStoreVersionUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppStoreVersionUpdateRequestDataTypeAppStoreVersions, id
	a := asc.Alloc(&body.Data.Attributes)
	a.VersionString = new(plan.VersionString.ValueString())
	a.Copyright = changedString(plan.Copyright, state.Copyright)
	a.EarliestReleaseDate = parseTime(plan.EarliestReleaseDate)
	if v := knownString(plan.ReleaseType); v != nil {
		a.ReleaseType = new(gen.AppStoreVersionUpdateRequestDataAttributesReleaseType(*v))
	}
	if v := knownString(plan.ReviewType); v != nil {
		a.ReviewType = new(gen.AppStoreVersionUpdateRequestDataAttributesReviewType(*v))
	}
	out, err := r.client.AppStoreVersionsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *appStoreVersionResource) create(ctx context.Context, plan *appStoreVersionModel) (*gen.AppStoreVersion, error) {
	var body gen.AppStoreVersionCreateRequest
	body.Data.Type = gen.AppStoreVersionCreateRequestDataTypeAppStoreVersions
	a := &body.Data.Attributes
	a.Platform, a.VersionString = gen.Platform(plan.Platform.ValueString()), plan.VersionString.ValueString()
	a.Copyright, a.EarliestReleaseDate = knownString(plan.Copyright), parseTime(plan.EarliestReleaseDate)
	if v := knownString(plan.ReleaseType); v != nil {
		a.ReleaseType = new(gen.AppStoreVersionCreateRequestDataAttributesReleaseType(*v))
	}
	if v := knownString(plan.ReviewType); v != nil {
		a.ReviewType = new(gen.AppStoreVersionCreateRequestDataAttributesReviewType(*v))
	}
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.AppStoreVersionCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.AppStoreVersionsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON201.Data, nil
}

// setBuild attaches the planned build when it differs from current.
func (r *appStoreVersionResource) setBuild(ctx context.Context, id string, plan types.String, current types.String) error {
	if plan.IsNull() || plan.IsUnknown() || plan.Equal(current) {
		return nil
	}
	var body gen.AppStoreVersionBuildLinkageRequest
	body.Data.Id, body.Data.Type = plan.ValueString(), gen.AppStoreVersionBuildLinkageRequestDataTypeBuilds
	out, err := r.client.AppStoreVersionsBuildUpdateToOneRelationshipWithResponse(ctx, id, body)
	return asc.Err(out, err)
}

func (r *appStoreVersionResource) readBuild(ctx context.Context, id string) (types.String, error) {
	out, err := r.client.AppStoreVersionsBuildGetToOneRelationshipWithResponse(ctx, id)
	if err := asc.Err(out, err); err != nil {
		return types.StringNull(), err
	}
	if out.JSON200 == nil || out.JSON200.Data.Id == "" {
		return types.StringNull(), nil
	}
	return types.StringValue(out.JSON200.Data.Id), nil
}

// finish attaches the planned build, then records v and its build in plan.
func (r *appStoreVersionResource) finish(ctx context.Context, plan *appStoreVersionModel, v *gen.AppStoreVersion) error {
	current, err := r.readBuild(ctx, v.Id)
	if err == nil {
		err = r.setBuild(ctx, v.Id, plan.BuildID, current)
	}
	if err == nil {
		plan.BuildID, err = r.readBuild(ctx, v.Id)
	}
	if err != nil {
		return err
	}
	plan.apply(v)
	return nil
}

func (r *appStoreVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appStoreVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	existing, err := editableVersion(ctx, r.client, plan.AppID.ValueString(), plan.Platform.ValueString())
	var v *gen.AppStoreVersion
	switch {
	case err == nil:
		var current appStoreVersionModel
		current.apply(existing)
		v, err = r.update(ctx, existing.Id, &plan, &current)
	case errors.Is(err, errNoMatch):
		v, err = r.create(ctx, &plan)
	}
	if err == nil {
		err = r.finish(ctx, &plan, v)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create App Store version", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appStoreVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state appStoreVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppStoreVersionsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppStoreVersionsGetInstanceParams{
		Include: &[]gen.AppStoreVersionsGetInstanceParamsInclude{gen.AppStoreVersionsGetInstanceParamsIncludeApp},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		state.BuildID, err = r.readBuild(ctx, state.ID.ValueString())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read App Store version", err)
		return
	}
	state.apply(&out.JSON200.Data)
	if app := versionAppID(&out.JSON200.Data); !app.IsNull() {
		state.AppID = app
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func versionAppID(v *gen.AppStoreVersion) types.String {
	if rel := v.Relationships; rel != nil && rel.App != nil && rel.App.Data != nil {
		return types.StringValue(rel.App.Data.Id)
	}
	return types.StringNull()
}

func (r *appStoreVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state appStoreVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.update(ctx, state.ID.ValueString(), &plan, &state)
	if err == nil {
		err = r.finish(ctx, &plan, v)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update App Store version", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes a version that was never submitted, and otherwise leaves
// it: a version that has been submitted stays in the app's history.
func (r *appStoreVersionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state appStoreVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.client.AppStoreVersionsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(got, err)
	if asc.IsNotFound(err) {
		return
	}
	if err == nil && asc.Value(asc.Value(got.JSON200.Data.Attributes).AppVersionState) == gen.AppVersionStatePREPAREFORSUBMISSION {
		out, e := r.client.AppStoreVersionsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
		if err = asc.Err(out, e); asc.IsNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "delete App Store version", err)
	}
}
