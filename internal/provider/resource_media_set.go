package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Screenshot and preview sets hold an ordered list of uploaded files for one
// display type of a version localization. A set is one resource: apply
// uploads new or changed files, deletes removed ones, and sets the order in
// one call. Files are compared by their MD5 checksums, which App Store
// Connect keeps, so changing a file's contents shows in the plan.

// mediaFile is an uploaded file of a set.
type mediaFile struct {
	id, checksum string
	failed       bool
}

// mediaAPI is the part of the API a kind of set needs.
type mediaAPI struct {
	typeName    string // resource type name without the provider prefix
	noun        string // "screenshot" or "app preview"
	description string
	types       []string
	maxFiles    int
	findSet     func(ctx context.Context, c *asc.Client, localizationID, typ string) (string, error)
	getSet      func(ctx context.Context, c *asc.Client, id string) error
	createSet   func(ctx context.Context, c *asc.Client, localizationID, typ string) (string, error)
	deleteSet   func(ctx context.Context, c *asc.Client, id string) error
	list        func(ctx context.Context, c *asc.Client, setID string) ([]mediaFile, error)
	reserve     func(ctx context.Context, c *asc.Client, setID, name string, size int) (string, []gen.UploadOperation, error)
	commit      func(ctx context.Context, c *asc.Client, id, checksum string) (failure string, err error)
	deleteFile  func(ctx context.Context, c *asc.Client, id string) error
	reorder     func(ctx context.Context, c *asc.Client, setID string, ids []string) error
}

var (
	_ resource.ResourceWithConfigure   = &mediaSetResource{}
	_ resource.ResourceWithImportState = &mediaSetResource{}
	_ resource.ResourceWithIdentity    = &mediaSetResource{}
	_ resource.ResourceWithModifyPlan  = &mediaSetResource{}
)

type mediaSetResource struct {
	resourceIdentity
	api    mediaAPI
	client *asc.Client
}

type mediaSetModel struct {
	LocalizationID types.String `tfsdk:"localization_id"`
	DisplayType    types.String `tfsdk:"display_type"`
	Files          types.List   `tfsdk:"files"`
	Checksums      types.List   `tfsdk:"checksums"`
	FileIDs        types.List   `tfsdk:"file_ids"`
	ID             types.String `tfsdk:"id"`
}

func newMediaSetResource(api mediaAPI) func() resource.Resource {
	return func() resource.Resource {
		r := &mediaSetResource{api: api}
		r.attrs = []identityAttribute{
			{name: "localization_id", description: "App Store Connect ID of the version localization.", state: "localization_id"},
			{name: "display_type", description: "Display type of the set.", state: "display_type"},
		}
		return r
	}
}

func (r *mediaSetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *mediaSetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: r.api.description + " Apply uploads, replaces and reorders files to match `files`, and " +
			"uploads a file again when its contents change. Changes need the version to be editable. Destroying the " +
			"resource deletes the set while the version is editable, and otherwise leaves it. Import it with " +
			"`<localization_id>/<display_type>`.",
		Attributes: map[string]schema.Attribute{
			"localization_id": replacedString("App Store Connect ID of the version localization, e.g. `apple_app_store_version_localization.en_us.id`."),
			"display_type":    replacedString("Device the "+r.api.noun+"s are for: "+docValues(r.api.types)+".", oneOf(r.api.types)),
			"files": schema.ListAttribute{
				MarkdownDescription: fmt.Sprintf("Paths of the %s files, in display order; up to %d. Relative paths are "+
					"relative to where Terraform runs, so use `\"${path.module}/...\"`.", r.api.noun, r.api.maxFiles),
				Required:    true,
				ElementType: types.StringType,
				Validators:  []validator.List{listvalidator.SizeBetween(1, r.api.maxFiles)},
			},
			"checksums": schema.ListAttribute{MarkdownDescription: "MD5 checksums of the files, in order.", Computed: true, ElementType: types.StringType},
			"file_ids":  schema.ListAttribute{MarkdownDescription: "App Store Connect IDs of the uploaded files, in order.", Computed: true, ElementType: types.StringType},
			"id":        idAttribute("App Store Connect ID of the set."),
		},
	}
}

func (r *mediaSetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *mediaSetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// readFiles reads the configured files.
func readFiles(paths []string) ([][]byte, error) {
	var out [][]byte
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // The configuration names the files to upload.
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("%s is empty", p)
		}
		out = append(out, data)
	}
	return out, nil
}

