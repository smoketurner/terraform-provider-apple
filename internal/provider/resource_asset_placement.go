package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Placements show App Asset Library assets on App Store surfaces, and
// ordering requests set their display order within a placement group.

// The localization attributes a placement or ordering can target.
const (
	versionLocalizationAttr     = "app_store_version_localization_id"
	productPageLocalizationAttr = "custom_product_page_localization_id"
	eventLocalizationAttr       = "app_event_localization_id"
	treatmentLocalizationAttr   = "experiment_treatment_localization_id"
)

var placementTypes = enum(gen.AppAssetLibraryPlacementTypeAPPSCREENSHOT, gen.AppAssetLibraryPlacementTypeIMESSAGEAPPSCREENSHOT,
	gen.AppAssetLibraryPlacementTypeAPPPREVIEW, gen.AppAssetLibraryPlacementTypePRODUCTPAGEHEADERASSET,
	gen.AppAssetLibraryPlacementTypeAPPSTORESEARCHRESULTSASSET, gen.AppAssetLibraryPlacementTypeSEARCHRESULTSADSASSET,
	gen.AppAssetLibraryPlacementTypeTODAYTABADSASSET, gen.AppAssetLibraryPlacementTypeEVENTCARDASSET,
	gen.AppAssetLibraryPlacementTypeEVENTDETAILSPAGEASSET, gen.AppAssetLibraryPlacementTypeRETENTIONMESSAGEASSET)

// localizationAttrs returns optional, replaced localization ID attributes,
// exactly one of which must be set.
func localizationAttrs(names ...string) map[string]schema.Attribute {
	descs := map[string]string{
		versionLocalizationAttr:     "App Store Connect ID of an App Store version localization, e.g. `apple_app_store_version_localization.en_US.id`.",
		productPageLocalizationAttr: "App Store Connect ID of a custom product page localization.",
		eventLocalizationAttr:       "App Store Connect ID of an in-app event localization.",
		treatmentLocalizationAttr:   "App Store Connect ID of a product page optimization treatment localization.",
	}
	paths := make([]path.Expression, len(names))
	for i, n := range names {
		paths[i] = path.MatchRoot(n)
	}
	out := map[string]schema.Attribute{}
	for _, n := range names {
		out[n] = replacedOptionalString(descs[n]+" Exactly one localization is required; changing it creates a new resource.",
			stringvalidator.ExactlyOneOf(paths...))
	}
	return out
}

type placementModel struct {
	ID                        types.String `tfsdk:"id"`
	ImageID                   types.String `tfsdk:"image_id"`
	VideoID                   types.String `tfsdk:"video_id"`
	VersionLocalizationID     types.String `tfsdk:"app_store_version_localization_id"`
	ProductPageLocalizationID types.String `tfsdk:"custom_product_page_localization_id"`
	EventLocalizationID       types.String `tfsdk:"app_event_localization_id"`
	TreatmentLocalizationID   types.String `tfsdk:"experiment_treatment_localization_id"`
	PlacementType             types.String `tfsdk:"placement_type"`
	PlacementGroup            types.String `tfsdk:"placement_group"`
	State                     types.String `tfsdk:"state"`
}

var (
	_ resource.ResourceWithConfigure   = &placementResource{}
	_ resource.ResourceWithImportState = &placementResource{}
	_ resource.ResourceWithIdentity    = &placementResource{}
)

type placementResource struct {
	resourceIdentity
	client *asc.Client
}

func newPlacementResource() resource.Resource {
	return &placementResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the placement.")}}
}

func (r *placementResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_asset_library_placement"
}

