package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Retention messaging: the message people see when they start to cancel a
// subscription, its reviewed versions and their localized text, the
// discounted offers shown with it, and the links from a subscription to its
// message and offers
// (https://developer.apple.com/documentation/appstoreconnectapi/setting-up-retention-messaging).

// ---- apple_retention_message ----

var (
	_ resource.ResourceWithConfigure   = &retentionMessageResource{}
	_ resource.ResourceWithImportState = &retentionMessageResource{}
	_ resource.ResourceWithIdentity    = &retentionMessageResource{}
)

type retentionMessageResource struct {
	resourceIdentity
	client *asc.Client
}

type retentionMessageModel struct {
	AppID         types.String `tfsdk:"app_id"`
	ReferenceName types.String `tfsdk:"reference_name"`
	ID            types.String `tfsdk:"id"`
}

// The API doesn't link a retention message to its app, so the import ID has
// both.
func newRetentionMessageResource() resource.Resource {
	return &retentionMessageResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "id", description: "App Store Connect ID of the retention message.", state: "id"},
	}}}
}

func (r *retentionMessageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_retention_message"
}

func (r *retentionMessageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines a retention message, which people see when they start to cancel an auto-renewable " +
			"subscription. The message holds its content in `apple_retention_message_version` resources, and " +
			"`apple_subscription_retention_message` assigns it to a subscription. An app can have 10 retention messages. " +
			"A message can't change, so every change creates a new one. Destroying the resource deletes the message " +
			"with its versions. Import it with `<app_id>/<id>`; `data.apple_retention_messages` lists an app's messages.",
		Attributes: map[string]schema.Attribute{
			"app_id":         replacedString("App Store Connect ID of the app."),
			"reference_name": replacedString("Reference name of the message, which people don't see.", stringvalidator.LengthBetween(1, 64)),
			"id":             idAttribute("App Store Connect ID of the retention message."),
		},
	}
}

func (r *retentionMessageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *retentionMessageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *retentionMessageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan retentionMessageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.RetentionMessageCreateRequest
	body.Data.Type = gen.RetentionMessageCreateRequestDataTypeRetentionMessages
	body.Data.Attributes.ReferenceName = plan.ReferenceName.ValueString()
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.RetentionMessageCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.RetentionMessagesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create retention message", err)
		return
	}
	plan.ID = types.StringValue(out.JSON201.Data.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionMessageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state retentionMessageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.RetentionMessagesGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read retention message", err)
		return
	}
	state.ReferenceName = stringValue(asc.Value(out.JSON200.Data.Attributes).ReferenceName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument replaces the resource.
func (r *retentionMessageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *retentionMessageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state retentionMessageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.RetentionMessagesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete retention message", err)
	}
}

// ---- data.apple_retention_messages ----

type retentionMessageData struct {
	ID            types.String `tfsdk:"id"`
	ReferenceName types.String `tfsdk:"reference_name"`
}

type retentionMessagesData struct {
	AppID    types.String           `tfsdk:"app_id"`
	Messages []retentionMessageData `tfsdk:"retention_messages"`
}

var newRetentionMessagesDataSource = listDataSource("retention_messages",
	"Lists an app's retention messages.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"retention_messages": listOf("The retention messages.", map[string]dsschema.Attribute{
			"id":             dsComputed("App Store Connect ID of the retention message."),
			"reference_name": dsComputed("Reference name of the message."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *retentionMessagesData, diags *diag.Diagnostics) {
		out, err := c.AppsRetentionMessagesGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsRetentionMessagesGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.RetentionMessage
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.RetentionMessagesResponse) []gen.RetentionMessage { return p.Data },
				func(p *gen.RetentionMessagesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list retention messages", err)
			return
		}
		m.Messages = []retentionMessageData{}
		for _, msg := range all {
			m.Messages = append(m.Messages, retentionMessageData{ID: types.StringValue(msg.Id), ReferenceName: stringValue(asc.Value(msg.Attributes).ReferenceName)})
		}
	})

