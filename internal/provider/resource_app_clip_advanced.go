package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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

// Advanced App Clip experiences, App Clip experiences for testers, and the
// App Clip domain status of a build
// (https://developer.apple.com/documentation/appstoreconnectapi/app-clips-and-app-clip-experiences).
// An advanced experience is an App Clip card for one invocation URL, with
// its own image, text and optionally a place on Apple Maps.

var (
	clipLanguages = strings.Fields("AR CA CS DA DE EL EN ES FI FR HE HI HR HU ID IT JA KO MS NL NO PL PT RO RU SK SV TH TR UK VI ZH")

	clipBusinessCategories = strings.Fields("AUTOMOTIVE BEAUTY BIKES BOOKS CASINO EDUCATION EDUCATION_JAPAN ENTERTAINMENT " +
		"EV_CHARGER FINANCIAL_USD FINANCIAL_CNY FINANCIAL_GBP FINANCIAL_JPY FINANCIAL_EUR FITNESS FOOD_AND_DRINK GAS " +
		"GROCERY HEALTH_AND_MEDICAL HOTEL_AND_TRAVEL MUSIC PARKING PET_SERVICES PROFESSIONAL_SERVICES SHOPPING TICKETING TRANSIT")

	placeRelationships = enum(gen.AppClipAdvancedExperienceAttributesPlaceRelationshipOWNER,
		gen.AppClipAdvancedExperienceAttributesPlaceRelationshipAUTHORIZED, gen.AppClipAdvancedExperienceAttributesPlaceRelationshipOTHER)
	placePhoneTypes = enum(gen.AppClipAdvancedExperienceAttributesPlacePhoneNumberTypeFAX, gen.AppClipAdvancedExperienceAttributesPlacePhoneNumberTypeLANDLINE,
		gen.AppClipAdvancedExperienceAttributesPlacePhoneNumberTypeMOBILE, gen.AppClipAdvancedExperienceAttributesPlacePhoneNumberTypeTOLLFREE)
	placePointSources = enum(gen.AppClipAdvancedExperienceAttributesPlaceDisplayPointSourceCALCULATED,
		gen.AppClipAdvancedExperienceAttributesPlaceDisplayPointSourceMANUALLYPLACED)
)

// ---- places ----

// clipPlace is the place of an advanced experience as the API sends it. The
// generated types hold coordinates as float32, which loses digits, so the
// provider reads and writes places with this type.
type clipPlace struct {
	PlaceID      *string           `json:"placeId,omitempty"`
	Names        *[]string         `json:"names,omitempty"`
	Categories   *[]string         `json:"categories,omitempty"`
	HomePage     *string           `json:"homePage,omitempty"`
	MapAction    *string           `json:"mapAction,omitempty"`
	Relationship *string           `json:"relationship,omitempty"`
	PhoneNumber  *clipPlacePhone   `json:"phoneNumber,omitempty"`
	MainAddress  *clipPlaceAddress `json:"mainAddress,omitempty"`
	DisplayPoint *clipPlacePoint   `json:"displayPoint,omitempty"`
}

type clipPlacePhone struct {
	Number *string `json:"number,omitempty"`
	Type   *string `json:"type,omitempty"`
	Intent *string `json:"intent,omitempty"`
}

type clipPlaceAddress struct {
	FullAddress       *string              `json:"fullAddress,omitempty"`
	StructuredAddress *clipPlaceStructured `json:"structuredAddress,omitempty"`
}

type clipPlaceStructured struct {
	StreetAddress *[]string `json:"streetAddress,omitempty"`
	Floor         *string   `json:"floor,omitempty"`
	Neighborhood  *string   `json:"neighborhood,omitempty"`
	Locality      *string   `json:"locality,omitempty"`
	StateProvince *string   `json:"stateProvince,omitempty"`
	PostalCode    *string   `json:"postalCode,omitempty"`
	CountryCode   *string   `json:"countryCode,omitempty"`
}

type clipPlacePoint struct {
	Coordinates *clipPlaceCoordinates `json:"coordinates,omitempty"`
	Source      *string               `json:"source,omitempty"`
}

