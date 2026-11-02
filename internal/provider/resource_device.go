package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var deviceStatuses = enum(gen.DeviceAttributesStatusENABLED, gen.DeviceAttributesStatusDISABLED)

var (
	_ resource.ResourceWithConfigure   = &deviceResource{}
	_ resource.ResourceWithImportState = &deviceResource{}
	_ resource.ResourceWithIdentity    = &deviceResource{}
)

type deviceResource struct {
	resourceIdentity
	client *asc.Client
}

type deviceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	UDID        types.String `tfsdk:"udid"`
	Platform    types.String `tfsdk:"platform"`
	Status      types.String `tfsdk:"status"`
	DeviceClass types.String `tfsdk:"device_class"`
	Model       types.String `tfsdk:"model"`
	AddedDate   types.String `tfsdk:"added_date"`
}

func newDeviceResource() resource.Resource {
	return &deviceResource{resourceIdentity: resourceIdentity{
		attrs:   idIdentity("App Store Connect ID of the device."),
		resolve: resolveDevice,
		keyName: "UDID",
	}}
}

func (r *deviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_device"
}

func (r *deviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Registers a device for development and ad hoc distribution.\n\n" +
			"App Store Connect cannot delete devices, so destroying this resource disables the device. A disabled device " +
			"still counts toward the 100 devices of its class a team can register each membership year, until the list is " +
			"reset at renewal. Disabling a device invalidates the provisioning profiles that contain it. Creating a device " +
			"whose UDID is already registered, for example after destroying it, adopts and re-enables the existing device. " +
			"Import it by its App Store Connect ID or UDID.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("App Store Connect ID of the device."),
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the device.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"udid":     replacedString("UDID of the device. Changing it registers a new device.", stringvalidator.LengthAtLeast(1)),
			"platform": platformAttribute(),
			"status": schema.StringAttribute{
				MarkdownDescription: "Status of the device: " + docValues(deviceStatuses) + ". Defaults to `ENABLED`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(string(gen.DeviceAttributesStatusENABLED)),
				Validators:          []validator.String{oneOf(deviceStatuses)},
			},
			"device_class": computedString("Class of the device, e.g. `IPHONE`."),
			"model":        computedString("Model of the device."),
			"added_date": schema.StringAttribute{
				MarkdownDescription: "When the device was registered, in RFC 3339 format.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *deviceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *deviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (m *deviceModel) apply(d *gen.Device) {
	m.ID = types.StringValue(d.Id)
	if a := d.Attributes; a != nil {
		m.Name = stringValue(a.Name)
		m.UDID = stringValue(a.Udid)
		m.Platform = stringValue(a.Platform)
		m.Status = stringValue(a.Status)
		m.DeviceClass = stringValue(a.DeviceClass)
		m.Model = stringValue(a.Model)
		m.AddedDate = timeValue(a.AddedDate)
	}
}

func (r *deviceResource) update(ctx context.Context, id string, name, status *string) (*gen.Device, error) {
	var body gen.DeviceUpdateRequest
	body.Data.Type, body.Data.Id = gen.DeviceUpdateRequestDataTypeDevices, id
	a := asc.Alloc(&body.Data.Attributes)
	a.Name = name
	if status != nil {
		a.Status = new(gen.DeviceUpdateRequestDataAttributesStatus(*status))
	}
	out, err := r.client.DevicesUpdateInstanceWithResponse(ctx, id, body)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (r *deviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan deviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	existing, err := deviceByUDID(ctx, r.client, plan.UDID.ValueString())
	if err != nil && !errors.Is(err, errNoMatch) {
		apiError(&resp.Diagnostics, "look up device", err)
		return
	}
	var device *gen.Device
	if existing != nil {
		if p := string(asc.Value(existing.Attributes.Platform)); p != plan.Platform.ValueString() {
			resp.Diagnostics.AddAttributeError(path.Root("platform"), "Device registered for another platform",
				"The device with UDID "+plan.UDID.ValueString()+" is already registered for "+p+". Set platform to "+p+".")
			return
		}
		// Devices cannot be deleted, so this one may have been destroyed by
		// Terraform before: adopt it.
		device, err = r.update(ctx, existing.Id, new(plan.Name.ValueString()), new(plan.Status.ValueString()))
		if err != nil {
			apiError(&resp.Diagnostics, "adopt registered device", err)
			return
		}
	} else {
		var body gen.DeviceCreateRequest
		body.Data.Type = gen.DeviceCreateRequestDataTypeDevices
		a := &body.Data.Attributes
		a.Name, a.Udid, a.Platform = plan.Name.ValueString(), plan.UDID.ValueString(), gen.BundleIdPlatform(plan.Platform.ValueString())
		out, err := r.client.DevicesCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			apiError(&resp.Diagnostics, "register device", err)
			return
		}
		device = &out.JSON201.Data
		if status := plan.Status.ValueString(); status != string(asc.Value(device.Attributes.Status)) {
			// Save the device first, so a failure below does not orphan it.
			plan.apply(device)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			if device, err = r.update(ctx, device.Id, nil, &status); err != nil {
				apiError(&resp.Diagnostics, "update device", err)
				return
			}
		}
	}
	plan.apply(device)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *deviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state deviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.DevicesGetInstanceWithResponse(ctx, state.ID.ValueString(), nil)
	err = asc.Err(out, err)
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read device", err)
		return
	}
	state.apply(&out.JSON200.Data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *deviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan deviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	device, err := r.update(ctx, plan.ID.ValueString(), new(plan.Name.ValueString()), new(plan.Status.ValueString()))
	if err != nil {
		apiError(&resp.Diagnostics, "update device", err)
		return
	}
	plan.apply(device)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete disables the device, because App Store Connect cannot delete one.
func (r *deviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state deviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.update(ctx, state.ID.ValueString(), nil, new(string(gen.DeviceAttributesStatusDISABLED)))
	if err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "disable device", err)
		return
	}
	resp.Diagnostics.AddWarning("Device disabled, not deleted",
		"App Store Connect cannot delete devices, so "+state.UDID.ValueString()+" was disabled. It still counts toward "+
			"the team's device limit until the device list is reset at the start of the next membership year.")
}

// deviceByUDID finds a device by its exact UDID.
func deviceByUDID(ctx context.Context, c *asc.Client, udid string) (*gen.Device, error) {
	all, err := findDevices(ctx, c, gen.DevicesGetCollectionParams{FilterUdid: &[]string{udid}})
	if err != nil {
		return nil, err
	}
	for i := range all {
		if asc.Value(all[i].Attributes.Udid) == udid {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

// findDevices lists the devices matching params, following pagination.
func findDevices(ctx context.Context, c *asc.Client, params gen.DevicesGetCollectionParams) ([]gen.Device, error) {
	params.Limit = new(listAll)
	out, err := c.DevicesGetCollectionWithResponse(ctx, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return asc.All(ctx, c, out.JSON200,
		func(p *gen.DevicesResponse) []gen.Device { return p.Data },
		func(p *gen.DevicesResponse) *string { return p.Links.Next })
}

func resolveDevice(ctx context.Context, c *asc.Client, key string) (string, error) {
	return resolveByID(ctx, key,
		func(ctx context.Context, id string) error {
			out, err := c.DevicesGetInstanceWithResponse(ctx, id, nil)
			return asc.Err(out, err)
		},
		func(ctx context.Context, key string) (string, error) {
			d, err := deviceByUDID(ctx, c, key)
			if err != nil {
				return "", err
			}
			return d.Id, nil
		})
}