// ---- apple_retention_message_version ----

var (
	_ resource.ResourceWithConfigure   = &retentionVersionResource{}
	_ resource.ResourceWithImportState = &retentionVersionResource{}
	_ resource.ResourceWithIdentity    = &retentionVersionResource{}
)

type retentionVersionResource struct {
	resourceIdentity
	client *asc.Client
}

type retentionVersionModel struct {
	MessageID   types.String `tfsdk:"retention_message_id"`
	PlacementID types.String `tfsdk:"placement_id"`
	State       types.String `tfsdk:"state"`
	ID          types.String `tfsdk:"id"`
}

func newRetentionVersionResource() resource.Resource {
	return &retentionVersionResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the retention message version.")}}
}

func (r *retentionVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_retention_message_version"
}

func (r *retentionVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a version of a retention message's content, which App Review reviews before people " +
			"see it. Add text with `apple_retention_message_localization`, then submit the version with the " +
			"`apple_submit_retention_message_version` action. A version can't be changed or deleted: every change creates " +
			"a new version, and destroying the resource only removes it from the Terraform state. Deleting the retention " +
			"message deletes its versions. To change approved content, create a new version. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"retention_message_id": replacedString("App Store Connect ID of the retention message, e.g. `apple_retention_message.example.id`."),
			"placement_id": replacedOptionalString("App Store Connect ID of a `" + retentionPlacementType + "` placement whose image the " +
				"message shows, e.g. `apple_app_asset_library_placement.example.id`. Unset means a text-only message."),
			"state": schema.StringAttribute{MarkdownDescription: "Review state of the version, e.g. `PREPARE_FOR_SUBMISSION`, " +
				"`WAITING_FOR_REVIEW` or `APPROVED`.", Computed: true},
			"id": idAttribute("App Store Connect ID of the retention message version."),
		},
	}
}

func (r *retentionVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *retentionVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *retentionVersionModel) apply(v *gen.RetentionMessageVersion) {
	m.ID = types.StringValue(v.Id)
	m.State = stringValue(asc.Value(v.Attributes).State)
	m.PlacementID = types.StringNull()
	rel := v.Relationships
	if rel == nil {
		return
	}
	if rel.RetentionMessage != nil && rel.RetentionMessage.Data != nil {
		m.MessageID = types.StringValue(rel.RetentionMessage.Data.Id)
	}
	if rel.Placement != nil && rel.Placement.Data != nil {
		m.PlacementID = types.StringValue(rel.Placement.Data.Id)
	}
}

