package provider

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Offer codes. An offer code defines an offer and its prices; customers
// redeem it with custom codes the developer names, or with batches of
// one-time-use codes App Store Connect generates. Neither can be deleted,
// only deactivated, which is permanent: destroying these resources
// deactivates them.
// (https://developer.apple.com/help/app-store-connect/manage-subscriptions/set-up-offer-codes)

var booldefaultTrue = booldefault.StaticBool(true)

// openapiDate is the generated date type.
type openapiDate = openapi_types.Date

var (
	subscriptionEligibilities = enum(gen.SubscriptionCustomerEligibilityNEW, gen.SubscriptionCustomerEligibilityEXISTING, gen.SubscriptionCustomerEligibilityEXPIRED)
	offerEligibilities        = enum(gen.SubscriptionOfferEligibilitySTACKWITHINTROOFFERS, gen.SubscriptionOfferEligibilityREPLACEINTROOFFERS)
	iapEligibilities          = enum(gen.InAppPurchaseOfferCodeCreateRequestDataAttributesCustomerEligibilitiesNONSPENDER,
		gen.InAppPurchaseOfferCodeCreateRequestDataAttributesCustomerEligibilitiesACTIVESPENDER,
		gen.InAppPurchaseOfferCodeCreateRequestDataAttributesCustomerEligibilitiesCHURNEDSPENDER)
	codeEnvironments = enum(gen.OfferCodeEnvironmentPRODUCTION, gen.OfferCodeEnvironmentSANDBOX)
)

// activeAttribute is the "active" flag of offer codes and code batches.
// Deactivating is permanent, so reactivating replaces the resource.
func activeAttribute(noun string) schema.BoolAttribute {
	return schema.BoolAttribute{
		MarkdownDescription: "Whether the " + noun + " can be redeemed. Deactivating is permanent, so setting it back to `true` creates a new " + noun + ". Defaults to `true`.",
		Optional:            true,
		Computed:            true,
		Default:             booldefaultTrue,
		PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplaceIf(reactivating, "Reactivating replaces the resource.", "")},
	}
}

func reactivating(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && !req.StateValue.ValueBool() && req.PlanValue.ValueBool()
}

func replacedSet(desc string, validators ...validator.Set) schema.SetAttribute {
	return schema.SetAttribute{
		MarkdownDescription: desc, Required: true, ElementType: types.StringType,
		Validators:    append([]validator.Set{setvalidator.SizeAtLeast(1)}, validators...),
		PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
	}
}

// patchActive sends an offer code's "active" flag, as raw JSON of type typ,
// e.g. "subscriptionOfferCodes".
func patchActive(ctx context.Context, send func(ctx context.Context, body []byte) error, typ, id string, active bool) error {
	body, err := json.Marshal(map[string]any{"data": map[string]any{"type": typ, "id": id, "attributes": map[string]any{"active": active}}})
	if err != nil {
		return err
	}
	return send(ctx, body)
}

// ---- offer code definitions ----

type subscriptionOfferCodeModel struct {
	SubscriptionID        types.String      `tfsdk:"subscription_id"`
	Name                  types.String      `tfsdk:"name"`
	CustomerEligibilities types.Set         `tfsdk:"customer_eligibilities"`
	OfferEligibility      types.String      `tfsdk:"offer_eligibility"`
	OfferMode             types.String      `tfsdk:"offer_mode"`
	Duration              types.String      `tfsdk:"duration"`
	NumberOfPeriods       types.Int64       `tfsdk:"number_of_periods"`
	AutoRenewEnabled      types.Bool        `tfsdk:"auto_renew_enabled"`
	PlanType              types.String      `tfsdk:"plan_type"`
	Prices                []offerPriceModel `tfsdk:"prices"`
	Active                types.Bool        `tfsdk:"active"`
	ID                    types.String      `tfsdk:"id"`
}

type iapOfferCodeModel struct {
	IAPID                 types.String      `tfsdk:"in_app_purchase_id"`
	Name                  types.String      `tfsdk:"name"`
	CustomerEligibilities types.Set         `tfsdk:"customer_eligibilities"`
	Prices                []offerPriceModel `tfsdk:"prices"`
	Active                types.Bool        `tfsdk:"active"`
	ID                    types.String      `tfsdk:"id"`
}

