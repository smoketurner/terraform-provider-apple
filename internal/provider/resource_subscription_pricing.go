package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Subscription prices, plan availabilities and the billing grace period.

var (
	planTypes      = enum(gen.SubscriptionPlanTypeUPFRONT, gen.SubscriptionPlanTypeMONTHLY)
	graceDurations = enum(gen.SubscriptionGracePeriodDurationTHREEDAYS, gen.SubscriptionGracePeriodDurationSIXTEENDAYS,
		gen.SubscriptionGracePeriodDurationTWENTYEIGHTDAYS)
	graceRenewalTypes = enum(gen.SubscriptionGracePeriodUpdateRequestDataAttributesRenewalTypeALLRENEWALS,
		gen.SubscriptionGracePeriodUpdateRequestDataAttributesRenewalTypePAIDTOPAIDONLY)
)

const planTypeDoc = "`UPFRONT` for the whole period at once, `MONTHLY` for a yearly plan billed monthly"

// ---- apple_subscription_price ----

var (
	_ resource.ResourceWithConfigure   = &subscriptionPriceResource{}
	_ resource.ResourceWithImportState = &subscriptionPriceResource{}
	_ resource.ResourceWithIdentity    = &subscriptionPriceResource{}
)

type subscriptionPriceResource struct {
	resourceIdentity
	client *asc.Client
}

type subscriptionPriceModel struct {
	SubscriptionID       types.String `tfsdk:"subscription_id"`
	PricePointID         types.String `tfsdk:"price_point_id"`
	StartDate            types.String `tfsdk:"start_date"`
	PreserveCurrentPrice types.Bool   `tfsdk:"preserve_current_price"`
	PlanType             types.String `tfsdk:"plan_type"`
	ID                   types.String `tfsdk:"id"`
	Territory            types.String `tfsdk:"territory"`
}

// A price can't be read by ID, so the import ID has the subscription.
func newSubscriptionPriceResource() resource.Resource {
	return &subscriptionPriceResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "subscription_id", description: "App Store Connect ID of the subscription.", state: "subscription_id"},
		{name: "id", description: "App Store Connect ID of the price.", state: "id"},
	}}}
}

func (r *subscriptionPriceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_price"
}

