package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Apple-hosted background assets
// (https://developer.apple.com/documentation/appstoreconnectapi/managing-apple-hosted-background-assets):
// an app's asset packs, and the versions of their contents. App Store
// Connect can archive an asset pack but not delete it, and can't delete a
// version.

var (
	_ resource.ResourceWithConfigure   = &backgroundAssetResource{}
	_ resource.ResourceWithImportState = &backgroundAssetResource{}
	_ resource.ResourceWithIdentity    = &backgroundAssetResource{}
)

type backgroundAssetResource struct {
	resourceIdentity
	client *asc.Client
}

type backgroundAssetModel struct {
	ID                  types.String `tfsdk:"id"`
	AppID               types.String `tfsdk:"app_id"`
	AssetPackIdentifier types.String `tfsdk:"asset_pack_identifier"`
	CreatedDate         types.String `tfsdk:"created_date"`
}

func newBackgroundAssetResource() resource.Resource {
	return &backgroundAssetResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the asset pack.")}}
}

func (r *backgroundAssetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_background_asset"
}

func (r *backgroundAssetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates an Apple-hosted asset pack for an app's background assets. Upload its contents with " +
			"`apple_background_asset_version`. App Store Connect can't delete an asset pack, so destroying the resource " +
			"archives it, and creating one with the identifier of an archived asset pack restores that one. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"id":     idAttribute("App Store Connect ID of the asset pack."),
			"app_id": replacedString("App Store Connect ID of the app. Changing it creates a new asset pack.", stringvalidator.LengthAtLeast(1)),
			"asset_pack_identifier": replacedString("Identifier of the asset pack, which must match the ID in its manifest. "+
				"Changing it creates a new asset pack.", stringvalidator.LengthAtLeast(1)),
			"created_date": computedString("When the asset pack was created, in RFC 3339 format."),
		},
	}
}

func (r *backgroundAssetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *backgroundAssetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *backgroundAssetModel) apply(a *gen.BackgroundAsset) {
	m.ID = types.StringValue(a.Id)
	attrs := asc.Value(a.Attributes)
	m.AssetPackIdentifier = stringValue(attrs.AssetPackIdentifier)
	m.CreatedDate = timeValue(attrs.CreatedDate)
	if rel := a.Relationships; rel != nil && rel.App != nil && rel.App.Data != nil {
		m.AppID = types.StringValue(rel.App.Data.Id)
	}
}

// appBackgroundAssets lists an app's asset packs matching params.
func appBackgroundAssets(ctx context.Context, c *asc.Client, appID string, params gen.AppsBackgroundAssetsGetToManyRelatedParams) ([]gen.BackgroundAsset, error) {
	params.Limit = new(listAll)
	out, err := c.AppsBackgroundAssetsGetToManyRelatedWithResponse(ctx, appID, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.BackgroundAssetsResponse) []gen.BackgroundAsset { return p.Data },
		func(p *gen.BackgroundAssetsResponse) *string { return p.Links.Next })
}

func (r *backgroundAssetResource) archive(ctx context.Context, id string, archived bool) (*gen.BackgroundAsset, error) {
	var body gen.BackgroundAssetUpdateRequest
	body.Data.Type, body.Data.Id = gen.BackgroundAssetUpdateRequestDataTypeBackgroundAssets, id
	asc.Alloc(&body.Data.Attributes).Archived = new(archived)
	out, err := r.client.BackgroundAssetsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// create restores an archived asset pack with the identifier, or creates one.
func (r *backgroundAssetResource) create(ctx context.Context, appID, identifier string) (*gen.BackgroundAsset, error) {
	existing, err := appBackgroundAssets(ctx, r.client, appID, gen.AppsBackgroundAssetsGetToManyRelatedParams{FilterAssetPackIdentifier: &[]string{identifier}})
	if err != nil {
		return nil, err
	}
	for _, a := range existing {
		attrs := asc.Value(a.Attributes)
		if asc.Value(attrs.AssetPackIdentifier) != identifier {
			continue
		}
		if !asc.Value(attrs.Archived) {
			return nil, fmt.Errorf("the app already has the asset pack %q (ID %s); import it instead", identifier, a.Id)
		}
		return r.archive(ctx, a.Id, false)
	}
	var body gen.BackgroundAssetCreateRequest
	body.Data.Type = gen.BackgroundAssetCreateRequestDataTypeBackgroundAssets
	body.Data.Attributes.AssetPackIdentifier = identifier
	d := &body.Data.Relationships.App.Data
	d.Id, d.Type = appID, gen.BackgroundAssetCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.BackgroundAssetsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON201.Data, nil
}

func (r *backgroundAssetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan backgroundAssetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.create(ctx, plan.AppID.ValueString(), plan.AssetPackIdentifier.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "create asset pack", err)
		return
	}
	plan.apply(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *backgroundAssetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state backgroundAssetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BackgroundAssetsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.BackgroundAssetsGetInstanceParams{
		Include: &[]gen.BackgroundAssetsGetInstanceParamsInclude{gen.BackgroundAssetsGetInstanceParamsIncludeApp},
	})
	err = asc.Err(out, err)
	// An archived asset pack is gone as far as Terraform is concerned.
	if asc.IsNotFound(err) || err == nil && asc.Value(asc.Value(out.JSON200.Data.Attributes).Archived) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read asset pack", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument replaces the resource.
func (r *backgroundAssetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *backgroundAssetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state backgroundAssetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.archive(ctx, state.ID.ValueString(), true); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "archive asset pack", err)
	}
}