var (
	_ resource.ResourceWithConfigure   = &subscriptionOfferCodeResource{}
	_ resource.ResourceWithImportState = &subscriptionOfferCodeResource{}
	_ resource.ResourceWithIdentity    = &subscriptionOfferCodeResource{}
	_ resource.ResourceWithConfigure   = &iapOfferCodeResource{}
	_ resource.ResourceWithImportState = &iapOfferCodeResource{}
	_ resource.ResourceWithIdentity    = &iapOfferCodeResource{}
)

type subscriptionOfferCodeResource struct {
	resourceIdentity
	client *asc.Client
}

type iapOfferCodeResource struct {
	resourceIdentity
	client *asc.Client
}

func newSubscriptionOfferCodeResource() resource.Resource {
	return &subscriptionOfferCodeResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the offer code.")}}
}

// The API doesn't link an in-app purchase offer code to its purchase, so
// the import ID has both.
func newIAPOfferCodeResource() resource.Resource {
	return &iapOfferCodeResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "in_app_purchase_id", description: "App Store Connect ID of the in-app purchase.", state: "in_app_purchase_id"},
		{name: "id", description: "App Store Connect ID of the offer code.", state: "id"},
	}}}
}

func (r *subscriptionOfferCodeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_offer_code"
}

func (r *iapOfferCodeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_in_app_purchase_offer_code"
}

const offerCodeDescription = " Customers redeem it with custom codes or one-time-use codes. Only `active` changes " +
	"in place; other changes create a new offer code. App Store Connect can't delete an offer code, so destroying " +
	"the resource deactivates it."

func (r *subscriptionOfferCodeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines an offer code for a subscription: a free or discounted period for eligible " +
			"customers." + offerCodeDescription + " Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"subscription_id":        replacedString("App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`."),
			"name":                   replacedString("Reference name of the offer code.", stringvalidator.LengthBetween(1, 64)),
			"customer_eligibilities": replacedSet("Customers who can redeem the code: "+docValues(subscriptionEligibilities)+".", setvalidator.ValueStringsAre(oneOf(subscriptionEligibilities))),
			"offer_eligibility":      replacedString("How the offer combines with an introductory offer: "+docValues(offerEligibilities)+".", oneOf(offerEligibilities)),
			"offer_mode":             replacedString("Kind of offer: "+offerModeDoc+".", oneOf(offerModes)),
			"duration":               replacedString("Length of each offer period: "+docValues(offerDurations)+".", oneOf(offerDurations)),
			"number_of_periods":      replacedInt("Number of offer periods.", int64validator.AtLeast(1)),
			"auto_renew_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the subscription renews after the offer. Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefaultTrue,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"plan_type": replacedPlanType(),
			"prices":    offerPricesAttribute("Prices of a paid offer, one per territory. A free trial has none.", setplanmodifier.RequiresReplace()),
			"active":    activeAttribute("offer code"),
			"id":        idAttribute("App Store Connect ID of the offer code."),
		},
	}
}

func (r *iapOfferCodeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	prices := offerPricesAttribute("Price of the offer in each territory where it's redeemable; see `data.apple_in_app_purchase_price_points`.",
		setplanmodifier.RequiresReplace())
	prices.Optional, prices.Required = false, true
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines an offer code for an in-app purchase: a discounted price for eligible customers." +
			offerCodeDescription + " Import it with `<in_app_purchase_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"in_app_purchase_id":     replacedString("App Store Connect ID of the in-app purchase, e.g. `apple_in_app_purchase.example.id`."),
			"name":                   replacedString("Reference name of the offer code.", stringvalidator.LengthBetween(1, 64)),
			"customer_eligibilities": replacedSet("Customers who can redeem the code: "+docValues(iapEligibilities)+".", setvalidator.ValueStringsAre(oneOf(iapEligibilities))),
			"prices":                 prices,
			"active":                 activeAttribute("offer code"),
			"id":                     idAttribute("App Store Connect ID of the offer code."),
		},
	}
}

