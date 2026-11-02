package provider

import (
	"cmp"
	"context"
	"slices"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// ---- builds ----

type buildData struct {
	ID                      types.String `tfsdk:"id"`
	AppID                   types.String `tfsdk:"app_id"`
	Version                 types.String `tfsdk:"version"`
	PreReleaseVersion       types.String `tfsdk:"pre_release_version"`
	Platform                types.String `tfsdk:"platform"`
	ProcessingState         types.String `tfsdk:"processing_state"`
	BuildAudienceType       types.String `tfsdk:"build_audience_type"`
	Expired                 types.Bool   `tfsdk:"expired"`
	UsesNonExemptEncryption types.Bool   `tfsdk:"uses_non_exempt_encryption"`
	MinOSVersion            types.String `tfsdk:"min_os_version"`
	UploadedDate            types.String `tfsdk:"uploaded_date"`
	ExpirationDate          types.String `tfsdk:"expiration_date"`
	InternalBuildState      types.String `tfsdk:"internal_build_state"`
	ExternalBuildState      types.String `tfsdk:"external_build_state"`
}

var buildDataAttributes = map[string]schema.Attribute{
	"id":                         dsComputed("App Store Connect ID of the build."),
	"app_id":                     dsComputed("App Store Connect ID of the app."),
	"version":                    dsComputed("Build number, e.g. `42`."),
	"pre_release_version":        dsComputed("Version the build belongs to, e.g. `1.2.0`."),
	"platform":                   dsComputed("Platform of the build's version: `IOS`, `MAC_OS`, `TV_OS` or `VISION_OS`."),
	"processing_state":           dsComputed("Processing state: `PROCESSING`, `FAILED`, `INVALID` or `VALID`."),
	"build_audience_type":        dsComputed("`APP_STORE_ELIGIBLE`, or `INTERNAL_ONLY` for builds uploaded as TestFlight Internal Only."),
	"expired":                    schema.BoolAttribute{MarkdownDescription: "Whether the build has expired.", Computed: true},
	"uses_non_exempt_encryption": schema.BoolAttribute{MarkdownDescription: "The build's export compliance answer; null if unanswered.", Computed: true},
	"min_os_version":             dsComputed("Minimum OS version the build supports."),
	"uploaded_date":              dsComputed("When the build was uploaded, in RFC 3339 format."),
	"expiration_date":            dsComputed("When the build stops being testable, in RFC 3339 format."),
	"internal_build_state":       dsComputed("TestFlight state of the build for internal testers, e.g. `IN_BETA_TESTING`."),
	"external_build_state":       dsComputed("TestFlight state of the build for external testers, e.g. `READY_FOR_BETA_SUBMISSION`."),
}

// buildIncluded indexes a builds response's included pre-release versions
// and build beta details by ID.
type buildIncluded struct {
	versions map[string]gen.PrereleaseVersion
	details  map[string]gen.BuildBetaDetail
}

func indexIncluded[I interface {
	Discriminator() (string, error)
	AsPrereleaseVersion() (gen.PrereleaseVersion, error)
	AsBuildBetaDetail() (gen.BuildBetaDetail, error)
}](items []I) buildIncluded {
	idx := buildIncluded{versions: map[string]gen.PrereleaseVersion{}, details: map[string]gen.BuildBetaDetail{}}
	for _, item := range items {
		kind, err := item.Discriminator()
		if err != nil {
			continue
		}
		switch kind {
		case "preReleaseVersions":
			if v, err := item.AsPrereleaseVersion(); err == nil {
				idx.versions[v.Id] = v
			}
		case "buildBetaDetails":
			if d, err := item.AsBuildBetaDetail(); err == nil {
				idx.details[d.Id] = d
			}
		}
	}
	return idx
}

func (m *buildData) apply(b *gen.Build, inc buildIncluded) {
	a := asc.Value(b.Attributes)
	m.ID = types.StringValue(b.Id)
	m.Version, m.ProcessingState = stringValue(a.Version), stringValue(a.ProcessingState)
	m.BuildAudienceType, m.MinOSVersion = stringValue(a.BuildAudienceType), stringValue(a.MinOsVersion)
	m.Expired = types.BoolValue(asc.Value(a.Expired))
	m.UsesNonExemptEncryption = types.BoolPointerValue(a.UsesNonExemptEncryption)
	m.UploadedDate, m.ExpirationDate = timeValue(a.UploadedDate), timeValue(a.ExpirationDate)
	m.AppID, m.PreReleaseVersion, m.Platform = types.StringNull(), types.StringNull(), types.StringNull()
	m.InternalBuildState, m.ExternalBuildState = types.StringNull(), types.StringNull()
	rel := asc.Value(b.Relationships)
	if rel.App != nil && rel.App.Data != nil {
		m.AppID = types.StringValue(rel.App.Data.Id)
	}
	if rel.PreReleaseVersion != nil && rel.PreReleaseVersion.Data != nil {
		v := asc.Value(inc.versions[rel.PreReleaseVersion.Data.Id].Attributes)
		m.PreReleaseVersion, m.Platform = stringValue(v.Version), stringValue(v.Platform)
	}
	if rel.BuildBetaDetail != nil && rel.BuildBetaDetail.Data != nil {
		d := asc.Value(inc.details[rel.BuildBetaDetail.Data.Id].Attributes)
		m.InternalBuildState, m.ExternalBuildState = stringValue(d.InternalBuildState), stringValue(d.ExternalBuildState)
	}
}

var buildIncludes = []gen.BuildsGetCollectionParamsInclude{
	gen.BuildsGetCollectionParamsIncludeApp, gen.BuildsGetCollectionParamsIncludePreReleaseVersion, gen.BuildsGetCollectionParamsIncludeBuildBetaDetail,
}

// findBuilds lists the builds matching params with their versions and beta
// states, newest upload first.
func findBuilds(ctx context.Context, c *asc.Client, params gen.BuildsGetCollectionParams) ([]buildData, error) {
	params.Limit = new(listAll)
	params.Include = &buildIncludes
	out, err := c.BuildsGetCollectionWithResponse(ctx, &params)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	var builds []buildData
	for page := out.JSON200; ; {
		inc := indexIncluded(asc.Value(page.Included))
		for i := range page.Data {
			var b buildData
			b.apply(&page.Data[i], inc)
			builds = append(builds, b)
		}
		if page.Links.Next == nil || *page.Links.Next == "" {
			break
		}
		next := new(gen.BuildsResponse)
		if err := c.Follow(ctx, *page.Links.Next, next); err != nil {
			return nil, err
		}
		page = next
	}
	slices.SortStableFunc(builds, func(a, b buildData) int {
		return cmp.Compare(b.UploadedDate.ValueString(), a.UploadedDate.ValueString())
	})
	return builds, nil
}

type buildLookupData struct {
	buildData
	Latest types.Bool `tfsdk:"latest"`
}

var newBuildDataSource = lookupDataSource("build",
	"Looks up a build by its App Store Connect ID, or the latest build of an app matching `version` and "+
		"`pre_release_version`.", "app_id",
	withAttributes(buildDataAttributes, map[string]schema.Attribute{
		"id":                  dsLookup("App Store Connect ID of the build. Set this or `app_id`."),
		"app_id":              dsLookup("App Store Connect ID of the app, to find its latest matching build. Set this or `id`."),
		"version":             dsLookup("With `app_id`: the build number to match."),
		"pre_release_version": dsLookup("With `app_id`: the version to match, e.g. `1.2.0`."),
		"latest": schema.BoolAttribute{
			MarkdownDescription: "With `app_id`: return the most recently uploaded match when several builds match. Defaults to `false`, which makes several matches an error.",
			Optional:            true,
		},
	}),
	func(ctx context.Context, c *asc.Client, m *buildLookupData, diags *diag.Diagnostics) {
		latest := m.Latest
		if id := m.ID.ValueString(); id != "" {
			out, err := c.BuildsGetInstanceWithResponse(ctx, id, &gen.BuildsGetInstanceParams{Include: &[]gen.BuildsGetInstanceParamsInclude{
				gen.BuildsGetInstanceParamsIncludeApp, gen.BuildsGetInstanceParamsIncludePreReleaseVersion, gen.BuildsGetInstanceParamsIncludeBuildBetaDetail,
			}})
			if err := asc.Err(out, err); err != nil {
				lookupError(diags, "build", err)
				return
			}
			m.apply(&out.JSON200.Data, indexIncluded(asc.Value(out.JSON200.Included)))
			m.Latest = latest
			return
		}
		params := gen.BuildsGetCollectionParams{FilterApp: &[]string{m.AppID.ValueString()}}
		if v := m.Version.ValueString(); v != "" {
			params.FilterVersion = &[]string{v}
		}
		if v := m.PreReleaseVersion.ValueString(); v != "" {
			params.FilterPreReleaseVersionVersion = &[]string{v}
		}
		builds, err := findBuilds(ctx, c, params)
		if err != nil {
			apiError(diags, "look up build", err)
			return
		}
		switch {
		case len(builds) == 0:
			lookupError(diags, "build", errNoMatch)
			return
		case len(builds) > 1 && !latest.ValueBool():
			diags.AddError("Several builds match",
				strconv.Itoa(len(builds))+" builds match. Set `version` and `pre_release_version`, or `latest = true`.")
			return
		}
		m.buildData = builds[0]
		m.Latest = latest
	})

type buildsData struct {
	AppID              types.String `tfsdk:"app_id"`
	Versions           types.Set    `tfsdk:"versions"`
	PreReleaseVersions types.Set    `tfsdk:"pre_release_versions"`
	Platforms          types.Set    `tfsdk:"platforms"`
	ProcessingStates   types.Set    `tfsdk:"processing_states"`
	Expired            types.Bool   `tfsdk:"expired"`
	BetaGroupID        types.String `tfsdk:"beta_group_id"`
	Builds             []buildData  `tfsdk:"builds"`
}

var buildPlatforms = enum(gen.PlatformIOS, gen.PlatformMACOS, gen.PlatformTVOS, gen.PlatformVISIONOS)

var buildProcessingStates = enum(gen.BuildAttributesProcessingStatePROCESSING, gen.BuildAttributesProcessingStateFAILED,
	gen.BuildAttributesProcessingStateINVALID, gen.BuildAttributesProcessingStateVALID)

var newBuildsDataSource = listDataSource("builds", "Lists the builds that match every given filter, newest upload first.",
	map[string]schema.Attribute{
		"app_id":               schema.StringAttribute{MarkdownDescription: "Only builds of this app.", Optional: true},
		"versions":             dsFilter("Build numbers to match."),
		"pre_release_versions": dsFilter("Versions to match, e.g. `1.2.0`."),
		"platforms":            dsFilter("Platforms to match.", buildPlatforms...),
		"processing_states":    dsFilter("Processing states to match.", buildProcessingStates...),
		"expired":              schema.BoolAttribute{MarkdownDescription: "Only expired builds, or only builds that have not expired.", Optional: true},
		"beta_group_id":        schema.StringAttribute{MarkdownDescription: "Only builds added to this beta group.", Optional: true},
		"builds":               listOf("The matching builds, newest upload first.", buildDataAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *buildsData, diags *diag.Diagnostics) {
		params := gen.BuildsGetCollectionParams{
			FilterVersion:                  filterParam[string](ctx, m.Versions, diags),
			FilterPreReleaseVersionVersion: filterParam[string](ctx, m.PreReleaseVersions, diags),
			FilterPreReleaseVersionPlatform: filterParam[gen.BuildsGetCollectionParamsFilterPreReleaseVersionPlatform](
				ctx, m.Platforms, diags),
			FilterProcessingState: filterParam[gen.BuildsGetCollectionParamsFilterProcessingState](ctx, m.ProcessingStates, diags),
		}
		if !m.AppID.IsNull() {
			params.FilterApp = &[]string{m.AppID.ValueString()}
		}
		if !m.BetaGroupID.IsNull() {
			params.FilterBetaGroups = &[]string{m.BetaGroupID.ValueString()}
		}
		if !m.Expired.IsNull() {
			params.FilterExpired = &[]string{strconv.FormatBool(m.Expired.ValueBool())}
		}
		builds, err := findBuilds(ctx, c, params)
		if err != nil {
			apiError(diags, "list builds", err)
			return
		}
		m.Builds = []buildData{}
		for _, b := range builds {
			if (m.AppID.IsNull() || b.AppID.Equal(m.AppID)) && passes(ctx, m.Versions, b.Version.ValueString(), diags) &&
				passes(ctx, m.PreReleaseVersions, b.PreReleaseVersion.ValueString(), diags) &&
				passes(ctx, m.Platforms, b.Platform.ValueString(), diags) &&
				passes(ctx, m.ProcessingStates, b.ProcessingState.ValueString(), diags) &&
				(m.Expired.IsNull() || b.Expired.Equal(m.Expired)) {
				m.Builds = append(m.Builds, b)
			}
		}
	})

// ---- pre-release versions ----

type preReleaseVersionData struct {
	ID       types.String `tfsdk:"id"`
	Version  types.String `tfsdk:"version"`
	Platform types.String `tfsdk:"platform"`
}

type preReleaseVersionsData struct {
	AppID              types.String            `tfsdk:"app_id"`
	Versions           types.Set               `tfsdk:"versions"`
	Platforms          types.Set               `tfsdk:"platforms"`
	PreReleaseVersions []preReleaseVersionData `tfsdk:"pre_release_versions"`
}

var newPreReleaseVersionsDataSource = listDataSource("pre_release_versions",
	"Lists an app's pre-release versions, the versions TestFlight builds belong to.",
	map[string]schema.Attribute{
		"app_id":    schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the app.", Required: true},
		"versions":  dsFilter("Versions to match, e.g. `1.2.0`."),
		"platforms": dsFilter("Platforms to match.", buildPlatforms...),
		"pre_release_versions": listOf("The matching versions, ordered by ID.", map[string]schema.Attribute{
			"id":       dsComputed("App Store Connect ID of the pre-release version."),
			"version":  dsComputed("Version, e.g. `1.2.0`."),
			"platform": dsComputed("Platform of the version."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *preReleaseVersionsData, diags *diag.Diagnostics) {
		out, err := c.PreReleaseVersionsGetCollectionWithResponse(ctx, &gen.PreReleaseVersionsGetCollectionParams{
			FilterApp:      &[]string{m.AppID.ValueString()},
			FilterVersion:  filterParam[string](ctx, m.Versions, diags),
			FilterPlatform: filterParam[gen.PreReleaseVersionsGetCollectionParamsFilterPlatform](ctx, m.Platforms, diags),
			Limit:          new(listAll),
		})
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "list pre-release versions", err)
			return
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.PreReleaseVersionsResponse) []gen.PrereleaseVersion { return p.Data },
			func(p *gen.PreReleaseVersionsResponse) *string { return p.Links.Next })
		if err != nil {
			apiError(diags, "list pre-release versions", err)
			return
		}
		m.PreReleaseVersions = []preReleaseVersionData{}
		for _, v := range all {
			a := asc.Value(v.Attributes)
			if passes(ctx, m.Versions, asc.Value(a.Version), diags) && passes(ctx, m.Platforms, string(asc.Value(a.Platform)), diags) {
				m.PreReleaseVersions = append(m.PreReleaseVersions, preReleaseVersionData{
					ID: types.StringValue(v.Id), Version: stringValue(a.Version), Platform: stringValue(a.Platform),
				})
			}
		}
	})

// ---- beta groups ----

type betaGroupData struct {
	ID                types.String `tfsdk:"id"`
	AppID             types.String `tfsdk:"app_id"`
	Name              types.String `tfsdk:"name"`
	Internal          types.Bool   `tfsdk:"internal"`
	PublicLinkEnabled types.Bool   `tfsdk:"public_link_enabled"`
	PublicLink        types.String `tfsdk:"public_link"`
}

func (m *betaGroupData) apply(g *gen.BetaGroup) {
	var r betaGroupModel
	r.apply(g)
	*m = betaGroupData{ID: r.ID, AppID: r.AppID, Name: r.Name, Internal: r.Internal, PublicLinkEnabled: r.PublicLinkEnabled, PublicLink: r.PublicLink}
}

var betaGroupDataAttributes = map[string]schema.Attribute{
	"id":                  dsComputed("App Store Connect ID of the beta group."),
	"app_id":              dsComputed("App Store Connect ID of the app."),
	"name":                dsComputed("Name of the group."),
	"internal":            schema.BoolAttribute{MarkdownDescription: "Whether the group is for internal testers.", Computed: true},
	"public_link_enabled": schema.BoolAttribute{MarkdownDescription: "Whether the public link is enabled.", Computed: true},
	"public_link":         dsComputed("The public link, when enabled."),
}

type betaGroupsData struct {
	AppID      types.String    `tfsdk:"app_id"`
	Names      types.Set       `tfsdk:"names"`
	Internal   types.Bool      `tfsdk:"internal"`
	BetaGroups []betaGroupData `tfsdk:"beta_groups"`
}

var newBetaGroupsDataSource = listDataSource("beta_groups", "Lists the beta groups that match every given filter.",
	map[string]schema.Attribute{
		"app_id":      schema.StringAttribute{MarkdownDescription: "Only groups of this app.", Optional: true},
		"names":       dsFilter("Names to match."),
		"internal":    schema.BoolAttribute{MarkdownDescription: "Only internal groups, or only external groups.", Optional: true},
		"beta_groups": listOf("The matching groups, ordered by ID.", betaGroupDataAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *betaGroupsData, diags *diag.Diagnostics) {
		params := gen.BetaGroupsGetCollectionParams{
			FilterName: filterParam[string](ctx, m.Names, diags),
			Include:    &[]gen.BetaGroupsGetCollectionParamsInclude{gen.BetaGroupsGetCollectionParamsIncludeApp},
			Limit:      new(listAll),
		}
		if !m.AppID.IsNull() {
			params.FilterApp = &[]string{m.AppID.ValueString()}
		}
		if !m.Internal.IsNull() {
			params.FilterIsInternalGroup = &[]string{strconv.FormatBool(m.Internal.ValueBool())}
		}
		out, err := c.BetaGroupsGetCollectionWithResponse(ctx, &params)
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "list beta groups", err)
			return
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.BetaGroupsResponse) []gen.BetaGroup { return p.Data },
			func(p *gen.BetaGroupsResponse) *string { return p.Links.Next })
		if err != nil {
			apiError(diags, "list beta groups", err)
			return
		}
		m.BetaGroups = []betaGroupData{}
		for i := range all {
			var g betaGroupData
			g.apply(&all[i])
			if (m.AppID.IsNull() || g.AppID.Equal(m.AppID)) && passes(ctx, m.Names, g.Name.ValueString(), diags) &&
				(m.Internal.IsNull() || g.Internal.Equal(m.Internal)) {
				m.BetaGroups = append(m.BetaGroups, g)
			}
		}
	})

