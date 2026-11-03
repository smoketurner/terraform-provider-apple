package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// In-app purchases. The purchase holds what stays fixed across review cycles:
// product ID, type and pricing. Its localizations and promotional image
// belong to a version, which is edited in Prepare for Submission and is
// read-only after review
// (https://developer.apple.com/documentation/appstoreconnectapi/working-with-in-app-purchase-versions).
// As with app info, the provider hides versions: the localization and image
// resources are keyed by purchase, change the version being prepared, and
// start the next version when the current one has been reviewed.

var iapTypes = enum(gen.InAppPurchaseTypeCONSUMABLE, gen.InAppPurchaseTypeNONCONSUMABLE, gen.InAppPurchaseTypeNONRENEWINGSUBSCRIPTION)

var iapVersionsInReview = enum(gen.InAppPurchaseVersionAttributesStateREADYFORREVIEW, gen.InAppPurchaseVersionAttributesStateWAITINGFORREVIEW,
	gen.InAppPurchaseVersionAttributesStateINREVIEW)

var errIAPInReview = errors.New("the in-app purchase is in review; its metadata can change once review finishes")

// iapVersions returns a purchase's version being prepared, if any, and the
// one to read: that version, or else the latest one that isn't replaced.
func iapVersions(ctx context.Context, c *asc.Client, iapID string) (draft, current, latest *gen.InAppPurchaseVersion, err error) {
	out, err := c.InAppPurchasesV2VersionsGetToManyRelatedWithResponse(ctx, iapID, &gen.InAppPurchasesV2VersionsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, nil, nil, err
	}
	for i := range out.JSON200.Data {
		v := &out.JSON200.Data[i]
		a := asc.Value(v.Attributes)
		if latest == nil || asc.Value(a.Version) > asc.Value(latest.Attributes.Version) {
			latest = v
		}
		state := asc.Value(a.State)
		switch {
		case state == gen.InAppPurchaseVersionAttributesStatePREPAREFORSUBMISSION:
			draft = v
		case state != gen.InAppPurchaseVersionAttributesStateREPLACEDWITHNEWVERSION &&
			(current == nil || asc.Value(a.Version) > asc.Value(current.Attributes.Version)):
			current = v
		}
	}
	if draft != nil {
		current = draft
	}
	return draft, current, latest, nil
}

// iapVersionLocks serializes starting a purchase's next version: the
// localization and image resources of one purchase apply in parallel, and
// only one of them may create the version.
var iapVersionLocks = struct {
	sync.Mutex
	byIAP map[string]*sync.Mutex
}{byIAP: map[string]*sync.Mutex{}}

// lockIAPVersions locks starting a purchase's next version and returns the
// unlock function.
func lockIAPVersions(iapID string) func() {
	iapVersionLocks.Lock()
	lock, ok := iapVersionLocks.byIAP[iapID]
	if !ok {
		lock = &sync.Mutex{}
		iapVersionLocks.byIAP[iapID] = lock
	}
	iapVersionLocks.Unlock()
	lock.Lock()
	return lock.Unlock
}

// editableIAPVersion returns the purchase's version being prepared,
// starting one when the latest has been reviewed.
func editableIAPVersion(ctx context.Context, c *asc.Client, iapID string) (string, error) {
	defer lockIAPVersions(iapID)()
	draft, _, latest, err := iapVersions(ctx, c, iapID)
	if err != nil {
		return "", err
	}
	if draft != nil {
		return draft.Id, nil
	}
	if latest != nil && slices.Contains(iapVersionsInReview, string(asc.Value(latest.Attributes.State))) {
		return "", errIAPInReview
	}
	var body gen.InAppPurchaseVersionCreateRequest
	body.Data.Type = gen.InAppPurchaseVersionCreateRequestDataTypeInAppPurchaseVersions
	body.Data.Relationships.InAppPurchase.Data.Id = iapID
	body.Data.Relationships.InAppPurchase.Data.Type = gen.InAppPurchaseVersionCreateRequestDataRelationshipsInAppPurchaseDataTypeInAppPurchases
	out, err := c.InAppPurchaseVersionsCreateInstanceWithResponse(ctx, body)
	err = asc.Err(out, err)
	if asc.HasCode(err, "STATE_ERROR") {
		// Another client started the version first; use it.
		if draft, _, _, lerr := iapVersions(ctx, c, iapID); lerr == nil && draft != nil {
			return draft.Id, nil
		}
	}
	if err != nil {
		return "", err
	}
	return out.JSON201.Data.Id, nil
}