type backgroundAssetData struct {
	ID                  types.String `tfsdk:"id"`
	AssetPackIdentifier types.String `tfsdk:"asset_pack_identifier"`
	Archived            types.Bool   `tfsdk:"archived"`
	CreatedDate         types.String `tfsdk:"created_date"`
}

type backgroundAssetsData struct {
	AppID  types.String          `tfsdk:"app_id"`
	Assets []backgroundAssetData `tfsdk:"asset_packs"`
}

var newBackgroundAssetsDataSource = listDataSource("background_assets",
	"Lists an app's Apple-hosted asset packs, including archived ones.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"asset_packs": listOf("The asset packs.", map[string]dsschema.Attribute{
			"id":                    dsComputed("App Store Connect ID of the asset pack."),
			"asset_pack_identifier": dsComputed("Identifier of the asset pack."),
			"archived":              dsschema.BoolAttribute{MarkdownDescription: "Whether the asset pack is archived.", Computed: true},
			"created_date":          dsComputed("When the asset pack was created, in RFC 3339 format."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *backgroundAssetsData, diags *diag.Diagnostics) {
		all, err := appBackgroundAssets(ctx, c, m.AppID.ValueString(), gen.AppsBackgroundAssetsGetToManyRelatedParams{})
		if err != nil {
			apiError(diags, "list asset packs", err)
			return
		}
		m.Assets = []backgroundAssetData{}
		for _, a := range all {
			attrs := asc.Value(a.Attributes)
			m.Assets = append(m.Assets, backgroundAssetData{
				ID:                  types.StringValue(a.Id),
				AssetPackIdentifier: stringValue(attrs.AssetPackIdentifier),
				Archived:            types.BoolValue(asc.Value(attrs.Archived)),
				CreatedDate:         timeValue(attrs.CreatedDate),
			})
		}
	})

// ---- versions ----

var (
	_ resource.ResourceWithConfigure   = &backgroundAssetVersionResource{}
	_ resource.ResourceWithImportState = &backgroundAssetVersionResource{}
	_ resource.ResourceWithIdentity    = &backgroundAssetVersionResource{}
	_ resource.ResourceWithModifyPlan  = &backgroundAssetVersionResource{}
)

type backgroundAssetVersionResource struct {
	resourceIdentity
	client *asc.Client
}

type backgroundAssetVersionModel struct {
	ID                types.String `tfsdk:"id"`
	BackgroundAssetID types.String `tfsdk:"background_asset_id"`
	File              types.String `tfsdk:"file"`
	ManifestFile      types.String `tfsdk:"manifest_file"`
	Checksum          types.String `tfsdk:"checksum"`
	ManifestChecksum  types.String `tfsdk:"manifest_checksum"`
	Version           types.String `tfsdk:"version"`
	State             types.String `tfsdk:"state"`
	InternalBetaState types.String `tfsdk:"internal_beta_state"`
	ExternalBetaState types.String `tfsdk:"external_beta_state"`
	AppStoreState     types.String `tfsdk:"app_store_state"`
}

func newBackgroundAssetVersionResource() resource.Resource {
	return &backgroundAssetVersionResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the asset pack version.")}}
}

func (r *backgroundAssetVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_background_asset_version"
}

