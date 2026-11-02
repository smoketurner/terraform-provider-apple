package asctest

import (
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// DeviceLimit is how many devices of each device class a team can register
// per membership year. Disabled devices still count: "You may disable a
// device on your list during the year, but doing so won't increase your
// number of available devices" ("Devices overview",
// https://developer.apple.com/help/account/devices/devices-overview/).
const DeviceLimit = 100

func deviceClass(p gen.BundleIdPlatform) gen.DeviceAttributesDeviceClass {
	if p == gen.BundleIdPlatformMACOS {
		return gen.DeviceAttributesDeviceClassMAC
	}
	return gen.DeviceAttributesDeviceClassIPHONE
}

func (s *Server) DevicesGetCollection(w http.ResponseWriter, r *http.Request, params gen.DevicesGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.Device
	for _, d := range sortedValues(s.devices) {
		a := d.Attributes
		if matches(params.FilterId, d.Id) && matches(params.FilterUdid, asc.Value(a.Udid)) &&
			matches(params.FilterName, asc.Value(a.Name)) && matches(params.FilterPlatform, string(asc.Value(a.Platform))) &&
			matches(params.FilterStatus, string(asc.Value(a.Status))) {
			items = append(items, *d)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.DevicesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// DevicesCreateInstance registers a device
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-devices).
// A UDID can be registered once, and each device class has DeviceLimit
// slots.
func (s *Server) DevicesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.DeviceCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	if !in.Platform.Valid() {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "platform is invalid", "/data/attributes/platform")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	class := deviceClass(in.Platform)
	used := 0
	for _, d := range s.devices {
		if asc.Value(d.Attributes.Udid) == in.Udid {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID",
				"A device with number '"+in.Udid+"' already exists on this team.", "/data/attributes/udid")
			return
		}
		if *d.Attributes.DeviceClass == class {
			used++
		}
	}
	if used >= DeviceLimit {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID",
			"You have reached the maximum number of "+string(class)+" devices for this membership year.", "/data/attributes/udid")
		return
	}
	d := &gen.Device{Id: s.newID("D"), Type: gen.DeviceTypeDevices}
	a := alloc(&d.Attributes)
	a.Name, a.Udid, a.Platform = new(in.Name), new(in.Udid), new(in.Platform)
	a.DeviceClass = new(class)
	a.Status = new(gen.DeviceAttributesStatusENABLED)
	a.Model = new("Fake " + string(class))
	a.AddedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	d.Links = resourceLinks(r, "devices", d.Id)
	s.devices[d.Id] = d
	writeJSON(w, http.StatusCreated, gen.DeviceResponse{Data: *d, Links: docLinks(r)})
}

func (s *Server) DevicesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.DevicesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		notFound(w, "devices", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.DeviceResponse{Data: *d, Links: docLinks(r)})
}

// DevicesUpdateInstance changes the name or status
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-devices-_id_).
// Devices cannot be deleted, only disabled, and "provisioning profiles that
// contain a disabled device become invalid" ("Disable or enable a device",
// https://developer.apple.com/help/account/devices/disable-or-enable-a-device/).
func (s *Server) DevicesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.DeviceUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		notFound(w, "devices", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.Name != nil {
			d.Attributes.Name = new(*in.Name)
		}
		if in.Status != nil {
			if !in.Status.Valid() {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "status is invalid", "/data/attributes/status")
				return
			}
			status := gen.DeviceAttributesStatus(*in.Status)
			if status == gen.DeviceAttributesStatusDISABLED && *d.Attributes.Status != status {
				s.invalidateProfiles(func(p *profile) bool { return slices.Contains(p.deviceIDs, id) })
			}
			d.Attributes.Status = &status
		}
	}
	writeJSON(w, http.StatusOK, gen.DeviceResponse{Data: *d, Links: docLinks(r)})
}