// readRetentionVersion gets a version with the relationships that hold its
// message and placement.
func readRetentionVersion(ctx context.Context, c *asc.Client, id string) (*gen.RetentionMessageVersion, error) {
	out, err := c.RetentionMessageVersionsGetInstanceWithResponse(ctx, id, &gen.RetentionMessageVersionsGetInstanceParams{
		Include: &[]gen.RetentionMessageVersionsGetInstanceParamsInclude{
			gen.RetentionMessageVersionsGetInstanceParamsIncludeRetentionMessage, gen.RetentionMessageVersionsGetInstanceParamsIncludePlacement,
		},
	})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *retentionVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan retentionVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.RetentionMessageVersionCreateRequest
	body.Data.Type = gen.RetentionMessageVersionCreateRequestDataTypeRetentionMessageVersions
	rel := &body.Data.Relationships
	rel.RetentionMessage.Data.Id = plan.MessageID.ValueString()
	rel.RetentionMessage.Data.Type = gen.RetentionMessageVersionCreateRequestDataRelationshipsRetentionMessageDataTypeRetentionMessages
	if id := plan.PlacementID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&rel.Placement).Data)
		d.Id, d.Type = id, gen.RetentionMessageVersionCreateRequestDataRelationshipsPlacementDataTypeAppAssetLibraryPlacements
	}
	out, err := r.client.RetentionMessageVersionsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create retention message version", err)
		return
	}
	// The create response may leave out relationships the request set.
	messageID, placementID := plan.MessageID, plan.PlacementID
	plan.apply(&out.JSON201.Data)
	plan.MessageID, plan.PlacementID = messageID, placementID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state retentionVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := readRetentionVersion(ctx, r.client, state.ID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read retention message version", err)
		return
	}
	state.apply(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument replaces the resource.
func (r *retentionVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

// Delete only forgets the version: the API has no way to delete one.
func (r *retentionVersionResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Retention message version not deleted",
		"App Store Connect can't delete a retention message version. It is removed from the Terraform state and stays "+
			"in App Store Connect until its retention message is deleted.")
}

// ---- apple_retention_message_localization ----

var (
	_ resource.ResourceWithConfigure   = &retentionLocalizationResource{}
	_ resource.ResourceWithImportState = &retentionLocalizationResource{}
	_ resource.ResourceWithIdentity    = &retentionLocalizationResource{}
)

type retentionLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type retentionLocalizationModel struct {
	VersionID types.String `tfsdk:"retention_message_version_id"`
	Locale    types.String `tfsdk:"locale"`
	Header    types.String `tfsdk:"header"`
	Body      types.String `tfsdk:"body"`
	ID        types.String `tfsdk:"id"`
}

// retentionLocalizations lists a version's localizations.
func retentionLocalizations(ctx context.Context, c *asc.Client, versionID string) ([]gen.RetentionMessageLocalization, error) {
	out, err := c.RetentionMessageVersionsLocalizationsGetToManyRelatedWithResponse(ctx, versionID,
		&gen.RetentionMessageVersionsLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.RetentionMessageLocalizationsResponse) []gen.RetentionMessageLocalization { return p.Data },
		func(p *gen.RetentionMessageLocalizationsResponse) *string { return p.Links.Next })
}

// resolveRetentionLocalization maps "<version_id>/<locale>" to the
// localization's ID, and returns any other key as the ID.
func resolveRetentionLocalization(ctx context.Context, c *asc.Client, key string) (string, error) {
	versionID, locale, ok := strings.Cut(key, "/")
	if !ok {
		return key, nil
	}
	all, err := retentionLocalizations(ctx, c, versionID)
	if asc.IsNotFound(err) {
		return "", errNoMatch
	}
	if err != nil {
		return "", err
	}
	for _, l := range all {
		if asc.Value(asc.Value(l.Attributes).Locale) == locale {
			return l.Id, nil
		}
	}
	return "", errNoMatch
}

func newRetentionLocalizationResource() resource.Resource {
	return &retentionLocalizationResource{resourceIdentity: resourceIdentity{
		attrs:   idIdentity("App Store Connect ID of the localization."),
		resolve: resolveRetentionLocalization,
		keyName: "version ID and locale",
	}}
}

func (r *retentionLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_retention_message_localization"
}

func (r *retentionLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the header and body text of a retention message version for one locale. A version has " +
			"one localization per locale, and an approved version takes no new localizations: create a new " +
			"`apple_retention_message_version` to change approved content. Destroying the resource deletes the localization. Import it by " +
			"ID or with `<retention_message_version_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"retention_message_version_id": replacedString("App Store Connect ID of the retention message version, e.g. " +
				"`apple_retention_message_version.example.id`. Changing it creates a new localization."),
			"locale": replacedString("Locale, e.g. `en-US`. Changing it creates a new localization.", localeValidator),
			"header": optionalString("Header of the message, up to 66 characters.", stringvalidator.LengthBetween(1, 66)),
			"body":   optionalString("Body of the message, up to 144 characters.", stringvalidator.LengthBetween(1, 144)),
			"id":     idAttribute("App Store Connect ID of the localization."),
		},
	}
}