func replacedOptionalString(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Validators:          validators,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

func (r *subscriptionPriceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a subscription's price in one territory, from a date. Each territory needs a price; " +
			"a dated price schedules a change. Find price points with `data.apple_subscription_price_points`. Prices " +
			"can't be changed, so changing any argument adds a new price. Destroying the resource deletes a price that " +
			"hasn't started; a price in effect stays until another replaces it. Import it with `<subscription_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"subscription_id": replacedString("App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`."),
			"price_point_id":  replacedString("ID of the price point, which sets the territory and price."),
			"start_date":      replacedOptionalString("First day of the price, e.g. `2026-12-01`. Unset means now.", dateValidator),
			"preserve_current_price": schema.BoolAttribute{
				MarkdownDescription: "For a price increase, whether existing subscribers keep their current price. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"plan_type": schema.StringAttribute{
				MarkdownDescription: "Plan the price is for: " + planTypeDoc + ". Defaults to `UPFRONT`.",
				Optional:            true,
				Computed:            true,
				Validators:          []validator.String{oneOf(planTypes)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"id":        idAttribute("App Store Connect ID of the price."),
			"territory": computedString("Territory of the price, e.g. `USA`."),
		},
	}
}

func (r *subscriptionPriceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *subscriptionPriceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *subscriptionPriceModel) apply(p *gen.SubscriptionPrice) {
	a := asc.Value(p.Attributes)
	m.ID, m.StartDate, m.PlanType = types.StringValue(p.Id), dateValue(a.StartDate), stringValue(a.PlanType)
	m.PreserveCurrentPrice = types.BoolValue(asc.Value(a.Preserved))
	if rel := p.Relationships; rel != nil {
		if rel.Territory != nil && rel.Territory.Data != nil {
			m.Territory = types.StringValue(rel.Territory.Data.Id)
		}
		if rel.SubscriptionPricePoint != nil && rel.SubscriptionPricePoint.Data != nil {
			m.PricePointID = types.StringValue(rel.SubscriptionPricePoint.Data.Id)
		}
	}
}

func (r *subscriptionPriceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionPriceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.SubscriptionPriceCreateRequest
	body.Data.Type = gen.SubscriptionPriceCreateRequestDataTypeSubscriptionPrices
	a := asc.Alloc(&body.Data.Attributes)
	a.StartDate, a.PreserveCurrentPrice = parseDate(plan.StartDate), new(plan.PreserveCurrentPrice.ValueBool())
	if v := knownString(plan.PlanType); v != nil {
		a.PlanType = new(gen.SubscriptionPlanType(*v))
	}
	rel := &body.Data.Relationships
	rel.Subscription.Data.Id, rel.Subscription.Data.Type = plan.SubscriptionID.ValueString(), gen.SubscriptionPriceCreateRequestDataRelationshipsSubscriptionDataTypeSubscriptions
	rel.SubscriptionPricePoint.Data.Id = plan.PricePointID.ValueString()
	rel.SubscriptionPricePoint.Data.Type = gen.SubscriptionPriceCreateRequestDataRelationshipsSubscriptionPricePointDataTypeSubscriptionPricePoints
	out, err := r.client.SubscriptionPricesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "add subscription price", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subscriptionPriceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state subscriptionPriceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionsPricesGetToManyRelatedWithResponse(ctx, state.SubscriptionID.ValueString(), &gen.SubscriptionsPricesGetToManyRelatedParams{
		Include: &[]gen.SubscriptionsPricesGetToManyRelatedParamsInclude{gen.SubscriptionsPricesGetToManyRelatedParamsIncludeSubscriptionPricePoint, gen.SubscriptionsPricesGetToManyRelatedParamsIncludeTerritory},
		Limit:   new(listAll),
	})
	var all []gen.SubscriptionPrice
	if err = asc.Err(out, err); err == nil {
		all, err = asc.All(ctx, r.client, out.JSON200,
			func(p *gen.SubscriptionPricesResponse) []gen.SubscriptionPrice { return p.Data },
			func(p *gen.SubscriptionPricesResponse) *string { return p.Links.Next })
	}
	if err == nil {
		err = errNoMatch
		for i := range all {
			if all[i].Id == state.ID.ValueString() {
				state.apply(&all[i])
				err = nil
			}
		}
	}
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read subscription price", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument requires replacement.
func (r *subscriptionPriceResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

// Delete deletes a price that hasn't started, and leaves one in effect.
func (r *subscriptionPriceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state subscriptionPriceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionPricesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete subscription price", err)
	}
}

// ---- apple_subscription_plan_availability ----

var (
	_ resource.ResourceWithConfigure   = &planAvailabilityResource{}
	_ resource.ResourceWithImportState = &planAvailabilityResource{}
	_ resource.ResourceWithIdentity    = &planAvailabilityResource{}
)

type planAvailabilityResource struct {
	resourceIdentity
	client *asc.Client
}

type planAvailabilityModel struct {
	SubscriptionID            types.String `tfsdk:"subscription_id"`
	PlanType                  types.String `tfsdk:"plan_type"`
	AvailableInNewTerritories types.Bool   `tfsdk:"available_in_new_territories"`
	Territories               types.Set    `tfsdk:"territories"`
	ID                        types.String `tfsdk:"id"`
}

func newPlanAvailabilityResource() resource.Resource {
	return &planAvailabilityResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "subscription_id", description: "App Store Connect ID of the subscription.", state: "subscription_id"},
		{name: "plan_type", description: "Plan type, e.g. `UPFRONT`.", state: "plan_type"},
	}}}
}

func (r *planAvailabilityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_plan_availability"
}

func (r *planAvailabilityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the territories where a subscription plan is sold. Creating the resource for a plan " +
			"that already has an availability manages it. App Store Connect can't delete an availability, so " +
			"destroying the resource leaves it. Import it with `<subscription_id>/<plan_type>`.",
		Attributes: map[string]schema.Attribute{
			"subscription_id": replacedString("App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`."),
			"plan_type":       replacedString("Plan: "+planTypeDoc+".", oneOf(planTypes)),
			"available_in_new_territories": schema.BoolAttribute{
				MarkdownDescription: "Whether the plan becomes available in territories the App Store adds later.",
				Required:            true,
			},
			"territories": schema.SetAttribute{
				MarkdownDescription: "Territories where the plan is sold, e.g. `USA`; see `data.apple_territories`.",
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []validator.Set{setvalidator.ValueStringsAre(territoryValidator)},
			},
			"id": idAttribute("App Store Connect ID of the plan availability."),
		},
	}
}

