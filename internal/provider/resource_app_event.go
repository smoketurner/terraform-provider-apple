package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// In-app events: timely events in an app, shown on the App Store as an
// event card and details page
// (https://developer.apple.com/documentation/appstoreconnectapi/app-events).
// Their images and videos are App Asset Library placements on an event
// localization; the event screenshot and video clip endpoints are deprecated.

var (
	eventBadges = enum(gen.AppEventAttributesBadgeLIVEEVENT, gen.AppEventAttributesBadgePREMIERE, gen.AppEventAttributesBadgeCHALLENGE,
		gen.AppEventAttributesBadgeCOMPETITION, gen.AppEventAttributesBadgeNEWSEASON, gen.AppEventAttributesBadgeMAJORUPDATE,
		gen.AppEventAttributesBadgeSPECIALEVENT)
	eventPriorities = enum(gen.AppEventAttributesPriorityHIGH, gen.AppEventAttributesPriorityNORMAL)
	eventPurposes   = enum(gen.AppEventAttributesPurposeAPPROPRIATEFORALLUSERS, gen.AppEventAttributesPurposeATTRACTNEWUSERS,
		gen.AppEventAttributesPurposeKEEPACTIVEUSERSINFORMED, gen.AppEventAttributesPurposeBRINGBACKLAPSEDUSERS)
	eventStates = enum(gen.AppEventAttributesEventStateDRAFT, gen.AppEventAttributesEventStateREADYFORREVIEW,
		gen.AppEventAttributesEventStateWAITINGFORREVIEW, gen.AppEventAttributesEventStateINREVIEW, gen.AppEventAttributesEventStateREJECTED,
		gen.AppEventAttributesEventStateACCEPTED, gen.AppEventAttributesEventStateAPPROVED, gen.AppEventAttributesEventStatePUBLISHED,
		gen.AppEventAttributesEventStatePAST, gen.AppEventAttributesEventStateARCHIVED)
)

var utcTimeValidator = stringvalidator.RegexMatches(utcTimeRegexp, "must be an RFC 3339 time in UTC, e.g. `2026-12-01T16:00:00Z`")

// ---- apple_app_event ----

var (
	_ resource.ResourceWithConfigure   = &appEventResource{}
	_ resource.ResourceWithImportState = &appEventResource{}
	_ resource.ResourceWithIdentity    = &appEventResource{}
)

type appEventResource struct {
	resourceIdentity
	client *asc.Client
}

type eventScheduleModel struct {
	Territories  types.Set    `tfsdk:"territories"`
	PublishStart types.String `tfsdk:"publish_start"`
	EventStart   types.String `tfsdk:"event_start"`
	EventEnd     types.String `tfsdk:"event_end"`
}

type appEventModel struct {
	AppID               types.String         `tfsdk:"app_id"`
	ReferenceName       types.String         `tfsdk:"reference_name"`
	Badge               types.String         `tfsdk:"badge"`
	DeepLink            types.String         `tfsdk:"deep_link"`
	PurchaseRequirement types.String         `tfsdk:"purchase_requirement"`
	PrimaryLocale       types.String         `tfsdk:"primary_locale"`
	Priority            types.String         `tfsdk:"priority"`
	Purpose             types.String         `tfsdk:"purpose"`
	TerritorySchedules  []eventScheduleModel `tfsdk:"territory_schedules"`
	State               types.String         `tfsdk:"state"`
	ID                  types.String         `tfsdk:"id"`
}

// The API doesn't link an in-app event to its app, so the import ID has
// both.
func newAppEventResource() resource.Resource {
	return &appEventResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "id", description: "App Store Connect ID of the in-app event.", state: "id"},
	}}}
}

func (r *appEventResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_event"
}