func (r *retentionLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *retentionLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *retentionLocalizationModel) apply(l *gen.RetentionMessageLocalization) {
	m.ID = types.StringValue(l.Id)
	a := asc.Value(l.Attributes)
	m.Locale, m.Header, m.Body = stringValue(a.Locale), stringValue(a.Header), stringValue(a.Body)
	if rel := l.Relationships; rel != nil && rel.RetentionMessageVersion != nil && rel.RetentionMessageVersion.Data != nil {
		m.VersionID = types.StringValue(rel.RetentionMessageVersion.Data.Id)
	}
}

func (r *retentionLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan retentionLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.RetentionMessageLocalizationCreateRequest
	body.Data.Type = gen.RetentionMessageLocalizationCreateRequestDataTypeRetentionMessageLocalizations
	a := &body.Data.Attributes
	a.Locale, a.Header, a.Body = plan.Locale.ValueString(), knownString(plan.Header), knownString(plan.Body)
	body.Data.Relationships.RetentionMessageVersion.Data.Id = plan.VersionID.ValueString()
	body.Data.Relationships.RetentionMessageVersion.Data.Type = gen.RetentionMessageLocalizationCreateRequestDataRelationshipsRetentionMessageVersionDataTypeRetentionMessageVersions
	out, err := r.client.RetentionMessageLocalizationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create retention message localization", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state retentionLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.RetentionMessageLocalizationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.RetentionMessageLocalizationsGetInstanceParams{
		Include: &[]gen.RetentionMessageLocalizationsGetInstanceParamsInclude{gen.RetentionMessageLocalizationsGetInstanceParamsIncludeRetentionMessageVersion},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read retention message localization", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sends raw JSON so that text removed from the configuration is sent
// as null.
func (r *retentionLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan retentionLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "retentionMessageLocalizations", "id": plan.ID.ValueString(),
		"attributes": map[string]any{"header": knownString(plan.Header), "body": knownString(plan.Body)},
	}})
	if err == nil {
		out, e := r.client.RetentionMessageLocalizationsUpdateInstanceWithBodyWithResponse(ctx, plan.ID.ValueString(), "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON200.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update retention message localization", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state retentionLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.RetentionMessageLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete retention message localization", err)
	}
}

// ---- apple_retention_offer ----

var (
	_ resource.ResourceWithConfigure   = &retentionOfferResource{}
	_ resource.ResourceWithImportState = &retentionOfferResource{}
	_ resource.ResourceWithIdentity    = &retentionOfferResource{}
)

type retentionOfferResource struct {
	resourceIdentity
	client *asc.Client
}

type retentionOfferModel struct {
	SubscriptionID types.String `tfsdk:"subscription_id"`
	ReferenceName  types.String `tfsdk:"reference_name"`
	OfferID        types.String `tfsdk:"offer_id"`
	OfferMode      types.String `tfsdk:"offer_mode"`
	Duration       types.String `tfsdk:"duration"`
	PeriodCount    types.Int64  `tfsdk:"period_count"`
	PlanType       types.String `tfsdk:"plan_type"`
	PricePointIDs  types.Set    `tfsdk:"price_point_ids"`
	PaidMonths     types.Int64  `tfsdk:"paid_subscription_months"`
	WaitMonths     types.Int64  `tfsdk:"wait_between_offers_months"`
	ID             types.String `tfsdk:"id"`
}

func newRetentionOfferResource() resource.Resource {
	return &retentionOfferResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the retention offer.")}}
}

func (r *retentionOfferResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_retention_offer"
}