type clipPlaceCoordinates struct {
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

type clipPlaceModel struct {
	PlaceID            types.String  `tfsdk:"place_id"`
	Names              types.List    `tfsdk:"names"`
	Categories         types.List    `tfsdk:"categories"`
	HomePage           types.String  `tfsdk:"home_page"`
	MapAction          types.String  `tfsdk:"map_action"`
	Relationship       types.String  `tfsdk:"relationship"`
	PhoneNumber        types.String  `tfsdk:"phone_number"`
	PhoneType          types.String  `tfsdk:"phone_type"`
	PhoneIntent        types.String  `tfsdk:"phone_intent"`
	FullAddress        types.String  `tfsdk:"full_address"`
	StreetAddress      types.List    `tfsdk:"street_address"`
	Floor              types.String  `tfsdk:"floor"`
	Neighborhood       types.String  `tfsdk:"neighborhood"`
	Locality           types.String  `tfsdk:"locality"`
	StateProvince      types.String  `tfsdk:"state_province"`
	PostalCode         types.String  `tfsdk:"postal_code"`
	CountryCode        types.String  `tfsdk:"country_code"`
	Latitude           types.Float64 `tfsdk:"latitude"`
	Longitude          types.Float64 `tfsdk:"longitude"`
	DisplayPointSource types.String  `tfsdk:"display_point_source"`
}

func placeAttribute() schema.SingleNestedAttribute {
	list := func(desc string) schema.ListAttribute {
		return schema.ListAttribute{MarkdownDescription: desc, Optional: true, ElementType: types.StringType}
	}
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Place on Apple Maps that the experience belongs to, which lets the App Clip appear on the " +
			"place's card. Apple Maps matches the place after the experience is saved; `place_status` reports the " +
			"result. Attributes left unset are not managed.",
		Optional: true,
		Attributes: map[string]schema.Attribute{
			"place_id":       optionalString("Apple Maps ID of the place, when known."),
			"names":          list("Names of the place, e.g. `[\"Example Cafe\"]`."),
			"categories":     list("Apple Maps categories of the place."),
			"home_page":      optionalString("URL of the place's website."),
			"map_action":     optionalString("Action the App Clip offers on the place's card, e.g. `RESTAURANT_ORDER_FOOD`, `BUY_TICKETS` or `PAY_TO_PARK`."),
			"relationship":   optionalString("How the developer relates to the place: "+docValues(placeRelationships)+".", oneOf(placeRelationships)),
			"phone_number":   optionalString("Phone number of the place."),
			"phone_type":     optionalString("Kind of phone number: "+docValues(placePhoneTypes)+".", oneOf(placePhoneTypes)),
			"phone_intent":   optionalString("What the phone number is for."),
			"full_address":   optionalString("Address of the place on one line."),
			"street_address": list("Street address lines."),
			"floor":          optionalString("Floor of the place."),
			"neighborhood":   optionalString("Neighborhood of the place."),
			"locality":       optionalString("City or town of the place."),
			"state_province": optionalString("State or province of the place."),
			"postal_code":    optionalString("Postal code of the place."),
			"country_code":   optionalString("Country code of the place, e.g. `US`."),
			"latitude":       schema.Float64Attribute{MarkdownDescription: "Latitude of the point the place is shown at.", Optional: true},
			"longitude":      schema.Float64Attribute{MarkdownDescription: "Longitude of the point the place is shown at.", Optional: true},
			"display_point_source": optionalString("Where the point comes from: "+docValues(placePointSources)+".",
				oneOf(placePointSources)),
		},
	}
}

func knownList(ctx context.Context, l types.List) *[]string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	return new(listStrings(ctx, l))
}

func optionalList(values *[]string) types.List {
	if values == nil {
		return types.ListNull(types.StringType)
	}
	return stringsList(*values)
}

// api returns the place as the API takes it, leaving out the groups without
// a value.
func (m *clipPlaceModel) api(ctx context.Context) *clipPlace {
	p := &clipPlace{
		PlaceID: knownString(m.PlaceID), Names: knownList(ctx, m.Names), Categories: knownList(ctx, m.Categories),
		HomePage: knownString(m.HomePage), MapAction: knownString(m.MapAction), Relationship: knownString(m.Relationship),
	}
	if phone := (clipPlacePhone{Number: knownString(m.PhoneNumber), Type: knownString(m.PhoneType), Intent: knownString(m.PhoneIntent)}); phone != (clipPlacePhone{}) {
		p.PhoneNumber = &phone
	}
	address := clipPlaceAddress{FullAddress: knownString(m.FullAddress)}
	structured := clipPlaceStructured{
		StreetAddress: knownList(ctx, m.StreetAddress), Floor: knownString(m.Floor), Neighborhood: knownString(m.Neighborhood),
		Locality: knownString(m.Locality), StateProvince: knownString(m.StateProvince), PostalCode: knownString(m.PostalCode),
		CountryCode: knownString(m.CountryCode),
	}
	if structured != (clipPlaceStructured{}) {
		address.StructuredAddress = &structured
	}
	if address != (clipPlaceAddress{}) {
		p.MainAddress = &address
	}
	point := clipPlacePoint{Source: knownString(m.DisplayPointSource)}
	if coordinates := (clipPlaceCoordinates{Latitude: m.Latitude.ValueFloat64Pointer(), Longitude: m.Longitude.ValueFloat64Pointer()}); coordinates != (clipPlaceCoordinates{}) {
		point.Coordinates = &coordinates
	}
	if point != (clipPlacePoint{}) {
		p.DisplayPoint = &point
	}
	return p
}

func placeModel(p *clipPlace) *clipPlaceModel {
	if p == nil {
		return nil
	}
	phone, address, point := asc.Value(p.PhoneNumber), asc.Value(p.MainAddress), asc.Value(p.DisplayPoint)
	structured, coordinates := asc.Value(address.StructuredAddress), asc.Value(point.Coordinates)
	return &clipPlaceModel{
		PlaceID: stringValue(p.PlaceID), Names: optionalList(p.Names), Categories: optionalList(p.Categories),
		HomePage: stringValue(p.HomePage), MapAction: stringValue(p.MapAction), Relationship: stringValue(p.Relationship),
		PhoneNumber: stringValue(phone.Number), PhoneType: stringValue(phone.Type), PhoneIntent: stringValue(phone.Intent),
		FullAddress: stringValue(address.FullAddress), StreetAddress: optionalList(structured.StreetAddress),
		Floor: stringValue(structured.Floor), Neighborhood: stringValue(structured.Neighborhood), Locality: stringValue(structured.Locality),
		StateProvince: stringValue(structured.StateProvince), PostalCode: stringValue(structured.PostalCode),
		CountryCode: stringValue(structured.CountryCode),
		Latitude:    types.Float64PointerValue(coordinates.Latitude), Longitude: types.Float64PointerValue(coordinates.Longitude),
		DisplayPointSource: stringValue(point.Source),
	}
}