// ---- beta testers ----

type betaTesterData struct {
	ID         types.String `tfsdk:"id"`
	Email      types.String `tfsdk:"email"`
	FirstName  types.String `tfsdk:"first_name"`
	LastName   types.String `tfsdk:"last_name"`
	InviteType types.String `tfsdk:"invite_type"`
	State      types.String `tfsdk:"state"`
}

func (m *betaTesterData) apply(t *gen.BetaTester) {
	a := asc.Value(t.Attributes)
	m.ID = types.StringValue(t.Id)
	m.Email = types.StringNull()
	if a.Email != nil {
		m.Email = types.StringValue(string(*a.Email))
	}
	m.FirstName, m.LastName = stringValue(a.FirstName), stringValue(a.LastName)
	m.InviteType, m.State = stringValue(a.InviteType), stringValue(a.State)
}

var betaTesterDataAttributes = map[string]schema.Attribute{
	"id":          dsComputed("App Store Connect ID of the tester."),
	"email":       dsComputed("Email address of the tester."),
	"first_name":  dsComputed("First name of the tester."),
	"last_name":   dsComputed("Last name of the tester."),
	"invite_type": dsComputed("How the tester was invited: `EMAIL` or `PUBLIC_LINK`."),
	"state":       dsComputed("State of the tester's invitation."),
}