func (r *retentionOfferResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines a retention offer: a discount for an auto-renewable subscription that can be shown " +
			"with the subscription's retention message when people start to cancel. Creating an offer doesn't present " +
			"it; `apple_subscription_retention_message_offer` does. The offer's identity, terms and prices can't change, " +
			"so changing them replaces the offer; the eligibility rules change in place. A subscription can have 10 " +
			"retention offers per plan type. Destroying the resource deletes the offer. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"subscription_id": replacedString("App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`."),
			"reference_name":  replacedString("Reference name of the offer.", stringvalidator.LengthBetween(1, 64)),
			"offer_id":        replacedString("Identifier of the offer in StoreKit, e.g. `stay_one_month`.", stringvalidator.LengthBetween(1, 64)),
			"offer_mode":      replacedString("Kind of offer: "+offerModeDoc+".", oneOf(offerModes)),
			"duration":        replacedString("Length of each offer period: "+docValues(offerDurations)+".", oneOf(offerDurations)),
			"period_count":    replacedInt("Number of offer periods.", int64validator.AtLeast(1)),
			"plan_type":       replacedPlanType(),
			"price_point_ids": schema.SetAttribute{
				MarkdownDescription: "Subscription price points of a paid offer, one per territory; see " +
					"`data.apple_subscription_price_points`. A price point sets the price in its own territory. A free " +
					"trial has none.",
				Optional:      true,
				ElementType:   types.StringType,
				Validators:    []validator.Set{setvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			},
			"paid_subscription_months": requiredInt("Months a customer must have paid for the subscription to be eligible: 1 to 24, 36, 48 or 60.",
				int64validator.Any(int64validator.Between(1, 24), int64validator.OneOf(36, 48, 60))),
			"wait_between_offers_months": optionalInt("Months a customer waits after redeeming a retention offer for the "+
				"subscription before another, from 2 to 24.", int64validator.Between(2, 24)),
			"id": idAttribute("App Store Connect ID of the retention offer."),
		},
	}
}

func (r *retentionOfferResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *retentionOfferResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *retentionOfferResource) read(ctx context.Context, m *retentionOfferModel) error {
	out, err := r.client.RetentionOffersGetInstanceWithResponse(ctx, m.ID.ValueString(), &gen.RetentionOffersGetInstanceParams{
		Include: &[]gen.RetentionOffersGetInstanceParamsInclude{gen.RetentionOffersGetInstanceParamsIncludeSubscription},
	})
	if err := asc.Err(out, err); err != nil {
		return err
	}
	o := out.JSON200.Data
	a := asc.Value(o.Attributes)
	m.ReferenceName, m.OfferID, m.OfferMode, m.Duration = stringValue(a.ReferenceName), stringValue(a.OfferId), stringValue(a.OfferMode), stringValue(a.Duration)
	m.PeriodCount, m.PlanType = intValue(a.PeriodCount), stringValue(a.TargetSubscriptionPlanType)
	m.PaidMonths, m.WaitMonths = intValue(a.CustomerEligibilityPaidSubscriptionDurationInMonths), intValue(a.CustomerEligibilityWaitBetweenOffersInMonths)
	if rel := o.Relationships; rel != nil && rel.Subscription != nil && rel.Subscription.Data != nil {
		m.SubscriptionID = types.StringValue(rel.Subscription.Data.Id)
	}
	prices, err := r.client.RetentionOffersPricesGetToManyRelatedWithResponse(ctx, m.ID.ValueString(), &gen.RetentionOffersPricesGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(prices, err); err != nil {
		return err
	}
	all, err := asc.All(ctx, r.client, prices.JSON200,
		func(p *gen.RetentionOfferPricesResponse) []gen.RetentionOfferPrice { return p.Data },
		func(p *gen.RetentionOfferPricesResponse) *string { return p.Links.Next })
	if err != nil {
		return err
	}
	m.PricePointIDs = types.SetNull(types.StringType)
	if len(all) > 0 {
		points := make([]string, 0, len(all))
		for _, p := range all {
			if rel := p.Relationships; rel != nil && rel.SubscriptionPricePoint != nil && rel.SubscriptionPricePoint.Data != nil {
				points = append(points, rel.SubscriptionPricePoint.Data.Id)
			}
		}
		m.PricePointIDs = stringsValue(points)
	}
	return nil
}

// Create sends raw JSON: the generated inline price type has no
// relationship for the price point. A price names only its price point,
// which sets the territory.
func (r *retentionOfferResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan retentionOfferModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	points := setStrings(ctx, plan.PricePointIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	slices.Sort(points)
	attrs := map[string]any{
		"referenceName": plan.ReferenceName.ValueString(), "offerId": plan.OfferID.ValueString(), "offerMode": plan.OfferMode.ValueString(),
		"duration": plan.Duration.ValueString(), "periodCount": plan.PeriodCount.ValueInt64(),
		"customerEligibilityPaidSubscriptionDurationInMonths": plan.PaidMonths.ValueInt64(),
	}
	if v := knownInt64(plan.WaitMonths); v != nil {
		attrs["customerEligibilityWaitBetweenOffersInMonths"] = v
	}
	if v := knownString(plan.PlanType); v != nil {
		attrs["targetSubscriptionPlanType"] = *v
	}
	refs, included := []map[string]any{}, []map[string]any{}
	for i, point := range points {
		local := fmt.Sprintf("${price-%d}", i)
		refs = append(refs, map[string]any{"type": "retentionOfferPrices", "id": local})
		included = append(included, map[string]any{"type": "retentionOfferPrices", "id": local, "relationships": map[string]any{
			"subscriptionPricePoint": map[string]any{"data": map[string]string{"type": "subscriptionPricePoints", "id": point}},
		}})
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "retentionOffers", "attributes": attrs,
		"relationships": map[string]any{
			"subscription": map[string]any{"data": map[string]string{"type": "subscriptions", "id": plan.SubscriptionID.ValueString()}},
			"prices":       map[string]any{"data": refs},
		},
	}, "included": included})
	if err == nil {
		out, e := r.client.RetentionOffersCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.ID = types.StringValue(out.JSON201.Data.Id)
			err = r.read(ctx, &plan)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create retention offer", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionOfferResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state retentionOfferModel
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
		apiError(&resp.Diagnostics, "read retention offer", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes the eligibility rules, as raw JSON so that a removed wait
// is sent as null.
func (r *retentionOfferResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan retentionOfferModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "retentionOffers", "id": plan.ID.ValueString(),
		"attributes": map[string]any{
			"customerEligibilityPaidSubscriptionDurationInMonths": plan.PaidMonths.ValueInt64(),
			"customerEligibilityWaitBetweenOffersInMonths":        knownInt64(plan.WaitMonths),
		},
	}})
	if err == nil {
		out, e := r.client.RetentionOffersUpdateInstanceWithBodyWithResponse(ctx, plan.ID.ValueString(), "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			err = r.read(ctx, &plan)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update retention offer", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionOfferResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state retentionOfferModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.RetentionOffersDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete retention offer", err)
	}
}

// ---- apple_subscription_retention_message ----

var (
	_ resource.ResourceWithConfigure   = &subscriptionRetentionMessageResource{}
	_ resource.ResourceWithImportState = &subscriptionRetentionMessageResource{}
	_ resource.ResourceWithIdentity    = &subscriptionRetentionMessageResource{}
)

type subscriptionRetentionMessageResource struct {
	resourceIdentity
	client *asc.Client
}

type subscriptionRetentionMessageModel struct {
	SubscriptionID types.String `tfsdk:"subscription_id"`
	MessageID      types.String `tfsdk:"retention_message_id"`
}

func newSubscriptionRetentionMessageResource() resource.Resource {
	return &subscriptionRetentionMessageResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "subscription_id", description: "App Store Connect ID of the subscription.", state: "subscription_id"},
	}}}
}

func (r *subscriptionRetentionMessageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_retention_message"
}

func (r *subscriptionRetentionMessageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Assigns a retention message to an auto-renewable subscription. A subscription has at most " +
			"one retention message, so use one of these resources per subscription: assigning a message replaces the one " +
			"the subscription had. App Store Connect can't unassign a message, so destroying the resource only removes " +
			"it from the Terraform state; deleting the retention message unassigns it. Import it with the subscription's ID.",
		Attributes: map[string]schema.Attribute{
			"subscription_id":      replacedString("App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`."),
			"retention_message_id": schema.StringAttribute{MarkdownDescription: "App Store Connect ID of a retention message of the subscription's app, e.g. `apple_retention_message.example.id`.", Required: true},
		},
	}
}