func (r *subscriptionOfferCodeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *iapOfferCodeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *subscriptionOfferCodeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *iapOfferCodeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// inlineOfferPrices builds offer code prices of type typ whose price point
// relationship is named pointRel.
func inlineOfferPrices(prices []offerPriceModel, typ, pointRel, pointType string) (refs, included []map[string]any) {
	refs, included = []map[string]any{}, []map[string]any{}
	for i, p := range prices {
		local := fmt.Sprintf("${price-%d}", i)
		refs = append(refs, map[string]any{"type": typ, "id": local})
		included = append(included, map[string]any{"type": typ, "id": local, "relationships": map[string]any{
			"territory": map[string]any{"data": map[string]string{"type": "territories", "id": p.Territory.ValueString()}},
			pointRel:    map[string]any{"data": map[string]string{"type": pointType, "id": p.PricePointID.ValueString()}},
		}})
	}
	return refs, included
}

func (r *subscriptionOfferCodeResource) read(ctx context.Context, m *subscriptionOfferCodeModel) error {
	out, err := r.client.SubscriptionOfferCodesGetInstanceWithResponse(ctx, m.ID.ValueString(), nil)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	o := out.JSON200.Data
	a := asc.Value(o.Attributes)
	var eligibilities []string
	for _, e := range asc.Value(a.CustomerEligibilities) {
		eligibilities = append(eligibilities, string(e))
	}
	m.Name, m.CustomerEligibilities, m.OfferEligibility = stringValue(a.Name), stringsValue(eligibilities), stringValue(a.OfferEligibility)
	m.OfferMode, m.Duration, m.NumberOfPeriods = stringValue(a.OfferMode), stringValue(a.Duration), intValue(a.NumberOfPeriods)
	m.AutoRenewEnabled, m.PlanType, m.Active = types.BoolValue(asc.Value(a.AutoRenewEnabled)), stringValue(a.TargetSubscriptionPlanType), types.BoolValue(asc.Value(a.Active))
	if rel := o.Relationships; rel != nil && rel.Subscription != nil && rel.Subscription.Data != nil {
		m.SubscriptionID = types.StringValue(rel.Subscription.Data.Id)
	}
	prices, err := r.client.SubscriptionOfferCodesPricesGetToManyRelatedWithResponse(ctx, m.ID.ValueString(), &gen.SubscriptionOfferCodesPricesGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(prices, err); err != nil {
		return err
	}
	all, err := asc.All(ctx, r.client, prices.JSON200,
		func(p *gen.SubscriptionOfferCodePricesResponse) []gen.SubscriptionOfferCodePrice { return p.Data },
		func(p *gen.SubscriptionOfferCodePricesResponse) *string { return p.Links.Next })
	if err != nil {
		return err
	}
	m.Prices = nil
	if len(all) > 0 {
		m.Prices = offerPriceModels(all,
			func(p gen.SubscriptionOfferCodePrice) string { return p.Relationships.Territory.Data.Id },
			func(p gen.SubscriptionOfferCodePrice) string { return p.Relationships.SubscriptionPricePoint.Data.Id })
	}
	return nil
}

func (r *subscriptionOfferCodeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionOfferCodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	attrs := map[string]any{
		"name": plan.Name.ValueString(), "customerEligibilities": setStrings(ctx, plan.CustomerEligibilities, &resp.Diagnostics),
		"offerEligibility": plan.OfferEligibility.ValueString(), "offerMode": plan.OfferMode.ValueString(), "duration": plan.Duration.ValueString(),
		"numberOfPeriods": plan.NumberOfPeriods.ValueInt64(), "autoRenewEnabled": plan.AutoRenewEnabled.ValueBool(),
	}
	if v := knownString(plan.PlanType); v != nil {
		attrs["targetSubscriptionPlanType"] = *v
	}
	refs, included := inlineOfferPrices(plan.Prices, "subscriptionOfferCodePrices", "subscriptionPricePoint", "subscriptionPricePoints")
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": "subscriptionOfferCodes", "attributes": attrs,
		"relationships": map[string]any{
			"subscription": map[string]any{"data": map[string]string{"type": "subscriptions", "id": plan.SubscriptionID.ValueString()}},
			"prices":       map[string]any{"data": refs},
		},
	}, "included": included})
	if err == nil {
		out, e := r.client.SubscriptionOfferCodesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.ID = types.StringValue(out.JSON201.Data.Id)
			err = r.setActive(ctx, plan.ID.ValueString(), plan.Active.ValueBool())
		}
	}
	if err == nil {
		err = r.read(ctx, &plan)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create subscription offer code", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subscriptionOfferCodeResource) setActive(ctx context.Context, id string, active bool) error {
	if active {
		return nil
	}
	return patchActive(ctx, func(ctx context.Context, body []byte) error {
		out, err := r.client.SubscriptionOfferCodesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
		return asc.Err(out, err)
	}, "subscriptionOfferCodes", id, false)
}

