package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Xcode Cloud workflows
// (https://developer.apple.com/documentation/appstoreconnectapi/xcode-cloud-workflows-and-builds).
// A workflow belongs to a product, builds one repository with an Xcode and
// a macOS version, starts on the conditions it lists and runs its actions.

var (
	ciFileRuleModes      = strings.Fields("START_IF_ANY_FILE_MATCHES DO_NOT_START_IF_ALL_FILES_MATCH")
	ciFrequencies        = strings.Fields("WEEKLY DAILY HOURLY")
	ciDays               = strings.Fields("SUNDAY MONDAY TUESDAY WEDNESDAY THURSDAY FRIDAY SATURDAY")
	ciActionTypes        = strings.Fields("BUILD ANALYZE TEST ARCHIVE")
	ciActionPlatforms    = strings.Fields("MACOS IOS TVOS WATCHOS VISIONOS")
	ciAudiences          = strings.Fields("INTERNAL_ONLY APP_STORE_ELIGIBLE")
	ciTestKinds          = strings.Fields("USE_SCHEME_SETTINGS SPECIFIC_TEST_PLANS")
	ciTestDestinations   = strings.Fields("SIMULATOR MAC")
	ciActionDestinations = strings.Fields("ANY_IOS_DEVICE ANY_IOS_SIMULATOR ANY_TVOS_DEVICE ANY_TVOS_SIMULATOR ANY_WATCHOS_DEVICE " +
		"ANY_WATCHOS_SIMULATOR ANY_MAC ANY_MAC_CATALYST ANY_VISIONOS_DEVICE ANY_VISIONOS_SIMULATOR")

	// ciStartConditions names the start conditions as the API does.
	ciStartConditions = []string{
		"branchStartCondition", "tagStartCondition", "pullRequestStartCondition", "scheduledStartCondition",
		"manualBranchStartCondition", "manualTagStartCondition", "manualPullRequestStartCondition",
	}
)

// The start conditions and actions of a workflow. The types carry both the
// Terraform and the API's JSON names, so one value is the plan, the state
// and the request.

type ciPattern struct {
	Pattern  *string `tfsdk:"pattern" json:"pattern,omitempty"`
	IsPrefix *bool   `tfsdk:"is_prefix" json:"isPrefix,omitempty"`
}

type ciPatterns struct {
	IsAllMatch *bool       `tfsdk:"is_all_match" json:"isAllMatch,omitempty"`
	Patterns   []ciPattern `tfsdk:"patterns" json:"patterns,omitempty"`
}

type ciFileMatcher struct {
	Directory     *string `tfsdk:"directory" json:"directory,omitempty"`
	FileExtension *string `tfsdk:"file_extension" json:"fileExtension,omitempty"`
	FileName      *string `tfsdk:"file_name" json:"fileName,omitempty"`
}

type ciFilesRule struct {
	Mode     *string         `tfsdk:"mode" json:"mode,omitempty"`
	Matchers []ciFileMatcher `tfsdk:"matchers" json:"matchers,omitempty"`
}

// ciRefStart is a branch or tag start condition.
type ciRefStart struct {
	Source              *ciPatterns  `tfsdk:"source" json:"source,omitempty"`
	FilesAndFoldersRule *ciFilesRule `tfsdk:"files_and_folders_rule" json:"filesAndFoldersRule,omitempty"`
	AutoCancel          *bool        `tfsdk:"auto_cancel" json:"autoCancel,omitempty"`
}

type ciPullRequestStart struct {
	Source              *ciPatterns  `tfsdk:"source" json:"source,omitempty"`
	Destination         *ciPatterns  `tfsdk:"destination" json:"destination,omitempty"`
	FilesAndFoldersRule *ciFilesRule `tfsdk:"files_and_folders_rule" json:"filesAndFoldersRule,omitempty"`
	AutoCancel          *bool        `tfsdk:"auto_cancel" json:"autoCancel,omitempty"`
}