var newBetaTesterDataSource = lookupDataSource("beta_tester", "Looks up a beta tester by App Store Connect ID or email.", "email",
	withAttributes(betaTesterDataAttributes, map[string]schema.Attribute{
		"id":    dsLookup("App Store Connect ID of the tester. Set this or `email`."),
		"email": dsLookup("Email address of the tester, in any case. Set this or `id`."),
	}),
	func(ctx context.Context, c *asc.Client, m *betaTesterData, diags *diag.Diagnostics) {
		var found *gen.BetaTester
		var err error
		if id := m.ID.ValueString(); id != "" {
			out, e := c.BetaTestersGetInstanceWithResponse(ctx, id, nil)
			if err = asc.Err(out, e); err == nil {
				found = &out.JSON200.Data
			}
		} else {
			found, err = betaTesterByEmail(ctx, c, m.Email.ValueString())
		}
		if err != nil {
			lookupError(diags, "beta tester", err)
			return
		}
		m.apply(found)
	})

type betaTestersData struct {
	AppID       types.String     `tfsdk:"app_id"`
	BetaGroupID types.String     `tfsdk:"beta_group_id"`
	BuildID     types.String     `tfsdk:"build_id"`
	Emails      types.Set        `tfsdk:"emails"`
	BetaTesters []betaTesterData `tfsdk:"beta_testers"`
}