func ifSet[T attr.Value](prior, got T) T {
	if prior.IsNull() {
		return prior
	}
	return got
}

// onlyManaged drops the attributes that prior leaves unset, so that what
// Apple Maps fills in while matching the place isn't read as a change.
func (m *clipPlaceModel) onlyManaged(prior *clipPlaceModel) {
	m.PlaceID, m.Names, m.Categories = ifSet(prior.PlaceID, m.PlaceID), ifSet(prior.Names, m.Names), ifSet(prior.Categories, m.Categories)
	m.HomePage, m.MapAction, m.Relationship = ifSet(prior.HomePage, m.HomePage), ifSet(prior.MapAction, m.MapAction), ifSet(prior.Relationship, m.Relationship)
	m.PhoneNumber, m.PhoneType, m.PhoneIntent = ifSet(prior.PhoneNumber, m.PhoneNumber), ifSet(prior.PhoneType, m.PhoneType), ifSet(prior.PhoneIntent, m.PhoneIntent)
	m.FullAddress, m.StreetAddress, m.Floor = ifSet(prior.FullAddress, m.FullAddress), ifSet(prior.StreetAddress, m.StreetAddress), ifSet(prior.Floor, m.Floor)
	m.Neighborhood, m.Locality, m.StateProvince = ifSet(prior.Neighborhood, m.Neighborhood), ifSet(prior.Locality, m.Locality), ifSet(prior.StateProvince, m.StateProvince)
	m.PostalCode, m.CountryCode = ifSet(prior.PostalCode, m.PostalCode), ifSet(prior.CountryCode, m.CountryCode)
	m.Latitude, m.Longitude = ifSet(prior.Latitude, m.Latitude), ifSet(prior.Longitude, m.Longitude)
	m.DisplayPointSource = ifSet(prior.DisplayPointSource, m.DisplayPointSource)
}

// ---- apple_app_clip_advanced_experience ----

var (
	_ resource.ResourceWithConfigure   = &advancedExperienceResource{}
	_ resource.ResourceWithImportState = &advancedExperienceResource{}
	_ resource.ResourceWithIdentity    = &advancedExperienceResource{}
	_ resource.ResourceWithModifyPlan  = &advancedExperienceResource{}
)

type advancedExperienceResource struct {
	resourceIdentity
	client *asc.Client
}

type advancedLocalizationModel struct {
	Language types.String `tfsdk:"language"`
	Title    types.String `tfsdk:"title"`
	Subtitle types.String `tfsdk:"subtitle"`
}

type advancedExperienceModel struct {
	AppClipID           types.String                `tfsdk:"app_clip_id"`
	Link                types.String                `tfsdk:"link"`
	DefaultLanguage     types.String                `tfsdk:"default_language"`
	IsPoweredBy         types.Bool                  `tfsdk:"is_powered_by"`
	Action              types.String                `tfsdk:"action"`
	BusinessCategory    types.String                `tfsdk:"business_category"`
	HeaderImageFile     types.String                `tfsdk:"header_image_file"`
	HeaderImageChecksum types.String                `tfsdk:"header_image_checksum"`
	HeaderImageID       types.String                `tfsdk:"header_image_id"`
	Localizations       []advancedLocalizationModel `tfsdk:"localizations"`
	Place               *clipPlaceModel             `tfsdk:"place"`
	Status              types.String                `tfsdk:"status"`
	PlaceStatus         types.String                `tfsdk:"place_status"`
	ID                  types.String                `tfsdk:"id"`
}

func newAdvancedExperienceResource() resource.Resource {
	return &advancedExperienceResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the advanced App Clip experience.")}}
}

func (r *advancedExperienceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_clip_advanced_experience"
}