func (r *subscriptionOfferCodeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state subscriptionOfferCodeModel
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
		apiError(&resp.Diagnostics, "read subscription offer code", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *subscriptionOfferCodeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionOfferCodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.setActive(ctx, plan.ID.ValueString(), plan.Active.ValueBool())
	if err == nil {
		err = r.read(ctx, &plan)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update subscription offer code", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deactivates the offer code, which the API can't delete.
func (r *subscriptionOfferCodeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state subscriptionOfferCodeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !state.Active.ValueBool() {
		return
	}
	if err := r.setActive(ctx, state.ID.ValueString(), false); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "deactivate subscription offer code", err)
	}
}

func (r *iapOfferCodeResource) read(ctx context.Context, m *iapOfferCodeModel) error {
	out, err := r.client.InAppPurchaseOfferCodesGetInstanceWithResponse(ctx, m.ID.ValueString(), nil)
	if err := asc.Err(out, err); err != nil {
		return err
	}
	a := asc.Value(out.JSON200.Data.Attributes)
	var eligibilities []string
	for _, e := range asc.Value(a.CustomerEligibilities) {
		eligibilities = append(eligibilities, string(e))
	}
	m.Name, m.CustomerEligibilities, m.Active = stringValue(a.Name), stringsValue(eligibilities), types.BoolValue(asc.Value(a.Active))
	prices, err := r.client.InAppPurchaseOfferCodesPricesGetToManyRelatedWithResponse(ctx, m.ID.ValueString(), &gen.InAppPurchaseOfferCodesPricesGetToManyRelatedParams{Limit: new(listAll)})
	if err := asc.Err(prices, err); err != nil {
		return err
	}
	all, err := asc.All(ctx, r.client, prices.JSON200,
		func(p *gen.InAppPurchaseOfferPricesResponse) []gen.InAppPurchaseOfferPrice { return p.Data },
		func(p *gen.InAppPurchaseOfferPricesResponse) *string { return p.Links.Next })
	if err != nil {
		return err
	}
	m.Prices = offerPriceModels(all,
		func(p gen.InAppPurchaseOfferPrice) string { return p.Relationships.Territory.Data.Id },
		func(p gen.InAppPurchaseOfferPrice) string { return p.Relationships.PricePoint.Data.Id })
	return nil
}

func (r *iapOfferCodeResource) setActive(ctx context.Context, id string, active bool) error {
	if active {
		return nil
	}
	return patchActive(ctx, func(ctx context.Context, body []byte) error {
		out, err := r.client.InAppPurchaseOfferCodesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", bytes.NewReader(body))
		return asc.Err(out, err)
	}, "inAppPurchaseOfferCodes", id, false)
}

func (r *iapOfferCodeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan iapOfferCodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	refs, included := inlineOfferPrices(plan.Prices, "inAppPurchaseOfferPrices", "pricePoint", "inAppPurchasePricePoints")
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type":       "inAppPurchaseOfferCodes",
		"attributes": map[string]any{"name": plan.Name.ValueString(), "customerEligibilities": setStrings(ctx, plan.CustomerEligibilities, &resp.Diagnostics)},
		"relationships": map[string]any{
			"inAppPurchase": map[string]any{"data": map[string]string{"type": "inAppPurchases", "id": plan.IAPID.ValueString()}},
			"prices":        map[string]any{"data": refs},
		},
	}, "included": included})
	if err == nil {
		out, e := r.client.InAppPurchaseOfferCodesCreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
		if err = asc.Err(out, e); err == nil {
			plan.ID = types.StringValue(out.JSON201.Data.Id)
			err = r.setActive(ctx, plan.ID.ValueString(), plan.Active.ValueBool())
		}
	}
	if err == nil {
		err = r.read(ctx, &plan)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create in-app purchase offer code", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iapOfferCodeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state iapOfferCodeModel
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
		apiError(&resp.Diagnostics, "read in-app purchase offer code", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *iapOfferCodeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan iapOfferCodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.setActive(ctx, plan.ID.ValueString(), plan.Active.ValueBool())
	if err == nil {
		err = r.read(ctx, &plan)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update in-app purchase offer code", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deactivates the offer code, which the API can't delete.
func (r *iapOfferCodeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state iapOfferCodeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !state.Active.ValueBool() {
		return
	}
	if err := r.setActive(ctx, state.ID.ValueString(), false); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "deactivate in-app purchase offer code", err)
	}
}

// ---- code batches: custom codes and one-time-use codes ----

// batchInfo is a custom code or one-time-use batch read from the API.
type batchInfo struct {
	id, customCode, expiration, environment, created string
	number                                           int
	active                                           bool
}

// batchAPI is the part of the API one kind of batch needs.
type batchAPI struct {
	typeName    string
	noun        string // e.g. "custom code"
	description string
	jsonType    string // e.g. "subscriptionOfferCodeCustomCodes"
	offerType   string // e.g. "subscriptionOfferCodes"
	oneTime     bool
	create      func(ctx context.Context, c *asc.Client, body []byte) (*batchInfo, error)
	get         func(ctx context.Context, c *asc.Client, id string) (*batchInfo, error)
	update      func(ctx context.Context, c *asc.Client, id string, body []byte) (*batchInfo, error)
}

// batchCommon holds the attributes of both kinds of batch; the models add
// their own.
type batchCommon struct {
	OfferCodeID    types.String `tfsdk:"offer_code_id"`
	NumberOfCodes  types.Int64  `tfsdk:"number_of_codes"`
	ExpirationDate types.String `tfsdk:"expiration_date"`
	Active         types.Bool   `tfsdk:"active"`
	ID             types.String `tfsdk:"id"`
	CreatedDate    types.String `tfsdk:"created_date"`
}

type customCodeModel struct {
	batchCommon
	CustomCode types.String `tfsdk:"custom_code"`
}

type oneTimeCodesModel struct {
	batchCommon
	Environment types.String `tfsdk:"environment"`
}

// batchState is a batch model.
type batchState interface {
	common() *batchCommon
	// attributes adds the kind's attributes to a create request.
	attributes(attrs map[string]any)
	// apply records the kind's attributes of b.
	apply(b *batchInfo)
}

func (m *customCodeModel) common() *batchCommon { return &m.batchCommon }

func (m *customCodeModel) attributes(attrs map[string]any) {
	attrs["customCode"] = m.CustomCode.ValueString()
}

func (m *customCodeModel) apply(b *batchInfo) { m.CustomCode = types.StringValue(b.customCode) }

func (m *oneTimeCodesModel) common() *batchCommon { return &m.batchCommon }

func (m *oneTimeCodesModel) attributes(attrs map[string]any) {
	if v := knownString(m.Environment); v != nil {
		attrs["environment"] = *v
	}
}

func (m *oneTimeCodesModel) apply(b *batchInfo) { m.Environment = types.StringValue(b.environment) }

func (c *batchCommon) apply(b *batchInfo) {
	c.ID, c.NumberOfCodes, c.Active, c.CreatedDate = types.StringValue(b.id), types.Int64Value(int64(b.number)), types.BoolValue(b.active), types.StringValue(b.created)
	c.ExpirationDate = types.StringNull()
	if b.expiration != "" {
		c.ExpirationDate = types.StringValue(b.expiration)
	}
}

// batchResource manages a custom code or a batch of one-time-use codes; M
// is its model.
type batchResource[M any, PM interface {
	*M
	batchState
}] struct {
	resourceIdentity
	api    batchAPI
	client *asc.Client
}

func newBatchResource[M any, PM interface {
	*M
	batchState
}](api batchAPI) func() resource.Resource {
	return func() resource.Resource {
		// The API doesn't link every kind of batch to its offer code, so the
		// import ID has both.
		return &batchResource[M, PM]{api: api, resourceIdentity: resourceIdentity{attrs: []identityAttribute{
			{name: "offer_code_id", description: "App Store Connect ID of the offer code.", state: "offer_code_id"},
			{name: "id", description: "App Store Connect ID of the " + api.noun + ".", state: "id"},
		}}}
	}
}

func (r *batchResource[M, PM]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *batchResource[M, PM]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"offer_code_id":   replacedString("App Store Connect ID of the offer code."),
		"number_of_codes": replacedInt("Number of redemptions the codes allow.", int64validator.AtLeast(1)),
		"active":          activeAttribute(r.api.noun),
		"id":              idAttribute("App Store Connect ID of the " + r.api.noun + "."),
		"created_date":    computedString("When the " + r.api.noun + " was created."),
	}
	if r.api.oneTime {
		attrs["expiration_date"] = replacedString("Last day the codes can be redeemed, e.g. `2026-12-31`.", dateValidator)
		attrs["environment"] = schema.StringAttribute{
			MarkdownDescription: "Where the codes work: " + docValues(codeEnvironments) + ". Defaults to `PRODUCTION`.",
			Optional:            true,
			Computed:            true,
			Validators:          []validator.String{oneOf(codeEnvironments)},
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
		}
	} else {
		attrs["expiration_date"] = replacedOptionalString("Last day the code can be redeemed, e.g. `2026-12-31`. Unset means no expiration.", dateValidator)
		attrs["custom_code"] = replacedString("Code customers enter, e.g. `SPRING26`; unique among the app's codes.", stringvalidator.LengthBetween(1, 64))
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: r.api.description + " Changing it creates a new one. App Store Connect can't delete " +
			"codes, so destroying the resource deactivates them. Import it with `<offer_code_id>/<id>`.",
		Attributes: attrs,
	}
}