func (r *placementResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	media := []path.Expression{path.MatchRoot("image_id"), path.MatchRoot("video_id")}
	attrs := localizationAttrs(versionLocalizationAttr, productPageLocalizationAttr, eventLocalizationAttr, treatmentLocalizationAttr)
	attrs["id"] = idAttribute("App Store Connect ID of the placement.")
	attrs["image_id"] = replacedOptionalString("App Store Connect ID of the image to show, e.g. `apple_app_asset_library_image.example.id`. "+
		"Exactly one of `image_id` and `video_id` is required.", stringvalidator.ExactlyOneOf(media...))
	attrs["video_id"] = replacedOptionalString("App Store Connect ID of the video to show, e.g. `apple_app_asset_library_video.example.id`.",
		stringvalidator.ExactlyOneOf(media...))
	attrs["placement_type"] = replacedString("Where the asset shows: "+docValues(placementTypes)+". Screenshots take images, "+
		"previews take videos, and only in-app event localizations take event placements.", oneOf(placementTypes))
	attrs["placement_group"] = replacedString("Device family the placement targets, such as `IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE`, " +
		"or `DEFAULT_PROFILE` for placements that don't depend on the device. `data.apple_app_asset_library_reference_data` " +
		"lists them.")
	attrs["state"] = schema.StringAttribute{MarkdownDescription: "State of the placement, e.g. `ASSET_PROCESSING` or `PARENT_APPROVED`.", Computed: true}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Shows an App Asset Library image or video on a localization: as a screenshot or app preview of an " +
			"App Store version, a custom product page or a product page optimization treatment, or as the card or details " +
			"page of an in-app event. Placements show in creation order within their placement group; " +
			"`apple_app_asset_library_placement_order` sets the order. A placement can't be changed, so every change " +
			"creates a new one. Import it by ID.",
		Attributes: attrs,
	}
}

func (r *placementResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *placementResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *placementModel) apply(p *gen.AppAssetLibraryPlacement) error {
	m.ID = types.StringValue(p.Id)
	a := asc.Value(p.Attributes)
	m.PlacementType, m.PlacementGroup, m.State = stringValue(a.PlacementType), stringValue(a.PlacementGroup), stringValue(a.State)
	if p.Relationships == nil {
		return nil
	}
	// The relationships are a union by media type; the image variant holds
	// the localizations, which both share.
	image, err := p.Relationships.AsAppAssetLibraryPlacementImageRelationships()
	if err != nil {
		return err
	}
	video, err := p.Relationships.AsAppAssetLibraryPlacementVideoRelationships()
	if err != nil {
		return err
	}
	m.ImageID, m.VideoID = types.StringNull(), types.StringNull()
	if image.Image != nil && image.Image.Data != nil {
		m.ImageID = types.StringValue(image.Image.Data.Id)
	}
	if video.Video != nil && video.Video.Data != nil {
		m.VideoID = types.StringValue(video.Video.Data.Id)
	}
	m.VersionLocalizationID, m.ProductPageLocalizationID = types.StringNull(), types.StringNull()
	m.EventLocalizationID, m.TreatmentLocalizationID = types.StringNull(), types.StringNull()
	if l := image.AppStoreVersionLocalization; l != nil && l.Data != nil {
		m.VersionLocalizationID = types.StringValue(l.Data.Id)
	}
	if l := image.AppCustomProductPageLocalization; l != nil && l.Data != nil {
		m.ProductPageLocalizationID = types.StringValue(l.Data.Id)
	}
	if l := image.AppEventLocalization; l != nil && l.Data != nil {
		m.EventLocalizationID = types.StringValue(l.Data.Id)
	}
	if l := image.AppStoreVersionExperimentTreatmentLocalization; l != nil && l.Data != nil {
		m.TreatmentLocalizationID = types.StringValue(l.Data.Id)
	}
	return nil
}