func (r *advancedExperienceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates an advanced App Clip experience: an App Clip card for one invocation URL, with its " +
			"own header image, title and subtitle, and optionally a place on Apple Maps. The resource uploads the header " +
			"image; a changed image file is uploaded as a new image. App Store Connect can't delete an advanced " +
			"experience, so destroying the resource removes it, which deactivates it for good; an experience removed " +
			"outside Terraform is created again. Import it by ID; the next apply then sets the configured image file.",
		Attributes: map[string]schema.Attribute{
			"app_clip_id": replacedString("App Store Connect ID of the App Clip; see `data.apple_app_clips`."),
			"link": replacedString("Invocation URL of the experience, e.g. `https://example.com/order/cafe`. Changing it " +
				"creates a new experience."),
			"default_language": schema.StringAttribute{
				MarkdownDescription: "Language of the card when none of `localizations` matches the person's language: " +
					docValues(clipLanguages) + ". `localizations` must include it.",
				Required:   true,
				Validators: []validator.String{oneOf(clipLanguages)},
			},
			"is_powered_by": schema.BoolAttribute{
				MarkdownDescription: "Whether a platform provider that serves several businesses submits the experience, " +
					"for example an ordering service's App Clip on a restaurant's card.",
				Required: true,
			},
			"action": optionalString("Verb on the App Clip card's button: "+docValues(appClipActions)+".", oneOf(appClipActions)),
			"business_category": optionalString("Category of the business: "+docValues(clipBusinessCategories)+".",
				oneOf(clipBusinessCategories)),
			"header_image_file": schema.StringAttribute{
				MarkdownDescription: "Path of the card's header image. Relative paths are relative to where Terraform runs, so use `\"${path.module}/...\"`.",
				Required:            true,
			},
			"header_image_checksum": schema.StringAttribute{MarkdownDescription: "MD5 checksum of the header image.", Computed: true},
			"header_image_id":       computedString("App Store Connect ID of the header image."),
			"localizations": schema.SetNestedAttribute{
				MarkdownDescription: "Text of the card per language. A change replaces every localization.",
				Required:            true,
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"language": schema.StringAttribute{
						MarkdownDescription: "Language: one of the values of `default_language`.",
						Required:            true,
						Validators:          []validator.String{oneOf(clipLanguages)},
					},
					"title":    optionalString("Title on the card.", stringvalidator.LengthAtLeast(1)),
					"subtitle": optionalString("Subtitle on the card.", stringvalidator.LengthAtLeast(1)),
				}},
			},
			"place":        placeAttribute(),
			"status":       computedOnly("State of the experience: `RECEIVED`, `DEACTIVATED` or `APP_TRANSFER_IN_PROGRESS`."),
			"place_status": computedOnly("Whether Apple Maps matched the place: `PENDING`, `MATCHED` or `NO_MATCH`. Null without a place."),
			"id":           idAttribute("App Store Connect ID of the advanced App Clip experience."),
		},
	}
}

func (r *advancedExperienceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *advancedExperienceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// ModifyPlan plans the header image's checksum, and a new image when it
// changes.
func (r *advancedExperienceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var file types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("header_image_file"), &file)...)
	if resp.Diagnostics.HasError() || file.IsUnknown() {
		return
	}
	data, err := readFiles([]string{file.ValueString()})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("header_image_file"), "Cannot read App Clip header image", err.Error())
		return
	}
	sum := types.StringValue(asc.Checksum(data[0]))
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("header_image_checksum"), sum)...)
	if req.State.Raw.IsNull() {
		return
	}
	var state types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("header_image_checksum"), &state)...)
	if !state.Equal(sum) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("header_image_id"), types.StringUnknown())...)
	}
}

// uploadAdvancedImage uploads a header image, which an experience then
// refers to, and returns its ID and checksum.
func uploadAdvancedImage(ctx context.Context, c *asc.Client, file string) (id, sum string, err error) {
	data, err := readFiles([]string{file})
	if err != nil {
		return "", "", err
	}
	var reserve gen.AppClipAdvancedExperienceImageCreateRequest
	reserve.Data.Type = gen.AppClipAdvancedExperienceImageCreateRequestDataTypeAppClipAdvancedExperienceImages
	reserve.Data.Attributes.FileName, reserve.Data.Attributes.FileSize = filepath.Base(file), len(data[0])
	reserved, err := c.AppClipAdvancedExperienceImagesCreateInstanceWithResponse(ctx, reserve)
	if err := asc.Err(reserved, err); err != nil {
		return "", "", err
	}
	id, sum = reserved.JSON201.Data.Id, asc.Checksum(data[0])
	if err := c.Upload(ctx, asc.Value(asc.Value(reserved.JSON201.Data.Attributes).UploadOperations), data[0]); err != nil {
		return "", "", err
	}
	var commit gen.AppClipAdvancedExperienceImageUpdateRequest
	commit.Data.Type, commit.Data.Id = gen.AppClipAdvancedExperienceImageUpdateRequestDataTypeAppClipAdvancedExperienceImages, id
	a := asc.Alloc(&commit.Data.Attributes)
	a.Uploaded, a.SourceFileChecksum = new(true), new(sum)
	committed, err := c.AppClipAdvancedExperienceImagesUpdateInstanceWithResponse(ctx, id, commit)
	if err := asc.Err(committed, err); err != nil {
		return "", "", err
	}
	if failure := assetFailure(asc.Value(committed.JSON200.Data.Attributes).AssetDeliveryState); failure != "" {
		return "", "", fmt.Errorf("the upload of %s failed: %s", file, failure)
	}
	return id, sum, nil
}

// localizationData returns the localizations as inline resources and the
// relationship data that refers to them.
func (m *advancedExperienceModel) localizationData() (refs, included []map[string]any) {
	const kind = "appClipAdvancedExperienceLocalizations"
	for i, l := range m.Localizations {
		id := fmt.Sprintf("${localization-%d}", i)
		refs = append(refs, map[string]any{"type": kind, "id": id})
		included = append(included, map[string]any{"type": kind, "id": id, "attributes": map[string]any{
			"language": l.Language.ValueString(), "title": knownString(l.Title), "subtitle": knownString(l.Subtitle),
		}})
	}
	return refs, included
}

