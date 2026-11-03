package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// The App Asset Library
// (https://developer.apple.com/documentation/appstoreconnectapi/placing-assets-on-your-app-store-surfaces):
// an app's images and videos, which placements show on App Store surfaces.
// Assets are uploaded like screenshots but committed without a checksum, so
// the provider keeps the checksum of the file it uploaded.

// libraryAsset is what the provider reads of an image or video.
type libraryAsset struct {
	category, referenceName, previewFrameTimeCode, state, specID string
	failure                                                      string
	uploadOperations                                             []gen.UploadOperation
}

func stateDetailsFailure(state string, details *[]gen.StateDetail) string {
	if state != string(gen.AppAssetLibraryAssetStateFAILED) {
		return ""
	}
	var parts []string
	for _, d := range asc.Value(details) {
		parts = append(parts, asc.Value(d.Code)+": "+asc.Value(d.Description))
	}
	if len(parts) == 0 {
		return "processing failed"
	}
	return strings.Join(parts, "; ")
}

// The attributes of images and videos are unions by state; the awaiting
// upload variants hold every attribute of the others.

func imageAsset(a *gen.AppAssetLibraryImage_Attributes) (libraryAsset, error) {
	if a == nil {
		return libraryAsset{}, nil
	}
	v, err := a.AsAppAssetLibraryImageAwaitingUploadAttributes()
	if err != nil {
		return libraryAsset{}, err
	}
	return libraryAsset{
		category: string(asc.Value(v.Category)), referenceName: asc.Value(v.ReferenceName), state: string(v.State),
		specID: asc.Value(v.SpecId), failure: stateDetailsFailure(string(v.State), v.StateDetails),
		uploadOperations: asc.Value(v.UploadOperations),
	}, nil
}

func videoAsset(a *gen.AppAssetLibraryVideo_Attributes) (libraryAsset, error) {
	if a == nil {
		return libraryAsset{}, nil
	}
	v, err := a.AsAppAssetLibraryVideoAwaitingUploadAttributes()
	if err != nil {
		return libraryAsset{}, err
	}
	return libraryAsset{
		category: string(asc.Value(v.Category)), referenceName: asc.Value(v.ReferenceName), state: string(v.State),
		previewFrameTimeCode: asc.Value(v.PreviewFrameTimeCode), specID: asc.Value(v.SpecId),
		failure: stateDetailsFailure(string(v.State), v.StateDetails), uploadOperations: asc.Value(v.UploadOperations),
	}, nil
}

// assetLibraryID returns the ID of an app's asset library.
func assetLibraryID(ctx context.Context, c *asc.Client, appID string) (string, error) {
	out, err := c.AppsAssetLibraryGetToOneRelatedWithResponse(ctx, appID, nil)
	if err := asc.Err(out, err); err != nil {
		return "", err
	}
	return out.JSON200.Data.Id, nil
}

// libraryAssetChange is an update of an image or video; nil fields are left
// alone.
type libraryAssetChange struct {
	uploaded                            bool
	referenceName, previewFrameTimeCode *string
}

type libraryAssetAPI struct {
	typeName, noun string
	video          bool
	get            func(ctx context.Context, c *asc.Client, id string) (libraryAsset, error)
	reserve        func(ctx context.Context, c *asc.Client, libraryID, category, name string, size int, change libraryAssetChange) (string, libraryAsset, error)
	update         func(ctx context.Context, c *asc.Client, id string, change libraryAssetChange) (libraryAsset, error)
	remove         func(ctx context.Context, c *asc.Client, id string) error
}