func (r *placementResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan placementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppAssetLibraryPlacementCreateRequest
	body.Data.Type = gen.AppAssetLibraryPlacementCreateRequestDataTypeAppAssetLibraryPlacements
	a := asc.Alloc(&body.Data.Attributes)
	a.PlacementType = new(gen.AppAssetLibraryPlacementType(plan.PlacementType.ValueString()))
	a.PlacementGroup = new(plan.PlacementGroup.ValueString())
	rel := asc.Alloc(&body.Data.Relationships)
	if id := plan.ImageID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&rel.Image).Data)
		d.Id, d.Type = id, gen.AppAssetLibraryPlacementCreateRequestDataRelationshipsImageDataTypeAppAssetLibraryImages
	}
	if id := plan.VideoID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&rel.Video).Data)
		d.Id, d.Type = id, gen.AppAssetLibraryPlacementCreateRequestDataRelationshipsVideoDataTypeAppAssetLibraryVideos
	}
	if id := plan.VersionLocalizationID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&rel.AppStoreVersionLocalization).Data)
		d.Id, d.Type = id, gen.AppAssetLibraryPlacementCreateRequestDataRelationshipsAppStoreVersionLocalizationDataTypeAppStoreVersionLocalizations
	}
	if id := plan.ProductPageLocalizationID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&rel.AppCustomProductPageLocalization).Data)
		d.Id, d.Type = id, gen.AppAssetLibraryPlacementCreateRequestDataRelationshipsAppCustomProductPageLocalizationDataTypeAppCustomProductPageLocalizations
	}
	if id := plan.EventLocalizationID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&rel.AppEventLocalization).Data)
		d.Id, d.Type = id, gen.AppAssetLibraryPlacementCreateRequestDataRelationshipsAppEventLocalizationDataTypeAppEventLocalizations
	}
	if id := plan.TreatmentLocalizationID.ValueString(); id != "" {
		d := asc.Alloc(&asc.Alloc(&rel.AppStoreVersionExperimentTreatmentLocalization).Data)
		d.Id, d.Type = id, gen.AppAssetLibraryPlacementCreateRequestDataRelationshipsAppStoreVersionExperimentTreatmentLocalizationDataTypeAppStoreVersionExperimentTreatmentLocalizations
	}
	out, err := r.client.AppAssetLibraryPlacementsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create placement", err)
		return
	}
	if err := plan.apply(&out.JSON201.Data); err != nil {
		apiError(&resp.Diagnostics, "read placement", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *placementResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state placementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppAssetLibraryPlacementsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		err = state.apply(&out.JSON200.Data)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read placement", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument replaces the resource.
func (r *placementResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	resp.State.Raw = req.Plan.Raw
}

func (r *placementResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppAssetLibraryPlacementsDeleteInstanceWithResponse(ctx, id.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete placement", err)
	}
}

// ---- placement order ----

type placementOrderModel struct {
	ID                        types.String `tfsdk:"id"`
	VersionLocalizationID     types.String `tfsdk:"app_store_version_localization_id"`
	ProductPageLocalizationID types.String `tfsdk:"custom_product_page_localization_id"`
	TreatmentLocalizationID   types.String `tfsdk:"experiment_treatment_localization_id"`
	PlacementGroup            types.String `tfsdk:"placement_group"`
	PlacementIDs              types.List   `tfsdk:"placement_ids"`
}

// target returns the attribute and ID of the localization the order is for.
func (m *placementOrderModel) target() (attr, id string) {
	for attr, v := range map[string]types.String{
		versionLocalizationAttr:     m.VersionLocalizationID,
		productPageLocalizationAttr: m.ProductPageLocalizationID,
		treatmentLocalizationAttr:   m.TreatmentLocalizationID,
	} {
		if v.ValueString() != "" {
			return attr, v.ValueString()
		}
	}
	return "", ""
}

var (
	_ resource.ResourceWithConfigure   = &placementOrderResource{}
	_ resource.ResourceWithImportState = &placementOrderResource{}
	_ resource.ResourceWithIdentity    = &placementOrderResource{}
)

type placementOrderResource struct {
	resourceIdentity
	client *asc.Client
}

func newPlacementOrderResource() resource.Resource {
	return &placementOrderResource{resourceIdentity: resourceIdentity{attrs: idIdentity("The localization's ID and the placement group, as `<localization_id>/<placement_group>`.")}}
}

func (r *placementOrderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_asset_library_placement_order"
}

func (r *placementOrderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := localizationAttrs(versionLocalizationAttr, productPageLocalizationAttr, treatmentLocalizationAttr)
	attrs["id"] = idAttribute("The localization's ID and the placement group, as `<localization_id>/<placement_group>`.")
	attrs["placement_group"] = replacedString("Placement group to order, such as `IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE`.", stringvalidator.LengthAtLeast(1))
	attrs["placement_ids"] = schema.ListAttribute{
		MarkdownDescription: "IDs of every placement in the group, in display order, e.g. `[apple_app_asset_library_placement.first.id, ...]`.",
		Required:            true,
		ElementType:         types.StringType,
		Validators:          []validator.List{listvalidator.SizeAtLeast(1), listvalidator.UniqueValues()},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the display order of the placements in a placement group of an App Store version, custom " +
			"product page or product page optimization treatment localization. In-app event placements can't be ordered. " +
			"List every placement of the group. Destroying the resource leaves the order as it is. Import it with " +
			"`<localization_id>/<placement_group>`.",
		Attributes: attrs,
	}
}

func (r *placementOrderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

// ImportState finds which kind of localization the ID names by listing its
// placements as each kind.
func (r *placementOrderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("id"), &id)...)
	localizationID, group, ok := strings.Cut(id.ValueString(), "/")
	if !ok || localizationID == "" || group == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected an import ID in the form \"<localization_id>/<placement_group>\", got %q.", id.ValueString()))
		return
	}
	for _, attr := range []string{versionLocalizationAttr, productPageLocalizationAttr, treatmentLocalizationAttr} {
		_, err := orderedPlacements(ctx, r.client, attr, localizationID, group)
		if asc.IsNotFound(err) {
			continue
		}
		if err != nil {
			apiError(&resp.Diagnostics, "look up "+localizationID, err)
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attr), localizationID)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("placement_group"), group)...)
		return
	}
	resp.Diagnostics.AddError("Cannot import", "No localization has the ID "+localizationID+".")
}