func (r *batchResource[M, PM]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *batchResource[M, PM]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *batchResource[M, PM]) setActive(ctx context.Context, id string, active bool) (*batchInfo, error) {
	body, err := json.Marshal(map[string]any{"data": map[string]any{"type": r.api.jsonType, "id": id, "attributes": map[string]any{"active": active}}})
	if err != nil {
		return nil, err
	}
	return r.api.update(ctx, r.client, id, body)
}

func record(m batchState, b *batchInfo) {
	m.common().apply(b)
	m.apply(b)
}

func (r *batchResource[M, PM]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	plan := PM(new(M))
	resp.Diagnostics.Append(req.Plan.Get(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c := plan.common()
	attrs := map[string]any{"numberOfCodes": c.NumberOfCodes.ValueInt64()}
	if v := knownString(c.ExpirationDate); v != nil {
		attrs["expirationDate"] = *v
	}
	plan.attributes(attrs)
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": r.api.jsonType, "attributes": attrs,
		"relationships": map[string]any{"offerCode": map[string]any{"data": map[string]string{"type": r.api.offerType, "id": c.OfferCodeID.ValueString()}}},
	}})
	var b *batchInfo
	if err == nil {
		b, err = r.api.create(ctx, r.client, body)
	}
	if err == nil && !c.Active.ValueBool() {
		b, err = r.setActive(ctx, b.id, false)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "create "+r.api.noun, err)
		return
	}
	record(plan, b)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *batchResource[M, PM]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	state := PM(new(M))
	resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.api.get(ctx, r.client, state.common().ID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+r.api.noun, err)
		return
	}
	record(state, b)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update deactivates the codes; every other change replaces the resource.