func (r *appEventResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines an in-app event: a timely event in an app, such as a competition or a premiere, that the " +
			"App Store shows as an event card and details page. Add its text with `apple_app_event_localization` and its " +
			"images or videos with `apple_app_asset_library_placement` on a localization, then submit it with the " +
			"`apple_submit_app_event` action. An app can have 50 draft events. An optional argument removed from the " +
			"configuration keeps its value. Destroying the resource deletes the event with its localizations. Import it " +
			"with `<app_id>/<id>`; `data.apple_app_events` lists an app's events.",
		Attributes: map[string]schema.Attribute{
			"app_id":         replacedString("App Store Connect ID of the app."),
			"reference_name": schema.StringAttribute{MarkdownDescription: "Reference name of the event, shown only in App Store Connect.", Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"badge":          optionalComputedString("Badge shown above the event's name: "+docValues(eventBadges)+".", oneOf(eventBadges)),
			"deep_link":      optionalComputedString("Link that opens the event in the app, e.g. `https://example.com/events/summer`."),
			"purchase_requirement": optionalComputedString("Whether taking part needs an in-app purchase or a subscription, e.g. " +
				"`NO_COST_ASSOCIATED`. The API doesn't list the values it accepts."),
			"primary_locale": optionalComputedString("Locale the App Store falls back to, e.g. `en-US`. The event needs an "+
				"`apple_app_event_localization` for it.", localeValidator),
			"priority": optionalComputedString("Priority among the app's events: "+docValues(eventPriorities)+".", oneOf(eventPriorities)),
			"purpose":  optionalComputedString("Audience of the event: "+docValues(eventPurposes)+".", oneOf(eventPurposes)),
			"territory_schedules": schema.ListNestedAttribute{
				MarkdownDescription: "When the event is published and takes place, per group of territories.",
				Optional:            true,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"territories": schema.SetAttribute{
						MarkdownDescription: "Territories the schedule applies to, e.g. `[\"USA\", \"CAN\"]`; see `data.apple_territories`.",
						Required:            true,
						ElementType:         types.StringType,
						Validators:          []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(territoryValidator)},
					},
					"publish_start": schema.StringAttribute{MarkdownDescription: "When the event appears on the App Store, in UTC, e.g. `2026-12-01T16:00:00Z`.", Required: true, Validators: []validator.String{utcTimeValidator}},
					"event_start":   schema.StringAttribute{MarkdownDescription: "When the event starts, in UTC.", Required: true, Validators: []validator.String{utcTimeValidator}},
					"event_end":     schema.StringAttribute{MarkdownDescription: "When the event ends, in UTC.", Required: true, Validators: []validator.String{utcTimeValidator}},
				}},
			},
			"state": computedOnly("State of the event: " + docValues(eventStates) + "."),
			"id":    idAttribute("App Store Connect ID of the in-app event."),
		},
	}
}

func (r *appEventResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *appEventResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *appEventModel) apply(e *gen.AppEvent) {
	m.ID = types.StringValue(e.Id)
	a := asc.Value(e.Attributes)
	m.ReferenceName, m.Badge, m.DeepLink = stringValue(a.ReferenceName), stringValue(a.Badge), stringValue(a.DeepLink)
	m.PurchaseRequirement, m.PrimaryLocale = stringValue(a.PurchaseRequirement), stringValue(a.PrimaryLocale)
	m.Priority, m.Purpose, m.State = stringValue(a.Priority), stringValue(a.Purpose), stringValue(a.EventState)
	m.TerritorySchedules = nil
	for _, s := range asc.Value(a.TerritorySchedules) {
		m.TerritorySchedules = append(m.TerritorySchedules, eventScheduleModel{
			Territories:  stringsValue(asc.Value(s.Territories)),
			PublishStart: timeValue(s.PublishStart), EventStart: timeValue(s.EventStart), EventEnd: timeValue(s.EventEnd),
		})
	}
}

// attributes returns the attributes the create and update requests share.
// An update sends an empty schedule list to remove the schedules.
func (m *appEventModel) attributes(ctx context.Context, update bool, diags *diag.Diagnostics) map[string]any {
	attrs := map[string]any{"referenceName": m.ReferenceName.ValueString()}
	for name, v := range map[string]types.String{
		"badge": m.Badge, "deepLink": m.DeepLink, "purchaseRequirement": m.PurchaseRequirement,
		"primaryLocale": m.PrimaryLocale, "priority": m.Priority, "purpose": m.Purpose,
	} {
		if s := knownString(v); s != nil {
			attrs[name] = *s
		}
	}
	schedules := []map[string]any{}
	for _, s := range m.TerritorySchedules {
		schedules = append(schedules, map[string]any{
			"territories":  setStrings(ctx, s.Territories, diags),
			"publishStart": s.PublishStart.ValueString(), "eventStart": s.EventStart.ValueString(), "eventEnd": s.EventEnd.ValueString(),
		})
	}
	if update || len(schedules) > 0 {
		attrs["territorySchedules"] = schedules
	}
	return attrs
}

func (r *appEventResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appEventModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	attrs := plan.attributes(ctx, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "appEvents", "attributes": attrs,
		"relationships": map[string]any{"app": map[string]any{"data": map[string]string{"type": "apps", "id": plan.AppID.ValueString()}}},
	}})
	if err == nil {
		out, e := r.client.AppEventsCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON201.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create in-app event", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appEventResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state appEventModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppEventsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read in-app event", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appEventResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appEventModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	attrs := plan.attributes(ctx, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{"type": "appEvents", "id": plan.ID.ValueString(), "attributes": attrs}})
	if err == nil {
		out, e := r.client.AppEventsUpdateInstanceWithBodyWithResponse(ctx, plan.ID.ValueString(), "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.apply(&out.JSON200.Data)
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update in-app event", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appEventResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state appEventModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppEventsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete in-app event", err)
	}
}