var newBetaTestersDataSource = listDataSource("beta_testers", "Lists the beta testers that match every given filter.",
	map[string]schema.Attribute{
		"app_id":        schema.StringAttribute{MarkdownDescription: "Only testers of this app.", Optional: true},
		"beta_group_id": schema.StringAttribute{MarkdownDescription: "Only testers in this beta group.", Optional: true},
		"build_id":      schema.StringAttribute{MarkdownDescription: "Only testers given this build individually.", Optional: true},
		"emails":        dsFilter("Email addresses to match."),
		"beta_testers":  listOf("The matching testers, ordered by ID.", betaTesterDataAttributes),
	},
	func(ctx context.Context, c *asc.Client, m *betaTestersData, diags *diag.Diagnostics) {
		params := gen.BetaTestersGetCollectionParams{FilterEmail: filterParam[string](ctx, m.Emails, diags)}
		if !m.AppID.IsNull() {
			params.FilterApps = &[]string{m.AppID.ValueString()}
		}
		if !m.BetaGroupID.IsNull() {
			params.FilterBetaGroups = &[]string{m.BetaGroupID.ValueString()}
		}
		if !m.BuildID.IsNull() {
			params.FilterBuilds = &[]string{m.BuildID.ValueString()}
		}
		all, err := findBetaTesters(ctx, c, params)
		if err != nil {
			apiError(diags, "list beta testers", err)
			return
		}
		m.BetaTesters = []betaTesterData{}
		for i := range all {
			var t betaTesterData
			t.apply(&all[i])
			if passes(ctx, m.Emails, t.Email.ValueString(), diags) {
				m.BetaTesters = append(m.BetaTesters, t)
			}
		}
	})

