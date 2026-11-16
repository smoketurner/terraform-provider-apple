package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// App Clips and their default experiences: the App Clip card people see on
// the App Store. An App Clip comes from an uploaded build; an experience is
// attached to an App Store version and can't change once that version is
// released
// (https://developer.apple.com/documentation/appstoreconnectapi/app-clips-and-app-clip-experiences).

var appClipActions = enum(gen.AppClipActionOPEN, gen.AppClipActionVIEW, gen.AppClipActionPLAY)

// ---- data.apple_app_clips ----

type appClipData struct {
	ID       types.String `tfsdk:"id"`
	BundleID types.String `tfsdk:"bundle_id"`
}

type appClipsData struct {
	AppID types.String  `tfsdk:"app_id"`
	Clips []appClipData `tfsdk:"app_clips"`
}

var newAppClipsDataSource = listDataSource("app_clips",
	"Lists an app's App Clips. An App Clip appears once a build that contains it is uploaded.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"app_clips": listOf("The App Clips.", map[string]dsschema.Attribute{
			"id":        dsComputed("App Store Connect ID of the App Clip."),
			"bundle_id": dsComputed("Bundle identifier of the App Clip, e.g. `com.example.app.Clip`."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *appClipsData, diags *diag.Diagnostics) {
		out, err := c.AppsAppClipsGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsAppClipsGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.AppClip
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AppClipsResponse) []gen.AppClip { return p.Data },
				func(p *gen.AppClipsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list App Clips", err)
			return
		}
		m.Clips = []appClipData{}
		for _, clip := range all {
			m.Clips = append(m.Clips, appClipData{ID: types.StringValue(clip.Id), BundleID: stringValue(asc.Value(clip.Attributes).BundleId)})
		}
	})

// ---- apple_app_clip_default_experience ----

var (
	_ resource.ResourceWithConfigure   = &clipExperienceResource{}
	_ resource.ResourceWithImportState = &clipExperienceResource{}
	_ resource.ResourceWithIdentity    = &clipExperienceResource{}
)

type clipExperienceResource struct {
	resourceIdentity
	client *asc.Client
}

type clipExperienceModel struct {
	AppClipID types.String `tfsdk:"app_clip_id"`
	VersionID types.String `tfsdk:"release_with_app_store_version_id"`
	Action    types.String `tfsdk:"action"`
	ID        types.String `tfsdk:"id"`
}

func newClipExperienceResource() resource.Resource {
	return &clipExperienceResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the default App Clip experience.")}}
}

func (r *clipExperienceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_clip_default_experience"
}

func (r *clipExperienceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates an App Clip's default experience: the App Clip card people see when they open the App " +
			"Clip from the App Store, Safari or Messages. Add a subtitle with " +
			"`apple_app_clip_default_experience_localization`, an image with `apple_app_clip_header_image` and the " +
			"invocation URLs App Review uses with `apple_app_clip_app_store_review_detail`. The experience is released " +
			"with an App Store version, and can't change once that version is released: a new version takes a new " +
			"experience, so changing the version creates one. Destroying the resource deletes the experience unless " +
			"its version is released, in which case it stays in App Store Connect. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"app_clip_id": replacedString("App Store Connect ID of the App Clip; see `data.apple_app_clips`."),
			"release_with_app_store_version_id": replacedOptionalString("App Store Connect ID of the App Store version the experience " +
				"is released with, e.g. `apple_app_store_version.example.id`. Changing it creates a new experience."),
			"action": optionalString("Verb on the App Clip card's button: "+docValues(appClipActions)+".", oneOf(appClipActions)),
			"id":     idAttribute("App Store Connect ID of the default App Clip experience."),
		},
	}
}

func (r *clipExperienceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *clipExperienceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *clipExperienceModel) apply(e *gen.AppClipDefaultExperience) {
	m.ID, m.Action = types.StringValue(e.Id), stringValue(asc.Value(e.Attributes).Action)
	rel := e.Relationships
	if rel == nil {
		return
	}
	if rel.AppClip != nil && rel.AppClip.Data != nil {
		m.AppClipID = types.StringValue(rel.AppClip.Data.Id)
	}
	if rel.ReleaseWithAppStoreVersion != nil && rel.ReleaseWithAppStoreVersion.Data != nil {
		m.VersionID = types.StringValue(rel.ReleaseWithAppStoreVersion.Data.Id)
	}
}