// ---- data.apple_app_events ----

type appEventData struct {
	ID            types.String `tfsdk:"id"`
	ReferenceName types.String `tfsdk:"reference_name"`
	Badge         types.String `tfsdk:"badge"`
	State         types.String `tfsdk:"state"`
}

type appEventsData struct {
	AppID  types.String   `tfsdk:"app_id"`
	States types.Set      `tfsdk:"states"`
	Events []appEventData `tfsdk:"app_events"`
}

var newAppEventsDataSource = listDataSource("app_events",
	"Lists an app's in-app events.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"states": dsFilter("States to match: "+docValues(eventStates)+".", eventStates...),
		"app_events": listOf("The in-app events.", map[string]dsschema.Attribute{
			"id":             dsComputed("App Store Connect ID of the in-app event."),
			"reference_name": dsComputed("Reference name of the event."),
			"badge":          dsComputed("Badge of the event."),
			"state":          dsComputed("State of the event."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *appEventsData, diags *diag.Diagnostics) {
		params := &gen.AppsAppEventsGetToManyRelatedParams{
			Limit:            new(listAll),
			FilterEventState: filterParam[gen.AppsAppEventsGetToManyRelatedParamsFilterEventState](ctx, m.States, diags),
		}
		if diags.HasError() {
			return
		}
		out, err := c.AppsAppEventsGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), params)
		var all []gen.AppEvent
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.AppEventsResponse) []gen.AppEvent { return p.Data },
				func(p *gen.AppEventsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list in-app events", err)
			return
		}
		m.Events = []appEventData{}
		for _, e := range all {
			a := asc.Value(e.Attributes)
			m.Events = append(m.Events, appEventData{
				ID: types.StringValue(e.Id), ReferenceName: stringValue(a.ReferenceName), Badge: stringValue(a.Badge), State: stringValue(a.EventState),
			})
		}
	})

// ---- apple_app_event_localization ----

var (
	_ resource.ResourceWithConfigure   = &appEventLocalizationResource{}
	_ resource.ResourceWithImportState = &appEventLocalizationResource{}
	_ resource.ResourceWithIdentity    = &appEventLocalizationResource{}
)

type appEventLocalizationResource struct {
	resourceIdentity
	client *asc.Client
}

type appEventLocalizationModel struct {
	EventID          types.String `tfsdk:"app_event_id"`
	Locale           types.String `tfsdk:"locale"`
	Name             types.String `tfsdk:"name"`
	ShortDescription types.String `tfsdk:"short_description"`
	LongDescription  types.String `tfsdk:"long_description"`
	ID               types.String `tfsdk:"id"`
}

// resolveEventLocalization maps "<app_event_id>/<locale>" to the
// localization's ID, and returns any other key as the ID.
func resolveEventLocalization(ctx context.Context, c *asc.Client, key string) (string, error) {
	eventID, locale, ok := strings.Cut(key, "/")
	if !ok {
		return key, nil
	}
	out, err := c.AppEventsLocalizationsGetToManyRelatedWithResponse(ctx, eventID, &gen.AppEventsLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		return "", errNoMatch
	}
	if err != nil {
		return "", err
	}
	all, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.AppEventLocalizationsResponse) []gen.AppEventLocalization { return p.Data },
		func(p *gen.AppEventLocalizationsResponse) *string { return p.Links.Next })
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

func newAppEventLocalizationResource() resource.Resource {
	return &appEventLocalizationResource{resourceIdentity: resourceIdentity{
		attrs:   idIdentity("App Store Connect ID of the localization."),
		resolve: resolveEventLocalization,
		keyName: "in-app event ID and locale",
	}}
}

func (r *appEventLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_event_localization"
}

func (r *appEventLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the name and descriptions of an in-app event for one locale. An event has one localization " +
			"per locale. To show an image or a video on the event card or details page, create an " +
			"`apple_app_asset_library_placement` with this localization's ID as `app_event_localization_id`. Destroying " +
			"the resource deletes the localization. Import it by ID or with `<app_event_id>/<locale>`.",
		Attributes: map[string]schema.Attribute{
			"app_event_id":      replacedString("App Store Connect ID of the in-app event, e.g. `apple_app_event.example.id`. Changing it creates a new localization."),
			"locale":            replacedString("Locale, e.g. `en-US`. Changing it creates a new localization.", localeValidator),
			"name":              optionalString("Name of the event, up to 30 characters.", stringvalidator.LengthBetween(1, 30)),
			"short_description": optionalString("Summary shown on the event card, up to 50 characters.", stringvalidator.LengthBetween(1, 50)),
			"long_description":  optionalString("Description shown on the event details page, up to 120 characters.", stringvalidator.LengthBetween(1, 120)),
			"id":                idAttribute("App Store Connect ID of the localization."),
		},
	}
}