func (r *subscriptionRetentionMessageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *subscriptionRetentionMessageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *subscriptionRetentionMessageResource) assign(ctx context.Context, plan *subscriptionRetentionMessageModel, diags *diag.Diagnostics) {
	var body gen.SubscriptionRetentionMessageLinkageRequest
	body.Data.Id, body.Data.Type = plan.MessageID.ValueString(), gen.SubscriptionRetentionMessageLinkageRequestDataTypeRetentionMessages
	out, err := r.client.SubscriptionsRetentionMessageUpdateToOneRelationshipWithResponse(ctx, plan.SubscriptionID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(diags, "assign retention message", err)
	}
}

func (r *subscriptionRetentionMessageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionRetentionMessageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.assign(ctx, &plan, &resp.Diagnostics); !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *subscriptionRetentionMessageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state subscriptionRetentionMessageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionsRetentionMessageGetToOneRelationshipWithResponse(ctx, state.SubscriptionID.ValueString())
	err = asc.Err(out, err)
	// A subscription without a message has null linkage data, which decodes
	// as an empty ID.
	if asc.IsNotFound(err) || (err == nil && out.JSON200.Data.Id == "") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read retention message of subscription", err)
		return
	}
	state.MessageID = types.StringValue(out.JSON200.Data.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *subscriptionRetentionMessageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionRetentionMessageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.assign(ctx, &plan, &resp.Diagnostics); !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

// Delete only forgets the assignment: the API can replace a subscription's
// retention message but not remove it.
func (r *subscriptionRetentionMessageResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Retention message still assigned",
		"App Store Connect can't unassign a subscription's retention message. The assignment is removed from the "+
			"Terraform state and stays in App Store Connect until another message is assigned or the message is deleted.")
}

