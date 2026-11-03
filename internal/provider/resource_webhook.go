package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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

// Webhooks notify a URL of an app's App Store Connect events
// (https://developer.apple.com/documentation/appstoreconnectapi/webhooks).

var (
	webhookEventTypes = enum(gen.WebhookEventTypeALTERNATIVEDISTRIBUTIONPACKAGEAVAILABLEUPDATED,
		gen.WebhookEventTypeALTERNATIVEDISTRIBUTIONPACKAGEVERSIONCREATED, gen.WebhookEventTypeALTERNATIVEDISTRIBUTIONTERRITORYAVAILABILITYUPDATED,
		gen.WebhookEventTypeAPPSTOREVERSIONAPPVERSIONSTATEUPDATED, gen.WebhookEventTypeBACKGROUNDASSETVERSIONAPPSTORERELEASESTATEUPDATED,
		gen.WebhookEventTypeBACKGROUNDASSETVERSIONEXTERNALBETARELEASESTATEUPDATED, gen.WebhookEventTypeBACKGROUNDASSETVERSIONINTERNALBETARELEASECREATED,
		gen.WebhookEventTypeBACKGROUNDASSETVERSIONSTATEUPDATED, gen.WebhookEventTypeBETAFEEDBACKCRASHSUBMISSIONCREATED,
		gen.WebhookEventTypeBETAFEEDBACKSCREENSHOTSUBMISSIONCREATED, gen.WebhookEventTypeBUILDBETADETAILEXTERNALBUILDSTATEUPDATED,
		gen.WebhookEventTypeBUILDUPLOADSTATEUPDATED)
	deliveryStates = enum(gen.WebhookDeliveryAttributesDeliveryStateSUCCEEDED, gen.WebhookDeliveryAttributesDeliveryStateFAILED,
		gen.WebhookDeliveryAttributesDeliveryStatePENDING)
)

var httpsURLValidator = stringvalidator.RegexMatches(regexp.MustCompile(`^https://[^/\s]+`), "must be an HTTPS URL")

var (
	_ resource.ResourceWithConfigure   = &webhookResource{}
	_ resource.ResourceWithImportState = &webhookResource{}
	_ resource.ResourceWithIdentity    = &webhookResource{}
)

type webhookResource struct {
	resourceIdentity
	client *asc.Client
}