type ciSchedule struct {
	Frequency *string  `tfsdk:"frequency" json:"frequency,omitempty"`
	Days      []string `tfsdk:"days" json:"days,omitempty"`
	Hour      *int64   `tfsdk:"hour" json:"hour,omitempty"`
	Minute    *int64   `tfsdk:"minute" json:"minute,omitempty"`
	Timezone  *string  `tfsdk:"timezone" json:"timezone,omitempty"`
}

type ciScheduledStart struct {
	Source   *ciPatterns `tfsdk:"source" json:"source,omitempty"`
	Schedule *ciSchedule `tfsdk:"schedule" json:"schedule,omitempty"`
}

type ciManualStart struct {
	Source *ciPatterns `tfsdk:"source" json:"source,omitempty"`
}

type ciManualPullRequestStart struct {
	Source      *ciPatterns `tfsdk:"source" json:"source,omitempty"`
	Destination *ciPatterns `tfsdk:"destination" json:"destination,omitempty"`
}

type ciTestDestination struct {
	Kind                 *string `tfsdk:"kind" json:"kind,omitempty"`
	DeviceTypeName       *string `tfsdk:"device_type_name" json:"deviceTypeName,omitempty"`
	DeviceTypeIdentifier *string `tfsdk:"device_type_identifier" json:"deviceTypeIdentifier,omitempty"`
	RuntimeName          *string `tfsdk:"runtime_name" json:"runtimeName,omitempty"`
	RuntimeIdentifier    *string `tfsdk:"runtime_identifier" json:"runtimeIdentifier,omitempty"`
}

type ciTestConfiguration struct {
	Kind             *string             `tfsdk:"kind" json:"kind,omitempty"`
	TestPlanName     *string             `tfsdk:"test_plan_name" json:"testPlanName,omitempty"`
	TestDestinations []ciTestDestination `tfsdk:"test_destinations" json:"testDestinations,omitempty"`
}

type ciAction struct {
	Name                      *string              `tfsdk:"name" json:"name,omitempty"`
	ActionType                *string              `tfsdk:"action_type" json:"actionType,omitempty"`
	Scheme                    *string              `tfsdk:"scheme" json:"scheme,omitempty"`
	Platform                  *string              `tfsdk:"platform" json:"platform,omitempty"`
	Destination               *string              `tfsdk:"destination" json:"destination,omitempty"`
	BuildDistributionAudience *string              `tfsdk:"build_distribution_audience" json:"buildDistributionAudience,omitempty"`
	IsRequiredToPass          *bool                `tfsdk:"is_required_to_pass" json:"isRequiredToPass,omitempty"`
	TestConfiguration         *ciTestConfiguration `tfsdk:"test_configuration" json:"testConfiguration,omitempty"`
}

type ciWorkflowSpec struct {
	BranchStart            *ciRefStart               `tfsdk:"branch_start_condition" json:"branchStartCondition,omitempty"`
	TagStart               *ciRefStart               `tfsdk:"tag_start_condition" json:"tagStartCondition,omitempty"`
	PullRequestStart       *ciPullRequestStart       `tfsdk:"pull_request_start_condition" json:"pullRequestStartCondition,omitempty"`
	ScheduledStart         *ciScheduledStart         `tfsdk:"scheduled_start_condition" json:"scheduledStartCondition,omitempty"`
	ManualBranchStart      *ciManualStart            `tfsdk:"manual_branch_start_condition" json:"manualBranchStartCondition,omitempty"`
	ManualTagStart         *ciManualStart            `tfsdk:"manual_tag_start_condition" json:"manualTagStartCondition,omitempty"`
	ManualPullRequestStart *ciManualPullRequestStart `tfsdk:"manual_pull_request_start_condition" json:"manualPullRequestStartCondition,omitempty"`
	Actions                []ciAction                `tfsdk:"actions" json:"actions,omitempty"`
}

