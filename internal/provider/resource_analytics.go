package provider

import (
	"context"

	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Analytics reports: a report request makes Apple generate an app's
// reports; data sources then walk from the request to its reports, their
// instances and the segments to download
// (https://developer.apple.com/documentation/appstoreconnectapi/downloading-analytics-reports).

var (
	analyticsAccessTypes = enum(gen.AnalyticsReportRequestCreateRequestDataAttributesAccessTypeONGOING,
		gen.AnalyticsReportRequestCreateRequestDataAttributesAccessTypeONETIMESNAPSHOT)
	analyticsCategories    = []string{"APP_USAGE", "APP_STORE_ENGAGEMENT", "COMMERCE", "FRAMEWORK_USAGE", "PERFORMANCE"}
	analyticsGranularities = []string{"DAILY", "WEEKLY", "MONTHLY"}
)

// ---- apple_analytics_report_request ----

var (
	_ resource.ResourceWithConfigure   = &analyticsRequestResource{}
	_ resource.ResourceWithImportState = &analyticsRequestResource{}
	_ resource.ResourceWithIdentity    = &analyticsRequestResource{}
	_ resource.ResourceWithModifyPlan  = &analyticsRequestResource{}
)

type analyticsRequestResource struct {
	resourceIdentity
	client *asc.Client
}

type analyticsRequestModel struct {
	AppID      types.String `tfsdk:"app_id"`
	AccessType types.String `tfsdk:"access_type"`
	Stopped    types.Bool   `tfsdk:"stopped_due_to_inactivity"`
	ID         types.String `tfsdk:"id"`
}

func newAnalyticsRequestResource() resource.Resource {
	return &analyticsRequestResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "id", description: "App Store Connect ID of the analytics report request.", state: "id"},
	}}}
}

func (r *analyticsRequestResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_analytics_report_request"
}

func (r *analyticsRequestResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Requests the analytics reports of an app. An `ONGOING` request generates reports every " +
			"day; a `ONE_TIME_SNAPSHOT` generates historical data once. The first reports take one to two days. App " +
			"Store Connect stops a request whose reports nobody downloads for a long time; the resource then replaces " +
			"it, because only a new request resumes the reports. Find the reports with `data.apple_analytics_reports`. Destroying the resource deletes the " +
			"request. Import it with `<app_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app. Changing it creates a new request."),
			"access_type": replacedString("Kind of request: "+docValues(analyticsAccessTypes)+". Changing it creates a new request.",
				oneOf(analyticsAccessTypes)),
			"stopped_due_to_inactivity": schema.BoolAttribute{
				MarkdownDescription: "Whether App Store Connect stopped the request because its reports were not downloaded. A stopped " +
					"request is replaced.",
				Computed: true,
			},
			"id": idAttribute("App Store Connect ID of the analytics report request."),
		},
	}
}

func (r *analyticsRequestResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *analyticsRequestResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ModifyPlan replaces a request that App Store Connect stopped: a new
// request is the only way to resume the reports.
func (r *analyticsRequestResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var stopped types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("stopped_due_to_inactivity"), &stopped)...)
	if !stopped.ValueBool() {
		return
	}
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("stopped_due_to_inactivity"))
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("stopped_due_to_inactivity"), types.BoolUnknown())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
}

func (m *analyticsRequestModel) apply(q *gen.AnalyticsReportRequest) {
	a := asc.Value(q.Attributes)
	m.ID, m.AccessType, m.Stopped = types.StringValue(q.Id), stringValue(a.AccessType), types.BoolValue(asc.Value(a.StoppedDueToInactivity))
}

func (r *analyticsRequestResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan analyticsRequestModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AnalyticsReportRequestCreateRequest
	body.Data.Type = gen.AnalyticsReportRequestCreateRequestDataTypeAnalyticsReportRequests
	body.Data.Attributes.AccessType = gen.AnalyticsReportRequestCreateRequestDataAttributesAccessType(plan.AccessType.ValueString())
	app := &body.Data.Relationships.App.Data
	app.Id, app.Type = plan.AppID.ValueString(), gen.AnalyticsReportRequestCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.AnalyticsReportRequestsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create analytics report request", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *analyticsRequestResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state analyticsRequestModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AnalyticsReportRequestsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read analytics report request", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every attribute replaces the request.
func (r *analyticsRequestResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *analyticsRequestResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state analyticsRequestModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AnalyticsReportRequestsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete analytics report request", err)
	}
}