// ---- apple_in_app_purchase ----

var (
	_ resource.ResourceWithConfigure   = &iapResource{}
	_ resource.ResourceWithImportState = &iapResource{}
	_ resource.ResourceWithIdentity    = &iapResource{}
)

type iapResource struct {
	resourceIdentity
	client *asc.Client
}

type iapModel struct {
	AppID          types.String `tfsdk:"app_id"`
	ProductID      types.String `tfsdk:"product_id"`
	Type           types.String `tfsdk:"type"`
	Name           types.String `tfsdk:"name"`
	ReviewNote     types.String `tfsdk:"review_note"`
	FamilySharable types.Bool   `tfsdk:"family_sharable"`
	ID             types.String `tfsdk:"id"`
	State          types.String `tfsdk:"state"`
	ContentHosting types.Bool   `tfsdk:"content_hosting"`
}

// The API doesn't link a purchase to its app, so the import ID has both.
func newIAPResource() resource.Resource {
	return &iapResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "id", description: "App Store Connect ID of the in-app purchase.", state: "id"},
	}}}
}

func (r *iapResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_in_app_purchase"
}

func (r *iapResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an in-app purchase: a consumable, non-consumable or non-renewing subscription. " +
			"Set its customer-facing names with `apple_in_app_purchase_localization`, and submit it with " +
			"`apple_submit_in_app_purchase`. Product IDs can't be reused, even after deletion. Destroying the resource " +
			"deletes a purchase that was never approved; an approved one stays, and can be removed from sale in App " +
			"Store Connect. Import it with `<app_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"app_id":      replacedString("App Store Connect ID of the app."),
			"product_id":  replacedString("Product ID the app uses in StoreKit, e.g. `com.example.coins100`. It can't be reused.", stringvalidator.LengthBetween(1, 100)),
			"type":        replacedString("Type: "+docValues(iapTypes)+".", oneOf(iapTypes)),
			"name":        schema.StringAttribute{MarkdownDescription: "Reference name, seen only in App Store Connect; up to 64 characters.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 64)}},
			"review_note": optionalString("Notes for App Review, up to 4,000 characters.", stringvalidator.LengthAtMost(4000)),
			"family_sharable": schema.BoolAttribute{
				MarkdownDescription: "Whether family members can use the purchase, for non-consumables and non-renewing " +
					"subscriptions. Once on, it can't be turned off. Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"id":              idAttribute("App Store Connect ID of the in-app purchase."),
			"state":           computedOnly("State of the purchase, e.g. `MISSING_METADATA`, `READY_TO_SUBMIT` or `APPROVED`."),
			"content_hosting": schema.BoolAttribute{MarkdownDescription: "Whether Apple hosts content for the purchase.", Computed: true},
		},
	}
}

func (r *iapResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *iapResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *iapModel) apply(p *gen.InAppPurchaseV2) {
	a := asc.Value(p.Attributes)
	m.ID, m.ProductID, m.Type, m.Name = types.StringValue(p.Id), stringValue(a.ProductId), stringValue(a.InAppPurchaseType), stringValue(a.Name)
	m.ReviewNote, m.State = stringValue(a.ReviewNote), stringValue(a.State)
	m.FamilySharable, m.ContentHosting = types.BoolValue(asc.Value(a.FamilySharable)), types.BoolValue(asc.Value(a.ContentHosting))
}