func (r *planAvailabilityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *planAvailabilityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *planAvailabilityResource) find(ctx context.Context, subID, planType string) (*gen.SubscriptionPlanAvailability, error) {
	out, err := r.client.SubscriptionsPlanAvailabilitiesGetToManyRelatedWithResponse(ctx, subID, &gen.SubscriptionsPlanAvailabilitiesGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	for i := range out.JSON200.Data {
		if string(asc.Value(asc.Value(out.JSON200.Data[i].Attributes).PlanType)) == planType {
			return &out.JSON200.Data[i], nil
		}
	}
	return nil, errNoMatch
}

func (r *planAvailabilityResource) write(ctx context.Context, m *planAvailabilityModel, diags *diag.Diagnostics) error {
	territories := setStrings(ctx, m.Territories, diags)
	existing, err := r.find(ctx, m.SubscriptionID.ValueString(), m.PlanType.ValueString())
	switch {
	case errors.Is(err, errNoMatch):
		var body gen.SubscriptionPlanAvailabilityCreateRequest
		body.Data.Type = gen.SubscriptionPlanAvailabilityCreateRequestDataTypeSubscriptionPlanAvailabilities
		body.Data.Attributes.PlanType = gen.SubscriptionPlanType(m.PlanType.ValueString())
		body.Data.Attributes.AvailableInNewTerritories = new(m.AvailableInNewTerritories.ValueBool())
		body.Data.Relationships.Subscription.Data.Id = m.SubscriptionID.ValueString()
		body.Data.Relationships.Subscription.Data.Type = gen.SubscriptionPlanAvailabilityCreateRequestDataRelationshipsSubscriptionDataTypeSubscriptions
		// The API needs an array, even an empty one.
		data := &body.Data.Relationships.AvailableTerritories.Data
		asc.Push(data)
		*data = (*data)[:0]
		for _, t := range territories {
			e := asc.Push(data)
			e.Id, e.Type = t, gen.SubscriptionPlanAvailabilityCreateRequestDataRelationshipsAvailableTerritoriesDataTypeTerritories
		}
		out, err := r.client.SubscriptionPlanAvailabilitiesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		m.ID = types.StringValue(out.JSON201.Data.Id)
		return nil
	case err != nil:
		return err
	}
	var body gen.SubscriptionPlanAvailabilityUpdateRequest
	body.Data.Type, body.Data.Id = gen.SubscriptionPlanAvailabilityUpdateRequestDataTypeSubscriptionPlanAvailabilities, existing.Id
	asc.Alloc(&body.Data.Attributes).AvailableInNewTerritories = new(m.AvailableInNewTerritories.ValueBool())
	out, err := r.client.SubscriptionPlanAvailabilitiesUpdateInstanceWithResponse(ctx, existing.Id, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	var links gen.SubscriptionPlanAvailabilityAvailableTerritoriesLinkagesRequest
	asc.Push(&links.Data)
	links.Data = links.Data[:0]
	for _, t := range territories {
		e := asc.Push(&links.Data)
		e.Id, e.Type = t, gen.SubscriptionPlanAvailabilityAvailableTerritoriesLinkagesRequestDataTypeTerritories
	}
	replaced, err := r.client.SubscriptionPlanAvailabilitiesAvailableTerritoriesReplaceToManyRelationshipWithResponse(ctx, existing.Id, links)
	if err := asc.Err(replaced, err); err != nil {
		return err
	}
	m.ID = types.StringValue(existing.Id)
	return nil
}

func (r *planAvailabilityResource) read(ctx context.Context, m *planAvailabilityModel) error {
	p, err := r.find(ctx, m.SubscriptionID.ValueString(), m.PlanType.ValueString())
	if err != nil {
		return err
	}
	out, err := r.client.SubscriptionPlanAvailabilitiesAvailableTerritoriesGetToManyRelatedWithResponse(ctx, p.Id,
		&gen.SubscriptionPlanAvailabilitiesAvailableTerritoriesGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(out, err); err != nil {
		return err
	}
	all, err := asc.All(ctx, r.client, out.JSON200,
		func(p *gen.TerritoriesResponse) []gen.Territory { return p.Data },
		func(p *gen.TerritoriesResponse) *string { return p.Links.Next })
	if err != nil {
		return err
	}
	var territories []string
	for _, t := range all {
		territories = append(territories, t.Id)
	}
	m.ID, m.Territories = types.StringValue(p.Id), stringsValue(territories)
	m.AvailableInNewTerritories = types.BoolValue(asc.Value(asc.Value(p.Attributes).AvailableInNewTerritories))
	return nil
}

func (r *planAvailabilityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan planAvailabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "set subscription plan availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *planAvailabilityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state planAvailabilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.read(ctx, &state)
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read subscription plan availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *planAvailabilityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan planAvailabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "set subscription plan availability", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the availability, which the API cannot delete.
func (r *planAvailabilityResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- apple_subscription_grace_period ----

var (
	_ resource.ResourceWithConfigure   = &gracePeriodResource{}
	_ resource.ResourceWithImportState = &gracePeriodResource{}
	_ resource.ResourceWithIdentity    = &gracePeriodResource{}
)

type gracePeriodResource struct {
	resourceIdentity
	client *asc.Client
}

type gracePeriodModel struct {
	AppID        types.String `tfsdk:"app_id"`
	OptIn        types.Bool   `tfsdk:"opt_in"`
	SandboxOptIn types.Bool   `tfsdk:"sandbox_opt_in"`
	Duration     types.String `tfsdk:"duration"`
	RenewalType  types.String `tfsdk:"renewal_type"`
	ID           types.String `tfsdk:"id"`
}

func newGracePeriodResource() resource.Resource {
	return &gracePeriodResource{resourceIdentity: appIdentity()}
}

func (r *gracePeriodResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_grace_period"
}

func (r *gracePeriodResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an app's Billing Grace Period: how long subscribers keep access while App Store " +
			"Connect retries a failed renewal payment. Every app has one setting, so this resource manages it, and " +
			"destroying the resource leaves it. Import it by app ID.",
		Attributes: map[string]schema.Attribute{
			"app_id":         replacedString("App Store Connect ID of the app."),
			"opt_in":         schema.BoolAttribute{MarkdownDescription: "Whether the grace period is on.", Required: true},
			"sandbox_opt_in": optionalBool("Whether the grace period is on in the sandbox."),
			"duration":       optionalComputedString("Length of the grace period: "+docValues(graceDurations)+".", oneOf(graceDurations)),
			"renewal_type":   optionalComputedString("Renewals the grace period applies to: "+docValues(graceRenewalTypes)+".", oneOf(graceRenewalTypes)),
			"id":             idAttribute("App Store Connect ID of the grace period setting."),
		},
	}
}

func (r *gracePeriodResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *gracePeriodResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *gracePeriodModel) apply(g *gen.SubscriptionGracePeriod) {
	a := asc.Value(g.Attributes)
	m.ID, m.OptIn, m.SandboxOptIn = types.StringValue(g.Id), types.BoolValue(asc.Value(a.OptIn)), types.BoolValue(asc.Value(a.SandboxOptIn))
	m.Duration, m.RenewalType = stringValue(a.Duration), stringValue(a.RenewalType)
}

func (r *gracePeriodResource) write(ctx context.Context, m *gracePeriodModel) error {
	got, err := r.client.AppsSubscriptionGracePeriodGetToOneRelatedWithResponse(ctx, m.AppID.ValueString(), nil)
	if err := asc.Err(got, err); err != nil {
		return err
	}
	id := got.JSON200.Data.Id
	var body gen.SubscriptionGracePeriodUpdateRequest
	body.Data.Type, body.Data.Id = gen.SubscriptionGracePeriodUpdateRequestDataTypeSubscriptionGracePeriods, id
	a := asc.Alloc(&body.Data.Attributes)
	a.OptIn = new(m.OptIn.ValueBool())
	if !m.SandboxOptIn.IsNull() && !m.SandboxOptIn.IsUnknown() {
		a.SandboxOptIn = new(m.SandboxOptIn.ValueBool())
	}
	if v := knownString(m.Duration); v != nil {
		a.Duration = new(gen.SubscriptionGracePeriodDuration(*v))
	}
	if v := knownString(m.RenewalType); v != nil {
		a.RenewalType = new(gen.SubscriptionGracePeriodUpdateRequestDataAttributesRenewalType(*v))
	}
	out, err := r.client.SubscriptionGracePeriodsUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	m.apply(&out.JSON200.Data)
	return nil
}

func (r *gracePeriodResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gracePeriodModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set subscription grace period", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gracePeriodResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state gracePeriodModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.AppsSubscriptionGracePeriodGetToOneRelatedWithResponse(ctx, state.AppID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read subscription grace period", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gracePeriodResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan gracePeriodModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "set subscription grace period", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the setting, which every app has.
func (r *gracePeriodResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ---- price point data sources ----

func subscriptionPricePointItems(data []gen.SubscriptionPricePoint) []pricePointData {
	var items []pricePointData
	for _, p := range data {
		a := asc.Value(p.Attributes)
		item := pricePointData{ID: types.StringValue(p.Id), Territory: types.StringNull(), CustomerPrice: stringValue(a.CustomerPrice), Proceeds: stringValue(a.Proceeds)}
		if rel := p.Relationships; rel != nil && rel.Territory != nil && rel.Territory.Data != nil {
			item.Territory = types.StringValue(rel.Territory.Data.Id)
		}
		items = append(items, item)
	}
	return items
}

func allSubscriptionPricePoints(ctx context.Context, c *asc.Client, first *gen.SubscriptionPricePointsResponse) ([]gen.SubscriptionPricePoint, error) {
	return asc.All(ctx, c, first,
		func(p *gen.SubscriptionPricePointsResponse) []gen.SubscriptionPricePoint { return p.Data },
		func(p *gen.SubscriptionPricePointsResponse) *string { return p.Links.Next })
}

type subscriptionPricePointsData struct {
	SubscriptionID types.String     `tfsdk:"subscription_id"`
	Territories    types.Set        `tfsdk:"territories"`
	CustomerPrices types.Set        `tfsdk:"customer_prices"`
	PricePoints    []pricePointData `tfsdk:"price_points"`
}

var newSubscriptionPricePointsDataSource = listDataSource("subscription_price_points",
	"Lists the price points a subscription can be sold at, per territory, for `apple_subscription_price`.",
	map[string]dsschema.Attribute{
		"subscription_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the subscription.", Required: true},
		"territories":     dsFilter("Only price points in these territories, e.g. `USA`."),
		"customer_prices": dsFilter("Only price points with these customer prices, e.g. `9.99`."),
		"price_points":    listOf("The price points.", pricePointAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *subscriptionPricePointsData, diags *diag.Diagnostics) {
		out, err := c.SubscriptionsPricePointsGetToManyRelatedWithResponse(ctx, m.SubscriptionID.ValueString(), &gen.SubscriptionsPricePointsGetToManyRelatedParams{
			FilterTerritory: filterParam[string](ctx, m.Territories, diags),
			Include:         &[]gen.SubscriptionsPricePointsGetToManyRelatedParamsInclude{gen.SubscriptionsPricePointsGetToManyRelatedParamsIncludeTerritory},
			Limit:           new(listAll),
		})
		var all []gen.SubscriptionPricePoint
		if err = asc.Err(out, err); err == nil {
			all, err = allSubscriptionPricePoints(ctx, c, out.JSON200)
		}
		if err != nil {
			apiError(diags, "list subscription price points", err)
			return
		}
		filterPricePoints(ctx, &m.PricePoints, m.CustomerPrices, subscriptionPricePointItems(all), diags)
	})

var newSubscriptionPricePointEqualizationsDataSource = listDataSource("subscription_price_point_equalizations",
	"Lists the price points in other territories that are equivalent to a subscription price point, for setting "+
		"each territory's `apple_subscription_price`.",
	map[string]dsschema.Attribute{
		"price_point_id": dsschema.StringAttribute{MarkdownDescription: "ID of the price point.", Required: true},
		"territories":    dsFilter("Only equivalents in these territories, e.g. `GBR`."),
		"price_points":   listOf("The equivalent price points.", pricePointAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *equalizationsData, diags *diag.Diagnostics) {
		out, err := c.SubscriptionPricePointsEqualizationsGetToManyRelatedWithResponse(ctx, m.PricePointID.ValueString(), &gen.SubscriptionPricePointsEqualizationsGetToManyRelatedParams{
			FilterTerritory: filterParam[string](ctx, m.Territories, diags),
			Include:         &[]gen.SubscriptionPricePointsEqualizationsGetToManyRelatedParamsInclude{gen.SubscriptionPricePointsEqualizationsGetToManyRelatedParamsIncludeTerritory},
			Limit:           new(listAll),
		})
		var all []gen.SubscriptionPricePoint
		if err = asc.Err(out, err); err == nil {
			all, err = allSubscriptionPricePoints(ctx, c, out.JSON200)
		}
		if err != nil {
			apiError(diags, "list price point equalizations", err)
			return
		}
		filterPricePoints(ctx, &m.PricePoints, types.SetNull(types.StringType), subscriptionPricePointItems(all), diags)
	})