func (r *batchResource[M, PM]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	plan := PM(new(M))
	resp.Diagnostics.Append(req.Plan.Get(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.setActive(ctx, plan.common().ID.ValueString(), plan.common().Active.ValueBool())
	if err != nil {
		apiError(&resp.Diagnostics, "update "+r.api.noun, err)
		return
	}
	record(plan, b)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete deactivates the codes, which the API can't delete.
func (r *batchResource[M, PM]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	state := PM(new(M))
	resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	if resp.Diagnostics.HasError() || !state.common().Active.ValueBool() {
		return
	}
	if _, err := r.setActive(ctx, state.common().ID.ValueString(), false); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "deactivate "+r.api.noun, err)
	}
}

func dateTimeString(d *gen.DateTime) string {
	if d == nil {
		return ""
	}
	return timeValue(d).ValueString()
}

func dateString(d *openapiDate) string {
	if d == nil {
		return ""
	}
	return dateValue(d).ValueString()
}

// batchFrom converts the four batch types, which share these attributes.
func batchFrom(id string, customCode *string, number *int, expiration *openapiDate, environment *gen.OfferCodeEnvironment, active *bool, created *gen.DateTime) *batchInfo {
	return &batchInfo{id: id, customCode: asc.Value(customCode), number: asc.Value(number), expiration: dateString(expiration),
		environment: string(asc.Value(environment)), active: asc.Value(active), created: dateTimeString(created)}
}

func reader(body []byte) *bytes.Reader { return bytes.NewReader(body) }

var subscriptionCustomCodeAPI = batchAPI{
	typeName: "subscription_offer_code_custom_code", noun: "custom code", jsonType: "subscriptionOfferCodeCustomCodes", offerType: "subscriptionOfferCodes",
	description: "Creates a custom code, such as `SPRING26`, that customers enter to redeem a subscription offer code.",
	create: func(ctx context.Context, c *asc.Client, body []byte) (*batchInfo, error) {
		out, err := c.SubscriptionOfferCodeCustomCodesCreateInstanceWithBodyWithResponse(ctx, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON201.Data, asc.Value(out.JSON201.Data.Attributes)
		return batchFrom(d.Id, a.CustomCode, a.NumberOfCodes, a.ExpirationDate, nil, a.Active, a.CreatedDate), nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (*batchInfo, error) {
		out, err := c.SubscriptionOfferCodeCustomCodesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, a.CustomCode, a.NumberOfCodes, a.ExpirationDate, nil, a.Active, a.CreatedDate), nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, body []byte) (*batchInfo, error) {
		out, err := c.SubscriptionOfferCodeCustomCodesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, a.CustomCode, a.NumberOfCodes, a.ExpirationDate, nil, a.Active, a.CreatedDate), nil
	},
}

var subscriptionOneTimeCodesAPI = batchAPI{
	typeName: "subscription_offer_code_one_time_use_codes", noun: "one-time-use code batch", oneTime: true,
	jsonType: "subscriptionOfferCodeOneTimeUseCodes", offerType: "subscriptionOfferCodes",
	description: "Generates a batch of one-time-use codes for a subscription offer code. The codes aren't kept in " +
		"state; read them with `data.apple_subscription_offer_code_one_time_use_code_values`.",
	create: func(ctx context.Context, c *asc.Client, body []byte) (*batchInfo, error) {
		out, err := c.SubscriptionOfferCodeOneTimeUseCodesCreateInstanceWithBodyWithResponse(ctx, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON201.Data, asc.Value(out.JSON201.Data.Attributes)
		return batchFrom(d.Id, nil, a.NumberOfCodes, a.ExpirationDate, a.Environment, a.Active, a.CreatedDate), nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (*batchInfo, error) {
		out, err := c.SubscriptionOfferCodeOneTimeUseCodesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, nil, a.NumberOfCodes, a.ExpirationDate, a.Environment, a.Active, a.CreatedDate), nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, body []byte) (*batchInfo, error) {
		out, err := c.SubscriptionOfferCodeOneTimeUseCodesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, nil, a.NumberOfCodes, a.ExpirationDate, a.Environment, a.Active, a.CreatedDate), nil
	},
}

var iapCustomCodeAPI = batchAPI{
	typeName: "in_app_purchase_offer_code_custom_code", noun: "custom code", jsonType: "inAppPurchaseOfferCodeCustomCodes", offerType: "inAppPurchaseOfferCodes",
	description: "Creates a custom code, such as `SPRING26`, that customers enter to redeem an in-app purchase offer code.",
	create: func(ctx context.Context, c *asc.Client, body []byte) (*batchInfo, error) {
		out, err := c.InAppPurchaseOfferCodeCustomCodesCreateInstanceWithBodyWithResponse(ctx, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON201.Data, asc.Value(out.JSON201.Data.Attributes)
		return batchFrom(d.Id, a.CustomCode, a.NumberOfCodes, a.ExpirationDate, nil, a.Active, a.CreatedDate), nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (*batchInfo, error) {
		out, err := c.InAppPurchaseOfferCodeCustomCodesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, a.CustomCode, a.NumberOfCodes, a.ExpirationDate, nil, a.Active, a.CreatedDate), nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, body []byte) (*batchInfo, error) {
		out, err := c.InAppPurchaseOfferCodeCustomCodesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, a.CustomCode, a.NumberOfCodes, a.ExpirationDate, nil, a.Active, a.CreatedDate), nil
	},
}

var iapOneTimeCodesAPI = batchAPI{
	typeName: "in_app_purchase_offer_code_one_time_use_codes", noun: "one-time-use code batch", oneTime: true,
	jsonType: "inAppPurchaseOfferCodeOneTimeUseCodes", offerType: "inAppPurchaseOfferCodes",
	description: "Generates a batch of one-time-use codes for an in-app purchase offer code. The codes aren't kept " +
		"in state; read them with `data.apple_in_app_purchase_offer_code_one_time_use_code_values`.",
	create: func(ctx context.Context, c *asc.Client, body []byte) (*batchInfo, error) {
		out, err := c.InAppPurchaseOfferCodeOneTimeUseCodesCreateInstanceWithBodyWithResponse(ctx, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON201.Data, asc.Value(out.JSON201.Data.Attributes)
		return batchFrom(d.Id, nil, a.NumberOfCodes, a.ExpirationDate, a.Environment, a.Active, a.CreatedDate), nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (*batchInfo, error) {
		out, err := c.InAppPurchaseOfferCodeOneTimeUseCodesGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, nil, a.NumberOfCodes, a.ExpirationDate, a.Environment, a.Active, a.CreatedDate), nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, body []byte) (*batchInfo, error) {
		out, err := c.InAppPurchaseOfferCodeOneTimeUseCodesUpdateInstanceWithBodyWithResponse(ctx, id, "application/json", reader(body))
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		d, a := out.JSON200.Data, asc.Value(out.JSON200.Data.Attributes)
		return batchFrom(d.Id, nil, a.NumberOfCodes, a.ExpirationDate, a.Environment, a.Active, a.CreatedDate), nil
	},
}

var (
	_ resource.ResourceWithConfigure   = &batchResource[customCodeModel, *customCodeModel]{}
	_ resource.ResourceWithImportState = &batchResource[customCodeModel, *customCodeModel]{}
	_ resource.ResourceWithIdentity    = &batchResource[customCodeModel, *customCodeModel]{}

	newSubscriptionCustomCodeResource   = newBatchResource[customCodeModel](subscriptionCustomCodeAPI)
	newSubscriptionOneTimeCodesResource = newBatchResource[oneTimeCodesModel](subscriptionOneTimeCodesAPI)
	newIAPCustomCodeResource            = newBatchResource[customCodeModel](iapCustomCodeAPI)
	newIAPOneTimeCodesResource          = newBatchResource[oneTimeCodesModel](iapOneTimeCodesAPI)
)

// ---- one-time-use code values ----

// parseCodes reads the codes of a one-time-use batch from its CSV: the first
// field of each record, after a header row naming the column "code".
func parseCodes(data []byte) ([]string, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading the codes: %w", err)
	}
	codes := []string{}
	for i, rec := range records {
		if len(rec) == 0 || strings.TrimSpace(rec[0]) == "" {
			continue
		}
		if i == 0 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(rec[0])), "code") {
			continue
		}
		codes = append(codes, strings.TrimSpace(rec[0]))
	}
	return codes, nil
}