// orderedPlacements lists the IDs of a localization's placements in a group,
// in display order.
func orderedPlacements(ctx context.Context, c *asc.Client, attr, localizationID, group string) ([]string, error) {
	groups := &[]string{group}
	var all []gen.AppAssetLibraryPlacement
	var err error
	next := func(p *gen.AppAssetLibraryPlacementsResponse) *string { return p.Links.Next }
	data := func(p *gen.AppAssetLibraryPlacementsResponse) []gen.AppAssetLibraryPlacement { return p.Data }
	switch attr {
	case versionLocalizationAttr:
		var out *gen.AppStoreVersionLocalizationsPlacementsGetToManyRelatedResponse
		out, err = c.AppStoreVersionLocalizationsPlacementsGetToManyRelatedWithResponse(ctx, localizationID, &gen.AppStoreVersionLocalizationsPlacementsGetToManyRelatedParams{
			FilterPlacementGroup: groups, Limit: new(listAll),
			Sort: &[]gen.AppStoreVersionLocalizationsPlacementsGetToManyRelatedParamsSort{gen.AppStoreVersionLocalizationsPlacementsGetToManyRelatedParamsSortPlacementGroupPosition},
		})
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200, data, next)
		}
	case productPageLocalizationAttr:
		var out *gen.AppCustomProductPageLocalizationsPlacementsGetToManyRelatedResponse
		out, err = c.AppCustomProductPageLocalizationsPlacementsGetToManyRelatedWithResponse(ctx, localizationID, &gen.AppCustomProductPageLocalizationsPlacementsGetToManyRelatedParams{
			FilterPlacementGroup: groups, Limit: new(listAll),
			Sort: &[]gen.AppCustomProductPageLocalizationsPlacementsGetToManyRelatedParamsSort{gen.AppCustomProductPageLocalizationsPlacementsGetToManyRelatedParamsSortPlacementGroupPosition},
		})
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200, data, next)
		}
	case treatmentLocalizationAttr:
		var out *gen.AppStoreVersionExperimentTreatmentLocalizationsPlacementsGetToManyRelatedResponse
		out, err = c.AppStoreVersionExperimentTreatmentLocalizationsPlacementsGetToManyRelatedWithResponse(ctx, localizationID, &gen.AppStoreVersionExperimentTreatmentLocalizationsPlacementsGetToManyRelatedParams{
			FilterPlacementGroup: groups, Limit: new(listAll),
			Sort: &[]gen.AppStoreVersionExperimentTreatmentLocalizationsPlacementsGetToManyRelatedParamsSort{gen.AppStoreVersionExperimentTreatmentLocalizationsPlacementsGetToManyRelatedParamsSortPlacementGroupPosition},
		})
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200, data, next)
		}
	default:
		return nil, errors.New("no localization is set")
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(all))
	for i, p := range all {
		ids[i] = p.Id
	}
	return ids, nil
}