func (r *clipExperienceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan clipExperienceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppClipDefaultExperienceCreateRequest
	body.Data.Type = gen.AppClipDefaultExperienceCreateRequestDataTypeAppClipDefaultExperiences
	if action := knownString(plan.Action); action != nil {
		asc.Alloc(&body.Data.Attributes).Action = new(gen.AppClipAction(*action))
	}
	rel := &body.Data.Relationships
	rel.AppClip.Data.Id, rel.AppClip.Data.Type = plan.AppClipID.ValueString(), gen.AppClipDefaultExperienceCreateRequestDataRelationshipsAppClipDataTypeAppClips
	if id := plan.VersionID.ValueString(); id != "" {
		v := asc.Alloc(&asc.Alloc(&rel.ReleaseWithAppStoreVersion).Data)
		v.Id, v.Type = id, gen.AppClipDefaultExperienceCreateRequestDataRelationshipsReleaseWithAppStoreVersionDataTypeAppStoreVersions
	}
	out, err := r.client.AppClipDefaultExperiencesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create default App Clip experience", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clipExperienceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state clipExperienceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppClipDefaultExperiencesGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppClipDefaultExperiencesGetInstanceParams{
		Include: &[]gen.AppClipDefaultExperiencesGetInstanceParamsInclude{
			gen.AppClipDefaultExperiencesGetInstanceParamsIncludeAppClip, gen.AppClipDefaultExperiencesGetInstanceParamsIncludeReleaseWithAppStoreVersion,
		},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read default App Clip experience", err)
		return
	}
	state.VersionID = types.StringNull()
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes the action, as raw JSON so that a removed action is sent
// as null.
func (r *clipExperienceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan clipExperienceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "appClipDefaultExperiences", "id": plan.ID.ValueString(), "attributes": map[string]any{"action": knownString(plan.Action)},
	}})
	if err == nil {
		out, e := r.client.AppClipDefaultExperiencesUpdateInstanceWithBodyWithResponse(ctx, plan.ID.ValueString(), "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON200.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update default App Clip experience", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the experience of a released App Clip, which App Store
// Connect refuses to delete.
func (r *clipExperienceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clipExperienceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppClipDefaultExperiencesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete default App Clip experience", err)
	}
}

// ---- apple_app_clip_default_experience_localization ----

var (
	_ resource.ResourceWithConfigure   = &clipLocalizationResource{}
	_ resource.ResourceWithImportState = &clipLocalizationResource{}
	_ resource.ResourceWithIdentity    = &clipLocalizationResource{}
)

type clipLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type clipLocalizationModel struct {
	ExperienceID types.String `tfsdk:"default_experience_id"`
	Locale       types.String `tfsdk:"locale"`
	Subtitle     types.String `tfsdk:"subtitle"`
	ID           types.String `tfsdk:"id"`
}

// resolveClipLocalization maps "<default_experience_id>/<locale>" to the
// localization's ID, and returns any other key as the ID.
func resolveClipLocalization(ctx context.Context, c *asc.Client, key string) (string, error) {
	experienceID, locale, ok := strings.Cut(key, "/")
	if !ok {
		return key, nil
	}
	out, err := c.AppClipDefaultExperiencesAppClipDefaultExperienceLocalizationsGetToManyRelatedWithResponse(ctx, experienceID,
		&gen.AppClipDefaultExperiencesAppClipDefaultExperienceLocalizationsGetToManyRelatedParams{FilterLocale: &[]string{locale}, Limit: new(listAll)})
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

func newClipLocalizationResource() resource.Resource {
	return &clipLocalizationResource{resourceIdentity: resourceIdentity{
		attrs:   idIdentity("App Store Connect ID of the localization."),
		resolve: resolveClipLocalization,
		keyName: "default experience ID and locale",
	}}
}

func (r *clipLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_clip_default_experience_localization"
}

func (r *clipLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the subtitle of a default App Clip experience for one locale; " +
			"`apple_app_clip_header_image` adds the locale's image. Provide a localization for the app's primary locale, " +
			"and the same locales as the app. It can't change once the experience's App Store version is released. " +
			"Destroying the resource deletes the localization with its header image. Import it by ID or with " +
			"`<default_experience_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"default_experience_id": replacedString("App Store Connect ID of the default experience, e.g. " +
				"`apple_app_clip_default_experience.example.id`. Changing it creates a new localization."),
			"locale":   replacedString("Locale, e.g. `en-US`. Changing it creates a new localization.", localeValidator),
			"subtitle": optionalString("Subtitle shown on the App Clip card.", stringvalidator.LengthAtLeast(1)),
			"id":       idAttribute("App Store Connect ID of the localization."),
		},
	}
}