func (r *backgroundAssetVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	fileDesc := " Relative paths are relative to where Terraform runs, so use `\"${path.module}/...\"`."
	resp.Schema = schema.Schema{
		MarkdownDescription: "Uploads a version of an Apple-hosted asset pack: its archive, made with `ba-package`, and " +
			"optionally its manifest, which App Store Connect checks first. App Store Connect numbers the versions. When it " +
			"has processed a version, the version is ready for internal TestFlight testing and supersedes the previous " +
			"one there; submit it for review to release it more widely. Changed files are uploaded as a new version, " +
			"which replaces the resource. App Store Connect can't delete a version, so destroying the resource leaves it. " +
			"Import it by ID; the next apply then uploads the configured files if they differ.",
		Attributes: map[string]schema.Attribute{
			"id":                  idAttribute("App Store Connect ID of the asset pack version."),
			"background_asset_id": replacedString("App Store Connect ID of the asset pack, e.g. `apple_background_asset.example.id`."),
			"file": schema.StringAttribute{
				MarkdownDescription: "Path of the asset pack archive, e.g. a `.aar` file." + fileDesc,
				Required:            true,
			},
			"manifest_file": schema.StringAttribute{
				MarkdownDescription: "Path of the asset pack's manifest, to check before the archive is uploaded." + fileDesc,
				Optional:            true,
			},
			"checksum":            schema.StringAttribute{MarkdownDescription: "MD5 checksum of the archive.", Computed: true},
			"manifest_checksum":   schema.StringAttribute{MarkdownDescription: "MD5 checksum of the manifest.", Computed: true},
			"version":             computedString("Version number App Store Connect assigned."),
			"state":               computedString("Processing state: `AWAITING_UPLOAD`, `PROCESSING`, `FAILED` or `COMPLETE`."),
			"internal_beta_state": computedString("State of the version for internal testing, e.g. `READY_FOR_TESTING` or `SUPERSEDED`."),
			"external_beta_state": computedString("State of the version for external testing, e.g. `READY_FOR_TESTING`, once submitted."),
			"app_store_state":     computedString("State of the version on the App Store, e.g. `READY_FOR_DISTRIBUTION`, once submitted."),
		},
	}
}

func (r *backgroundAssetVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *backgroundAssetVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// fileChecksum returns the checksum of the file at a path attribute, or null
// when the attribute is null.
func fileChecksum(ctx context.Context, plan tfsdk.Plan, attr string, diags *diag.Diagnostics) (sum types.String, known bool) {
	var file types.String
	diags.Append(plan.GetAttribute(ctx, path.Root(attr), &file)...)
	switch {
	case diags.HasError() || file.IsUnknown():
		return types.StringUnknown(), false
	case file.IsNull():
		return types.StringNull(), true
	}
	data, err := readFiles([]string{file.ValueString()})
	if err != nil {
		diags.AddAttributeError(path.Root(attr), "Cannot read asset pack file", err.Error())
		return types.StringUnknown(), false
	}
	return types.StringValue(asc.Checksum(data[0])), true
}

// ModifyPlan plans the files' checksums, and replaces the resource when one
// changes.
func (r *backgroundAssetVersionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	for attr, sumAttr := range map[string]string{"file": "checksum", "manifest_file": "manifest_checksum"} {
		sum, known := fileChecksum(ctx, req.Plan, attr, &resp.Diagnostics)
		if !known {
			continue
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(sumAttr), sum)...)
		if req.State.Raw.IsNull() {
			continue
		}
		var state types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(sumAttr), &state)...)
		if !state.Equal(sum) {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root(sumAttr))
		}
	}
}

