package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/action"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Images of versioned localizations, such as a Game Center achievement's
// image for a locale. Like the localization, the image belongs to a version
// of the product: the resource is keyed by the product and the locale,
// uploads to the version being prepared and starts the next version when
// the current one has been reviewed.

// localizationImageAPI is the part of the API a kind of localization image
// needs.
type localizationImageAPI struct {
	typeName      string
	noun          string // e.g. "Game Center achievement image"
	description   string
	localizations localizationAPI
	// versionLevel is set for a version's default image, which belongs to
	// the version instead of one of its localizations: the resource then
	// has no locale, and the owner below is the version.
	versionLevel bool
	// existing returns the ID of the image of a localization, or of a
	// version for a version-level image.
	existing func(ctx context.Context, c *asc.Client, ownerID string) (string, error)
	reserve  func(ctx context.Context, c *asc.Client, ownerID, name string, size int) (string, []gen.UploadOperation, error)
	commit   func(ctx context.Context, c *asc.Client, id string) (failure string, err error)
	remove   func(ctx context.Context, c *asc.Client, id string) error
}

var (
	_ resource.ResourceWithConfigure   = &localizationImageResource{}
	_ resource.ResourceWithImportState = &localizationImageResource{}
	_ resource.ResourceWithIdentity    = &localizationImageResource{}
	_ resource.ResourceWithModifyPlan  = &localizationImageResource{}
)

type localizationImageResource struct {
	resourceIdentity
	api    localizationImageAPI
	client *asc.Client
}

func newLocalizationImageResource(api localizationImageAPI) func() resource.Resource {
	parent := api.localizations.parent
	identity := []identityAttribute{{name: parent, description: api.localizations.parentDesc, state: parent}}
	if !api.versionLevel {
		identity = append(identity, identityAttribute{name: "locale", description: "Locale, e.g. `en-US`.", state: "locale"})
	}
	return func() resource.Resource {
		return &localizationImageResource{api: api, resourceIdentity: resourceIdentity{attrs: identity}}
	}
}

func (r *localizationImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *localizationImageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	parent := r.api.localizations.parent
	needs, key := " The locale needs its localization first, so refer to the localization resource's `locale`.", "`<"+parent+">/<locale>`"
	if r.api.versionLevel {
		needs, key = "", "the "+strings.TrimSuffix(strings.ReplaceAll(parent, "_", " "), " id")+"'s ID"
	}
	attributes := map[string]schema.Attribute{
		parent:   replacedString(r.api.localizations.parentDesc),
		"locale": replacedString("Locale of the localization the image is for, e.g. `en-US`.", localeValidator),
	}
	if r.api.versionLevel {
		delete(attributes, "locale")
	}
	maps.Copy(attributes, r.imageAttributes())
	resp.Schema = schema.Schema{
		MarkdownDescription: r.api.description + needs + " The image goes to the version being prepared; when the current " +
			"version has been reviewed, applying a changed file starts the next one, and a change fails while a version " +
			"is in review. Destroying the resource deletes the image from the version being prepared, and otherwise " +
			"leaves it. Import it with " + key + "; the next apply then uploads the configured file.",
		Attributes: attributes,
	}
}

func (r *localizationImageResource) imageAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"file": schema.StringAttribute{
			MarkdownDescription: "Path of the image. Relative paths are relative to where Terraform runs, so use `\"${path.module}/...\"`.",
			Required:            true,
		},
		"checksum": schema.StringAttribute{MarkdownDescription: "MD5 checksum of the uploaded file.", Computed: true},
		"id":       schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the image, which changes with each version and upload.", Computed: true},
	}
}

func (r *localizationImageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *localizationImageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ModifyPlan plans the file's checksum, and an upload when it changes.
func (r *localizationImageResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
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
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
	}
}

// localization returns the ID of the image's owner in a version: the
// locale's localization, or the version itself for a version-level image.
func (r *localizationImageResource) localization(ctx context.Context, versionID, locale string) (string, error) {
	if r.api.versionLevel {
		return versionID, nil
	}
	all, err := r.api.localizations.list(ctx, r.client, versionID)
	if err != nil {
		return "", err
	}
	for _, l := range all {
		if l.locale == locale {
			return l.id, nil
		}
	}
	return "", errNoMatch
}

// upload replaces the locale's image in the version being prepared.
func (r *localizationImageResource) upload(ctx context.Context, parentID, locale, file string) (id, sum string, err error) {
	data, err := readFiles([]string{file})
	if err != nil {
		return "", "", err
	}
	versionID, err := r.api.localizations.versions.editable(ctx, r.client, parentID)
	if err != nil {
		return "", "", err
	}
	localizationID, err := r.localization(ctx, versionID, locale)
	if errors.Is(err, errNoMatch) {
		return "", "", fmt.Errorf("the version being prepared has no %s localization; create it first", locale)
	}
	if err != nil {
		return "", "", err
	}
	switch old, err := r.api.existing(ctx, r.client, localizationID); {
	case err == nil:
		if err := r.api.remove(ctx, r.client, old); err != nil && !asc.IsNotFound(err) {
			return "", "", fmt.Errorf("deleting the current image: %w", err)
		}
	case !asc.IsNotFound(err):
		return "", "", err
	}
	id, operations, err := r.api.reserve(ctx, r.client, localizationID, filepath.Base(file), len(data[0]))
	if err != nil {
		return "", "", err
	}
	if err := r.client.Upload(ctx, operations, data[0]); err != nil {
		return "", "", err
	}
	failure, err := r.api.commit(ctx, r.client, id)
	if err != nil {
		return "", "", err
	}
	if failure != "" {
		return "", "", fmt.Errorf("the upload of %s failed: %s", file, failure)
	}
	return id, asc.Checksum(data[0]), nil
}