func (r *clipLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *clipLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *clipLocalizationModel) apply(l *gen.AppClipDefaultExperienceLocalization) {
	m.ID = types.StringValue(l.Id)
	a := asc.Value(l.Attributes)
	m.Locale, m.Subtitle = stringValue(a.Locale), stringValue(a.Subtitle)
	if rel := l.Relationships; rel != nil && rel.AppClipDefaultExperience != nil && rel.AppClipDefaultExperience.Data != nil {
		m.ExperienceID = types.StringValue(rel.AppClipDefaultExperience.Data.Id)
	}
}

func (r *clipLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan clipLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppClipDefaultExperienceLocalizationCreateRequest
	body.Data.Type = gen.AppClipDefaultExperienceLocalizationCreateRequestDataTypeAppClipDefaultExperienceLocalizations
	body.Data.Attributes.Locale, body.Data.Attributes.Subtitle = plan.Locale.ValueString(), knownString(plan.Subtitle)
	e := &body.Data.Relationships.AppClipDefaultExperience.Data
	e.Id, e.Type = plan.ExperienceID.ValueString(), gen.AppClipDefaultExperienceLocalizationCreateRequestDataRelationshipsAppClipDefaultExperienceDataTypeAppClipDefaultExperiences
	out, err := r.client.AppClipDefaultExperienceLocalizationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create App Clip experience localization", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clipLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state clipLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppClipDefaultExperienceLocalizationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppClipDefaultExperienceLocalizationsGetInstanceParams{
		Include: &[]gen.AppClipDefaultExperienceLocalizationsGetInstanceParamsInclude{gen.AppClipDefaultExperienceLocalizationsGetInstanceParamsIncludeAppClipDefaultExperience},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read App Clip experience localization", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sends raw JSON so that a removed subtitle is sent as null.
func (r *clipLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan clipLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "appClipDefaultExperienceLocalizations", "id": plan.ID.ValueString(), "attributes": map[string]any{"subtitle": knownString(plan.Subtitle)},
	}})
	if err == nil {
		out, e := r.client.AppClipDefaultExperienceLocalizationsUpdateInstanceWithBodyWithResponse(ctx, plan.ID.ValueString(), "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON200.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update App Clip experience localization", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the localization of a released App Clip, like its
// experience.
func (r *clipLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clipLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppClipDefaultExperienceLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete App Clip experience localization", err)
	}
}

// ---- apple_app_clip_header_image ----

var newClipHeaderImageResource = newUploadedFileResource(fileAPI{
	typeName: "app_clip_header_image",
	noun:     "App Clip header image",
	description: "Uploads the image on the App Clip card of a default App Clip experience, for one localization. A " +
		"localization has one header image; uploading another replaces it.",
	parent:     "localization_id",
	parentDesc: "App Store Connect ID of the localization, e.g. `apple_app_clip_default_experience_localization.en_US.id`.",
	existing: func(ctx context.Context, c *asc.Client, localizationID string) (string, error) {
		out, err := c.AppClipDefaultExperienceLocalizationsAppClipHeaderImageGetToOneRelatedWithResponse(ctx, localizationID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.AppClipHeaderImagesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return asc.Value(asc.Value(out.JSON200.Data.Attributes).SourceFileChecksum), nil
	},
	reserve: func(ctx context.Context, c *asc.Client, localizationID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.AppClipHeaderImageCreateRequest
		body.Data.Type = gen.AppClipHeaderImageCreateRequestDataTypeAppClipHeaderImages
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		d := &body.Data.Relationships.AppClipDefaultExperienceLocalization.Data
		d.Id, d.Type = localizationID, gen.AppClipHeaderImageCreateRequestDataRelationshipsAppClipDefaultExperienceLocalizationDataTypeAppClipDefaultExperienceLocalizations
		out, err := c.AppClipHeaderImagesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.AppClipHeaderImageUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppClipHeaderImageUpdateRequestDataTypeAppClipHeaderImages, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.AppClipHeaderImagesUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppClipHeaderImagesDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
})

// ---- apple_app_clip_app_store_review_detail ----

var (
	_ resource.ResourceWithConfigure   = &clipReviewDetailResource{}
	_ resource.ResourceWithImportState = &clipReviewDetailResource{}
	_ resource.ResourceWithIdentity    = &clipReviewDetailResource{}
)

type clipReviewDetailResource struct {
	resourceIdentity
	client *asc.Client
}

type clipReviewDetailModel struct {
	ExperienceID   types.String `tfsdk:"default_experience_id"`
	InvocationURLs types.List   `tfsdk:"invocation_urls"`
	ID             types.String `tfsdk:"id"`
}

func newClipReviewDetailResource() resource.Resource {
	return &clipReviewDetailResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the App Clip's App Review detail.")}}
}

func (r *clipReviewDetailResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_clip_app_store_review_detail"
}

func (r *clipReviewDetailResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the invocation URLs App Review uses to launch a default App Clip experience. An " +
			"experience has one App Review detail: the resource creates it, or manages the existing one. App Store " +
			"Connect can't delete it, so destroying the resource leaves the URLs in place. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"default_experience_id": replacedString("App Store Connect ID of the default experience, e.g. `apple_app_clip_default_experience.example.id`."),
			"invocation_urls": schema.ListAttribute{
				MarkdownDescription: "URLs that launch the App Clip, e.g. `[\"https://example.com/order\"]`.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1), listvalidator.UniqueValues()},
			},
			"id": idAttribute("App Store Connect ID of the App Clip's App Review detail."),
		},
	}
}

func (r *clipReviewDetailResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *clipReviewDetailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *clipReviewDetailModel) apply(d *gen.AppClipAppStoreReviewDetail) {
	m.ID = types.StringValue(d.Id)
	m.InvocationURLs = stringsList(asc.Value(asc.Value(d.Attributes).InvocationUrls))
	if rel := d.Relationships; rel != nil && rel.AppClipDefaultExperience != nil && rel.AppClipDefaultExperience.Data != nil {
		m.ExperienceID = types.StringValue(rel.AppClipDefaultExperience.Data.Id)
	}
}

// update sets the invocation URLs of the detail id.
func (r *clipReviewDetailResource) update(ctx context.Context, id string, urls []string) (*gen.AppClipAppStoreReviewDetail, error) {
	var body gen.AppClipAppStoreReviewDetailUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppClipAppStoreReviewDetailUpdateRequestDataTypeAppClipAppStoreReviewDetails, id
	asc.Alloc(&body.Data.Attributes).InvocationUrls = &urls
	out, err := r.client.AppClipAppStoreReviewDetailsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// Create creates the experience's App Review detail, or sets the URLs of
// the one it has.
func (r *clipReviewDetailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan clipReviewDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	urls := listStrings(ctx, plan.InvocationURLs)
	if resp.Diagnostics.HasError() {
		return
	}
	experienceID := plan.ExperienceID.ValueString()
	var detail *gen.AppClipAppStoreReviewDetail
	existing, err := r.client.AppClipDefaultExperiencesAppClipAppStoreReviewDetailGetToOneRelatedWithResponse(ctx, experienceID, nil)
	switch err = asc.Err(existing, err); {
	case err == nil:
		detail, err = r.update(ctx, existing.JSON200.Data.Id, urls)
	case asc.IsNotFound(err):
		var body gen.AppClipAppStoreReviewDetailCreateRequest
		body.Data.Type = gen.AppClipAppStoreReviewDetailCreateRequestDataTypeAppClipAppStoreReviewDetails
		asc.Alloc(&body.Data.Attributes).InvocationUrls = &urls
		e := &body.Data.Relationships.AppClipDefaultExperience.Data
		e.Id, e.Type = experienceID, gen.AppClipAppStoreReviewDetailCreateRequestDataRelationshipsAppClipDefaultExperienceDataTypeAppClipDefaultExperiences
		out, cerr := r.client.AppClipAppStoreReviewDetailsCreateInstanceWithResponse(ctx, body)
		if err = asc.Err(out, cerr); err == nil {
			detail = &out.JSON201.Data
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "set App Clip App Review detail", err)
		return
	}
	plan.apply(detail)
	plan.ExperienceID = types.StringValue(experienceID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clipReviewDetailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state clipReviewDetailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppClipAppStoreReviewDetailsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppClipAppStoreReviewDetailsGetInstanceParams{
		Include: &[]gen.AppClipAppStoreReviewDetailsGetInstanceParamsInclude{gen.AppClipAppStoreReviewDetailsGetInstanceParamsIncludeAppClipDefaultExperience},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read App Clip App Review detail", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *clipReviewDetailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan clipReviewDetailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	urls := listStrings(ctx, plan.InvocationURLs)
	if resp.Diagnostics.HasError() {
		return
	}
	detail, err := r.update(ctx, plan.ID.ValueString(), urls)
	if err != nil {
		apiError(&resp.Diagnostics, "update App Clip App Review detail", err)
		return
	}
	plan.apply(detail)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete only forgets the detail: the API has no way to delete one.
func (r *clipReviewDetailResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