func (r *backgroundAssetVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan backgroundAssetVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	paths := []string{plan.File.ValueString()}
	if !plan.ManifestFile.IsNull() {
		paths = append(paths, plan.ManifestFile.ValueString())
	}
	data, err := readFiles(paths)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read asset pack file", err.Error())
		return
	}
	var body gen.BackgroundAssetVersionCreateRequest
	body.Data.Type = gen.BackgroundAssetVersionCreateRequestDataTypeBackgroundAssetVersions
	d := &body.Data.Relationships.BackgroundAsset.Data
	d.Id, d.Type = plan.BackgroundAssetID.ValueString(), gen.BackgroundAssetVersionCreateRequestDataRelationshipsBackgroundAssetDataTypeBackgroundAssets
	out, err := r.client.BackgroundAssetVersionsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create asset pack version", err)
		return
	}
	id := out.JSON201.Data.Id
	// The manifest goes first, so a bad one fails before the archive is sent.
	if len(data) > 1 {
		if err := r.upload(ctx, id, gen.BackgroundAssetUploadFileCreateRequestDataAttributesAssetTypeMANIFEST, paths[1], data[1]); err != nil {
			apiError(&resp.Diagnostics, "upload asset pack manifest", err)
			return
		}
	}
	if err := r.upload(ctx, id, gen.BackgroundAssetUploadFileCreateRequestDataAttributesAssetTypeASSET, paths[0], data[0]); err != nil {
		apiError(&resp.Diagnostics, "upload asset pack archive", err)
		return
	}
	plan.ID = types.StringValue(id)
	if err := r.read(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "read asset pack version", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// upload reserves, sends and commits one of a version's files.
func (r *backgroundAssetVersionResource) upload(ctx context.Context, versionID string, assetType gen.BackgroundAssetUploadFileCreateRequestDataAttributesAssetType, file string, data []byte) error {
	var body gen.BackgroundAssetUploadFileCreateRequest
	body.Data.Type = gen.BackgroundAssetUploadFileCreateRequestDataTypeBackgroundAssetUploadFiles
	a := &body.Data.Attributes
	a.AssetType, a.FileName, a.FileSize = assetType, filepath.Base(file), int64(len(data))
	d := &body.Data.Relationships.BackgroundAssetVersion.Data
	d.Id, d.Type = versionID, gen.BackgroundAssetUploadFileCreateRequestDataRelationshipsBackgroundAssetVersionDataTypeBackgroundAssetVersions
	out, err := r.client.BackgroundAssetUploadFilesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	id := out.JSON201.Data.Id
	ops := asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations)
	if err := r.client.Upload(ctx, asc.DeliveryOperations(ops), data); err != nil {
		return err
	}
	var commit gen.BackgroundAssetUploadFileUpdateRequest
	commit.Data.Type, commit.Data.Id = gen.BackgroundAssetUploadFileUpdateRequestDataTypeBackgroundAssetUploadFiles, id
	attrs := asc.Alloc(&commit.Data.Attributes)
	attrs.Uploaded = new(true)
	f := asc.Alloc(&asc.Alloc(&attrs.SourceFileChecksums).File)
	f.Hash, f.Algorithm = new(asc.Checksum(data)), new(gen.ChecksumAlgorithmMD5)
	done, err := r.client.BackgroundAssetUploadFilesUpdateInstanceWithResponse(ctx, id, commit)
	if err := asc.Err(done, err); err != nil {
		return err
	}
	if failure := assetFailure(asc.Value(done.JSON200.Data.Attributes).AssetDeliveryState); failure != "" {
		return fmt.Errorf("the upload of %s failed: %s", file, failure)
	}
	return nil
}

