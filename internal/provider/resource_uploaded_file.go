package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Single uploaded files: App Review attachments, encryption declaration
// documents and routing app coverage files. Each resource uploads one file
// for its parent; a file can't be changed after upload, so changing its
// contents replaces the resource.

type fileAPI struct {
	typeName    string
	noun        string
	description string
	parent      string // attribute name of the parent's ID
	parentDesc  string
	// existing returns the parent's current file, for parents with one
	// file, which a new upload replaces; nil for parents with many.
	existing func(ctx context.Context, c *asc.Client, parentID string) (string, error)
	get      func(ctx context.Context, c *asc.Client, id string) (checksum string, err error)
	reserve  func(ctx context.Context, c *asc.Client, parentID, name string, size int) (string, []gen.UploadOperation, error)
	commit   func(ctx context.Context, c *asc.Client, id, checksum string) (failure string, err error)
	// remove deletes a file; nil when the API can't.
	remove func(ctx context.Context, c *asc.Client, id string) error
}

var (
	_ resource.ResourceWithConfigure   = &uploadedFileResource{}
	_ resource.ResourceWithImportState = &uploadedFileResource{}
	_ resource.ResourceWithIdentity    = &uploadedFileResource{}
	_ resource.ResourceWithModifyPlan  = &uploadedFileResource{}
)

type uploadedFileResource struct {
	resourceIdentity
	api    fileAPI
	client *asc.Client
}

func newUploadedFileResource(api fileAPI) func() resource.Resource {
	return func() resource.Resource {
		return &uploadedFileResource{api: api, resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the " + api.noun + ".")}}
	}
}

func (r *uploadedFileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *uploadedFileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	destroy := "Destroying the resource deletes the file."
	if r.api.remove == nil {
		destroy = "App Store Connect can't delete it, so destroying the resource leaves it."
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: r.api.description + " A changed file is uploaded as a new one, which replaces the " +
			"resource. " + destroy + " Import it by ID; the next apply then uploads the configured file.",
		Attributes: map[string]schema.Attribute{
			r.api.parent: replacedString(r.api.parentDesc),
			"file": schema.StringAttribute{
				MarkdownDescription: "Path of the file to upload. Relative paths are relative to where Terraform runs, so use `\"${path.module}/...\"`.",
				Required:            true,
			},
			"checksum": schema.StringAttribute{MarkdownDescription: "MD5 checksum of the file.", Computed: true},
			"id":       idAttribute("App Store Connect ID of the " + r.api.noun + "."),
		},
	}
}

func (r *uploadedFileResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *uploadedFileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ModifyPlan plans the file's checksum, and replaces the resource when it
// changes.
func (r *uploadedFileResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
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
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
	}
}

func (r *uploadedFileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var parent, file types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(r.api.parent), &parent)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("file"), &file)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, sum, err := r.upload(ctx, parent.ValueString(), file.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "upload "+r.api.noun, err)
		return
	}
	resp.State.Raw = req.Plan.Raw
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("checksum"), sum)...)
}

func (r *uploadedFileResource) upload(ctx context.Context, parentID, file string) (id, sum string, err error) {
	data, err := readFiles([]string{file})
	if err != nil {
		return "", "", err
	}
	if r.api.existing != nil {
		old, err := r.api.existing(ctx, r.client, parentID)
		switch {
		case err == nil && r.api.remove != nil:
			if err := r.api.remove(ctx, r.client, old); err != nil && !asc.IsNotFound(err) {
				return "", "", fmt.Errorf("deleting the current %s: %w", r.api.noun, err)
			}
		case err != nil && !asc.IsNotFound(err):
			return "", "", err
		}
	}
	sum = asc.Checksum(data[0])
	id, ops, err := r.api.reserve(ctx, r.client, parentID, filepath.Base(file), len(data[0]))
	if err != nil {
		return "", "", err
	}
	if err := r.client.Upload(ctx, ops, data[0]); err != nil {
		return "", "", err
	}
	failure, err := r.api.commit(ctx, r.client, id, sum)
	if err != nil {
		return "", "", err
	}
	if failure != "" {
		return "", "", fmt.Errorf("the upload of %s failed: %s", file, failure)
	}
	return id, sum, nil
}

func (r *uploadedFileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sum, err := r.api.get(ctx, r.client, id.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+r.api.noun, err)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("checksum"), sum)...)
}

// Update changes only the file path: a changed checksum replaces the
// resource.
func (r *uploadedFileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *uploadedFileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.api.remove == nil {
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.remove(ctx, r.client, id.ValueString())
	if err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete "+r.api.noun, err)
	}
}

