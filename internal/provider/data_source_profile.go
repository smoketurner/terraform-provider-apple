package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

type profileItemData struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	ProfileType    types.String `tfsdk:"profile_type"`
	Platform       types.String `tfsdk:"platform"`
	ProfileState   types.String `tfsdk:"profile_state"`
	ProfileContent types.String `tfsdk:"profile_content"`
	UUID           types.String `tfsdk:"uuid"`
	CreatedDate    types.String `tfsdk:"created_date"`
	ExpirationDate types.String `tfsdk:"expiration_date"`
}

func (m *profileItemData) apply(p *gen.Profile) {
	var r profileModel
	r.apply(p)
	*m = profileItemData{
		ID: r.ID, Name: r.Name, ProfileType: r.ProfileType, Platform: r.Platform, ProfileState: r.ProfileState,
		ProfileContent: r.ProfileContent, UUID: r.UUID, CreatedDate: r.CreatedDate, ExpirationDate: r.ExpirationDate,
	}
}

var profileItemAttributes = map[string]schema.Attribute{
	"id":              dsComputed("App Store Connect ID of the profile."),
	"name":            dsComputed("Name of the profile."),
	"profile_type":    dsComputed("Profile type."),
	"platform":        dsComputed("Platform of the profile."),
	"profile_state":   dsComputed("State: `ACTIVE` or `INVALID`."),
	"profile_content": dsSensitive("The profile, base64-encoded."),
	"uuid":            dsComputed("UUID of the profile."),
	"created_date":    dsComputed("Creation date, in RFC 3339 format."),
	"expiration_date": dsComputed("Expiration date, in RFC 3339 format."),
}

type profileData struct {
	profileItemData
	BundleID       types.String `tfsdk:"bundle_id"`
	CertificateIDs types.Set    `tfsdk:"certificate_ids"`
	DeviceIDs      types.Set    `tfsdk:"device_ids"`
}

var newProfileDataSource = lookupDataSource("profile",
	"Looks up a provisioning profile by its App Store Connect ID or name.", "name",
	withAttributes(profileItemAttributes, map[string]schema.Attribute{
		"id":              dsLookup("App Store Connect ID of the profile. Set this or `name`."),
		"name":            dsLookup("Name of the profile. Set this or `id`."),
		"bundle_id":       dsComputed("App Store Connect ID of the profile's bundle ID."),
		"certificate_ids": dsComputedSet("App Store Connect IDs of the profile's certificates."),
		"device_ids":      dsComputedSet("App Store Connect IDs of the profile's devices."),
	}),
	func(ctx context.Context, c *asc.Client, m *profileData, diags *diag.Diagnostics) {
		var found *gen.Profile
		var err error
		if id := m.ID.ValueString(); id != "" {
			out, e := c.ProfilesGetInstanceWithResponse(ctx, id, nil)
			if err = asc.Err(out, e); err == nil {
				found = &out.JSON200.Data
			}
		} else {
			found, err = profileByName(ctx, c, m.Name.ValueString())
		}
		if err != nil {
			lookupError(diags, "profile", err)
			return
		}
		m.apply(found)
		bundle, err := c.ProfilesBundleIdGetToOneRelationshipWithResponse(ctx, found.Id)
		if err := asc.Err(bundle, err); err != nil {
			apiError(diags, "read profile bundle ID", err)
			return
		}
		m.BundleID = types.StringValue(bundle.JSON200.Data.Id)
		certs, err := profileCertificateIDs(ctx, c, found.Id)
		if err != nil {
			apiError(diags, "read profile certificates", err)
			return
		}
		m.CertificateIDs = stringSet(ctx, certs, diags)
		devices, err := profileDeviceIDs(ctx, c, found.Id)
		if err != nil {
			apiError(diags, "read profile devices", err)
			return
		}
		m.DeviceIDs = stringSet(ctx, devices, diags)
	})

type profilesData struct {
	Names         types.Set         `tfsdk:"names"`
	ProfileTypes  types.Set         `tfsdk:"profile_types"`
	ProfileStates types.Set         `tfsdk:"profile_states"`
	BundleID      types.String      `tfsdk:"bundle_id"`
	Profiles      []profileItemData `tfsdk:"profiles"`
}

var profileStates = enum(gen.ProfileAttributesProfileStateACTIVE, gen.ProfileAttributesProfileStateINVALID)

var newProfilesDataSource = listDataSource("profiles", "Lists the provisioning profiles that match every given filter.",
	map[string]schema.Attribute{
		"names":          dsFilter("Names to match."),
		"profile_types":  dsFilter("Profile types to match.", profileTypes...),
		"profile_states": dsFilter("States to match.", profileStates...),
		"bundle_id":      schema.StringAttribute{MarkdownDescription: "Only profiles of this bundle ID.", Optional: true},
		"profiles":       listOf("The matching profiles, ordered by ID.", profileItemAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *profilesData, diags *diag.Diagnostics) {
		var all []gen.Profile
		var err error
		if !m.BundleID.IsNull() {
			var out *gen.BundleIdsProfilesGetToManyRelatedResponse
			out, err = c.BundleIdsProfilesGetToManyRelatedWithResponse(ctx, m.BundleID.ValueString(),
				&gen.BundleIdsProfilesGetToManyRelatedParams{Limit: new(listAll)})
			if err = asc.Err(out, err); err == nil {
				all, err = asc.All(ctx, c, out.JSON200,
					func(p *gen.ProfilesWithoutIncludesResponse) []gen.Profile { return p.Data },
					func(p *gen.ProfilesWithoutIncludesResponse) *string { return p.Links.Next })
			}
		} else {
			all, err = findProfiles(ctx, c, gen.ProfilesGetCollectionParams{
				FilterName:         filterParam[string](ctx, m.Names, diags),
				FilterProfileType:  filterParam[gen.ProfilesGetCollectionParamsFilterProfileType](ctx, m.ProfileTypes, diags),
				FilterProfileState: filterParam[gen.ProfilesGetCollectionParamsFilterProfileState](ctx, m.ProfileStates, diags),
			})
		}
		if err != nil {
			apiError(diags, "list profiles", err)
			return
		}
		m.Profiles = []profileItemData{}
		for i := range all {
			a := asc.Value(all[i].Attributes)
			if passes(ctx, m.Names, asc.Value(a.Name), diags) && passes(ctx, m.ProfileTypes, string(asc.Value(a.ProfileType)), diags) &&
				passes(ctx, m.ProfileStates, string(asc.Value(a.ProfileState)), diags) {
				var item profileItemData
				item.apply(&all[i])
				m.Profiles = append(m.Profiles, item)
			}
		}
	})