func (m *advancedExperienceModel) apply(e *gen.AppClipAdvancedExperience) {
	a := asc.Value(e.Attributes)
	m.ID, m.Link, m.DefaultLanguage = types.StringValue(e.Id), stringValue(a.Link), stringValue(a.DefaultLanguage)
	m.IsPoweredBy, m.Action, m.BusinessCategory = types.BoolPointerValue(a.IsPoweredBy), stringValue(a.Action), stringValue(a.BusinessCategory)
	m.Status, m.PlaceStatus = stringValue(a.Status), stringValue(a.PlaceStatus)
	rel := e.Relationships
	if rel == nil {
		return
	}
	if rel.AppClip != nil && rel.AppClip.Data != nil {
		m.AppClipID = types.StringValue(rel.AppClip.Data.Id)
	}
	if rel.HeaderImage != nil && rel.HeaderImage.Data != nil {
		m.HeaderImageID = types.StringValue(rel.HeaderImage.Data.Id)
	}
}

func linkage(kind, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": kind, "id": id}}
}

// Create uploads the header image and creates the experience with its
// localizations, as raw JSON because of the place's coordinates.
func (r *advancedExperienceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan advancedExperienceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	imageID, sum, err := uploadAdvancedImage(ctx, r.client, plan.HeaderImageFile.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "upload App Clip header image", err)
		return
	}
	attributes := map[string]any{
		"link": plan.Link.ValueString(), "defaultLanguage": plan.DefaultLanguage.ValueString(), "isPoweredBy": plan.IsPoweredBy.ValueBool(),
	}
	if action := knownString(plan.Action); action != nil {
		attributes["action"] = action
	}
	if category := knownString(plan.BusinessCategory); category != nil {
		attributes["businessCategory"] = category
	}
	if plan.Place != nil {
		attributes["place"] = plan.Place.api(ctx)
	}
	refs, included := plan.localizationData()
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{"type": "appClipAdvancedExperiences", "attributes": attributes, "relationships": map[string]any{
			"appClip":       linkage("appClips", plan.AppClipID.ValueString()),
			"headerImage":   linkage("appClipAdvancedExperienceImages", imageID),
			"localizations": map[string]any{"data": refs},
		}},
		"included": included,
	})
	if err == nil {
		out, e := r.client.AppClipAdvancedExperiencesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON201.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create advanced App Clip experience", err)
		return
	}
	plan.HeaderImageID, plan.HeaderImageChecksum = types.StringValue(imageID), types.StringValue(sum)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *advancedExperienceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state advancedExperienceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppClipAdvancedExperiencesGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppClipAdvancedExperiencesGetInstanceParams{
		Include: &[]gen.AppClipAdvancedExperiencesGetInstanceParamsInclude{
			gen.AppClipAdvancedExperiencesGetInstanceParamsIncludeAppClip, gen.AppClipAdvancedExperiencesGetInstanceParamsIncludeHeaderImage,
			gen.AppClipAdvancedExperiencesGetInstanceParamsIncludeLocalizations,
		},
		LimitLocalizations: new(50),
	})
	err = asc.Err(out, err)
	// A removed experience stays in App Store Connect, deactivated.
	if asc.IsNotFound(err) || err == nil && asc.Value(asc.Value(out.JSON200.Data.Attributes).Status) == gen.AppClipAdvancedExperienceAttributesStatusDEACTIVATED {
		resp.State.RemoveResource(ctx)
		return
	}
	var doc struct {
		Data struct {
			Attributes struct {
				Place *clipPlace `json:"place"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err == nil {
		err = json.Unmarshal(out.Body, &doc)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read advanced App Clip experience", err)
		return
	}
	state.apply(&out.JSON200.Data)
	state.Localizations = []advancedLocalizationModel{}
	for _, item := range asc.Value(out.JSON200.Included) {
		if kind, _ := item.Discriminator(); kind != "appClipAdvancedExperienceLocalizations" {
			continue
		}
		if l, err := item.AsAppClipAdvancedExperienceLocalization(); err == nil {
			a := asc.Value(l.Attributes)
			state.Localizations = append(state.Localizations, advancedLocalizationModel{
				Language: stringValue(a.Language), Title: stringValue(a.Title), Subtitle: stringValue(a.Subtitle),
			})
		}
	}
	place := placeModel(doc.Data.Attributes.Place)
	if place != nil && state.Place != nil {
		place.onlyManaged(state.Place)
	}
	state.Place = place
	if id := state.HeaderImageID.ValueString(); id != "" {
		image, err := r.client.AppClipAdvancedExperienceImagesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(image, err); err != nil {
			apiError(&resp.Diagnostics, "read App Clip header image", err)
			return
		}
		if sum := asc.Value(asc.Value(image.JSON200.Data.Attributes).SourceFileChecksum); sum != "" {
			state.HeaderImageChecksum = types.StringValue(sum)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// patch sends an update of the experience id.
func (r *advancedExperienceResource) patch(ctx context.Context, id string, attributes, relationships map[string]any, included []map[string]any) (*gen.AppClipAdvancedExperience, error) {
	data := map[string]any{"type": "appClipAdvancedExperiences", "id": id, "attributes": attributes}
	if len(relationships) > 0 {
		data["relationships"] = relationships
	}
	doc := map[string]any{"data": data}
	if len(included) > 0 {
		doc["included"] = included
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	out, err := r.client.AppClipAdvancedExperiencesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

// Update sends the attributes, with nulls for the removed ones, and the
// place, image and localizations only when they change: a new place is
// matched again, and new localizations replace the current ones.
func (r *advancedExperienceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state advancedExperienceModel
	var planPlace, statePlace types.Object
	var planLocalizations, stateLocalizations types.Set
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("place"), &planPlace)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("place"), &statePlace)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("localizations"), &planLocalizations)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("localizations"), &stateLocalizations)...)
	if resp.Diagnostics.HasError() {
		return
	}
	attributes := map[string]any{
		"action": knownString(plan.Action), "businessCategory": knownString(plan.BusinessCategory),
		"defaultLanguage": plan.DefaultLanguage.ValueString(), "isPoweredBy": plan.IsPoweredBy.ValueBool(),
	}
	if !planPlace.Equal(statePlace) {
		attributes["place"] = nil
		if plan.Place != nil {
			attributes["place"] = plan.Place.api(ctx)
		}
	}
	relationships := map[string]any{}
	imageID, sum := state.HeaderImageID.ValueString(), state.HeaderImageChecksum.ValueString()
	if !plan.HeaderImageChecksum.Equal(state.HeaderImageChecksum) {
		var err error
		if imageID, sum, err = uploadAdvancedImage(ctx, r.client, plan.HeaderImageFile.ValueString()); err != nil {
			apiError(&resp.Diagnostics, "upload App Clip header image", err)
			return
		}
		relationships["headerImage"] = linkage("appClipAdvancedExperienceImages", imageID)
	}
	var included []map[string]any
	if !planLocalizations.Equal(stateLocalizations) {
		var refs []map[string]any
		refs, included = plan.localizationData()
		relationships["localizations"] = map[string]any{"data": refs}
	}
	experience, err := r.patch(ctx, plan.ID.ValueString(), attributes, relationships, included)
	if err != nil {
		apiError(&resp.Diagnostics, "update advanced App Clip experience", err)
		return
	}
	plan.apply(experience)
	plan.HeaderImageID, plan.HeaderImageChecksum = types.StringValue(imageID), types.StringValue(sum)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the experience, which deactivates it: the API can't delete
// one.
func (r *advancedExperienceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state advancedExperienceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.patch(ctx, state.ID.ValueString(), map[string]any{"removed": true}, nil, nil)
	if err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "remove advanced App Clip experience", err)
	}
}

// ---- apple_beta_app_clip_invocation ----

var (
	_ resource.ResourceWithConfigure   = &clipInvocationResource{}
	_ resource.ResourceWithImportState = &clipInvocationResource{}
	_ resource.ResourceWithIdentity    = &clipInvocationResource{}
)

type clipInvocationResource struct {
	resourceIdentity
	client *asc.Client
}

type invocationLocalizationModel struct {
	Locale types.String `tfsdk:"locale"`
	Title  types.String `tfsdk:"title"`
}

type clipInvocationModel struct {
	BuildBundleID types.String                  `tfsdk:"build_bundle_id"`
	URL           types.String                  `tfsdk:"url"`
	Localizations []invocationLocalizationModel `tfsdk:"localizations"`
	ID            types.String                  `tfsdk:"id"`
}

func newClipInvocationResource() resource.Resource {
	return &clipInvocationResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "build_bundle_id", description: "App Store Connect ID of the App Clip's build bundle.", state: "build_bundle_id"},
		{name: "id", description: "App Store Connect ID of the invocation.", state: "id"},
	}}}
}

func (r *clipInvocationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_beta_app_clip_invocation"
}

func (r *clipInvocationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds an App Clip experience for testers: an invocation URL that TestFlight lists for a " +
			"build's App Clip, with a title per locale. It belongs to the App Clip bundle of one build, so a new build " +
			"takes a new invocation. Import it with `<build_bundle_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"build_bundle_id": replacedString("App Store Connect ID of the App Clip's build bundle; see " +
				"`data.apple_build_bundles`. Changing it creates a new invocation."),
			"url": schema.StringAttribute{MarkdownDescription: "Invocation URL that launches the App Clip, e.g. `https://example.com/order`.", Required: true},
			"localizations": schema.SetNestedAttribute{
				MarkdownDescription: "Title of the experience in TestFlight, per locale.",
				Required:            true,
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"locale": schema.StringAttribute{MarkdownDescription: "Locale, e.g. `en-US`.", Required: true, Validators: []validator.String{localeValidator}},
					"title": schema.StringAttribute{
						MarkdownDescription: "Title testers see.", Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
					},
				}},
			},
			"id": idAttribute("App Store Connect ID of the invocation."),
		},
	}
}