// ---- data.apple_analytics_report_requests ----

type analyticsRequestData struct {
	ID         types.String `tfsdk:"id"`
	AccessType types.String `tfsdk:"access_type"`
	Stopped    types.Bool   `tfsdk:"stopped_due_to_inactivity"`
}

type analyticsRequestsData struct {
	AppID       types.String           `tfsdk:"app_id"`
	AccessTypes types.Set              `tfsdk:"access_types"`
	Requests    []analyticsRequestData `tfsdk:"requests"`
}

var newAnalyticsRequestsDataSource = listDataSource("analytics_report_requests",
	"Lists the analytics report requests of an app.",
	map[string]dsschema.Attribute{
		"app_id":       dsRequired("App Store Connect ID of the app."),
		"access_types": dsFilter("Kinds of request to list.", analyticsAccessTypes...),
		"requests": listOf("The requests.", map[string]dsschema.Attribute{
			"id":                        dsComputed("App Store Connect ID of the request."),
			"access_type":               dsComputed("Kind of request: " + docValues(analyticsAccessTypes) + "."),
			"stopped_due_to_inactivity": dsBool("Whether App Store Connect stopped the request because its reports were not downloaded."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *analyticsRequestsData, diags *diag.Diagnostics) {
		out, err := c.AppsAnalyticsReportRequestsGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsAnalyticsReportRequestsGetToManyRelatedParams{
			FilterAccessType: filterParam[gen.AppsAnalyticsReportRequestsGetToManyRelatedParamsFilterAccessType](ctx, m.AccessTypes, diags), Limit: new(listAll),
		})
		var all []gen.AnalyticsReportRequest
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AnalyticsReportRequestsResponse) []gen.AnalyticsReportRequest { return p.Data },
				func(p *gen.AnalyticsReportRequestsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "app", err)
			return
		}
		m.Requests = []analyticsRequestData{}
		for _, q := range all {
			a := asc.Value(q.Attributes)
			if passes(ctx, m.AccessTypes, string(asc.Value(a.AccessType)), diags) {
				m.Requests = append(m.Requests, analyticsRequestData{
					ID: types.StringValue(q.Id), AccessType: stringValue(a.AccessType), Stopped: types.BoolValue(asc.Value(a.StoppedDueToInactivity)),
				})
			}
		}
	})

// ---- data.apple_analytics_reports ----

type analyticsReportData struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Category types.String `tfsdk:"category"`
}

type analyticsReportsData struct {
	RequestID  types.String          `tfsdk:"request_id"`
	Names      types.Set             `tfsdk:"names"`
	Categories types.Set             `tfsdk:"categories"`
	Reports    []analyticsReportData `tfsdk:"reports"`
}