// key reads the product and locale of a plan or state.
func (r *localizationImageResource) key(ctx context.Context, get func(context.Context, path.Path, any) diag.Diagnostics, diags *diag.Diagnostics) (parent, locale string) {
	var p, l types.String
	diags.Append(get(ctx, path.Root(r.api.localizations.parent), &p)...)
	if !r.api.versionLevel {
		diags.Append(get(ctx, path.Root("locale"), &l)...)
	}
	return p.ValueString(), l.ValueString()
}

func (r *localizationImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	r.write(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *localizationImageResource) write(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	parent, locale := r.key(ctx, plan.GetAttribute, diags)
	var file types.String
	diags.Append(plan.GetAttribute(ctx, path.Root("file"), &file)...)
	if diags.HasError() {
		return
	}
	id, sum, err := r.upload(ctx, parent, locale, file.ValueString())
	if err != nil {
		apiError(diags, "upload "+r.api.noun, err)
		return
	}
	state.Raw = plan.Raw
	diags.Append(state.SetAttribute(ctx, path.Root("id"), id)...)
	diags.Append(state.SetAttribute(ctx, path.Root("checksum"), sum)...)
}

// current returns the ID of the locale's image in the version to read, or
// errNoMatch.
func (r *localizationImageResource) current(ctx context.Context, parentID, locale string, draftOnly bool) (string, error) {
	draft, current, _, err := r.api.localizations.versions.find(ctx, r.client, parentID)
	if err != nil {
		return "", err
	}
	if draftOnly {
		current = draft
	}
	if current == nil {
		return "", errNoMatch
	}
	localizationID, err := r.localization(ctx, current.id, locale)
	if err != nil {
		return "", err
	}
	return r.api.existing(ctx, r.client, localizationID)
}

func (r *localizationImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	parent, locale := r.key(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.current(ctx, parent, locale, false)
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+r.api.noun, err)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// Update uploads the file when its checksum changes; a changed path alone
// is only recorded.
func (r *localizationImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var planned, uploaded types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("checksum"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("checksum"), &uploaded)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if planned.Equal(uploaded) {
		resp.State.Raw = req.Plan.Raw
		return
	}
	r.write(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

// Delete removes the image from the version being prepared; reviewed
// versions keep it.
func (r *localizationImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	parent, locale := r.key(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.current(ctx, parent, locale, true)
	if err == nil {
		err = r.api.remove(ctx, r.client, id)
	}
	if err != nil && !asc.IsNotFound(err) && !errors.Is(err, errNoMatch) {
		apiError(&resp.Diagnostics, "delete "+r.api.noun, err)
	}
}

// ---- submitting a versioned item for review ----

type submitVersionedModel struct {
	AppID    types.String `tfsdk:"app_id"`
	ItemID   types.String `tfsdk:"item_id"`
	Platform types.String `tfsdk:"platform"`
	Submit   types.Bool   `tfsdk:"submit"`
}

// newSubmitVersionedAction returns an action that adds the version being
// prepared of a versioned item, such as a Game Center achievement, to the
// app's review submission. setItem names the version in the item request.
func newSubmitVersionedAction(typeName, noun, example string, versions metadataVersions,
	setItem func(item *gen.ReviewSubmissionItemCreateRequest, versionID string),
) func() action.Action {
	return newAction(typeName,
		"Submits the version being prepared of a "+noun+" to App Review, by adding it to the platform's review submission "+
			"that is being prepared, or a new one. The version needs a localization.",
		map[string]actionschema.Attribute{
			"app_id":  actionString("App Store Connect ID of an app the " + noun + " belongs to."),
			"item_id": actionString("App Store Connect ID of the " + noun + ", e.g. `" + example + "`."),
			"platform": actionschema.StringAttribute{
				MarkdownDescription: "Platform of the review submission: " + docValues(storePlatforms) + ". Defaults to `IOS`.",
				Optional:            true,
				Validators:          []validator.String{oneOf(storePlatforms)},
			},
			"submit": actionschema.BoolAttribute{
				MarkdownDescription: "Whether to submit the review submission. Set it to `false` to add other items to the same submission. Defaults to `true`.",
				Optional:            true,
			},
		},
		func(ctx context.Context, c *asc.Client, m *submitVersionedModel) (string, error) {
			draft, _, _, err := versions.find(ctx, c, m.ItemID.ValueString())
			if err != nil {
				return "", err
			}
			if draft == nil {
				return "", fmt.Errorf("%s %s has no version being prepared", noun, m.ItemID.ValueString())
			}
			platform := gen.PlatformIOS
			if !m.Platform.IsNull() {
				platform = gen.Platform(m.Platform.ValueString())
			}
			submissionID, err := openSubmission(ctx, c, m.AppID.ValueString(), platform)
			if err != nil {
				return "", err
			}
			var item gen.ReviewSubmissionItemCreateRequest
			item.Data.Type = gen.ReviewSubmissionItemCreateRequestDataTypeReviewSubmissionItems
			item.Data.Relationships.ReviewSubmission.Data.Id = submissionID
			item.Data.Relationships.ReviewSubmission.Data.Type = gen.ReviewSubmissionItemCreateRequestDataRelationshipsReviewSubmissionDataTypeReviewSubmissions
			setItem(&item, draft.id)
			out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			if !m.Submit.IsNull() && !m.Submit.ValueBool() {
				return "Added the " + noun + " to review submission " + submissionID, nil
			}
			if err := updateSubmission(ctx, c, submissionID, true); err != nil {
				return "", err
			}
			return "Submitted the " + noun + " in review submission " + submissionID, nil
		})
}
