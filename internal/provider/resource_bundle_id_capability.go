package provider

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// The values of the spec's CapabilityType, CapabilitySetting.key and
// CapabilityOption.key enums. TestCapabilityEnums checks them against the
// generated types.
var (
	capabilityTypes = []string{
		"ACCESS_WIFI_INFORMATION", "APPLE_ID_AUTH", "APPLE_PAY", "APP_GROUPS", "ASSOCIATED_DOMAINS",
		"AUTOFILL_CREDENTIAL_PROVIDER", "CLASSKIT", "COREMEDIA_HLS_LOW_LATENCY", "DATA_PROTECTION", "GAME_CENTER",
		"HEALTHKIT", "HOMEKIT", "HOT_SPOT", "ICLOUD", "INTER_APP_AUDIO", "IN_APP_PURCHASE", "MAPS", "MULTIPATH",
		"NETWORK_CUSTOM_PROTOCOL", "NETWORK_EXTENSIONS", "NFC_TAG_READING", "PERSONAL_VPN", "PUSH_NOTIFICATIONS",
		"SIRIKIT", "SYSTEM_EXTENSION_INSTALL", "USER_MANAGEMENT", "WALLET", "WIRELESS_ACCESSORY_CONFIGURATION",
	}
	capabilitySettingKeys = enum(gen.CapabilitySettingKeyAPPLEIDAUTHAPPCONSENT, gen.CapabilitySettingKeyDATAPROTECTIONPERMISSIONLEVEL,
		gen.CapabilitySettingKeyICLOUDVERSION)
	capabilityOptionKeys = enum(gen.CapabilityOptionKeyCOMPLETEPROTECTION, gen.CapabilityOptionKeyPRIMARYAPPCONSENT,
		gen.CapabilityOptionKeyPROTECTEDUNLESSOPEN, gen.CapabilityOptionKeyPROTECTEDUNTILFIRSTUSERAUTH,
		gen.CapabilityOptionKeyXCODE5, gen.CapabilityOptionKeyXCODE6)
)

var (
	_ resource.ResourceWithConfigure   = &bundleIDCapabilityResource{}
	_ resource.ResourceWithImportState = &bundleIDCapabilityResource{}
	_ resource.ResourceWithIdentity    = &bundleIDCapabilityResource{}
)

type bundleIDCapabilityResource struct {
	resourceIdentity
	client *asc.Client
}

type bundleIDCapabilityModel struct {
	ID             types.String `tfsdk:"id"`
	BundleID       types.String `tfsdk:"bundle_id"`
	CapabilityType types.String `tfsdk:"capability_type"`
	Settings       types.Map    `tfsdk:"settings"`
}

func newBundleIDCapabilityResource() resource.Resource {
	return &bundleIDCapabilityResource{resourceIdentity: resourceIdentity{
		attrs: []identityAttribute{
			{name: "bundle_id", description: "App Store Connect ID of the bundle ID.", state: "bundle_id"},
			{name: "capability_type", description: "Capability type, e.g. `PUSH_NOTIFICATIONS`.", state: "capability_type"},
		},
		resolve: resolveBundleID,
		keyName: "bundle identifier",
	}}
}

func (r *bundleIDCapabilityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bundle_id_capability"
}

func (r *bundleIDCapabilityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enables a capability (app service) on a bundle ID. Enabling, changing or disabling a capability " +
			"invalidates the bundle ID's provisioning profiles, which `apple_profile` then replaces. Import it with " +
			"`<bundle_id>/<capability_type>`, where `<bundle_id>` is the bundle ID's App Store Connect ID or bundle identifier.",
		Attributes: map[string]schema.Attribute{
			"id":        idAttribute("App Store Connect ID of the capability."),
			"bundle_id": replacedString("App Store Connect ID of the bundle ID, e.g. `apple_bundle_id.app.id`. Changing it creates a new capability."),
			"capability_type": replacedString("Capability to enable: "+docValues(capabilityTypes)+". Changing it creates a new capability.",
				oneOf(capabilityTypes)),
			"settings": schema.MapAttribute{
				MarkdownDescription: "Settings of the capability, from setting key (" + docValues(capabilitySettingKeys) + ") to the " +
					"options to enable (" + docValues(capabilityOptionKeys) + "), for example " +
					"`{ ICLOUD_VERSION = [\"XCODE_6\"] }`. Changes are made in place. When unset, the provider does not manage the settings.",
				Optional:    true,
				ElementType: types.SetType{ElemType: types.StringType},
				Validators: []validator.Map{
					mapvalidator.KeysAre(oneOf(capabilitySettingKeys)),
					mapvalidator.ValueSetsAre(setvalidator.ValueStringsAre(oneOf(capabilityOptionKeys))),
				},
			},
		},
	}
}

