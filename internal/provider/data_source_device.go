package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

var deviceDataAttributes = map[string]schema.Attribute{
	"id":           dsComputed("App Store Connect ID of the device."),
	"udid":         dsComputed("UDID of the device."),
	"name":         dsComputed("Name of the device."),
	"platform":     dsComputed("Platform of the device."),
	"status":       dsComputed("Status: `ENABLED` or `DISABLED`."),
	"device_class": dsComputed("Class of the device, e.g. `IPHONE`."),
	"model":        dsComputed("Model of the device."),
	"added_date":   dsComputed("When the device was registered, in RFC 3339 format."),
}

var newDeviceDataSource = lookupDataSource("device",
	"Looks up a registered device by its App Store Connect ID or UDID.", "udid",
	withAttributes(deviceDataAttributes, map[string]schema.Attribute{
		"id":   dsLookup("App Store Connect ID of the device. Set this or `udid`."),
		"udid": dsLookup("UDID of the device. Set this or `id`."),
	}),
	func(ctx context.Context, c *asc.Client, m *deviceModel, diags *diag.Diagnostics) {
		var found *gen.Device
		var err error
		if id := m.ID.ValueString(); id != "" {
			out, e := c.DevicesGetInstanceWithResponse(ctx, id, nil)
			if err = asc.Err(out, e); err == nil {
				found = &out.JSON200.Data
			}
		} else {
			found, err = deviceByUDID(ctx, c, m.UDID.ValueString())
		}
		if err != nil {
			lookupError(diags, "device", err)
			return
		}
		m.apply(found)
	})

type devicesData struct {
	Names     types.Set     `tfsdk:"names"`
	Platforms types.Set     `tfsdk:"platforms"`
	UDIDs     types.Set     `tfsdk:"udids"`
	Statuses  types.Set     `tfsdk:"statuses"`
	Devices   []deviceModel `tfsdk:"devices"`
}

var newDevicesDataSource = listDataSource("devices", "Lists the registered devices that match every given filter.",
	map[string]schema.Attribute{
		"names":     dsFilter("Names to match."),
		"platforms": dsFilter("Platforms to match.", platforms...),
		"udids":     dsFilter("UDIDs to match."),
		"statuses":  dsFilter("Statuses to match.", deviceStatuses...),
		"devices":   listOf("The matching devices, ordered by ID.", deviceDataAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *devicesData, diags *diag.Diagnostics) {
		all, err := findDevices(ctx, c, gen.DevicesGetCollectionParams{
			FilterName:     filterParam[string](ctx, m.Names, diags),
			FilterPlatform: filterParam[gen.DevicesGetCollectionParamsFilterPlatform](ctx, m.Platforms, diags),
			FilterUdid:     filterParam[string](ctx, m.UDIDs, diags),
			FilterStatus:   filterParam[gen.DevicesGetCollectionParamsFilterStatus](ctx, m.Statuses, diags),
		})
		if err != nil {
			apiError(diags, "list devices", err)
			return
		}
		m.Devices = []deviceModel{}
		for i := range all {
			a := asc.Value(all[i].Attributes)
			if passes(ctx, m.Names, asc.Value(a.Name), diags) && passes(ctx, m.Platforms, string(asc.Value(a.Platform)), diags) &&
				passes(ctx, m.UDIDs, asc.Value(a.Udid), diags) && passes(ctx, m.Statuses, string(asc.Value(a.Status)), diags) {
				var item deviceModel
				item.apply(&all[i])
				m.Devices = append(m.Devices, item)
			}
		}
	})
