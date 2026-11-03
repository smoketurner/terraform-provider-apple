package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Auto-renewable subscriptions and the groups that hold them. Their
// localizations and images are versioned, as in-app purchases' are; see
// versioned.go.

var subscriptionPeriods = enum(gen.SubscriptionAttributesSubscriptionPeriodONEWEEK, gen.SubscriptionAttributesSubscriptionPeriodONEMONTH,
	gen.SubscriptionAttributesSubscriptionPeriodTWOMONTHS, gen.SubscriptionAttributesSubscriptionPeriodTHREEMONTHS,
	gen.SubscriptionAttributesSubscriptionPeriodSIXMONTHS, gen.SubscriptionAttributesSubscriptionPeriodONEYEAR)

var groupMetadata = metadataVersions{
	noun: "subscription group",
	list: func(ctx context.Context, c *asc.Client, groupID string) ([]versionInfo, error) {
		out, err := c.SubscriptionGroupsVersionsGetToManyRelatedWithResponse(ctx, groupID, &gen.SubscriptionGroupsVersionsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		var vs []versionInfo
		for _, v := range out.JSON200.Data {
			a := asc.Value(v.Attributes)
			vs = append(vs, versionInfo{id: v.Id, number: asc.Value(a.Version), state: string(asc.Value(a.State))})
		}
		return vs, nil
	},
	create: func(ctx context.Context, c *asc.Client, groupID string) (string, error) {
		var body gen.SubscriptionGroupVersionCreateRequest
		body.Data.Type = gen.SubscriptionGroupVersionCreateRequestDataTypeSubscriptionGroupVersions
		body.Data.Relationships.SubscriptionGroup.Data.Id = groupID
		body.Data.Relationships.SubscriptionGroup.Data.Type = gen.SubscriptionGroupVersionCreateRequestDataRelationshipsSubscriptionGroupDataTypeSubscriptionGroups
		out, err := c.SubscriptionGroupVersionsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

var subscriptionMetadata = metadataVersions{
	noun: "subscription",
	list: func(ctx context.Context, c *asc.Client, subID string) ([]versionInfo, error) {
		out, err := c.SubscriptionsVersionsGetToManyRelatedWithResponse(ctx, subID, &gen.SubscriptionsVersionsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		var vs []versionInfo
		for _, v := range out.JSON200.Data {
			a := asc.Value(v.Attributes)
			vs = append(vs, versionInfo{id: v.Id, number: asc.Value(a.Version), state: string(asc.Value(a.State))})
		}
		return vs, nil
	},
	create: func(ctx context.Context, c *asc.Client, subID string) (string, error) {
		var body gen.SubscriptionVersionCreateRequest
		body.Data.Type = gen.SubscriptionVersionCreateRequestDataTypeSubscriptionVersions
		body.Data.Relationships.Subscription.Data.Id = subID
		body.Data.Relationships.Subscription.Data.Type = gen.SubscriptionVersionCreateRequestDataRelationshipsSubscriptionDataTypeSubscriptions
		out, err := c.SubscriptionVersionsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
}

// ---- apple_subscription_group ----

var (
	_ resource.ResourceWithConfigure   = &subscriptionGroupResource{}
	_ resource.ResourceWithImportState = &subscriptionGroupResource{}
	_ resource.ResourceWithIdentity    = &subscriptionGroupResource{}
)

type subscriptionGroupResource struct {
	resourceIdentity
	client *asc.Client
}

type subscriptionGroupModel struct {
	AppID         types.String `tfsdk:"app_id"`
	ReferenceName types.String `tfsdk:"reference_name"`
	ID            types.String `tfsdk:"id"`
}

// The API doesn't link a group to its app, so the import ID has both.
func newSubscriptionGroupResource() resource.Resource {
	return &subscriptionGroupResource{resourceIdentity: resourceIdentity{attrs: []identityAttribute{
		{name: "app_id", description: "App Store Connect ID of the app.", state: "app_id"},
		{name: "id", description: "App Store Connect ID of the subscription group.", state: "id"},
	}}}
}

func (r *subscriptionGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_group"
}

func (r *subscriptionGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a group of auto-renewable subscriptions, of which a customer can have one at a " +
			"time. Set its customer-facing name with `apple_subscription_group_localization`. Destroying the resource " +
			"deletes a group without subscriptions that was never approved. Import it with `<app_id>/<id>`.",
		Attributes: map[string]schema.Attribute{
			"app_id":         replacedString("App Store Connect ID of the app."),
			"reference_name": schema.StringAttribute{MarkdownDescription: "Reference name, seen only in App Store Connect; up to 64 characters.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 64)}},
			"id":             idAttribute("App Store Connect ID of the subscription group."),
		},
	}
}

func (r *subscriptionGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *subscriptionGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *subscriptionGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.SubscriptionGroupCreateRequest
	body.Data.Type = gen.SubscriptionGroupCreateRequestDataTypeSubscriptionGroups
	body.Data.Attributes.ReferenceName = plan.ReferenceName.ValueString()
	body.Data.Relationships.App.Data.Id = plan.AppID.ValueString()
	body.Data.Relationships.App.Data.Type = gen.SubscriptionGroupCreateRequestDataRelationshipsAppDataTypeApps
	out, err := r.client.SubscriptionGroupsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create subscription group", err)
		return
	}
	plan.ID = types.StringValue(out.JSON201.Data.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subscriptionGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state subscriptionGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionGroupsGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read subscription group", err)
		return
	}
	state.ReferenceName = stringValue(asc.Value(out.JSON200.Data.Attributes).ReferenceName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *subscriptionGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.SubscriptionGroupUpdateRequest
	body.Data.Type, body.Data.Id = gen.SubscriptionGroupUpdateRequestDataTypeSubscriptionGroups, plan.ID.ValueString()
	asc.Alloc(&body.Data.Attributes).ReferenceName = new(plan.ReferenceName.ValueString())
	out, err := r.client.SubscriptionGroupsUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update subscription group", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes a group without subscriptions that was never approved,
// and otherwise leaves it.
func (r *subscriptionGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state subscriptionGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionGroupsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete subscription group", err)
	}
}

// ---- apple_subscription_group_localization ----

func groupLocalizationValues(l gen.SubscriptionGroupLocalizationV2) localization {
	a := asc.Value(l.Attributes)
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: map[string]*string{"name": a.Name, "customAppName": a.CustomAppName}}
}

var groupLocalizationAPI = localizationAPI{
	typeName: "subscription_group_localization",
	description: "Sets a subscription group's display name, and optionally the app name shown with it, for one " +
		"locale.",
	parent:     "subscription_group_id",
	parentDesc: "App Store Connect ID of the subscription group, e.g. `apple_subscription_group.example.id`.",
	versions:   groupMetadata,
	fields: []localizationField{
		{attr: "name", json: "name", desc: "Display name of the group, 2 to 75 characters.", required: true, min: 2, max: 75},
		{attr: "custom_app_name", json: "customAppName", desc: "App name shown with the group, up to 30 characters.", max: 30},
	},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.SubscriptionGroupVersionsLocalizationsGetToManyRelatedWithResponse(ctx, versionID, &gen.SubscriptionGroupVersionsLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		var ls []localization
		for _, l := range out.JSON200.Data {
			ls = append(ls, groupLocalizationValues(l))
		}
		return ls, nil
	},
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		var body gen.SubscriptionGroupLocalizationV2CreateRequest
		body.Data.Type = gen.SubscriptionGroupLocalizationV2CreateRequestDataTypeSubscriptionGroupLocalizations
		a := &body.Data.Attributes
		a.Locale, a.Name, a.CustomAppName = locale, asc.Value(values["name"]), values["customAppName"]
		body.Data.Relationships.Version.Data.Id = versionID
		body.Data.Relationships.Version.Data.Type = gen.SubscriptionGroupLocalizationV2CreateRequestDataRelationshipsVersionDataTypeSubscriptionGroupVersions
		out, err := c.SubscriptionGroupLocalizationsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		l := groupLocalizationValues(out.JSON201.Data)
		return &l, nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		var body gen.SubscriptionGroupLocalizationV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.SubscriptionGroupLocalizationV2UpdateRequestDataTypeSubscriptionGroupLocalizations, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Name, a.CustomAppName = values["name"], clearable(values["customAppName"])
		out, err := c.SubscriptionGroupLocalizationsV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		l := groupLocalizationValues(out.JSON200.Data)
		return &l, nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.SubscriptionGroupLocalizationsV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var newGroupLocalizationResource = newVersionedLocalizationResource(groupLocalizationAPI)

// ---- apple_subscription ----

var (
	_ resource.ResourceWithConfigure   = &subscriptionResource{}
	_ resource.ResourceWithImportState = &subscriptionResource{}
	_ resource.ResourceWithIdentity    = &subscriptionResource{}
)

type subscriptionResource struct {
	resourceIdentity
	client *asc.Client
}

type subscriptionModel struct {
	GroupID        types.String `tfsdk:"subscription_group_id"`
	ProductID      types.String `tfsdk:"product_id"`
	Name           types.String `tfsdk:"name"`
	Period         types.String `tfsdk:"period"`
	GroupLevel     types.Int64  `tfsdk:"group_level"`
	ReviewNote     types.String `tfsdk:"review_note"`
	FamilySharable types.Bool   `tfsdk:"family_sharable"`
	ID             types.String `tfsdk:"id"`
	State          types.String `tfsdk:"state"`
}

func newSubscriptionResource() resource.Resource {
	return &subscriptionResource{resourceIdentity: resourceIdentity{attrs: idIdentity("App Store Connect ID of the subscription.")}}
}

func (r *subscriptionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription"
}

func (r *subscriptionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an auto-renewable subscription in a subscription group. Set its customer-facing " +
			"names with `apple_subscription_localization`, and submit it with `apple_submit_subscription`. Product IDs " +
			"can't be reused, even after deletion. Destroying the resource deletes a subscription that was never " +
			"approved; an approved one stays. Import it by ID.",
		Attributes: map[string]schema.Attribute{
			"subscription_group_id": replacedString("App Store Connect ID of the subscription group, e.g. `apple_subscription_group.example.id`."),
			"product_id":            replacedString("Product ID the app uses in StoreKit, e.g. `com.example.pro.monthly`. It can't be reused.", stringvalidator.LengthBetween(1, 100)),
			"name":                  schema.StringAttribute{MarkdownDescription: "Reference name, seen only in App Store Connect; up to 64 characters.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 64)}},
			"period":                schema.StringAttribute{MarkdownDescription: "Length of a subscription period: " + docValues(subscriptionPeriods) + ".", Required: true, Validators: []validator.String{oneOf(subscriptionPeriods)}},
			"group_level": schema.Int64Attribute{
				MarkdownDescription: "Rank of the subscription in its group, from 1 for the highest level of service. " +
					"Customers who move to a higher level upgrade at once. Defaults to the next level.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"review_note": optionalString("Notes for App Review, up to 4,000 characters.", stringvalidator.LengthAtMost(4000)),
			"family_sharable": schema.BoolAttribute{
				MarkdownDescription: "Whether family members can use the subscription. Once on, it can't be turned off. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"id":    idAttribute("App Store Connect ID of the subscription."),
			"state": computedOnly("State of the subscription, e.g. `MISSING_METADATA`, `READY_TO_SUBMIT` or `APPROVED`."),
		},
	}
}

func (r *subscriptionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *subscriptionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *subscriptionModel) apply(s *gen.Subscription) {
	a := asc.Value(s.Attributes)
	m.ID, m.ProductID, m.Name, m.Period = types.StringValue(s.Id), stringValue(a.ProductId), stringValue(a.Name), stringValue(a.SubscriptionPeriod)
	m.ReviewNote, m.State, m.FamilySharable = stringValue(a.ReviewNote), stringValue(a.State), types.BoolValue(asc.Value(a.FamilySharable))
	m.GroupLevel = types.Int64Null()
	if a.GroupLevel != nil {
		m.GroupLevel = types.Int64Value(int64(*a.GroupLevel))
	}
	if rel := s.Relationships; rel != nil && rel.Group != nil && rel.Group.Data != nil {
		m.GroupID = types.StringValue(rel.Group.Data.Id)
	}
}

func knownInt(v types.Int64) *int {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return new(int(v.ValueInt64()))
}

func (r *subscriptionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan subscriptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.SubscriptionCreateRequest
	body.Data.Type = gen.SubscriptionCreateRequestDataTypeSubscriptions
	a := &body.Data.Attributes
	a.Name, a.ProductId, a.ReviewNote = plan.Name.ValueString(), plan.ProductID.ValueString(), knownString(plan.ReviewNote)
	a.FamilySharable, a.GroupLevel = new(plan.FamilySharable.ValueBool()), knownInt(plan.GroupLevel)
	a.SubscriptionPeriod = new(gen.SubscriptionCreateRequestDataAttributesSubscriptionPeriod(plan.Period.ValueString()))
	body.Data.Relationships.Group.Data.Id = plan.GroupID.ValueString()
	body.Data.Relationships.Group.Data.Type = gen.SubscriptionCreateRequestDataRelationshipsGroupDataTypeSubscriptionGroups
	out, err := r.client.SubscriptionsCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "create subscription", err)
		return
	}
	plan.apply(&out.JSON201.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subscriptionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionsGetInstanceWithResponse(ctx, state.ID.ValueString(), &gen.SubscriptionsGetInstanceParams{
		Include: &[]gen.SubscriptionsGetInstanceParamsInclude{gen.SubscriptionsGetInstanceParamsIncludeGroup},
	})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read subscription", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *subscriptionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan, state subscriptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.SubscriptionUpdateRequest
	body.Data.Type, body.Data.Id = gen.SubscriptionUpdateRequestDataTypeSubscriptions, state.ID.ValueString()
	a := asc.Alloc(&body.Data.Attributes)
	a.Name, a.ReviewNote = new(plan.Name.ValueString()), changedString(plan.ReviewNote, state.ReviewNote)
	a.FamilySharable, a.GroupLevel = new(plan.FamilySharable.ValueBool()), knownInt(plan.GroupLevel)
	a.SubscriptionPeriod = new(gen.SubscriptionUpdateRequestDataAttributesSubscriptionPeriod(plan.Period.ValueString()))
	out, err := r.client.SubscriptionsUpdateInstanceWithResponse(ctx, state.ID.ValueString(), body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "update subscription", err)
		return
	}
	plan.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes a subscription that was never approved, and leaves an
// approved one.
func (r *subscriptionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.SubscriptionsDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) && !asc.HasCode(err, "STATE_ERROR") {
		apiError(&resp.Diagnostics, "delete subscription", err)
	}
}

// ---- apple_subscription_localization ----

func subscriptionLocalizationValues(l gen.SubscriptionLocalizationV2) localization {
	a := asc.Value(l.Attributes)
	return localization{id: l.Id, locale: asc.Value(a.Locale), values: map[string]*string{"name": a.Name, "description": a.Description}}
}

var subscriptionLocalizationAPI = localizationAPI{
	typeName:    "subscription_localization",
	description: "Sets a subscription's display name and description for one locale.",
	parent:      "subscription_id",
	parentDesc:  "App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`.",
	versions:    subscriptionMetadata,
	fields: []localizationField{
		{attr: "name", json: "name", desc: "Display name, 2 to 35 characters.", required: true, min: 2, max: 35},
		{attr: "description", json: "description", desc: "Description, up to 55 characters.", max: 55},
	},
	list: func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error) {
		out, err := c.SubscriptionVersionsLocalizationsGetToManyRelatedWithResponse(ctx, versionID, &gen.SubscriptionVersionsLocalizationsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		var ls []localization
		for _, l := range out.JSON200.Data {
			ls = append(ls, subscriptionLocalizationValues(l))
		}
		return ls, nil
	},
	create: func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error) {
		var body gen.SubscriptionLocalizationV2CreateRequest
		body.Data.Type = gen.SubscriptionLocalizationV2CreateRequestDataTypeSubscriptionLocalizations
		a := &body.Data.Attributes
		a.Locale, a.Name, a.Description = locale, asc.Value(values["name"]), values["description"]
		body.Data.Relationships.Version.Data.Id = versionID
		body.Data.Relationships.Version.Data.Type = gen.SubscriptionLocalizationV2CreateRequestDataRelationshipsVersionDataTypeSubscriptionVersions
		out, err := c.SubscriptionLocalizationsV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		l := subscriptionLocalizationValues(out.JSON201.Data)
		return &l, nil
	},
	update: func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error) {
		var body gen.SubscriptionLocalizationV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.SubscriptionLocalizationV2UpdateRequestDataTypeSubscriptionLocalizations, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Name, a.Description = values["name"], clearable(values["description"])
		out, err := c.SubscriptionLocalizationsV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		l := subscriptionLocalizationValues(out.JSON200.Data)
		return &l, nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.SubscriptionLocalizationsV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var newSubscriptionLocalizationResource = newVersionedLocalizationResource(subscriptionLocalizationAPI)

// ---- images ----

var subscriptionImageAPI = fileAPI{
	typeName: "subscription_image",
	noun:     "subscription image",
	description: "Uploads a subscription's promotional image, 1024 by 1024 pixels, shown on the App Store. It " +
		"belongs to the version being prepared; when the current version has been reviewed, applying starts the next " +
		"one. App Store Connect doesn't report the image's checksum, so changes made outside Terraform aren't detected.",
	parent:     "subscription_id",
	parentDesc: "App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`.",
	existing: func(ctx context.Context, c *asc.Client, subID string) (string, error) {
		versionID, err := subscriptionMetadata.editable(ctx, c, subID)
		if err != nil {
			return "", err
		}
		out, err := c.SubscriptionVersionsImageGetToOneRelatedWithResponse(ctx, versionID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.SubscriptionImagesV2GetInstanceWithResponse(ctx, id, nil)
		return "", asc.Err(out, err)
	},
	reserve: func(ctx context.Context, c *asc.Client, subID, name string, size int) (string, []gen.UploadOperation, error) {
		versionID, err := subscriptionMetadata.editable(ctx, c, subID)
		if err != nil {
			return "", nil, err
		}
		var body gen.SubscriptionImageV2CreateRequest
		body.Data.Type = gen.SubscriptionImageV2CreateRequestDataTypeSubscriptionImages
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		body.Data.Relationships.Version.Data.Id = versionID
		body.Data.Relationships.Version.Data.Type = gen.SubscriptionImageV2CreateRequestDataRelationshipsVersionDataTypeSubscriptionVersions
		out, err := c.SubscriptionImagesV2CreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, _ string) (string, error) {
		var body gen.SubscriptionImageV2UpdateRequest
		body.Data.Type, body.Data.Id = gen.SubscriptionImageV2UpdateRequestDataTypeSubscriptionImages, id
		asc.Alloc(&body.Data.Attributes).Uploaded = new(true)
		out, err := c.SubscriptionImagesV2UpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.SubscriptionImagesV2DeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var subscriptionReviewScreenshotAPI = fileAPI{
	typeName:    "subscription_review_screenshot",
	noun:        "subscription review screenshot",
	description: "Uploads the screenshot App Review uses to find a subscription in the app. A subscription has one; creating this resource replaces an existing one.",
	parent:      "subscription_id",
	parentDesc:  "App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`.",
	existing: func(ctx context.Context, c *asc.Client, subID string) (string, error) {
		out, err := c.SubscriptionsAppStoreReviewScreenshotGetToOneRelatedWithResponse(ctx, subID, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON200.Data.Id, nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (string, error) {
		out, err := c.SubscriptionAppStoreReviewScreenshotsGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return asc.Value(asc.Value(out.JSON200.Data.Attributes).SourceFileChecksum), nil
	},
	reserve: func(ctx context.Context, c *asc.Client, subID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.SubscriptionAppStoreReviewScreenshotCreateRequest
		body.Data.Type = gen.SubscriptionAppStoreReviewScreenshotCreateRequestDataTypeSubscriptionAppStoreReviewScreenshots
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		body.Data.Relationships.Subscription.Data.Id = subID
		body.Data.Relationships.Subscription.Data.Type = gen.SubscriptionAppStoreReviewScreenshotCreateRequestDataRelationshipsSubscriptionDataTypeSubscriptions
		out, err := c.SubscriptionAppStoreReviewScreenshotsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.SubscriptionAppStoreReviewScreenshotUpdateRequest
		body.Data.Type, body.Data.Id = gen.SubscriptionAppStoreReviewScreenshotUpdateRequestDataTypeSubscriptionAppStoreReviewScreenshots, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.SubscriptionAppStoreReviewScreenshotsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return assetFailure(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.SubscriptionAppStoreReviewScreenshotsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
}

var (
	newSubscriptionImageResource            = newUploadedFileResource(subscriptionImageAPI)
	newSubscriptionReviewScreenshotResource = newUploadedFileResource(subscriptionReviewScreenshotAPI)
)

// ---- apple_submit_subscription ----

type submitSubscriptionModel struct {
	AppID          types.String `tfsdk:"app_id"`
	SubscriptionID types.String `tfsdk:"subscription_id"`
	Platform       types.String `tfsdk:"platform"`
	Submit         types.Bool   `tfsdk:"submit"`
}

var newSubmitSubscriptionAction = newAction("submit_subscription",
	"Submits a subscription's version being prepared to App Review, with its group's version being prepared if there "+
		"is one, by adding them to the platform's review submission that is being prepared, or a new one. An app's first "+
		"subscription must be reviewed with an App Store version: set `submit = false`, then run `apple_submit_for_review`.",
	map[string]actionschema.Attribute{
		"app_id":          actionString("App Store Connect ID of the app."),
		"subscription_id": actionString("App Store Connect ID of the subscription, e.g. `apple_subscription.example.id`."),
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
	func(ctx context.Context, c *asc.Client, m *submitSubscriptionModel) (string, error) {
		subID := m.SubscriptionID.ValueString()
		draft, _, _, err := subscriptionMetadata.find(ctx, c, subID)
		if err != nil {
			return "", err
		}
		if draft == nil {
			return "", fmt.Errorf("subscription %s has no version being prepared", subID)
		}
		sub, err := c.SubscriptionsGetInstanceWithResponse(ctx, subID, nil)
		if err := asc.Err(sub, err); err != nil {
			return "", err
		}
		var sm subscriptionModel
		sm.apply(&sub.JSON200.Data)
		groupDraft, _, _, err := groupMetadata.find(ctx, c, sm.GroupID.ValueString())
		if err != nil {
			return "", err
		}
		platform := gen.PlatformIOS
		if !m.Platform.IsNull() {
			platform = gen.Platform(m.Platform.ValueString())
		}
		submissionID, err := openSubmission(ctx, c, m.AppID.ValueString(), platform)
		if err != nil {
			return "", err
		}
		add := func(set func(*gen.ReviewSubmissionItemCreateRequest)) error {
			var item gen.ReviewSubmissionItemCreateRequest
			item.Data.Type = gen.ReviewSubmissionItemCreateRequestDataTypeReviewSubmissionItems
			item.Data.Relationships.ReviewSubmission.Data.Id = submissionID
			item.Data.Relationships.ReviewSubmission.Data.Type = gen.ReviewSubmissionItemCreateRequestDataRelationshipsReviewSubmissionDataTypeReviewSubmissions
			set(&item)
			out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
			return asc.Err(out, err)
		}
		if groupDraft != nil {
			err := add(func(item *gen.ReviewSubmissionItemCreateRequest) {
				v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.SubscriptionGroupVersion).Data)
				v.Id, v.Type = groupDraft.id, gen.ReviewSubmissionItemCreateRequestDataRelationshipsSubscriptionGroupVersionDataTypeSubscriptionGroupVersions
			})
			if err != nil {
				return "", err
			}
		}
		err = add(func(item *gen.ReviewSubmissionItemCreateRequest) {
			v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.SubscriptionVersion).Data)
			v.Id, v.Type = draft.id, gen.ReviewSubmissionItemCreateRequestDataRelationshipsSubscriptionVersionDataTypeSubscriptionVersions
		})
		if err != nil {
			return "", err
		}
		if !m.Submit.IsNull() && !m.Submit.ValueBool() {
			return "Added subscription " + subID + " to review submission " + submissionID, nil
		}
		return "Submitted subscription " + subID + " for review", updateSubmission(ctx, c, submissionID, true)
	})

// ---- data sources ----

type subscriptionGroupData struct {
	ID            types.String `tfsdk:"id"`
	ReferenceName types.String `tfsdk:"reference_name"`
}

type subscriptionGroupsData struct {
	AppID  types.String            `tfsdk:"app_id"`
	Groups []subscriptionGroupData `tfsdk:"subscription_groups"`
}

var newSubscriptionGroupsDataSource = listDataSource("subscription_groups",
	"Lists an app's subscription groups.",
	map[string]dsschema.Attribute{
		"app_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"subscription_groups": listOf("The subscription groups.", map[string]dsschema.Attribute{
			"id":             dsComputed("App Store Connect ID of the group."),
			"reference_name": dsComputed("Reference name."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *subscriptionGroupsData, diags *diag.Diagnostics) {
		out, err := c.AppsSubscriptionGroupsGetToManyRelatedWithResponse(ctx, m.AppID.ValueString(), &gen.AppsSubscriptionGroupsGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.SubscriptionGroup
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.SubscriptionGroupsResponse) []gen.SubscriptionGroup { return p.Data },
				func(p *gen.SubscriptionGroupsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list subscription groups", err)
			return
		}
		m.Groups = []subscriptionGroupData{}
		for _, g := range all {
			m.Groups = append(m.Groups, subscriptionGroupData{ID: types.StringValue(g.Id), ReferenceName: stringValue(asc.Value(g.Attributes).ReferenceName)})
		}
	})

type subscriptionData struct {
	ID        types.String `tfsdk:"id"`
	ProductID types.String `tfsdk:"product_id"`
	Name      types.String `tfsdk:"name"`
	Period    types.String `tfsdk:"period"`
	State     types.String `tfsdk:"state"`
}

type subscriptionsData struct {
	GroupID       types.String       `tfsdk:"subscription_group_id"`
	Subscriptions []subscriptionData `tfsdk:"subscriptions"`
}

var newSubscriptionsDataSource = listDataSource("subscriptions",
	"Lists the subscriptions of a subscription group.",
	map[string]dsschema.Attribute{
		"subscription_group_id": dsschema.StringAttribute{MarkdownDescription: "App Store Connect ID of the subscription group.", Required: true},
		"subscriptions": listOf("The subscriptions.", map[string]dsschema.Attribute{
			"id":         dsComputed("App Store Connect ID of the subscription."),
			"product_id": dsComputed("Product ID."),
			"name":       dsComputed("Reference name."),
			"period":     dsComputed("Subscription period."),
			"state":      dsComputed("State."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *subscriptionsData, diags *diag.Diagnostics) {
		out, err := c.SubscriptionGroupsSubscriptionsGetToManyRelatedWithResponse(ctx, m.GroupID.ValueString(), &gen.SubscriptionGroupsSubscriptionsGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.Subscription
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.SubscriptionsResponse) []gen.Subscription { return p.Data },
				func(p *gen.SubscriptionsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list subscriptions", err)
			return
		}
		m.Subscriptions = []subscriptionData{}
		for _, s := range all {
			var sm subscriptionModel
			sm.apply(&s)
			m.Subscriptions = append(m.Subscriptions, subscriptionData{ID: sm.ID, ProductID: sm.ProductID, Name: sm.Name, Period: sm.Period, State: sm.State})
		}
	})