func (r *bundleIDCapabilityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *bundleIDCapabilityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

// apiSettings converts the settings map to the request form, which lists the
// enabled options of each setting.
func apiSettings(ctx context.Context, m types.Map, diags *diag.Diagnostics) *[]gen.CapabilitySetting {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	var values map[string][]string
	diags.Append(m.ElementsAs(ctx, &values, false)...)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	settings := []gen.CapabilitySetting{}
	for _, k := range keys {
		s := gen.CapabilitySetting{Key: new(gen.CapabilitySettingKey(k))}
		options := slices.Sorted(slices.Values(values[k]))
		for _, o := range options {
			opt := asc.Append(&s.Options)
			opt.Key, opt.Enabled = new(gen.CapabilityOptionKey(o)), new(true)
		}
		if s.Options == nil {
			s.Options = &[]gen.CapabilityOption{}
		}
		settings = append(settings, s)
	}
	return &settings
}

// settingsValue converts the API's settings to the map form, keeping only
// the enabled options.
func settingsValue(ctx context.Context, settings *[]gen.CapabilitySetting, diags *diag.Diagnostics) types.Map {
	elems := map[string]attr.Value{}
	for _, s := range asc.Value(settings) {
		if s.Key == nil {
			continue
		}
		var enabled []string
		for _, o := range asc.Value(s.Options) {
			if o.Key != nil && asc.Value(o.Enabled) {
				enabled = append(enabled, string(*o.Key))
			}
		}
		elems[string(*s.Key)] = stringSet(ctx, enabled, diags)
	}
	m, d := types.MapValue(types.SetType{ElemType: types.StringType}, elems)
	diags.Append(d...)
	return m
}

func (r *bundleIDCapabilityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan bundleIDCapabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body gen.BundleIdCapabilityCreateRequest
	body.Data.Type = gen.BundleIdCapabilityCreateRequestDataTypeBundleIdCapabilities
	body.Data.Attributes.CapabilityType = gen.CapabilityType(plan.CapabilityType.ValueString())
	body.Data.Attributes.Settings = apiSettings(ctx, plan.Settings, &resp.Diagnostics)
	body.Data.Relationships.BundleId.Data.Id = plan.BundleID.ValueString()
	body.Data.Relationships.BundleId.Data.Type = gen.BundleIdCapabilityCreateRequestDataRelationshipsBundleIdDataTypeBundleIds
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BundleIdCapabilitiesCreateInstanceWithResponse(ctx, body)
	if err := asc.Err(out, err); err != nil {
		apiError(&resp.Diagnostics, "enable capability", err)
		return
	}
	plan.ID = types.StringValue(out.JSON201.Data.Id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// findCapability returns the bundle ID's capability of the given type, or nil
// when it is not enabled or the bundle ID is gone.
func findCapability(ctx context.Context, c *asc.Client, bundleID, capabilityType string) (*gen.BundleIdCapability, error) {
	out, err := c.BundleIdsBundleIdCapabilitiesGetToManyRelatedWithResponse(ctx, bundleID,
		&gen.BundleIdsBundleIdCapabilitiesGetToManyRelatedParams{Limit: new(listAll)})
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	all, err := asc.All(ctx, c, out.JSON200,
		func(p *gen.BundleIdCapabilitiesWithoutIncludesResponse) []gen.BundleIdCapability { return p.Data },
		func(p *gen.BundleIdCapabilitiesWithoutIncludesResponse) *string { return p.Links.Next })
	if err != nil {
		return nil, err
	}
	for i := range all {
		if a := all[i].Attributes; a != nil && string(asc.Value(a.CapabilityType)) == capabilityType {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (r *bundleIDCapabilityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state bundleIDCapabilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := findCapability(ctx, r.client, state.BundleID.ValueString(), state.CapabilityType.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "read capability", err)
		return
	}
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	// Settings are only managed when configured. An import, which leaves the
	// ID unset, reads them.
	imported := state.ID.IsNull()
	state.ID = types.StringValue(found.Id)
	if !state.Settings.IsNull() || imported {
		state.Settings = settingsValue(ctx, found.Attributes.Settings, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bundleIDCapabilityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan bundleIDCapabilityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Settings.IsNull() {
		var body gen.BundleIdCapabilityUpdateRequest
		body.Data.Type, body.Data.Id = gen.BundleIdCapabilityUpdateRequestDataTypeBundleIdCapabilities, plan.ID.ValueString()
		a := asc.Alloc(&body.Data.Attributes)
		a.CapabilityType = new(gen.CapabilityType(plan.CapabilityType.ValueString()))
		a.Settings = apiSettings(ctx, plan.Settings, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		out, err := r.client.BundleIdCapabilitiesUpdateInstanceWithResponse(ctx, plan.ID.ValueString(), body)
		if err := asc.Err(out, err); err != nil {
			apiError(&resp.Diagnostics, "update capability", err)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bundleIDCapabilityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bundleIDCapabilityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.BundleIdCapabilitiesDeleteInstanceWithResponse(ctx, state.ID.ValueString())
	if err := asc.Err(out, err); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "disable capability", err)
	}
}