// read refreshes m from the version, its releases and its files.
func (r *backgroundAssetVersionResource) read(ctx context.Context, m *backgroundAssetVersionModel) error {
	out, err := r.client.BackgroundAssetVersionsGetInstanceWithResponse(ctx, m.ID.ValueString(), nil)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	v := out.JSON200.Data
	attrs := asc.Value(v.Attributes)
	m.Version, m.State = stringValue(attrs.Version), stringValue(attrs.State)
	rel := asc.Value(v.Relationships)
	if rel.BackgroundAsset != nil && rel.BackgroundAsset.Data != nil {
		m.BackgroundAssetID = types.StringValue(rel.BackgroundAsset.Data.Id)
	}
	m.InternalBetaState, m.ExternalBetaState, m.AppStoreState = types.StringNull(), types.StringNull(), types.StringNull()
	if rel.InternalBetaRelease != nil && rel.InternalBetaRelease.Data != nil {
		out, err := r.client.BackgroundAssetVersionInternalBetaReleasesGetInstanceWithResponse(ctx, rel.InternalBetaRelease.Data.Id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		m.InternalBetaState = stringValue(asc.Value(out.JSON200.Data.Attributes).State)
	}
	if rel.ExternalBetaRelease != nil && rel.ExternalBetaRelease.Data != nil {
		out, err := r.client.BackgroundAssetVersionExternalBetaReleasesGetInstanceWithResponse(ctx, rel.ExternalBetaRelease.Data.Id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		m.ExternalBetaState = stringValue(asc.Value(out.JSON200.Data.Attributes).State)
	}
	if rel.AppStoreRelease != nil && rel.AppStoreRelease.Data != nil {
		out, err := r.client.BackgroundAssetVersionAppStoreReleasesGetInstanceWithResponse(ctx, rel.AppStoreRelease.Data.Id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		m.AppStoreState = stringValue(asc.Value(out.JSON200.Data.Attributes).State)
	}
	var asset, manifest string
	if rel.AssetFile != nil && rel.AssetFile.Data != nil {
		asset = rel.AssetFile.Data.Id
	}
	if rel.ManifestFile != nil && rel.ManifestFile.Data != nil {
		manifest = rel.ManifestFile.Data.Id
	}
	if m.Checksum, err = r.fileChecksum(ctx, asset); err != nil {
		return err
	}
	m.ManifestChecksum, err = r.fileChecksum(ctx, manifest)
	return err
}

// fileChecksum returns the checksum of an upload file, or null when there
// is no file.
func (r *backgroundAssetVersionResource) fileChecksum(ctx context.Context, id string) (types.String, error) {
	if id == "" {
		return types.StringNull(), nil
	}
	out, err := r.client.BackgroundAssetUploadFilesGetInstanceWithResponse(ctx, id, nil)
	if err := asc.Err(out, err); err != nil {
		return types.StringNull(), err
	}
	sums := asc.Value(asc.Value(out.JSON200.Data.Attributes).SourceFileChecksums)
	return stringValue(asc.Value(sums.File).Hash), nil
}

func (r *backgroundAssetVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state backgroundAssetVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.read(ctx, &state)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read asset pack version", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes only file paths: changed contents replace the resource.
func (r *backgroundAssetVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

// Delete leaves the version, which App Store Connect can't delete.
func (r *backgroundAssetVersionResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

type backgroundAssetVersionData struct {
	ID          types.String `tfsdk:"id"`
	Version     types.String `tfsdk:"version"`
	State       types.String `tfsdk:"state"`
	CreatedDate types.String `tfsdk:"created_date"`
}

type backgroundAssetVersionsData struct {
	BackgroundAssetID types.String                 `tfsdk:"background_asset_id"`
	States            types.Set                    `tfsdk:"states"`
	Versions          []backgroundAssetVersionData `tfsdk:"versions"`
}

var backgroundAssetVersionStates = enum(gen.BackgroundAssetVersionStateAWAITINGUPLOAD, gen.BackgroundAssetVersionStatePROCESSING,
	gen.BackgroundAssetVersionStateFAILED, gen.BackgroundAssetVersionStateCOMPLETE)

var newBackgroundAssetVersionsDataSource = listDataSource("background_asset_versions",
	"Lists the versions of an Apple-hosted asset pack.",
	map[string]dsschema.Attribute{
		"background_asset_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the asset pack.", Required: true},
		"states":              dsFilter("Only versions in one of these processing states.", backgroundAssetVersionStates...),
		"versions": listOf("The versions, oldest first.", map[string]dsschema.Attribute{
			"id":           dsComputed("App Store Connect ID of the version."),
			"version":      dsComputed("Version number."),
			"state":        dsComputed("Processing state."),
			"created_date": dsComputed("When the version was created, in RFC 3339 format."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *backgroundAssetVersionsData, diags *diag.Diagnostics) {
		out, err := c.BackgroundAssetsVersionsGetToManyRelatedWithResponse(ctx, m.BackgroundAssetID.ValueString(), &gen.BackgroundAssetsVersionsGetToManyRelatedParams{
			FilterState: filterParam[gen.BackgroundAssetsVersionsGetToManyRelatedParamsFilterState](ctx, m.States, diags),
			Limit:       new(listAll),
		})
		var all []gen.BackgroundAssetVersion
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.BackgroundAssetVersionsResponse) []gen.BackgroundAssetVersion { return p.Data },
				func(p *gen.BackgroundAssetVersionsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list asset pack versions", err)
			return
		}
		m.Versions = []backgroundAssetVersionData{}
		for _, v := range all {
			attrs := asc.Value(v.Attributes)
			m.Versions = append(m.Versions, backgroundAssetVersionData{
				ID:          types.StringValue(v.Id),
				Version:     stringValue(attrs.Version),
				State:       stringValue(attrs.State),
				CreatedDate: timeValue(attrs.CreatedDate),
			})
		}
	})