var newAnalyticsReportsDataSource = listDataSource("analytics_reports",
	"Lists the reports App Store Connect generated for an analytics report request. The list is empty until the first "+
		"reports are ready, one to two days after the request.",
	map[string]dsschema.Attribute{
		"request_id": dsRequired("App Store Connect ID of the request, e.g. `apple_analytics_report_request.example.id`."),
		"names":      dsFilter("Names of the reports to list, e.g. `App Sessions Standard`."),
		"categories": dsFilter("Categories to list.", analyticsCategories...),
		"reports": listOf("The reports.", map[string]dsschema.Attribute{
			"id":       dsComputed("App Store Connect ID of the report."),
			"name":     dsComputed("Name of the report."),
			"category": dsComputed("Category of the report: " + docValues(analyticsCategories) + "."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *analyticsReportsData, diags *diag.Diagnostics) {
		out, err := c.AnalyticsReportRequestsReportsGetToManyRelatedWithResponse(ctx, m.RequestID.ValueString(), &gen.AnalyticsReportRequestsReportsGetToManyRelatedParams{
			FilterName:     filterParam[string](ctx, m.Names, diags),
			FilterCategory: filterParam[gen.AnalyticsReportRequestsReportsGetToManyRelatedParamsFilterCategory](ctx, m.Categories, diags),
			Limit:          new(listAll),
		})
		var all []gen.AnalyticsReport
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AnalyticsReportsResponse) []gen.AnalyticsReport { return p.Data },
				func(p *gen.AnalyticsReportsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "analytics report request", err)
			return
		}
		m.Reports = []analyticsReportData{}
		for _, report := range all {
			a := asc.Value(report.Attributes)
			if passes(ctx, m.Names, asc.Value(a.Name), diags) && passes(ctx, m.Categories, string(asc.Value(a.Category)), diags) {
				m.Reports = append(m.Reports, analyticsReportData{ID: types.StringValue(report.Id), Name: stringValue(a.Name), Category: stringValue(a.Category)})
			}
		}
	})

// ---- data.apple_analytics_report_instances ----

type analyticsInstanceData struct {
	ID             types.String `tfsdk:"id"`
	Granularity    types.String `tfsdk:"granularity"`
	ProcessingDate types.String `tfsdk:"processing_date"`
}

type analyticsInstancesData struct {
	ReportID        types.String            `tfsdk:"report_id"`
	Granularities   types.Set               `tfsdk:"granularities"`
	ProcessingDates types.Set               `tfsdk:"processing_dates"`
	Instances       []analyticsInstanceData `tfsdk:"instances"`
}

var newAnalyticsInstancesDataSource = listDataSource("analytics_report_instances",
	"Lists the instances of an analytics report: one per granularity and processing date, each a set of files to download.",
	map[string]dsschema.Attribute{
		"report_id":        dsRequired("App Store Connect ID of the report; see `data.apple_analytics_reports`."),
		"granularities":    dsFilter("Granularities to list.", analyticsGranularities...),
		"processing_dates": dsFilter("Processing dates to list, e.g. `2026-11-16`."),
		"instances": listOf("The instances.", map[string]dsschema.Attribute{
			"id":              dsComputed("App Store Connect ID of the instance."),
			"granularity":     dsComputed("Granularity of the data: " + docValues(analyticsGranularities) + "."),
			"processing_date": dsComputed("Day the data became available."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *analyticsInstancesData, diags *diag.Diagnostics) {
		out, err := c.AnalyticsReportsInstancesGetToManyRelatedWithResponse(ctx, m.ReportID.ValueString(), &gen.AnalyticsReportsInstancesGetToManyRelatedParams{
			FilterGranularity:    filterParam[gen.AnalyticsReportsInstancesGetToManyRelatedParamsFilterGranularity](ctx, m.Granularities, diags),
			FilterProcessingDate: filterParam[string](ctx, m.ProcessingDates, diags),
			Limit:                new(listAll),
		})
		var all []gen.AnalyticsReportInstance
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AnalyticsReportInstancesResponse) []gen.AnalyticsReportInstance { return p.Data },
				func(p *gen.AnalyticsReportInstancesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "analytics report", err)
			return
		}
		m.Instances = []analyticsInstanceData{}
		for _, instance := range all {
			a := asc.Value(instance.Attributes)
			date := dateValue(a.ProcessingDate)
			if passes(ctx, m.Granularities, string(asc.Value(a.Granularity)), diags) && passes(ctx, m.ProcessingDates, date.ValueString(), diags) {
				m.Instances = append(m.Instances, analyticsInstanceData{ID: types.StringValue(instance.Id), Granularity: stringValue(a.Granularity), ProcessingDate: date})
			}
		}
	})

// ---- data.apple_analytics_report_segments ----

type analyticsSegmentData struct {
	ID          types.String `tfsdk:"id"`
	URL         types.String `tfsdk:"url"`
	Checksum    types.String `tfsdk:"checksum"`
	SizeInBytes types.Int64  `tfsdk:"size_in_bytes"`
}

type analyticsSegmentsData struct {
	InstanceID types.String           `tfsdk:"instance_id"`
	Segments   []analyticsSegmentData `tfsdk:"segments"`
}

var newAnalyticsSegmentsDataSource = listDataSource("analytics_report_segments",
	"Lists the segments of an analytics report instance: the files that hold its data, with the URLs they download from.",
	map[string]dsschema.Attribute{
		"instance_id": dsRequired("App Store Connect ID of the instance; see `data.apple_analytics_report_instances`."),
		"segments": listOf("The segments.", map[string]dsschema.Attribute{
			"id":            dsComputed("App Store Connect ID of the segment."),
			"url":           dsSensitive("URL the file downloads from."),
			"checksum":      dsComputed("Checksum of the file."),
			"size_in_bytes": dsInt("Size of the file in bytes."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *analyticsSegmentsData, diags *diag.Diagnostics) {
		out, err := c.AnalyticsReportInstancesSegmentsGetToManyRelatedWithResponse(ctx, m.InstanceID.ValueString(),
			&gen.AnalyticsReportInstancesSegmentsGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.AnalyticsReportSegment
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AnalyticsReportSegmentsResponse) []gen.AnalyticsReportSegment { return p.Data },
				func(p *gen.AnalyticsReportSegmentsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "analytics report instance", err)
			return
		}
		m.Segments = []analyticsSegmentData{}
		for _, segment := range all {
			a := asc.Value(segment.Attributes)
			m.Segments = append(m.Segments, analyticsSegmentData{
				ID: types.StringValue(segment.Id), URL: stringValue(a.Url), Checksum: stringValue(a.Checksum), SizeInBytes: intValue(a.SizeInBytes),
			})
		}
	})

// ---- data.apple_build_bundle_file_sizes ----

type bundleFileSizeData struct {
	DeviceModel   types.String `tfsdk:"device_model"`
	OSVersion     types.String `tfsdk:"os_version"`
	DownloadBytes types.Int64  `tfsdk:"download_bytes"`
	InstallBytes  types.Int64  `tfsdk:"install_bytes"`
}

type bundleFileSizesData struct {
	BuildBundleID types.String         `tfsdk:"build_bundle_id"`
	FileSizes     []bundleFileSizeData `tfsdk:"file_sizes"`
}

var newBuildBundleFileSizesDataSource = listDataSource("build_bundle_file_sizes",
	"Lists the download and install sizes of a build bundle per device model and OS version, for example to check an "+
		"app's size before release.",
	map[string]dsschema.Attribute{
		"build_bundle_id": dsRequired("App Store Connect ID of the build bundle; see `data.apple_build_bundles`."),
		"file_sizes": listOf("The sizes.", map[string]dsschema.Attribute{
			"device_model":   dsComputed("Device model the size is for, e.g. `iPhone18,1`, or `Universal`."),
			"os_version":     dsComputed("OS version the size is for, or `Universal`."),
			"download_bytes": dsInt("Size of the download in bytes."),
			"install_bytes":  dsInt("Size once installed, in bytes."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *bundleFileSizesData, diags *diag.Diagnostics) {
		out, err := c.BuildBundlesBuildBundleFileSizesGetToManyRelatedWithResponse(ctx, m.BuildBundleID.ValueString(),
			&gen.BuildBundlesBuildBundleFileSizesGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.BuildBundleFileSize
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.BuildBundleFileSizesResponse) []gen.BuildBundleFileSize { return p.Data },
				func(p *gen.BuildBundleFileSizesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "build bundle", err)
			return
		}
		m.FileSizes = []bundleFileSizeData{}
		for _, size := range all {
			a := asc.Value(size.Attributes)
			m.FileSizes = append(m.FileSizes, bundleFileSizeData{
				DeviceModel: stringValue(a.DeviceModel), OSVersion: stringValue(a.OsVersion), DownloadBytes: intValue(a.DownloadBytes), InstallBytes: intValue(a.InstallBytes),
			})
		}
	})