func (r *placementOrderResource) order(ctx context.Context, m *placementOrderModel, diags *diag.Diagnostics) {
	ids := listStrings(ctx, m.PlacementIDs)
	var body gen.AppAssetLibraryPlacementOrderingRequestCreateRequest
	body.Data.Type = gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataTypeAppAssetLibraryPlacementOrderingRequests
	asc.Alloc(&body.Data.Attributes).PlacementGroup = new(m.PlacementGroup.ValueString())
	rel := asc.Alloc(&body.Data.Relationships)
	placements := asc.Alloc(&asc.Alloc(&rel.OrderedPlacements).Data)
	for _, id := range ids {
		d := asc.Push(placements)
		d.Id, d.Type = id, gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataRelationshipsOrderedPlacementsDataTypeAppAssetLibraryPlacements
	}
	attr, localizationID := m.target()
	switch attr {
	case versionLocalizationAttr:
		d := asc.Alloc(&asc.Alloc(&rel.AppStoreVersionLocalization).Data)
		d.Id, d.Type = localizationID, gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataRelationshipsAppStoreVersionLocalizationDataTypeAppStoreVersionLocalizations
	case productPageLocalizationAttr:
		d := asc.Alloc(&asc.Alloc(&rel.AppCustomProductPageLocalization).Data)
		d.Id, d.Type = localizationID, gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataRelationshipsAppCustomProductPageLocalizationDataTypeAppCustomProductPageLocalizations
	case treatmentLocalizationAttr:
		d := asc.Alloc(&asc.Alloc(&rel.AppStoreVersionExperimentTreatmentLocalization).Data)
		d.Id, d.Type = localizationID, gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataRelationshipsAppStoreVersionExperimentTreatmentLocalizationDataTypeAppStoreVersionExperimentTreatmentLocalizations
	}
	out, err := r.client.AppAssetLibraryPlacementOrderingRequestsCreateInstanceWithResponse(ctx, nil, body)
	if err := asc.Err(out, err); err != nil {
		apiError(diags, "order placements", err)
		return
	}
	m.ID = types.StringValue(localizationID + "/" + m.PlacementGroup.ValueString())
}

func (r *placementOrderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan placementOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.order(ctx, &plan, &resp.Diagnostics); resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *placementOrderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state placementOrderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	attr, localizationID := state.target()
	ids, err := orderedPlacements(ctx, r.client, attr, localizationID, state.PlacementGroup.ValueString())
	if asc.IsNotFound(err) || err == nil && len(ids) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read placement order", err)
		return
	}
	state.PlacementIDs = stringsList(ids)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *placementOrderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan placementOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.order(ctx, &plan, &resp.Diagnostics); resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the order: there is no order to remove.