var imageAPI = libraryAssetAPI{
	typeName: "app_asset_library_image",
	noun:     "asset library image",
	get: func(ctx context.Context, c *asc.Client, id string) (libraryAsset, error) {
		out, err := c.AppAssetLibraryImagesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return libraryAsset{}, err
		}
		return imageAsset(out.JSON200.Data.Attributes)
	},
	reserve: func(ctx context.Context, c *asc.Client, libraryID, category, name string, size int, change libraryAssetChange) (string, libraryAsset, error) {
		var body gen.AppAssetLibraryImageCreateRequest
		body.Data.Type = gen.AppAssetLibraryImageCreateRequestDataTypeAppAssetLibraryImages
		a := &body.Data.Attributes
		a.Category, a.FileName, a.FileSize, a.ReferenceName = gen.AppAssetLibraryAssetCategory(category), name, int64(size), change.referenceName
		d := &body.Data.Relationships.AssetLibrary.Data
		d.Id, d.Type = libraryID, gen.AppAssetLibraryImageCreateRequestDataRelationshipsAssetLibraryDataTypeAppAssetLibraries
		out, err := c.AppAssetLibraryImagesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", libraryAsset{}, err
		}
		asset, err := imageAsset(out.JSON201.Data.Attributes)
		return out.JSON201.Data.Id, asset, err
	},
	update: func(ctx context.Context, c *asc.Client, id string, change libraryAssetChange) (libraryAsset, error) {
		var body gen.AppAssetLibraryImageUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppAssetLibraryImageUpdateRequestDataTypeAppAssetLibraryImages, id
		a := asc.Alloc(&body.Data.Attributes)
		a.ReferenceName = change.referenceName
		if change.uploaded {
			a.Uploaded = new(true)
		}
		out, err := c.AppAssetLibraryImagesUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return libraryAsset{}, err
		}
		return imageAsset(out.JSON200.Data.Attributes)
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppAssetLibraryImagesDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var videoAPI = libraryAssetAPI{
	typeName: "app_asset_library_video",
	noun:     "asset library video",
	video:    true,
	get: func(ctx context.Context, c *asc.Client, id string) (libraryAsset, error) {
		out, err := c.AppAssetLibraryVideosGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return libraryAsset{}, err
		}
		return videoAsset(out.JSON200.Data.Attributes)
	},
	reserve: func(ctx context.Context, c *asc.Client, libraryID, category, name string, size int, change libraryAssetChange) (string, libraryAsset, error) {
		var body gen.AppAssetLibraryVideoCreateRequest
		body.Data.Type = gen.AppAssetLibraryVideoCreateRequestDataTypeAppAssetLibraryVideos
		a := &body.Data.Attributes
		a.Category, a.FileName, a.FileSize = gen.AppAssetLibraryAssetCategory(category), name, int64(size)
		a.ReferenceName, a.PreviewFrameTimeCode = change.referenceName, change.previewFrameTimeCode
		d := &body.Data.Relationships.AssetLibrary.Data
		d.Id, d.Type = libraryID, gen.AppAssetLibraryVideoCreateRequestDataRelationshipsAssetLibraryDataTypeAppAssetLibraries
		out, err := c.AppAssetLibraryVideosCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", libraryAsset{}, err
		}
		asset, err := videoAsset(out.JSON201.Data.Attributes)
		return out.JSON201.Data.Id, asset, err
	},
	update: func(ctx context.Context, c *asc.Client, id string, change libraryAssetChange) (libraryAsset, error) {
		var body gen.AppAssetLibraryVideoUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppAssetLibraryVideoUpdateRequestDataTypeAppAssetLibraryVideos, id
		a := asc.Alloc(&body.Data.Attributes)
		a.ReferenceName, a.PreviewFrameTimeCode = change.referenceName, change.previewFrameTimeCode
		if change.uploaded {
			a.Uploaded = new(true)
		}
		out, err := c.AppAssetLibraryVideosUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return libraryAsset{}, err
		}
		return videoAsset(out.JSON200.Data.Attributes)
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppAssetLibraryVideosDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

// libraryAssetFields are the attributes images and videos share.
type libraryAssetFields struct {
	ID            types.String `tfsdk:"id"`
	AppID         types.String `tfsdk:"app_id"`
	File          types.String `tfsdk:"file"`
	Checksum      types.String `tfsdk:"checksum"`
	Category      types.String `tfsdk:"category"`
	ReferenceName types.String `tfsdk:"reference_name"`
	State         types.String `tfsdk:"state"`
	SpecID        types.String `tfsdk:"spec_id"`
}

type libraryImageModel struct {
	libraryAssetFields
}