// ---- recruitment criterion options ----

type criterionOptionData struct {
	DeviceFamily types.String `tfsdk:"device_family"`
	OSVersions   types.List   `tfsdk:"os_versions"`
}

type criterionOptionsData struct {
	Options []criterionOptionData `tfsdk:"options"`
}

var newBetaRecruitmentCriterionOptionsDataSource = listDataSource("beta_recruitment_criterion_options",
	"Lists the device families and OS versions `apple_beta_recruitment_criteria` filters can name.",
	map[string]schema.Attribute{
		"options": listOf("Device families and their OS versions.", map[string]schema.Attribute{
			"device_family": dsComputed("Device family, e.g. `IPHONE`."),
			"os_versions":   schema.ListAttribute{MarkdownDescription: "OS versions of the family.", Computed: true, ElementType: types.StringType},
		}),
	},
	func(ctx context.Context, c *asc.Client, m *criterionOptionsData, diags *diag.Diagnostics) {
		out, err := c.BetaRecruitmentCriterionOptionsGetCollectionWithResponse(ctx, nil)
		if err := asc.Err(out, err); err != nil {
			apiError(diags, "list recruitment criterion options", err)
			return
		}
		m.Options = []criterionOptionData{}
		for _, o := range out.JSON200.Data {
			for _, f := range asc.Value(asc.Value(o.Attributes).DeviceFamilyOsVersions) {
				versions, d := types.ListValueFrom(ctx, types.StringType, asc.Value(f.OsVersions))
				diags.Append(d...)
				m.Options = append(m.Options, criterionOptionData{DeviceFamily: stringValue(f.DeviceFamily), OSVersions: versions})
			}
		}
	})