func (r *clipInvocationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *clipInvocationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *clipInvocationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan clipInvocationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BetaAppClipInvocationCreateRequest
	body.Data.Type, body.Data.Attributes.Url = gen.BetaAppClipInvocationCreateRequestDataTypeBetaAppClipInvocations, plan.URL.ValueString()
	rel := &body.Data.Relationships
	rel.BuildBundle.Data.Id, rel.BuildBundle.Data.Type = plan.BuildBundleID.ValueString(), gen.BetaAppClipInvocationCreateRequestDataRelationshipsBuildBundleDataTypeBuildBundles
	rel.BetaAppClipInvocationLocalizations.Data = nil
	for i, l := range plan.Localizations {
		id := fmt.Sprintf("${localization-%d}", i)
		ref := asc.Push(&rel.BetaAppClipInvocationLocalizations.Data)
		ref.Id, ref.Type = id, gen.BetaAppClipInvocationCreateRequestDataRelationshipsBetaAppClipInvocationLocalizationsDataTypeBetaAppClipInvocationLocalizations
		item := asc.Push(asc.Alloc(&body.Included))
		item.Id, item.Type = new(id), gen.BetaAppClipInvocationLocalizationInlineCreateTypeBetaAppClipInvocationLocalizations
		item.Attributes.Locale, item.Attributes.Title = l.Locale.ValueString(), l.Title.ValueString()
	}
	out, err := r.client.BetaAppClipInvocationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create beta App Clip invocation", err)
		return
	}
	plan.ID = types.StringValue(out.JSON201.Data.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// get returns the invocation with its localizations.
func (r *clipInvocationResource) get(ctx context.Context, id string) (*gen.BetaAppClipInvocationResponse, error) {
	out, err := r.client.BetaAppClipInvocationsGetInstanceWithResponse(ctx, id, &gen.BetaAppClipInvocationsGetInstanceParams{
		Include:                                 &[]gen.BetaAppClipInvocationsGetInstanceParamsInclude{gen.BetaAppClipInvocationsGetInstanceParamsIncludeBetaAppClipInvocationLocalizations},
		LimitBetaAppClipInvocationLocalizations: new(50),
	})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return out.JSON200, nil
}

func (r *clipInvocationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state clipInvocationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.get(ctx, state.ID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read beta App Clip invocation", err)
		return
	}
	state.URL = stringValue(asc.Value(out.Data.Attributes).Url)
	state.Localizations = []invocationLocalizationModel{}
	for _, l := range asc.Value(out.Included) {
		a := asc.Value(l.Attributes)
		state.Localizations = append(state.Localizations, invocationLocalizationModel{Locale: stringValue(a.Locale), Title: stringValue(a.Title)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes the URL, then creates, retitles and deletes localizations
// to match the plan.
func (r *clipInvocationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state clipInvocationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.update(ctx, &plan, &state); err != nil {
		apiError(&resp.Diagnostics, "update beta App Clip invocation", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clipInvocationResource) update(ctx context.Context, plan, state *clipInvocationModel) error {
	id := plan.ID.ValueString()
	if !plan.URL.Equal(state.URL) {
		var body gen.BetaAppClipInvocationUpdateRequest
		body.Data.Type, body.Data.Id = gen.BetaAppClipInvocationUpdateRequestDataTypeBetaAppClipInvocations, id
		asc.Alloc(&body.Data.Attributes).Url = new(plan.URL.ValueString())
		out, err := r.client.BetaAppClipInvocationsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return err
		}
	}
	current, err := r.get(ctx, id)
	if err != nil {
		return err
	}
	existing := map[string]gen.BetaAppClipInvocationLocalization{}
	for _, l := range asc.Value(current.Included) {
		existing[asc.Value(asc.Value(l.Attributes).Locale)] = l
	}
	for _, l := range plan.Localizations {
		locale, title := l.Locale.ValueString(), l.Title.ValueString()
		old, ok := existing[locale]
		delete(existing, locale)
		switch {
		case !ok:
			var body gen.BetaAppClipInvocationLocalizationCreateRequest
			body.Data.Type = gen.BetaAppClipInvocationLocalizationCreateRequestDataTypeBetaAppClipInvocationLocalizations
			body.Data.Attributes.Locale, body.Data.Attributes.Title = locale, title
			d := &body.Data.Relationships.BetaAppClipInvocation.Data
			d.Id, d.Type = id, gen.BetaAppClipInvocationLocalizationCreateRequestDataRelationshipsBetaAppClipInvocationDataTypeBetaAppClipInvocations
			out, err := r.client.BetaAppClipInvocationLocalizationsCreateInstanceWithResponse(ctx, body)
			if err := asc.Err(out, err); err != nil {
				return fmt.Errorf("adding the %s localization: %w", locale, err)
			}
		case asc.Value(asc.Value(old.Attributes).Title) != title:
			var body gen.BetaAppClipInvocationLocalizationUpdateRequest
			body.Data.Type, body.Data.Id = gen.BetaAppClipInvocationLocalizationUpdateRequestDataTypeBetaAppClipInvocationLocalizations, old.Id
			asc.Alloc(&body.Data.Attributes).Title = new(title)
			out, err := r.client.BetaAppClipInvocationLocalizationsUpdateInstanceWithResponse(ctx, old.Id, body)
			if err := asc.Err(out, err); err != nil {
				return fmt.Errorf("changing the %s localization: %w", locale, err)
			}
		}
	}
	for locale, old := range existing {
		out, err := r.client.BetaAppClipInvocationLocalizationsDeleteInstanceWithResponse(ctx, old.Id)
		if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
			return fmt.Errorf("deleting the %s localization: %w", locale, err)
		}
	}
	return nil
}

func (r *clipInvocationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clipInvocationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BetaAppClipInvocationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete beta App Clip invocation", err)
	}
}

// ---- data.apple_build_bundles ----

type buildBundleData struct {
	ID         types.String `tfsdk:"id"`
	BundleID   types.String `tfsdk:"bundle_id"`
	BundleType types.String `tfsdk:"bundle_type"`
	FileName   types.String `tfsdk:"file_name"`
}

type buildBundlesData struct {
	BuildID types.String      `tfsdk:"build_id"`
	Bundles []buildBundleData `tfsdk:"build_bundles"`
}

var newBuildBundlesDataSource = listDataSource("build_bundles",
	"Lists the bundles of a build: the app, and the App Clip and watch app it contains. An App Clip's bundle takes "+
		"`apple_beta_app_clip_invocation` and `data.apple_app_clip_domain_status`.",
	map[string]dsschema.Attribute{
		"build_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the build; see `data.apple_build`.", Required: true},
		"build_bundles": listOf("The bundles.", map[string]dsschema.Attribute{
			"id":        dsComputed("App Store Connect ID of the build bundle."),
			"bundle_id": dsComputed("Bundle identifier, e.g. `com.example.app.Clip`."),
			"bundle_type": dsComputed("Kind of bundle: " + docValues(enum(gen.BuildBundleTypeAPP, gen.BuildBundleTypeAPPCLIP,
				gen.BuildBundleTypeWATCHAPP)) + "."),
			"file_name": dsComputed("File name of the bundle."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *buildBundlesData, diags *diag.Diagnostics) {
		out, err := c.BuildsGetInstanceWithResponse(ctx, m.BuildID.ValueString(), &gen.BuildsGetInstanceParams{
			Include: &[]gen.BuildsGetInstanceParamsInclude{gen.BuildsGetInstanceParamsIncludeBuildBundles}, LimitBuildBundles: new(50),
		})
		if err := asc.Err(out, err); err != nil {
			lookupError(diags, "build", err)
			return
		}
		m.Bundles = []buildBundleData{}
		for _, item := range asc.Value(out.JSON200.Included) {
			if kind, _ := item.Discriminator(); kind != "buildBundles" {
				continue
			}
			if b, err := item.AsBuildBundle(); err == nil {
				a := asc.Value(b.Attributes)
				m.Bundles = append(m.Bundles, buildBundleData{
					ID: types.StringValue(b.Id), BundleID: stringValue(a.BundleId), BundleType: stringValue(a.BundleType), FileName: stringValue(a.FileName),
				})
			}
		}
	})

// ---- data.apple_app_clip_domain_status ----

type clipDomainData struct {
	Domain          types.String `tfsdk:"domain"`
	IsValid         types.Bool   `tfsdk:"is_valid"`
	ErrorCode       types.String `tfsdk:"error_code"`
	LastUpdatedDate types.String `tfsdk:"last_updated_date"`
}

type clipDomainStatusData struct {
	BuildBundleID types.String     `tfsdk:"build_bundle_id"`
	CacheUpdated  types.String     `tfsdk:"cache_last_updated_date"`
	CacheDomains  []clipDomainData `tfsdk:"cache_domains"`
	DebugUpdated  types.String     `tfsdk:"debug_last_updated_date"`
	DebugDomains  []clipDomainData `tfsdk:"debug_domains"`
}

func clipDomains(status *gen.AppClipDomainStatus) (types.String, []clipDomainData) {
	a := asc.Value(status.Attributes)
	domains := []clipDomainData{}
	for _, d := range asc.Value(a.Domains) {
		domains = append(domains, clipDomainData{
			Domain: stringValue(d.Domain), IsValid: types.BoolPointerValue(d.IsValid), ErrorCode: stringValue(d.ErrorCode),
			LastUpdatedDate: timeValue(d.LastUpdatedDate),
		})
	}
	return timeValue(a.LastUpdatedDate), domains
}

func clipDomainAttributes(which string) dsschema.ListNestedAttribute {
	return listOf("The App Clip's associated domains with their "+which+" status.", map[string]dsschema.Attribute{
		"domain":   dsComputed("The associated domain."),
		"is_valid": dsschema.BoolAttribute{MarkdownDescription: "Whether the domain's association with the App Clip is valid.", Computed: true},
		"error_code": dsComputed("Why the association is invalid, e.g. `CANNOT_REACH_AASA_FILE`, `BAD_JSON_CONTENT` or " +
			"`INVALID_ENTITLEMENT_UNKNOWN_ID`. Null for a valid domain."),
		"last_updated_date": dsComputed("When the domain was last checked."),
	})
}

var newClipDomainStatusDataSource = listDataSource("app_clip_domain_status",
	"Reads whether the associated domains of a build's App Clip are reachable and set up correctly, which App Clip "+
		"invocation URLs depend on. App Store Connect reports a cache status and a debug status for each domain.",
	map[string]dsschema.Attribute{
		"build_bundle_id":         dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the App Clip's build bundle; see `data.apple_build_bundles`.", Required: true},
		"cache_last_updated_date": dsComputed("When the cache status was last updated."),
		"cache_domains":           clipDomainAttributes("cache"),
		"debug_last_updated_date": dsComputed("When the debug status was last updated."),
		"debug_domains":           clipDomainAttributes("debug"),
	},
	func(ctx context.Context, c *asc.Client, m *clipDomainStatusData, diags *diag.Diagnostics) {
		id := m.BuildBundleID.ValueString()
		cache, err := c.BuildBundlesAppClipDomainCacheStatusGetToOneRelatedWithResponse(ctx, id, nil)
		if err := asc.Err(cache, err); err != nil {
			lookupError(diags, "App Clip domain cache status", err)
			return
		}
		debug, err := c.BuildBundlesAppClipDomainDebugStatusGetToOneRelatedWithResponse(ctx, id, nil)
		if err := asc.Err(debug, err); err != nil {
			lookupError(diags, "App Clip domain debug status", err)
			return
		}
		m.CacheUpdated, m.CacheDomains = clipDomains(&cache.JSON200.Data)
		m.DebugUpdated, m.DebugDomains = clipDomains(&debug.JSON200.Data)
	})