// ---- apple_subscription_retention_message_offer ----

var (
	_ resource.ResourceWithConfigure   = &retentionMessageOfferResource{}
	_ resource.ResourceWithImportState = &retentionMessageOfferResource{}
	_ resource.ResourceWithIdentity    = &retentionMessageOfferResource{}
)

type retentionMessageOfferResource struct {
	resourceIdentity
	client *asc.Client
}

type retentionMessageOfferModel struct {
	SubscriptionID types.String `tfsdk:"subscription_id"`
	OfferID        types.String `tfsdk:"retention_offer_id"`
}

func newRetentionMessageOfferResource() resource.Resource {
	return &retentionMessageOfferResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "subscription_id", description: "App Store Connect ID of the subscription.", state: "subscription_id"},
		{name: "retention_offer_id", description: "App Store Connect ID of the retention offer.", state: "retention_offer_id"},
	}}}
}

func (r *retentionMessageOfferResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_retention_message_offer"
}

func (r *retentionMessageOfferResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Presents one retention offer with a subscription's retention message. Use one resource " +
			"per offer. Destroying the resource stops presenting the offer and leaves the offer itself alone. Import it " +
			"with `<subscription_id>/<retention_offer_id>`.",
		Attributes: map[string]schema.Attribute{
			"subscription_id":    replacedString("App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`."),
			"retention_offer_id": replacedString("App Store Connect ID of one of the subscription's retention offers, e.g. `apple_retention_offer.example.id`."),
		},
	}
}