func (r *placementOrderResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- data sources ----

type placementData struct {
	ID             types.String `tfsdk:"id"`
	PlacementType  types.String `tfsdk:"placement_type"`
	PlacementGroup types.String `tfsdk:"placement_group"`
	State          types.String `tfsdk:"state"`
}

type placementsData struct {
	ImageID    types.String    `tfsdk:"image_id"`
	VideoID    types.String    `tfsdk:"video_id"`
	Placements []placementData `tfsdk:"placements"`
}

var newPlacementsDataSource = listDataSource("app_asset_library_placements",
	"Lists the placements of an App Asset Library image or video.",
	map[string]dsschema.Attribute{
		"image_id": dsschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of the image. Exactly one of `image_id` and `video_id` is required.",
			Optional:            true,
			Validators:          []validator.String{stringvalidator.ExactlyOneOf(path.MatchRoot("image_id"), path.MatchRoot("video_id"))},
		},
		"video_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the video.", Optional: true},
		"placements": listOf("The placements.", map[string]dsschema.Attribute{
			"id":              dsComputed("App Store Connect ID of the placement."),
			"placement_type":  dsComputed("Where the asset shows."),
			"placement_group": dsComputed("Device family the placement targets."),
			"state":           dsComputed("State of the placement."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *placementsData, diags *diag.Diagnostics) {
		next := func(p *gen.AppAssetLibraryPlacementsResponse) *string { return p.Links.Next }
		data := func(p *gen.AppAssetLibraryPlacementsResponse) []gen.AppAssetLibraryPlacement { return p.Data }
		var all []gen.AppAssetLibraryPlacement
		var err error
		if id := m.ImageID.ValueString(); id != "" {
			out, e := c.AppAssetLibraryImagesPlacementsGetToManyRelatedWithResponse(ctx, id, &gen.AppAssetLibraryImagesPlacementsGetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				all, err = asc.All(ctx, c, out.JSON200, data, next)
			}
		} else {
			out, e := c.AppAssetLibraryVideosPlacementsGetToManyRelatedWithResponse(ctx, m.VideoID.ValueString(), &gen.AppAssetLibraryVideosPlacementsGetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, e); err == nil {
				all, err = asc.All(ctx, c, out.JSON200, data, next)
			}
		}
		if err != nil {
			apiError(diags, "list placements", err)
			return
		}
		m.Placements = []placementData{}
		for _, p := range all {
			a := asc.Value(p.Attributes)
			m.Placements = append(m.Placements, placementData{
				ID: types.StringValue(p.Id), PlacementType: stringValue(a.PlacementType),
				PlacementGroup: stringValue(a.PlacementGroup), State: stringValue(a.State),
			})
		}
	})

type profileGroupData struct {
	ID           types.String `tfsdk:"id"`
	Platform     types.String `tfsdk:"platform"`
	DisplayClass types.String `tfsdk:"display_class"`
}

type assetSpecData struct {
	SpecID                   types.String `tfsdk:"spec_id"`
	ShortName                types.String `tfsdk:"short_name"`
	AspectRatio              types.String `tfsdk:"aspect_ratio"`
	MinWidth                 types.Int64  `tfsdk:"min_width"`
	MaxWidth                 types.Int64  `tfsdk:"max_width"`
	MinHeight                types.Int64  `tfsdk:"min_height"`
	MaxHeight                types.Int64  `tfsdk:"max_height"`
	MaxFileSize              types.Int64  `tfsdk:"max_file_size"`
	FileExtensions           types.Set    `tfsdk:"file_extensions"`
	CompatiblePlacementTypes types.Set    `tfsdk:"compatible_placement_types"`
}

type placementPolicyData struct {
	Feature       types.String `tfsdk:"feature"`
	PlacementType types.String `tfsdk:"placement_type"`
	GroupIDs      types.Set    `tfsdk:"group_ids"`
	MaxCount      types.Int64  `tfsdk:"max_count"`
}

type referenceData struct {
	ProfileGroups []profileGroupData    `tfsdk:"placement_groups"`
	ImageSpecs    []assetSpecData       `tfsdk:"image_specs"`
	VideoSpecs    []assetSpecData       `tfsdk:"video_specs"`
	Policies      []placementPolicyData `tfsdk:"placement_policies"`
}

func dsComputedInt(desc string) dsschema.Int64Attribute {
	return dsschema.Int64Attribute{MarkdownDescription: desc, Computed: true}
}

func specAttributes(kind string) map[string]dsschema.Attribute {
	return map[string]dsschema.Attribute{
		"spec_id":                    dsComputed("ID of the " + kind + " specification."),
		"short_name":                 dsComputed("Name of the specification."),
		"aspect_ratio":               dsComputed("Aspect ratio, e.g. `9:19.5`."),
		"min_width":                  dsComputedInt("Minimum width in pixels."),
		"max_width":                  dsComputedInt("Maximum width in pixels."),
		"min_height":                 dsComputedInt("Minimum height in pixels."),
		"max_height":                 dsComputedInt("Maximum height in pixels."),
		"max_file_size":              dsComputedInt("Maximum file size in bytes."),
		"file_extensions":            dsComputedSet("Accepted file extensions."),
		"compatible_placement_types": dsComputedSet("Placement types that accept the " + kind + "."),
	}
}

type specDimensions = struct {
	MaxHeight *int `json:"maxHeight,omitempty"`
	MaxWidth  *int `json:"maxWidth,omitempty"`
	MinHeight *int `json:"minHeight,omitempty"`
	MinWidth  *int `json:"minWidth,omitempty"`
}

func intValue(p *int) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*p))
}