type libraryVideoModel struct {
	libraryAssetFields
	PreviewFrameTimeCode types.String `tfsdk:"preview_frame_time_code"`
}

// libraryAssetModel is a pointer to an image or video model.
type libraryAssetModel[M any] interface {
	*M
	fields() *libraryAssetFields
	// frame returns the video's preview frame time code, or nil for images.
	frame() *types.String
}

func (m *libraryImageModel) fields() *libraryAssetFields { return &m.libraryAssetFields }
func (m *libraryImageModel) frame() *types.String        { return nil }
func (m *libraryVideoModel) fields() *libraryAssetFields { return &m.libraryAssetFields }
func (m *libraryVideoModel) frame() *types.String        { return &m.PreviewFrameTimeCode }

var (
	_ resource.ResourceWithConfigure   = &libraryAssetResource[libraryImageModel, *libraryImageModel]{}
	_ resource.ResourceWithImportState = &libraryAssetResource[libraryImageModel, *libraryImageModel]{}
	_ resource.ResourceWithIdentity    = &libraryAssetResource[libraryImageModel, *libraryImageModel]{}
	_ resource.ResourceWithModifyPlan  = &libraryAssetResource[libraryImageModel, *libraryImageModel]{}
)

type libraryAssetResource[M any, PM libraryAssetModel[M]] struct {
	resourceIdentity
	api    libraryAssetAPI
	client *asc.Client
}

func newLibraryAssetResource[M any, PM libraryAssetModel[M]](api libraryAssetAPI) func() resource.Resource {
	return func() resource.Resource {
		return &libraryAssetResource[M, PM]{api: api, resourceIdentity: resourceIdentity{attrs: []identityAttribute{
			{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
			{name: "id", description: "App Store Connect ID of the " + api.noun + ".", state: "id"},
		}}}
	}
}

var (
	newLibraryImageResource = newLibraryAssetResource[libraryImageModel](imageAPI)
	newLibraryVideoResource = newLibraryAssetResource[libraryVideoModel](videoAPI)
)

var assetCategories = enum(gen.AppAssetLibraryAssetCategoryAPPSCREENSHOTSANDPREVIEWS, gen.AppAssetLibraryAssetCategoryCREATIVEASSETS)

func (r *libraryAssetResource[M, PM]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *libraryAssetResource[M, PM]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	media := "an image (PNG or JPEG)"
	if r.api.video {
		media = "a video"
	}
	attrs := map[string]schema.Attribute{
		"id":     idAttribute("App Store Connect ID of the " + r.api.noun + "."),
		"app_id": replacedString("App Store Connect ID of the app. Changing it creates a new " + r.api.noun + "."),
		"file": schema.StringAttribute{
			MarkdownDescription: "Path of the file to upload. Relative paths are relative to where Terraform runs, so use `\"${path.module}/...\"`.",
			Required:            true,
		},
		"checksum": schema.StringAttribute{MarkdownDescription: "MD5 checksum of the uploaded file.", Computed: true},
		"category": replacedString("Category: "+docValues(assetCategories)+". Changing it uploads a new "+r.api.noun+".",
			oneOf(assetCategories)),
		"reference_name": optionalString("Name of the asset in the library.", stringvalidator.LengthAtLeast(1)),
		"state":          schema.StringAttribute{MarkdownDescription: "State of the asset, e.g. `COMPLETE` once processed or `APPROVED` after review.", Computed: true},
		"spec_id":        schema.StringAttribute{MarkdownDescription: "ID of the asset specification App Store Connect matched the file to.", Computed: true},
	}
	if r.api.video {
		attrs["preview_frame_time_code"] = optionalString("Time code of the frame shown before the video plays, e.g. `00:00:05:00`.",
			stringvalidator.LengthAtLeast(1))
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Uploads " + media + " to an app's App Asset Library, for `apple_app_asset_library_placement` " +
			"to show. A changed file is uploaded as a new asset, which replaces the resource and its placements. Destroying " +
			"the resource deletes the asset, which must have no placements left. Import it with `<app_id>/<id>`; the next " +
			"apply then uploads the configured file, because App Store Connect doesn't report a checksum to compare.",
		Attributes: attrs,
	}
}

func (r *libraryAssetResource[M, PM]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *libraryAssetResource[M, PM]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ModifyPlan plans the file's checksum, and replaces the resource when it
// changes.
func (r *libraryAssetResource[M, PM]) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var file types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("file"), &file)...)
	if resp.Diagnostics.HasError() || file.IsUnknown() {
		return
	}
	data, err := readFiles([]string{file.ValueString()})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("file"), "Cannot read "+r.api.noun, err.Error())
		return
	}
	sum := types.StringValue(asc.Checksum(data[0]))
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("checksum"), sum)...)
	if req.State.Raw.IsNull() {
		return
	}
	var state types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("checksum"), &state)...)
	if !state.Equal(sum) {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("checksum"))
	}
}