type codeValuesData struct {
	BatchID types.String `tfsdk:"batch_id"`
	Codes   types.List   `tfsdk:"codes"`
}

func codeValuesDataSource(typeName, noun, resourceName string, fetch func(ctx context.Context, c *asc.Client, id string) ([]byte, error)) func() datasource.DataSource {
	return listDataSource(typeName,
		"Reads the codes of a batch of one-time-use codes for "+noun+", e.g. to export them. They're sensitive, so "+
			"read them only where needed; they're also stored in the state of the configuration that reads them.",
		map[string]dsschema.Attribute{
			"batch_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the batch, e.g. `" + resourceName + ".example.id`.", Required: true},
			"codes":    dsschema.ListAttribute{MarkdownDescription: "The codes.", Computed: true, Sensitive: true, ElementType: types.StringType},
		},
		func(ctx context.Context, c *asc.Client, m *codeValuesData, diags *diag.Diagnostics) {
			data, err := fetch(ctx, c, m.BatchID.ValueString())
			var codes []string
			if err == nil {
				codes, err = parseCodes(data)
			}
			if err != nil {
				apiError(diags, "read one-time-use codes", err)
				return
			}
			m.Codes = stringsList(codes)
		})
}

var (
	newSubscriptionCodeValuesDataSource = codeValuesDataSource("subscription_offer_code_one_time_use_code_values", "a subscription offer code",
		"apple_subscription_offer_code_one_time_use_codes", func(ctx context.Context, c *asc.Client, id string) ([]byte, error) {
			out, err := c.SubscriptionOfferCodeOneTimeUseCodesValuesGetToOneRelatedWithResponse(ctx, id)
			if err := asc.Err(out, err); err != nil {
				return nil, err
			}
			return out.Body, nil
		})
	newIAPCodeValuesDataSource = codeValuesDataSource("in_app_purchase_offer_code_one_time_use_code_values", "an in-app purchase offer code",
		"apple_in_app_purchase_offer_code_one_time_use_codes", func(ctx context.Context, c *asc.Client, id string) ([]byte, error) {
			out, err := c.InAppPurchaseOfferCodeOneTimeUseCodesValuesGetToOneRelatedWithResponse(ctx, id)
			if err := asc.Err(out, err); err != nil {
				return nil, err
			}
			return out.Body, nil
		})
)