func (r *appEventLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *appEventLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *appEventLocalizationModel) apply(l *gen.AppEventLocalization) {
	m.ID = types.StringValue(l.Id)
	a := asc.Value(l.Attributes)
	m.Locale, m.Name, m.ShortDescription, m.LongDescription = stringValue(a.Locale), stringValue(a.Name), stringValue(a.ShortDescription), stringValue(a.LongDescription)
	if rel := l.Relationships; rel != nil && rel.AppEvent != nil && rel.AppEvent.Data != nil {
		m.EventID = types.StringValue(rel.AppEvent.Data.Id)
	}
}

func (r *appEventLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan appEventLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppEventLocalizationCreateRequest
	body.Data.Type = gen.AppEventLocalizationCreateRequestDataTypeAppEventLocalizations
	a := &body.Data.Attributes
	a.Locale, a.Name = plan.Locale.ValueString(), knownString(plan.Name)
	a.ShortDescription, a.LongDescription = knownString(plan.ShortDescription), knownString(plan.LongDescription)
	body.Data.Relationships.AppEvent.Data.Id = plan.EventID.ValueString()
	body.Data.Relationships.AppEvent.Data.Type = gen.AppEventLocalizationCreateRequestDataRelationshipsAppEventDataTypeAppEvents
	out, err := r.client.AppEventLocalizationsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create in-app event localization", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appEventLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state appEventLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppEventLocalizationsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.AppEventLocalizationsGetInstanceParams{
		Include: &[]gen.AppEventLocalizationsGetInstanceParamsInclude{gen.AppEventLocalizationsGetInstanceParamsIncludeAppEvent},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read in-app event localization", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sends an empty string for text removed from the configuration,
// which clears it.
func (r *appEventLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state appEventLocalizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.AppEventLocalizationUpdateRequest
	body.Data.Type, body.Data.Id = gen.AppEventLocalizationUpdateRequestDataTypeAppEventLocalizations, plan.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.Name = changedString(plan.Name, state.Name)
	a.ShortDescription = changedString(plan.ShortDescription, state.ShortDescription)
	a.LongDescription = changedString(plan.LongDescription, state.LongDescription)
	out, err := r.client.AppEventLocalizationsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update in-app event localization", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appEventLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state appEventLocalizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppEventLocalizationsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete in-app event localization", err)
	}
}

// ---- apple_submit_app_event ----

type submitAppEventModel struct {
	AppID    types.String `tfsdk:"app_id"`
	EventID  types.String `tfsdk:"app_event_id"`
	Platform types.String `tfsdk:"platform"`
	Submit   types.Bool   `tfsdk:"submit"`
}

var newSubmitAppEventAction = newAction("submit_app_event",
	"Submits an in-app event to App Review, by adding it to the platform's review submission that is being prepared, "+
		"or a new one. The event must be a draft or rejected.",
	map[string]actionschema.Attribute{
		"app_id":       actionString("App Store Connect ID of the app."),
		"app_event_id": actionString("App Store Connect ID of the in-app event, e.g. `apple_app_event.example.id`."),
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
	func(ctx context.Context, c *asc.Client, m *submitAppEventModel) (string, error) {
		eventID := m.EventID.ValueString()
		event, err := c.AppEventsGetInstanceWithResponse(ctx, eventID, nil)
		if err := asc.Err(event, err); err != nil {
			return "", err
		}
		state := asc.Value(asc.Value(event.JSON200.Data.Attributes).EventState)
		if state != gen.AppEventAttributesEventStateDRAFT && state != gen.AppEventAttributesEventStateREJECTED {
			return "", fmt.Errorf("in-app event %s is %s, and can't be submitted", eventID, state)
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
		d := asc.Alloc(&asc.Alloc(&item.Data.Relationships.AppEvent).Data)
		d.Id, d.Type = eventID, gen.ReviewSubmissionItemCreateRequestDataRelationshipsAppEventDataTypeAppEvents
		out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		if !m.Submit.IsNull() && !m.Submit.ValueBool() {
			return "Added in-app event " + eventID + " to review submission " + submissionID, nil
		}
		return "Submitted in-app event " + eventID + " for review", updateSubmission(ctx, c, submissionID, true)
	})