// canonical drops what the API may add to or leave out of a value without
// changing its meaning: nulls, false and empty objects. Empty lists are
// already left out of the JSON the values are compared as.
func canonical(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range v {
			if c := canonical(value); c != nil {
				out[key] = c
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = canonical(value)
		}
		return out
	case bool:
		if !v {
			return nil
		}
	}
	return v
}

func sameMeaning(a, b any) bool {
	var decoded [2]any
	for i, v := range []any{a, b} {
		data, err := json.Marshal(v)
		if err != nil || json.Unmarshal(data, &decoded[i]) != nil {
			return false
		}
	}
	return reflect.DeepEqual(canonical(decoded[0]), canonical(decoded[1]))
}

// reconcile takes each start condition, and the actions, from the API unless
// the current value means the same, so that an omitted false or empty list
// isn't read as a change.
func (s *ciWorkflowSpec) reconcile(api *ciWorkflowSpec) {
	current, read := reflect.ValueOf(s).Elem(), reflect.ValueOf(api).Elem()
	for i := range current.NumField() {
		if !sameMeaning(current.Field(i).Interface(), read.Field(i).Interface()) {
			current.Field(i).Set(read.Field(i))
		}
	}
}

// ---- schema ----

// ciOptionalBool is a boolean that the API treats as false when unset.
func ciOptionalBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{MarkdownDescription: desc, Optional: true}
}

func ciPatternsAttribute(desc string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"is_all_match": ciOptionalBool("Whether every name matches. Set this or `patterns`."),
			"patterns": schema.ListNestedAttribute{
				MarkdownDescription: "Names that match.",
				Optional:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"pattern":   schema.StringAttribute{MarkdownDescription: "The name, or the start of the names with `is_prefix`, e.g. `main` or `release/`.", Required: true},
					"is_prefix": ciOptionalBool("Whether `pattern` is the start of the names that match rather than a whole name."),
				}},
			},
		},
	}
}

func ciFilesRuleAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Starts a build depending on the files a change touches.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"mode": schema.StringAttribute{
				MarkdownDescription: "How the matchers apply: " + docValues(ciFileRuleModes) + ".", Required: true,
				Validators: []validator.String{oneOf(ciFileRuleModes)},
			},
			"matchers": schema.ListNestedAttribute{
				MarkdownDescription: "Files and folders to match.",
				Required:            true,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"directory":      optionalString("Folder to match, e.g. `/Sources`."),
					"file_extension": optionalString("File extension to match, e.g. `swift`."),
					"file_name":      optionalString("File name to match."),
				}},
			},
		},
	}
}

func ciAutoCancel() schema.BoolAttribute {
	return ciOptionalBool("Whether a new build cancels the builds of earlier changes that are still running.")
}

func ciStartAttribute(desc string, attributes map[string]schema.Attribute) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{MarkdownDescription: desc, Optional: true, Attributes: attributes}
}

func ciActionsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "What the workflow does, in order.",
		Required:            true,
		Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{MarkdownDescription: "Name of the action, e.g. `Build - iOS`.", Required: true},
			"action_type": schema.StringAttribute{
				MarkdownDescription: "Kind of action: " + docValues(ciActionTypes) + ".", Required: true, Validators: []validator.String{oneOf(ciActionTypes)},
			},
			"scheme":      schema.StringAttribute{MarkdownDescription: "Xcode scheme the action uses.", Required: true},
			"platform":    optionalString("Platform the action builds for: "+docValues(ciActionPlatforms)+".", oneOf(ciActionPlatforms)),
			"destination": optionalString("Where a build or test action runs: "+docValues(ciActionDestinations)+".", oneOf(ciActionDestinations)),
			"build_distribution_audience": optionalString("Who an archive action's build can go to: "+docValues(ciAudiences)+".",
				oneOf(ciAudiences)),
			"is_required_to_pass": schema.BoolAttribute{MarkdownDescription: "Whether the workflow fails when the action does.", Required: true},
			"test_configuration": schema.SingleNestedAttribute{
				MarkdownDescription: "What a test action tests.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{
						MarkdownDescription: "Where the tests come from: " + docValues(ciTestKinds) + ".", Required: true,
						Validators: []validator.String{oneOf(ciTestKinds)},
					},
					"test_plan_name": optionalString("Name of the test plan, with `SPECIFIC_TEST_PLANS`."),
					"test_destinations": schema.ListNestedAttribute{
						MarkdownDescription: "Simulators or Macs to test on; `data.apple_ci_xcode_versions` lists the ones an Xcode version offers.",
						Optional:            true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"kind":                   optionalString("Kind of destination: "+docValues(ciTestDestinations)+".", oneOf(ciTestDestinations)),
							"device_type_name":       optionalString("Name of the device type, e.g. `iPhone 17`."),
							"device_type_identifier": optionalString("Identifier of the device type, e.g. `com.apple.CoreSimulator.SimDeviceType.iPhone-17`."),
							"runtime_name":           optionalString("Name of the runtime, e.g. `Latest from Selected Xcode`."),
							"runtime_identifier":     optionalString("Identifier of the runtime, e.g. `default`."),
						}},
					},
				},
			},
		}},
	}
}