// ModifyPlan plans the files' checksums, so changed contents show as a
// change, and keeps the file IDs when the checksums don't change.
func (r *mediaSetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan mediaSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Files.IsUnknown() || slices.ContainsFunc(plan.Files.Elements(), func(v attr.Value) bool { return v.IsUnknown() }) {
		return
	}
	data, err := readFiles(listStrings(ctx, plan.Files))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("files"), "Cannot read "+r.api.noun, err.Error())
		return
	}
	var sums []string
	for _, d := range data {
		sums = append(sums, asc.Checksum(d))
	}
	plan.Checksums, plan.FileIDs = stringsList(sums), types.ListUnknown(types.StringType)
	if !req.State.Raw.IsNull() {
		var state mediaSetModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if state.Checksums.Equal(plan.Checksums) {
			plan.FileIDs = state.FileIDs
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// sync makes the set's files match paths, in order, and returns their IDs
// and checksums.
func (r *mediaSetResource) sync(ctx context.Context, setID string, paths []string) (ids, sums []string, err error) {
	data, err := readFiles(paths)
	if err != nil {
		return nil, nil, err
	}
	current, err := r.api.list(ctx, r.client, setID)
	if err != nil {
		return nil, nil, err
	}
	// Reuse complete files with the same contents. Delete the rest before
	// uploading, so the set stays within its limit.
	used := make([]bool, len(current))
	ids = make([]string, len(data))
	for i, d := range data {
		sum := asc.Checksum(d)
		sums = append(sums, sum)
		for j, f := range current {
			if !used[j] && !f.failed && f.checksum == sum {
				used[j], ids[i] = true, f.id
				break
			}
		}
	}
	for j, f := range current {
		if used[j] {
			continue
		}
		if err := r.api.deleteFile(ctx, r.client, f.id); err != nil && !asc.IsNotFound(err) {
			return nil, nil, err
		}
	}
	for i, d := range data {
		if ids[i] != "" {
			continue
		}
		id, ops, err := r.api.reserve(ctx, r.client, setID, filepath.Base(paths[i]), len(d))
		if err != nil {
			return nil, nil, fmt.Errorf("reserving %s: %w", paths[i], err)
		}
		if err := r.client.Upload(ctx, ops, d); err != nil {
			return nil, nil, fmt.Errorf("uploading %s: %w", paths[i], err)
		}
		failure, err := r.api.commit(ctx, r.client, id, sums[i])
		if err != nil {
			return nil, nil, fmt.Errorf("committing %s: %w", paths[i], err)
		}
		if failure != "" {
			return nil, nil, fmt.Errorf("the upload of %s failed: %s", paths[i], failure)
		}
		ids[i] = id
	}
	// New files are added at the end; set the configured order.
	after, err := r.api.list(ctx, r.client, setID)
	if err != nil {
		return nil, nil, err
	}
	var order []string
	for _, f := range after {
		order = append(order, f.id)
	}
	if !slices.Equal(order, ids) {
		if err := r.api.reorder(ctx, r.client, setID, ids); err != nil {
			return nil, nil, err
		}
	}
	return ids, sums, nil
}

func (r *mediaSetResource) write(ctx context.Context, plan *mediaSetModel, setID string) error {
	ids, sums, err := r.sync(ctx, setID, listStrings(ctx, plan.Files))
	if err != nil {
		return err
	}
	plan.ID, plan.FileIDs, plan.Checksums = types.StringValue(setID), stringsList(ids), stringsList(sums)
	return nil
}

func (r *mediaSetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan mediaSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A set for the display type may already exist; manage it.
	id, err := r.api.findSet(ctx, r.client, plan.LocalizationID.ValueString(), plan.DisplayType.ValueString())
	if errors.Is(err, errNoMatch) {
		id, err = r.api.createSet(ctx, r.client, plan.LocalizationID.ValueString(), plan.DisplayType.ValueString())
	}
	if err == nil {
		err = r.write(ctx, &plan, id)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "set "+r.api.noun+"s", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mediaSetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state mediaSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An import has no ID: find the set by its localization and type.
	var err error
	if state.ID.IsNull() || state.ID.ValueString() == "" {
		var id string
		id, err = r.api.findSet(ctx, r.client, state.LocalizationID.ValueString(), state.DisplayType.ValueString())
		state.ID = types.StringValue(id)
	} else {
		err = r.api.getSet(ctx, r.client, state.ID.ValueString())
	}
	var files []mediaFile
	if err == nil {
		files, err = r.api.list(ctx, r.client, state.ID.ValueString())
	}
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+r.api.noun+"s", err)
		return
	}
	var ids, sums []string
	for _, f := range files {
		ids, sums = append(ids, f.id), append(sums, f.checksum)
	}
	state.FileIDs, state.Checksums = stringsList(ids), stringsList(sums)
	if state.Files.IsNull() {
		state.Files = types.ListValueMust(types.StringType, nil)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *mediaSetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state mediaSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan, state.ID.ValueString()); err != nil {
		apiError(&resp.Diagnostics, "set "+r.api.noun+"s", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the set, with its files, while the version is editable; a
// released version keeps its media.
func (r *mediaSetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mediaSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.deleteSet(ctx, r.client, state.ID.ValueString())
	if err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete "+r.api.noun+" set", err)
	}
}