func int64Value(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

func specData(id, name, ratio *string, dims *specDimensions, maxSize *int64, extensions *[]string, compatible *[]gen.AppAssetLibraryPlacementType) assetSpecData {
	d := asc.Value(dims)
	return assetSpecData{
		SpecID: stringValue(id), ShortName: stringValue(name), AspectRatio: stringValue(ratio),
		MinWidth: intValue(d.MinWidth), MaxWidth: intValue(d.MaxWidth), MinHeight: intValue(d.MinHeight), MaxHeight: intValue(d.MaxHeight),
		MaxFileSize: int64Value(maxSize), FileExtensions: stringsValue(asc.Value(extensions)),
		CompatiblePlacementTypes: stringsValue(enum(asc.Value(compatible)...)),
	}
}

var newReferenceDataDataSource = listDataSource("app_asset_library_reference_data",
	"Reads the App Asset Library's reference data: the placement groups placements can target, the specifications "+
		"images and videos must meet, and how many placements of each type a group takes.",
	map[string]dsschema.Attribute{
		"placement_groups": listOf("Placement groups, for `placement_group`.", map[string]dsschema.Attribute{
			"id":            dsComputed("ID of the placement group, e.g. `IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE`."),
			"platform":      dsComputed("App Store the group shows on, e.g. `IPHONE_APP_STORE`."),
			"display_class": dsComputed("Display class of the group's devices."),
		}),
		"image_specs": listOf("Image specifications.", specAttributes("image")),
		"video_specs": listOf("Video specifications.", specAttributes("video")),
		"placement_policies": listOf("Limits on the placements of a type, per feature.", map[string]dsschema.Attribute{
			"feature":        dsComputed("Feature the limit applies to, e.g. `APP_STORE_VERSIONS`."),
			"placement_type": dsComputed("Placement type the limit applies to."),
			"group_ids":      dsComputedSet("Placement groups the limit applies to."),
			"max_count":      dsComputedInt("Maximum number of placements in each group."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *referenceData, diags *diag.Diagnostics) {
		out, err := c.AppAssetLibraryRefDataGetCollectionWithResponse(ctx, nil)
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "read asset library reference data", err)
			return
		}
		m.ProfileGroups, m.ImageSpecs, m.VideoSpecs, m.Policies = []profileGroupData{}, []assetSpecData{}, []assetSpecData{}, []placementPolicyData{}
		for _, datum := range out.JSON200.Data {
			a := asc.Value(datum.Attributes)
			for _, g := range asc.Value(a.PlacementProfileGroups) {
				m.ProfileGroups = append(m.ProfileGroups, profileGroupData{
					ID: stringValue(g.PlacementProfileGroupId), Platform: stringValue(g.Platform), DisplayClass: stringValue(g.DisplayClassId),
				})
			}
			for _, s := range asc.Value(a.ImageSpecs) {
				m.ImageSpecs = append(m.ImageSpecs, specData(s.SpecId, s.ShortName, s.AspectRatio, s.Dimensions, s.MaxFileSize, s.FileExtensions, s.CompatiblePlacementTypes))
			}
			for _, s := range asc.Value(a.VideoSpecs) {
				m.VideoSpecs = append(m.VideoSpecs, specData(s.SpecId, s.ShortName, s.AspectRatio, s.Dimensions, s.MaxFileSize, s.FileExtensions, s.CompatiblePlacementTypes))
			}
			for _, f := range asc.Value(a.Features) {
				for _, p := range asc.Value(f.PlacementPolicies) {
					for _, limit := range asc.Value(p.GroupLimits) {
						m.Policies = append(m.Policies, placementPolicyData{
							Feature: stringValue(f.FeatureId), PlacementType: stringValue(p.PlacementType),
							GroupIDs: stringsValue(asc.Value(limit.GroupIds)), MaxCount: intValue(limit.MaxCount),
						})
					}
				}
			}
		}
	})