func (r *iapResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan iapModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.InAppPurchaseV2CreateRequest
	body.Data.Type = gen.InAppPurchaseV2CreateRequestDataTypeInAppPurchases
	a := &body.Data.Attributes
	a.Name, a.ProductId, a.InAppPurchaseType = plan.Name.ValueString(), plan.ProductID.ValueString(), gen.InAppPurchaseType(plan.Type.ValueString())
	a.ReviewNote, a.FamilySharable = knownString(plan.ReviewNote), new(plan.FamilySharable.ValueBool())
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.InAppPurchaseV2CreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.InAppPurchasesV2CreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create in-app purchase", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iapResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state iapModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.InAppPurchasesV2GetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read in-app purchase", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *iapResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state iapModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.InAppPurchaseV2UpdateRequest
	body.Data.Type, body.Data.Id = gen.InAppPurchaseV2UpdateRequestDataTypeInAppPurchases, state.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.Name, a.ReviewNote = new(plan.Name.ValueString()), changedString(plan.ReviewNote, state.ReviewNote)
	a.FamilySharable = new(plan.FamilySharable.ValueBool())
	out, err := r.client.InAppPurchasesV2UpdateInstanceWithResponse(ctx, state.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update in-app purchase", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes a purchase that was never approved, and leaves an
// approved one.
func (r *iapResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state iapModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.InAppPurchasesV2DeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete in-app purchase", err)
	}
}

// ---- apple_in_app_purchase_localization ----

var (
	_ resource.ResourceWithConfigure   = &iapLocalizationResource{}
	_ resource.ResourceWithImportState = &iapLocalizationResource{}
	_ resource.ResourceWithIdentity    = &iapLocalizationResource{}
)

type iapLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type iapLocalizationModel struct {
	IAPID       types.String `tfsdk:"in_app_purchase_id"`
	Locale      types.String `tfsdk:"locale"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ID          types.String `tfsdk:"id"`
}

func newIAPLocalizationResource() resource.Resource {
	return &iapLocalizationResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "in_app_purchase_id", description: "App Store Connect ID of the in-app purchase.", state: "in_app_purchase_id"},
		{name: "locale", description: "Locale, e.g. `en-US`.", state: "locale"},
	}}}
}

func (r *iapLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_in_app_purchase_localization"
}

func (r *iapLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an in-app purchase's display name and description for one locale. Changes go to the " +
			"version being prepared; when the current version has been reviewed, applying starts the next one. Changes " +
			"fail while a version is in review. Destroying the resource deletes the localization from the version being " +
			"prepared, and otherwise leaves it. Import it with `<in_app_purchase_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"in_app_purchase_id": replacedString("App Store Connect ID of the in-app purchase, e.g. `apple_in_app_purchase.example.id`."),
			"locale":             replacedString("Locale, e.g. `en-US`.", localeValidator),
			"name":               schema.StringAttribute{MarkdownDescription: "Display name, 2 to 35 characters.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(2, 35)}},
			"description":        optionalString("Description, up to 55 characters.", stringvalidator.LengthAtMost(55)),
			"id":                 schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the localization, which changes with each version.", Computed: true},
		},
	}
}

func (r *iapLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *iapLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *iapLocalizationModel) apply(l *gen.InAppPurchaseLocalizationV2) {
	a := asc.Value(l.Attributes)
	m.ID, m.Name, m.Description = types.StringValue(l.Id), stringValue(a.Name), stringValue(a.Description)
}