func assetFailure(state *gen.AppMediaAssetState) string {
	s := asc.Value(state)
	return mediaFailure(string(asc.Value(s.State)), s.Errors)
}

var reviewAttachmentAPI = fileAPI{
	typeName:    "app_store_review_attachment",
	noun:        "App Review attachment",
	description: "Uploads a file for App Review with an App Store version's review details, such as a demo video or documentation.",
	parent:      "review_detail_id",
	parentDesc:  "App Store Connect ID of the review details, e.g. `apple_app_store_review_detail.example.id`.",
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.AppStoreReviewAttachmentsGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return asc.Value(asc.Value(out.JSON200.Data.Attributes).SourceFileChecksum), nil
	},
	reserve: func(ctx context.Context, c *asc.Client, parentID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.AppStoreReviewAttachmentCreateRequest
		body.Data.Type = gen.AppStoreReviewAttachmentCreateRequestDataTypeAppStoreReviewAttachments
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		d := &body.Data.Relationships.AppStoreReviewDetail.Data
		d.Id, d.Type = parentID, gen.AppStoreReviewAttachmentCreateRequestDataRelationshipsAppStoreReviewDetailDataTypeAppStoreReviewDetails
		out, err := c.AppStoreReviewAttachmentsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.AppStoreReviewAttachmentUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppStoreReviewAttachmentUpdateRequestDataTypeAppStoreReviewAttachments, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.AppStoreReviewAttachmentsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppStoreReviewAttachmentsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var declarationDocumentAPI = fileAPI{
	typeName: "app_encryption_declaration_document",
	noun:     "encryption declaration document",
	description: "Uploads the export compliance documentation of an encryption declaration, such as a French " +
		"encryption declaration or a CCATS. A declaration has one document; uploading another replaces it.",
	parent:     "declaration_id",
	parentDesc: "App Store Connect ID of the encryption declaration, e.g. `apple_app_encryption_declaration.example.id`.",
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.AppEncryptionDeclarationDocumentsGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return asc.Value(asc.Value(out.JSON200.Data.Attributes).SourceFileChecksum), nil
	},
	reserve: func(ctx context.Context, c *asc.Client, parentID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.AppEncryptionDeclarationDocumentCreateRequest
		body.Data.Type = gen.AppEncryptionDeclarationDocumentCreateRequestDataTypeAppEncryptionDeclarationDocuments
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		d := &body.Data.Relationships.AppEncryptionDeclaration.Data
		d.Id, d.Type = parentID, gen.AppEncryptionDeclarationDocumentCreateRequestDataRelationshipsAppEncryptionDeclarationDataTypeAppEncryptionDeclarations
		out, err := c.AppEncryptionDeclarationDocumentsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.AppEncryptionDeclarationDocumentUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppEncryptionDeclarationDocumentUpdateRequestDataTypeAppEncryptionDeclarationDocuments, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.AppEncryptionDeclarationDocumentsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
}

var routingCoverageAPI = fileAPI{
	typeName: "app_store_version_routing_app_coverage",
	noun:     "routing app coverage file",
	description: "Uploads the GeoJSON file of the regions a routing app covers for an App Store version. A " +
		"version has one; creating this resource replaces an existing file.",
	parent:     "version_id",
	parentDesc: "App Store Connect ID of the App Store version, e.g. `apple_app_store_version.example.id`.",
	existing: func(ctx context.Context, c *asc.Client, parentID string) (string, error) {
		out, err := c.AppStoreVersionsRoutingAppCoverageGetToOneRelatedWithResponse(ctx, parentID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.RoutingAppCoveragesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return asc.Value(asc.Value(out.JSON200.Data.Attributes).SourceFileChecksum), nil
	},
	reserve: func(ctx context.Context, c *asc.Client, parentID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.RoutingAppCoverageCreateRequest
		body.Data.Type = gen.RoutingAppCoverageCreateRequestDataTypeRoutingAppCoverages
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		d := &body.Data.Relationships.AppStoreVersion.Data
		d.Id, d.Type = parentID, gen.RoutingAppCoverageCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		out, err := c.RoutingAppCoveragesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.RoutingAppCoverageUpdateRequest
		body.Data.Type, body.Data.Id = gen.RoutingAppCoverageUpdateRequestDataTypeRoutingAppCoverages, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.RoutingAppCoveragesUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.RoutingAppCoveragesDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var (
	newReviewAttachmentResource    = newUploadedFileResource(reviewAttachmentAPI)
	newDeclarationDocumentResource = newUploadedFileResource(declarationDocumentAPI)
	newRoutingCoverageResource     = newUploadedFileResource(routingCoverageAPI)
)