func (r *libraryAssetResource[M, PM]) apply(m PM, a libraryAsset) {
	f := m.fields()
	f.Category, f.State = types.StringValue(a.category), types.StringValue(a.state)
	f.ReferenceName, f.SpecID = types.StringNull(), types.StringNull()
	if a.referenceName != "" {
		f.ReferenceName = types.StringValue(a.referenceName)
	}
	if a.specID != "" {
		f.SpecID = types.StringValue(a.specID)
	}
	if frame := m.frame(); frame != nil {
		*frame = types.StringNull()
		if a.previewFrameTimeCode != "" {
			*frame = types.StringValue(a.previewFrameTimeCode)
		}
	}
}

func (r *libraryAssetResource[M, PM]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan M
	m := PM(&plan)
	resp.Diagnostics.Append(req.Plan.Get(ctx, m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f := m.fields()
	data, err := readFiles([]string{f.File.ValueString()})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("file"), "Cannot read "+r.api.noun, err.Error())
		return
	}
	change := libraryAssetChange{referenceName: knownString(f.ReferenceName)}
	if frame := m.frame(); frame != nil {
		change.previewFrameTimeCode = knownString(*frame)
	}
	id, asset, err := r.upload(ctx, f.AppID.ValueString(), f.Category.ValueString(), f.File.ValueString(), data[0], change)
	if err != nil {
		apiError(&resp.Diagnostics, "upload "+r.api.noun, err)
		return
	}
	f.ID, f.Checksum = types.StringValue(id), types.StringValue(asc.Checksum(data[0]))
	r.apply(m, asset)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *libraryAssetResource[M, PM]) upload(ctx context.Context, appID, category, file string, data []byte, change libraryAssetChange) (string, libraryAsset, error) {
	libraryID, err := assetLibraryID(ctx, r.client, appID)
	if err != nil {
		return "", libraryAsset{}, err
	}
	id, reserved, err := r.api.reserve(ctx, r.client, libraryID, category, filepath.Base(file), len(data), change)
	if err != nil {
		return "", libraryAsset{}, err
	}
	if err := r.client.Upload(ctx, reserved.uploadOperations, data); err != nil {
		return "", libraryAsset{}, err
	}
	asset, err := r.api.update(ctx, r.client, id, libraryAssetChange{uploaded: true})
	if err != nil {
		return "", libraryAsset{}, err
	}
	if asset.failure != "" {
		return "", libraryAsset{}, fmt.Errorf("the upload of %s failed: %s", file, asset.failure)
	}
	return id, asset, nil
}