func findIAPLocalization(ctx context.Context, c *asc.Client, versionID, locale string) (*gen.InAppPurchaseLocalizationV2, error) {
	out, err := c.InAppPurchaseVersionsLocalizationsGetToManyRelatedWithResponse(ctx, versionID, &gen.InAppPurchaseVersionsLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	for i := range out.JSON200.Data {
		if asc.Value(asc.Value(out.JSON200.Data[i].Attributes).Locale) == locale {
			return &out.JSON200.Data[i], nil
		}
	}
	return nil, errNoMatch
}

// write sets the locale's localization in the version being prepared:
// a version starts with copies of the previous one's localizations, so it
// updates the copy when there is one.
func (r *iapLocalizationResource) write(ctx context.Context, plan *iapLocalizationModel) (*gen.InAppPurchaseLocalizationV2, error) {
	versionID, err := editableIAPVersion(ctx, r.client, plan.IAPID.ValueString())
	if err != nil {
		return nil, err
	}
	existing, err := findIAPLocalization(ctx, r.client, versionID, plan.Locale.ValueString())
	switch {
	case err == nil:
		var current iapLocalizationModel
		current.apply(existing)
		var body gen.InAppPurchaseLocalizationV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.InAppPurchaseLocalizationV2UpdateRequestDataTypeInAppPurchaseLocalizations, existing.Id
		a := asc.Alloc(&body.Data.Attributes)
		a.Name, a.Description = new(plan.Name.ValueString()), changedString(plan.Description, current.Description)
		out, err := r.client.InAppPurchaseLocalizationsV2UpdateInstanceWithResponse(ctx, existing.Id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return &out.JSON200.Data, nil
	case errors.Is(err, errNoMatch):
		var body gen.InAppPurchaseLocalizationV2CreateRequest
		body.Data.Type = gen.InAppPurchaseLocalizationV2CreateRequestDataTypeInAppPurchaseLocalizations
		a := &body.Data.Attributes
		a.Locale, a.Name, a.Description = plan.Locale.ValueString(), plan.Name.ValueString(), knownString(plan.Description)
		body.Data.Relationships.Version.Data.Id = versionID
		body.Data.Relationships.Version.Data.Type = gen.InAppPurchaseLocalizationV2CreateRequestDataRelationshipsVersionDataTypeInAppPurchaseVersions
		out, err := r.client.InAppPurchaseLocalizationsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return &out.JSON201.Data, nil
	}
	return nil, err
}

func (r *iapLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan iapLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.write(ctx, &plan)
	if err != nil {
		apiError(&resp.Diagnostics, "set in-app purchase localization", err)
		return
	}
	plan.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iapLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state iapLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, current, _, err := iapVersions(ctx, r.client, state.IAPID.ValueString())
	var l *gen.InAppPurchaseLocalizationV2
	if err == nil && current == nil {
		err = errNoMatch
	}
	if err == nil {
		l, err = findIAPLocalization(ctx, r.client, current.Id, state.Locale.ValueString())
	}
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read in-app purchase localization", err)
		return
	}
	state.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *iapLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan iapLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.write(ctx, &plan)
	if err != nil {
		apiError(&resp.Diagnostics, "set in-app purchase localization", err)
		return
	}
	plan.apply(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the locale from the version being prepared; reviewed
// versions keep it.
func (r *iapLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state iapLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	draft, _, _, err := iapVersions(ctx, r.client, state.IAPID.ValueString())
	if asc.IsNotFound(err) || err == nil && draft == nil {
		return
	}
	var l *gen.InAppPurchaseLocalizationV2
	if err == nil {
		l, err = findIAPLocalization(ctx, r.client, draft.Id, state.Locale.ValueString())
	}
	if errors.Is(err, errNoMatch) {
		return
	}
	if err == nil {
		out, e := r.client.InAppPurchaseLocalizationsV2DeleteInstanceWithResponse(ctx, l.Id)
		if err = asc.Err(out, e); asc.IsNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "delete in-app purchase localization", err)
	}
}

// ---- images ----

var iapImageAPI = fileAPI{
	typeName: "in_app_purchase_image",
	noun:     "in-app purchase image",
	description: "Uploads an in-app purchase's promotional image, 1024 by 1024 pixels, shown on the App Store. It " +
		"belongs to the version being prepared; when the current version has been reviewed, applying starts the next " +
		"one. App Store Connect doesn't report the image's checksum, so changes to the image made outside Terraform " +
		"aren't detected.",
	parent:     "in_app_purchase_id",
	parentDesc: "App Store Connect ID of the in-app purchase, e.g. `apple_in_app_purchase.example.id`.",
	existing: func(ctx context.Context, c *asc.Client, iapID string) (string, error) {
		versionID, err := editableIAPVersion(ctx, c, iapID)
		if err != nil {
			return "", err
		}
		out, err := c.InAppPurchaseVersionsImageGetToOneRelatedWithResponse(ctx, versionID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.InAppPurchaseImagesV2GetInstanceWithResponse(ctx, id, nil)
		return "", asc.Err(out, err)
	},
	reserve: func(ctx context.Context, c *asc.Client, iapID, name string, size int) (string, []gen.UploadOperation, error) {
		versionID, err := editableIAPVersion(ctx, c, iapID)
		if err != nil {
			return "", nil, err
		}
		var body gen.InAppPurchaseImageV2CreateRequest
		body.Data.Type = gen.InAppPurchaseImageV2CreateRequestDataTypeInAppPurchaseImages
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		body.Data.Relationships.Version.Data.Id = versionID
		body.Data.Relationships.Version.Data.Type = gen.InAppPurchaseImageV2CreateRequestDataRelationshipsVersionDataTypeInAppPurchaseVersions
		out, err := c.InAppPurchaseImagesV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, _ string) (string, error) {
		var body gen.InAppPurchaseImageV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.InAppPurchaseImageV2UpdateRequestDataTypeInAppPurchaseImages, id
		asc.Alloc(&body.Data.Attributes).Uploaded = new(true)
		out, err := c.InAppPurchaseImagesV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.InAppPurchaseImagesV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var iapReviewScreenshotAPI = fileAPI{
	typeName:    "in_app_purchase_review_screenshot",
	noun:        "in-app purchase review screenshot",
	description: "Uploads the screenshot App Review uses to find an in-app purchase in the app. A purchase has one; creating this resource replaces an existing one.",
	parent:      "in_app_purchase_id",
	parentDesc:  "App Store Connect ID of the in-app purchase, e.g. `apple_in_app_purchase.example.id`.",
	existing: func(ctx context.Context, c *asc.Client, iapID string) (string, error) {
		out, err := c.InAppPurchasesV2AppStoreReviewScreenshotGetToOneRelatedWithResponse(ctx, iapID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.InAppPurchaseAppStoreReviewScreenshotsGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return asc.Value(asc.Value(out.JSON200.Data.Attributes).SourceFileChecksum), nil
	},
	reserve: func(ctx context.Context, c *asc.Client, iapID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.InAppPurchaseAppStoreReviewScreenshotCreateRequest
		body.Data.Type = gen.InAppPurchaseAppStoreReviewScreenshotCreateRequestDataTypeInAppPurchaseAppStoreReviewScreenshots
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		body.Data.Relationships.InAppPurchaseV2.Data.Id = iapID
		body.Data.Relationships.InAppPurchaseV2.Data.Type = gen.InAppPurchaseAppStoreReviewScreenshotCreateRequestDataRelationshipsInAppPurchaseV2DataTypeInAppPurchases
		out, err := c.InAppPurchaseAppStoreReviewScreenshotsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.InAppPurchaseAppStoreReviewScreenshotUpdateRequest
		body.Data.Type, body.Data.Id = gen.InAppPurchaseAppStoreReviewScreenshotUpdateRequestDataTypeInAppPurchaseAppStoreReviewScreenshots, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.InAppPurchaseAppStoreReviewScreenshotsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.InAppPurchaseAppStoreReviewScreenshotsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var (
	newIAPImageResource            = newUploadedFileResource(iapImageAPI)
	newIAPReviewScreenshotResource = newUploadedFileResource(iapReviewScreenshotAPI)
)

// ---- apple_submit_in_app_purchase ----

type submitIAPModel struct {
	AppID    types.String `tfsdk:"app_id"`
	IAPID    types.String `tfsdk:"in_app_purchase_id"`
	Platform types.String `tfsdk:"platform"`
	Submit   types.Bool   `tfsdk:"submit"`
}

var newSubmitIAPAction = newAction("submit_in_app_purchase",
	"Submits an in-app purchase's version being prepared to App Review, by adding it to the platform's review "+
		"submission that is being prepared, or a new one. An app's first in-app purchase must be reviewed with an App "+
		"Store version: set `submit = false`, then run `apple_submit_for_review`, which adds the version to the same "+
		"submission and submits both.",
	map[string]actionschema.Attribute{
		"app_id":             actionString("App Store Connect ID of the app."),
		"in_app_purchase_id": actionString("App Store Connect ID of the in-app purchase, e.g. `apple_in_app_purchase.example.id`."),
		"platform": actionschema.StringAttribute{
			MarkdownDescription: "Platform of the review submission: " + docValues(storePlatforms) + ". Defaults to `IOS`.",
			Optional:            true,
			Validators:          []validator.String{oneOf(storePlatforms)},
		},
		"submit": actionschema.BoolAttribute{
			MarkdownDescription: "Whether to submit the review submission. Set it to `false` to add an App Store version to the same submission. Defaults to `true`.",
			Optional:            true,
		},
	},
	func(ctx context.Context, c *asc.Client, m *submitIAPModel) (string, error) {
		draft, _, _, err := iapVersions(ctx, c, m.IAPID.ValueString())
		if err != nil {
			return "", err
		}
		if draft == nil {
			return "", fmt.Errorf("in-app purchase %s has no version being prepared", m.IAPID.ValueString())
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
		v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.InAppPurchaseVersion).Data)
		v.Id, v.Type = draft.Id, gen.ReviewSubmissionItemCreateRequestDataRelationshipsInAppPurchaseVersionDataTypeInAppPurchaseVersions
		out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		if !m.Submit.IsNull() && !m.Submit.ValueBool() {
			return "Added in-app purchase " + m.IAPID.ValueString() + " to review submission " + submissionID, nil
		}
		return "Submitted in-app purchase " + m.IAPID.ValueString() + " for review", updateSubmission(ctx, c, submissionID, true)
	})

// ---- data.apple_in_app_purchases ----

type iapData struct {
	ID        types.String `tfsdk:"id"`
	ProductID types.String `tfsdk:"product_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	State     types.String `tfsdk:"state"`
}

type iapsData struct {
	AppID          types.String `tfsdk:"app_id"`
	ProductIDs     types.Set    `tfsdk:"product_ids"`
	Types          types.Set    `tfsdk:"types"`
	InAppPurchases []iapData    `tfsdk:"in_app_purchases"`
}

var newIAPsDataSource = listDataSource("in_app_purchases",
	"Lists an app's in-app purchases.",
	map[string]dsschema.Attribute{
		"app_id":      dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"product_ids": dsFilter("Only purchases with these product IDs."),
		"types":       dsFilter("Only purchases of these types.", iapTypes...),
		"in_app_purchases": listOf("The in-app purchases.", map[string]dsschema.Attribute{
			"id":         dsComputed("App Store Connect ID of the purchase."),
			"product_id": dsComputed("Product ID."),
			"name":       dsComputed("Reference name."),
			"type":       dsComputed("Type."),
			"state":      dsComputed("State."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *iapsData, diags *diag.Diagnostics) {
		out, err := c.AppsInAppPurchasesV2GetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsInAppPurchasesV2GetToManyRelatedParams{
			FilterProductId:         filterParam[string](ctx, m.ProductIDs, diags),
			FilterInAppPurchaseType: filterParam[gen.AppsInAppPurchasesV2GetToManyRelatedParamsFilterInAppPurchaseType](ctx, m.Types, diags),
			Limit:                   new(listAll),
		})
		var all []gen.InAppPurchaseV2
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.InAppPurchasesV2Response) []gen.InAppPurchaseV2 { return p.Data },
				func(p *gen.InAppPurchasesV2Response) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list in-app purchases", err)
			return
		}
		m.InAppPurchases = []iapData{}
		for _, p := range all {
			var im iapModel
			im.apply(&p)
			m.InAppPurchases = append(m.InAppPurchases, iapData{ID: im.ID, ProductID: im.ProductID, Name: im.Name, Type: im.Type, State: im.State})
		}
	})