func (r *retentionMessageOfferResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *retentionMessageOfferResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func retentionOfferLinkage(offerID string) gen.SubscriptionRetentionMessageOffersLinkagesRequest {
	var body gen.SubscriptionRetentionMessageOffersLinkagesRequest
	e := asc.Push(&body.Data)
	e.Id, e.Type = offerID, gen.SubscriptionRetentionMessageOffersLinkagesRequestDataTypeRetentionOffers
	return body
}

func (r *retentionMessageOfferResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan retentionMessageOfferModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionsRetentionMessageOffersCreateToManyRelationshipWithResponse(ctx, plan.SubscriptionID.ValueString(),
		retentionOfferLinkage(plan.OfferID.ValueString()))
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "add retention offer to the subscription's retention message", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionMessageOfferResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state retentionMessageOfferModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionsRetentionMessageOffersGetToManyRelationshipWithResponse(ctx, state.SubscriptionID.ValueString(),
		&gen.SubscriptionsRetentionMessageOffersGetToManyRelationshipParams{Limit: new(linkageLimit)})
	var offerIDs []string
	if err = asc.Err(out, err); err == nil {
		offerIDs, err = asc.All(ctx, r.client, out.JSON200,
			func(p *gen.SubscriptionRetentionMessageOffersLinkagesResponse) []string {
				var ids []string
				for _, d := range p.Data {
					ids = append(ids, d.Id)
				}
				return ids
			},
			func(p *gen.SubscriptionRetentionMessageOffersLinkagesResponse) *string { return p.Links.Next })
	}
	if asc.IsNotFound(err) || (err == nil && !slices.Contains(offerIDs, state.OfferID.ValueString())) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read the subscription's retention message offers", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument replaces the resource.
func (r *retentionMessageOfferResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *retentionMessageOfferResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state retentionMessageOfferModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionsRetentionMessageOffersDeleteToManyRelationshipWithResponse(ctx, state.SubscriptionID.ValueString(),
		retentionOfferLinkage(state.OfferID.ValueString()))
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "remove retention offer from the subscription's retention message", err)
	}
}

// ---- apple_submit_retention_message_version ----

type submitRetentionVersionModel struct {
	AppID     types.String `tfsdk:"app_id"`
	VersionID types.String `tfsdk:"retention_message_version_id"`
	Platform  types.String `tfsdk:"platform"`
	Submit    types.Bool   `tfsdk:"submit"`
}

// submittableRetentionStates are the states a version can be submitted from.
var submittableRetentionStates = []gen.RetentionMessageVersionAttributesState{
	gen.RetentionMessageVersionAttributesStatePREPAREFORSUBMISSION,
	gen.RetentionMessageVersionAttributesStateDEVELOPERREJECTED,
	gen.RetentionMessageVersionAttributesStateREJECTED,
}

var newSubmitRetentionVersionAction = newAction("submit_retention_message_version",
	"Submits a retention message version to App Review, by adding it to the platform's review submission that is "+
		"being prepared, or a new one. The version needs a localization. People see a retention message only after "+
		"App Review approves a version of it.",
	map[string]actionschema.Attribute{
		"app_id":                       actionString("App Store Connect ID of the app."),
		"retention_message_version_id": actionString("App Store Connect ID of the version, e.g. `apple_retention_message_version.example.id`."),
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
	func(ctx context.Context, c *asc.Client, m *submitRetentionVersionModel) (string, error) {
		versionID := m.VersionID.ValueString()
		v, err := readRetentionVersion(ctx, c, versionID)
		if err != nil {
			return "", err
		}
		if state := asc.Value(asc.Value(v.Attributes).State); !slices.Contains(submittableRetentionStates, state) {
			return "", fmt.Errorf("retention message version %s is %s, and can't be submitted again", versionID, state)
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
		d := asc.Alloc(&asc.Alloc(&item.Data.Relationships.RetentionMessageVersion).Data)
		d.Id, d.Type = versionID, gen.ReviewSubmissionItemCreateRequestDataRelationshipsRetentionMessageVersionDataTypeRetentionMessageVersions
		out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		if !m.Submit.IsNull() && !m.Submit.ValueBool() {
			return "Added retention message version " + versionID + " to review submission " + submissionID, nil
		}
		return "Submitted retention message version " + versionID + " for review", updateSubmission(ctx, c, submissionID, true)
	})