func (r *libraryAssetResource[M, PM]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state M
	m := PM(&state)
	resp.Diagnostics.Append(req.State.Get(ctx, m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	asset, err := r.api.get(ctx, r.client, m.fields().ID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+r.api.noun, err)
		return
	}
	r.apply(m, asset)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Update changes the reference name or preview frame; a changed file
// replaces the resource.
func (r *libraryAssetResource[M, PM]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state M
	m, prior := PM(&plan), PM(&state)
	resp.Diagnostics.Append(req.Plan.Get(ctx, m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	change := libraryAssetChange{referenceName: changedString(m.fields().ReferenceName, prior.fields().ReferenceName)}
	if frame := m.frame(); frame != nil {
		change.previewFrameTimeCode = changedString(*frame, *prior.frame())
	}
	asset, err := r.api.update(ctx, r.client, m.fields().ID.ValueString(), change)
	if err != nil {
		apiError(&resp.Diagnostics, "update "+r.api.noun, err)
		return
	}
	r.apply(m, asset)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *libraryAssetResource[M, PM]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.api.remove(ctx, r.client, id.ValueString()); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete "+r.api.noun, err)
	}
}

// ---- data source ----

type libraryAssetData struct {
	ID            types.String `tfsdk:"id"`
	ReferenceName types.String `tfsdk:"reference_name"`
	Category      types.String `tfsdk:"category"`
	State         types.String `tfsdk:"state"`
}

type assetLibraryData struct {
	AppID  types.String       `tfsdk:"app_id"`
	ID     types.String       `tfsdk:"id"`
	Images []libraryAssetData `tfsdk:"images"`
	Videos []libraryAssetData `tfsdk:"videos"`
}

func libraryAssetItem(id string, a libraryAsset) libraryAssetData {
	item := libraryAssetData{ID: types.StringValue(id), Category: types.StringValue(a.category), State: types.StringValue(a.state), ReferenceName: types.StringNull()}
	if a.referenceName != "" {
		item.ReferenceName = types.StringValue(a.referenceName)
	}
	return item
}

func libraryItems() map[string]dsschema.Attribute {
	return map[string]dsschema.Attribute{
		"id":             dsComputed("App Store Connect ID of the asset."),
		"reference_name": dsComputed("Name of the asset in the library."),
		"category":       dsComputed("Category of the asset."),
		"state":          dsComputed("State of the asset."),
	}
}

var newAssetLibraryDataSource = listDataSource("app_asset_library",
	"Reads an app's App Asset Library: its ID and its images and videos.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"id":     dsComputed("App Store Connect ID of the asset library."),
		"images": listOf("The library's images.", libraryItems()),
		"videos": listOf("The library's videos.", libraryItems()),
	},
	func(ctx context.Context, c *asc.Client, m *assetLibraryData, diags *diag.Diagnostics) {
		id, err := assetLibraryID(ctx, c, m.AppID.ValueString())
		if err != nil {
			apiError(diags, "read asset library", err)
			return
		}
		m.ID = types.StringValue(id)
		m.Images, m.Videos = []libraryAssetData{}, []libraryAssetData{}
		images, err := c.AppAssetLibrariesImagesGetToManyRelatedWithResponse(ctx, id, &gen.AppAssetLibrariesImagesGetToManyRelatedParams{Limit: new(listAll)})
		var allImages []gen.AppAssetLibraryImage
		if err = asc.Err(images, err); err == nil {
			allImages, err = asc.All(ctx, c, images.JSON200,
				func(p *gen.AppAssetLibraryImagesResponse) []gen.AppAssetLibraryImage { return p.Data },
				func(p *gen.AppAssetLibraryImagesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list asset library images", err)
			return
		}
		for _, img := range allImages {
			a, err := imageAsset(img.Attributes)
			if err != nil {
				apiError(diags, "read asset library image", err)
				return
			}
			m.Images = append(m.Images, libraryAssetItem(img.Id, a))
		}
		videos, err := c.AppAssetLibrariesVideosGetToManyRelatedWithResponse(ctx, id, &gen.AppAssetLibrariesVideosGetToManyRelatedParams{Limit: new(listAll)})
		var allVideos []gen.AppAssetLibraryVideo
		if err = asc.Err(videos, err); err == nil {
			allVideos, err = asc.All(ctx, c, videos.JSON200,
				func(p *gen.AppAssetLibraryVideosResponse) []gen.AppAssetLibraryVideo { return p.Data },
				func(p *gen.AppAssetLibraryVideosResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list asset library videos", err)
			return
		}
		for _, v := range allVideos {
			a, err := videoAsset(v.Attributes)
			if err != nil {
				apiError(diags, "read asset library video", err)
				return
			}
			m.Videos = append(m.Videos, libraryAssetItem(v.Id, a))
		}
	})