// ---- apple_ci_workflow ----

var (
	_ resource.ResourceWithConfigure   = &ciWorkflowResource{}
	_ resource.ResourceWithImportState = &ciWorkflowResource{}
	_ resource.ResourceWithIdentity    = &ciWorkflowResource{}
)

type ciWorkflowResource struct {
	resourceIdentity
	client *asc.Client
}

type ciWorkflowModel struct {
	ProductID         types.String `tfsdk:"product_id"`
	RepositoryID      types.String `tfsdk:"repository_id"`
	XcodeVersionID    types.String `tfsdk:"xcode_version_id"`
	MacOSVersionID    types.String `tfsdk:"mac_os_version_id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Clean             types.Bool   `tfsdk:"clean"`
	LockedForEditing  types.Bool   `tfsdk:"locked_for_editing"`
	ContainerFilePath types.String `tfsdk:"container_file_path"`
	ciWorkflowSpec
	LastModifiedDate types.String `tfsdk:"last_modified_date"`
	ID               types.String `tfsdk:"id"`
}

func newCiWorkflowResource() resource.Resource {
	return &ciWorkflowResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the Xcode Cloud workflow.")}}
}

func (r *ciWorkflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ci_workflow"
}

func (r *ciWorkflowResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	source := ciPatternsAttribute("Branches the condition applies to.")
	tags := ciPatternsAttribute("Tags the condition applies to.")
	destination := ciPatternsAttribute("Branches the pull request merges into.")
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates an Xcode Cloud workflow: when builds start, with which Xcode and macOS version, and " +
			"what they do. Xcode creates the product and connects the repository; find them with " +
			"`data.apple_ci_products` and `data.apple_scm_repositories`. A workflow needs at least one start condition " +
			"to start by itself; `apple_start_ci_build_run` starts one on demand. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"product_id":    replacedString("App Store Connect ID of the Xcode Cloud product. Changing it creates a new workflow."),
			"repository_id": replacedString("App Store Connect ID of the repository the workflow builds. Changing it creates a new workflow."),
			"xcode_version_id": schema.StringAttribute{
				MarkdownDescription: "App Store Connect ID of the Xcode version; see `data.apple_ci_xcode_versions`.", Required: true,
			},
			"mac_os_version_id": schema.StringAttribute{
				MarkdownDescription: "App Store Connect ID of the macOS version, which must run the Xcode version; see `data.apple_ci_mac_os_versions`.",
				Required:            true,
			},
			"name":        schema.StringAttribute{MarkdownDescription: "Name of the workflow.", Required: true},
			"description": schema.StringAttribute{MarkdownDescription: "Description of the workflow.", Required: true},
			"enabled":     schema.BoolAttribute{MarkdownDescription: "Whether the workflow starts builds.", Required: true},
			"clean":       schema.BoolAttribute{MarkdownDescription: "Whether builds start without the cached data of earlier builds.", Required: true},
			"locked_for_editing": schema.BoolAttribute{
				MarkdownDescription: "Whether only an Admin or Account Holder can edit the workflow. Defaults to `false`.",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"container_file_path": schema.StringAttribute{
				MarkdownDescription: "Path of the Xcode project or workspace in the repository, e.g. `Example.xcodeproj`.", Required: true,
			},
			"branch_start_condition": ciStartAttribute("Starts a build when a branch changes.", map[string]schema.Attribute{
				"source": source, "files_and_folders_rule": ciFilesRuleAttribute(), "auto_cancel": ciAutoCancel(),
			}),
			"tag_start_condition": ciStartAttribute("Starts a build when a tag is pushed.", map[string]schema.Attribute{
				"source": tags, "files_and_folders_rule": ciFilesRuleAttribute(), "auto_cancel": ciAutoCancel(),
			}),
			"pull_request_start_condition": ciStartAttribute("Starts a build when a pull request opens or changes.", map[string]schema.Attribute{
				"source": source, "destination": destination, "files_and_folders_rule": ciFilesRuleAttribute(), "auto_cancel": ciAutoCancel(),
			}),
			"scheduled_start_condition": ciStartAttribute("Starts a build of a branch on a schedule.", map[string]schema.Attribute{
				"source": source,
				"schedule": schema.SingleNestedAttribute{
					MarkdownDescription: "When builds start.",
					Required:            true,
					Attributes: map[string]schema.Attribute{
						"frequency": schema.StringAttribute{
							MarkdownDescription: "How often: " + docValues(ciFrequencies) + ".", Required: true,
							Validators: []validator.String{oneOf(ciFrequencies)},
						},
						"days": schema.SetAttribute{
							MarkdownDescription: "Days of a weekly schedule: " + docValues(ciDays) + ".", Optional: true, ElementType: types.StringType,
							Validators: []validator.Set{setvalidator.ValueStringsAre(oneOf(ciDays))},
						},
						"hour": schema.Int64Attribute{
							MarkdownDescription: "Hour of the day, 0 to 23.", Optional: true, Validators: []validator.Int64{int64validator.Between(0, 23)},
						},
						"minute": schema.Int64Attribute{
							MarkdownDescription: "Minute of the hour, 0 to 59.", Optional: true, Validators: []validator.Int64{int64validator.Between(0, 59)},
						},
						"timezone": optionalString("Time zone of `hour`, e.g. `America/Los_Angeles`."),
					},
				},
			}),
			"manual_branch_start_condition": ciStartAttribute("Lets people start a build of a branch.", map[string]schema.Attribute{"source": source}),
			"manual_tag_start_condition":    ciStartAttribute("Lets people start a build of a tag.", map[string]schema.Attribute{"source": tags}),
			"manual_pull_request_start_condition": ciStartAttribute("Lets people start a build of a pull request.", map[string]schema.Attribute{
				"source": source, "destination": destination,
			}),
			"actions":            ciActionsAttribute(),
			"last_modified_date": computedOnly("When the workflow last changed."),
			"id":                 idAttribute("App Store Connect ID of the Xcode Cloud workflow."),
		},
	}
}

func (r *ciWorkflowResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *ciWorkflowResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// attributes returns the workflow's attributes as the API takes them. An
// update names the start conditions the workflow doesn't have as null, which
// removes them.
func (m *ciWorkflowModel) attributes(update bool) (map[string]any, error) {
	var attributes map[string]any
	data, err := json.Marshal(m.ciWorkflowSpec)
	if err == nil {
		err = json.Unmarshal(data, &attributes)
	}
	if err != nil {
		return nil, err
	}
	if update {
		for _, name := range ciStartConditions {
			if _, ok := attributes[name]; !ok {
				attributes[name] = nil
			}
		}
	}
	attributes["name"], attributes["description"] = m.Name.ValueString(), m.Description.ValueString()
	attributes["isEnabled"], attributes["clean"] = m.Enabled.ValueBool(), m.Clean.ValueBool()
	attributes["isLockedForEditing"], attributes["containerFilePath"] = m.LockedForEditing.ValueBool(), m.ContainerFilePath.ValueString()
	return attributes, nil
}

// apply sets everything but the start conditions and actions.
func (m *ciWorkflowModel) apply(w *gen.CiWorkflow) {
	a := asc.Value(w.Attributes)
	m.ID, m.Name, m.Description = types.StringValue(w.Id), stringValue(a.Name), stringValue(a.Description)
	m.Enabled, m.Clean, m.LockedForEditing = types.BoolPointerValue(a.IsEnabled), types.BoolPointerValue(a.Clean), types.BoolPointerValue(a.IsLockedForEditing)
	m.ContainerFilePath, m.LastModifiedDate = stringValue(a.ContainerFilePath), timeValue(a.LastModifiedDate)
	rel := w.Relationships
	if rel == nil {
		return
	}
	if rel.Product != nil && rel.Product.Data != nil {
		m.ProductID = types.StringValue(rel.Product.Data.Id)
	}
	if rel.Repository != nil && rel.Repository.Data != nil {
		m.RepositoryID = types.StringValue(rel.Repository.Data.Id)
	}
	if rel.XcodeVersion != nil && rel.XcodeVersion.Data != nil {
		m.XcodeVersionID = types.StringValue(rel.XcodeVersion.Data.Id)
	}
	if rel.MacOsVersion != nil && rel.MacOsVersion.Data != nil {
		m.MacOSVersionID = types.StringValue(rel.MacOsVersion.Data.Id)
	}
}

// versions returns the relationships a create or an update sets.
func (m *ciWorkflowModel) versions() map[string]any {
	return map[string]any{
		"xcodeVersion": linkage("ciXcodeVersions", m.XcodeVersionID.ValueString()),
		"macOsVersion": linkage("ciMacOsVersions", m.MacOSVersionID.ValueString()),
	}
}

func (r *ciWorkflowResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan ciWorkflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	attributes, err := plan.attributes(false)
	var body []byte
	if err == nil {
		relationships := plan.versions()
		relationships["product"] = linkage("ciProducts", plan.ProductID.ValueString())
		relationships["repository"] = linkage("scmRepositories", plan.RepositoryID.ValueString())
		body, err = json.Marshal(map[string]any{"data": map[string]any{"type": "ciWorkflows", "attributes": attributes, "relationships": relationships}})
	}
	if err == nil {
		out, e := r.client.CiWorkflowsCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON201.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create Xcode Cloud workflow", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ciWorkflowResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state ciWorkflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CiWorkflowsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.CiWorkflowsGetInstanceParams{
		Include: &[]gen.CiWorkflowsGetInstanceParamsInclude{
			gen.CiWorkflowsGetInstanceParamsIncludeProduct, gen.CiWorkflowsGetInstanceParamsIncludeRepository,
			gen.CiWorkflowsGetInstanceParamsIncludeXcodeVersion, gen.CiWorkflowsGetInstanceParamsIncludeMacOsVersion,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	var doc struct {
		Data struct {
			Attributes ciWorkflowSpec `json:"attributes"`
		} `json:"data"`
	}
	if err == nil {
		err = json.Unmarshal(out.Body, &doc)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read Xcode Cloud workflow", err)
		return
	}
	state.apply(&out.JSON200.Data)
	state.reconcile(&doc.Data.Attributes)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ciWorkflowResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan ciWorkflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := plan.ID.ValueString()
	attributes, err := plan.attributes(true)
	var body []byte
	if err == nil {
		body, err = json.Marshal(map[string]any{"data": map[string]any{
			"type": "ciWorkflows", "id": id, "attributes": attributes, "relationships": plan.versions(),
		}})
	}
	if err == nil {
		out, e := r.client.CiWorkflowsUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON200.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update Xcode Cloud workflow", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ciWorkflowResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ciWorkflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CiWorkflowsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete Xcode Cloud workflow", err)
	}
}