type webhookModel struct {
	AppID      types.String `tfsdk:"app_id"`
	Name       types.String `tfsdk:"name"`
	URL        types.String `tfsdk:"url"`
	EventTypes types.Set    `tfsdk:"event_types"`
	Secret     types.String `tfsdk:"secret"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	ID         types.String `tfsdk:"id"`
}

func newWebhookResource() resource.Resource {
	return &webhookResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the webhook.")}}
}

func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sends an app's App Store Connect events, such as build uploads and version state " +
			"changes, to a URL. Events are signed with `secret`, which App Store Connect never returns: changes made " +
			"outside Terraform aren't detected, and after an import the next apply sets it. Test the webhook with " +
			"`apple_ping_webhook`. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"app_id": replacedString("App Store Connect ID of the app."),
			"name":   schema.StringAttribute{MarkdownDescription: "Name of the webhook.", Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"url":    schema.StringAttribute{MarkdownDescription: "HTTPS URL events are sent to.", Required: true, Validators: []validator.String{httpsURLValidator}},
			"event_types": schema.SetAttribute{
				MarkdownDescription: "Events to send: " + docValues(webhookEventTypes) + ".",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(oneOf(webhookEventTypes))},
			},
			"secret": schema.StringAttribute{
				MarkdownDescription: "Secret App Store Connect signs events with, so the receiver can verify them.",
				Required:            true,
				Sensitive:           true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether events are sent. Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"id": idAttribute("App Store Connect ID of the webhook."),
		},
	}
}

func (r *webhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *webhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// apply records h; the secret is kept, because the API doesn't return it.
func (m *webhookModel) apply(h *gen.Webhook) {
	a := asc.Value(h.Attributes)
	m.ID, m.Name, m.URL = types.StringValue(h.Id), stringValue(a.Name), stringValue(a.Url)
	m.Enabled = types.BoolValue(asc.Value(a.Enabled))
	var events []string
	for _, e := range asc.Value(a.EventTypes) {
		events = append(events, string(e))
	}
	m.EventTypes = stringsValue(events)
	if rel := h.Relationships; rel != nil && rel.App != nil && rel.App.Data != nil {
		m.AppID = types.StringValue(rel.App.Data.Id)
	}
}

func eventTypes(ctx context.Context, set types.Set, diags *diag.Diagnostics) []gen.WebhookEventType {
	return slicesOf[gen.WebhookEventType](setStrings(ctx, set, diags))
}

func (r *webhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.WebhookCreateRequest
	body.Data.Type = gen.WebhookCreateRequestDataTypeWebhooks
	a := &body.Data.Attributes
	a.Name, a.Url, a.Secret, a.Enabled = plan.Name.ValueString(), plan.URL.ValueString(), plan.Secret.ValueString(), plan.Enabled.ValueBool()
	a.EventTypes = eventTypes(ctx, plan.EventTypes, &resp.Diagnostics)
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.WebhookCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.WebhooksCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create webhook", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.WebhooksGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.WebhooksGetInstanceParams{
		Include: &[]gen.WebhooksGetInstanceParamsInclude{gen.WebhooksGetInstanceParamsIncludeApp},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read webhook", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *webhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.WebhookUpdateRequest
	body.Data.Type, body.Data.Id = gen.WebhookUpdateRequestDataTypeWebhooks, state.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.Name, a.Url, a.Enabled = new(plan.Name.ValueString()), new(plan.URL.ValueString()), new(plan.Enabled.ValueBool())
	a.EventTypes = new(eventTypes(ctx, plan.EventTypes, &resp.Diagnostics))
	if !plan.Secret.Equal(state.Secret) {
		a.Secret = new(plan.Secret.ValueString())
	}
	out, err := r.client.WebhooksUpdateInstanceWithResponse(ctx, state.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update webhook", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.WebhooksDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete webhook", err)
	}
}

// ---- data.apple_webhooks ----

type webhookData struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	URL        types.String `tfsdk:"url"`
	EventTypes types.Set    `tfsdk:"event_types"`
	Enabled    types.Bool   `tfsdk:"enabled"`
}

type webhooksData struct {
	AppID    types.String  `tfsdk:"app_id"`
	Webhooks []webhookData `tfsdk:"webhooks"`
}

var newWebhooksDataSource = listDataSource("webhooks",
	"Lists an app's webhooks.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"webhooks": listOf("The webhooks.", map[string]dsschema.Attribute{
			"id":          dsComputed("App Store Connect ID of the webhook."),
			"name":        dsComputed("Name of the webhook."),
			"url":         dsComputed("URL events are sent to."),
			"event_types": dsComputedSet("Events sent."),
			"enabled":     dsschema.BoolAttribute{MarkdownDescription: "Whether events are sent.", Computed: true},
		}),
	},
	func(ctx context.Context, c *asc.Client, m *webhooksData, diags *diag.Diagnostics) {
		out, err := c.AppsWebhooksGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsWebhooksGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.Webhook
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.WebhooksResponse) []gen.Webhook { return p.Data },
				func(p *gen.WebhooksResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list webhooks", err)
			return
		}
		m.Webhooks = []webhookData{}
		for _, h := range all {
			var w webhookModel
			w.apply(&h)
			m.Webhooks = append(m.Webhooks, webhookData{ID: w.ID, Name: w.Name, URL: w.URL, EventTypes: w.EventTypes, Enabled: w.Enabled})
		}
	})

// ---- data.apple_webhook_deliveries ----

type deliveryData struct {
	ID             types.String `tfsdk:"id"`
	State          types.String `tfsdk:"state"`
	CreatedDate    types.String `tfsdk:"created_date"`
	SentDate       types.String `tfsdk:"sent_date"`
	Redelivery     types.Bool   `tfsdk:"redelivery"`
	HTTPStatusCode types.Int64  `tfsdk:"http_status_code"`
	ErrorMessage   types.String `tfsdk:"error_message"`
}

type deliveriesData struct {
	WebhookID  types.String   `tfsdk:"webhook_id"`
	States     types.Set      `tfsdk:"states"`
	Deliveries []deliveryData `tfsdk:"deliveries"`
}

var newWebhookDeliveriesDataSource = listDataSource("webhook_deliveries",
	"Lists a webhook's deliveries, for example to find failed ones to send again with `apple_redeliver_webhook`.",
	map[string]dsschema.Attribute{
		"webhook_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the webhook.", Required: true},
		"states":     dsFilter("Only deliveries in these states.", deliveryStates...),
		"deliveries": listOf("The deliveries.", map[string]dsschema.Attribute{
			"id":               dsComputed("App Store Connect ID of the delivery."),
			"state":            dsComputed("State of the delivery: " + docValues(deliveryStates) + "."),
			"created_date":     dsComputed("When the event was created."),
			"sent_date":        dsComputed("When the delivery was sent."),
			"redelivery":       dsschema.BoolAttribute{MarkdownDescription: "Whether this delivery sent an earlier one again.", Computed: true},
			"http_status_code": dsschema.Int64Attribute{MarkdownDescription: "HTTP status the receiver returned.", Computed: true},
			"error_message":    dsComputed("Why the delivery failed."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *deliveriesData, diags *diag.Diagnostics) {
		out, err := c.WebhooksDeliveriesGetToManyRelatedWithResponse(ctx, m.WebhookID.ValueString(), &gen.WebhooksDeliveriesGetToManyRelatedParams{
			FilterDeliveryState: filterParam[gen.WebhooksDeliveriesGetToManyRelatedParamsFilterDeliveryState](ctx, m.States, diags),
			Limit:               new(listAll),
		})
		var all []gen.WebhookDelivery
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.WebhookDeliveriesResponse) []gen.WebhookDelivery { return p.Data },
				func(p *gen.WebhookDeliveriesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list webhook deliveries", err)
			return
		}
		m.Deliveries = []deliveryData{}
		for _, d := range all {
			a := asc.Value(d.Attributes)
			item := deliveryData{
				ID: types.StringValue(d.Id), State: stringValue(a.DeliveryState), CreatedDate: timeValue(a.CreatedDate),
				SentDate: timeValue(a.SentDate), Redelivery: types.BoolValue(asc.Value(a.Redelivery)),
				HTTPStatusCode: types.Int64Null(), ErrorMessage: stringValue(a.ErrorMessage),
			}
			if code := asc.Value(a.Response).HttpStatusCode; code != nil {
				item.HTTPStatusCode = types.Int64Value(int64(*code))
			}
			m.Deliveries = append(m.Deliveries, item)
		}
	})

// ---- actions ----

type webhookActionModel struct {
	WebhookID types.String `tfsdk:"webhook_id"`
}

var newPingWebhookAction = newAction("ping_webhook",
	"Sends a test event to a webhook, to check that the receiver gets and verifies it. The webhook must be enabled.",
	map[string]actionschema.Attribute{"webhook_id": actionString("App Store Connect ID of the webhook, e.g. `apple_webhook.example.id`.")},
	func(ctx context.Context, c *asc.Client, m *webhookActionModel) (string, error) {
		var body gen.WebhookPingCreateRequest
		body.Data.Type = gen.WebhookPingCreateRequestDataTypeWebhookPings
		body.Data.Relationships.Webhook.Data.Id = m.WebhookID.ValueString()
		body.Data.Relationships.Webhook.Data.Type = gen.WebhookPingCreateRequestDataRelationshipsWebhookDataTypeWebhooks
		out, err := c.WebhookPingsCreateInstanceWithResponse(ctx, body)
		return "Pinged webhook " + m.WebhookID.ValueString(), asc.Err(out, err)
	})

type redeliverModel struct {
	DeliveryID types.String `tfsdk:"delivery_id"`
}

var newRedeliverWebhookAction = newAction("redeliver_webhook",
	"Sends a webhook delivery again, for example one that failed; find them with `data.apple_webhook_deliveries`.",
	map[string]actionschema.Attribute{"delivery_id": actionString("App Store Connect ID of the delivery to send again.")},
	func(ctx context.Context, c *asc.Client, m *redeliverModel) (string, error) {
		var body gen.WebhookDeliveryCreateRequest
		body.Data.Type = gen.WebhookDeliveryCreateRequestDataTypeWebhookDeliveries
		body.Data.Relationships.Template.Data.Id = m.DeliveryID.ValueString()
		body.Data.Relationships.Template.Data.Type = gen.WebhookDeliveryCreateRequestDataRelationshipsTemplateDataTypeWebhookDeliveries
		out, err := c.WebhookDeliveriesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent delivery %s again as %s", m.DeliveryID.ValueString(), out.JSON201.Data.Id), nil
	})
