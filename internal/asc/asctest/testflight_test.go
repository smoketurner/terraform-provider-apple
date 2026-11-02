package asctest

import (
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func createGroup(t *testing.T, c *asc.Client, appID, name string, internal bool, publicLink bool) (string, error) {
	t.Helper()
	var body gen.BetaGroupCreateRequest
	body.Data.Type = gen.BetaGroupCreateRequestDataTypeBetaGroups
	body.Data.Attributes.Name = name
	body.Data.Attributes.IsInternalGroup = new(internal)
	if publicLink {
		body.Data.Attributes.PublicLinkEnabled = new(true)
	}
	body.Data.Relationships.App.Data.Id = appID
	body.Data.Relationships.App.Data.Type = gen.BetaGroupCreateRequestDataRelationshipsAppDataTypeApps
	out, err := c.BetaGroupsCreateInstanceWithResponse(t.Context(), body)
	if err := asc.Err(out, err); err != nil {
		return "", err
	}
	return out.JSON201.Data.Id, nil
}

func mustNot(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("got %v, want an error containing %q", err, want)
	}
}

func addBuilds(t *testing.T, c *asc.Client, buildID string, groups ...string) error {
	t.Helper()
	var body gen.BuildBetaGroupsLinkagesRequest
	for _, g := range groups {
		e := push(&body.Data)
		e.Id, e.Type = g, gen.BuildBetaGroupsLinkagesRequestDataTypeBetaGroups
	}
	out, err := c.BuildsBetaGroupsCreateToManyRelationshipWithResponse(t.Context(), buildID, body)
	return asc.Err(out, err)
}

func submitReview(t *testing.T, c *asc.Client, buildID string) error {
	t.Helper()
	var body gen.BetaAppReviewSubmissionCreateRequest
	body.Data.Type = gen.BetaAppReviewSubmissionCreateRequestDataTypeBetaAppReviewSubmissions
	body.Data.Relationships.Build.Data.Id = buildID
	body.Data.Relationships.Build.Data.Type = gen.BetaAppReviewSubmissionCreateRequestDataRelationshipsBuildDataTypeBuilds
	out, err := c.BetaAppReviewSubmissionsCreateInstanceWithResponse(t.Context(), body)
	return asc.Err(out, err)
}

func TestBetaGroupRules(t *testing.T) {
	s, c := newServer(t)
	app := s.AddApp("App", "com.example.app", "SKU")
	_, err := createGroup(t, c, app, "External", false, false)
	mustNot(t, err, "first create an internal group")
	_, err = createGroup(t, c, app, "Internal", true, true)
	mustNot(t, err, "only available for external groups")
	internal, err := createGroup(t, c, app, "Internal", true, false)
	if err != nil {
		t.Fatal(err)
	}
	external, err := createGroup(t, c, app, "External", false, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.BetaGroupsGetInstanceWithResponse(t.Context(), external, &gen.BetaGroupsGetInstanceParams{
		Include: &[]gen.BetaGroupsGetInstanceParamsInclude{gen.BetaGroupsGetInstanceParamsIncludeApp},
	})
	if err := asc.Err(got, err); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(asc.Value(got.JSON200.Data.Attributes.PublicLink), "https://testflight.apple.com/join/") ||
		got.JSON200.Data.Relationships.App.Data.Id != app {
		t.Errorf("external group = %s", got.Body)
	}

	internalOnly := s.AddBuild(app, BuildOptions{Version: "1", Marketing: "1.0", InternalOnly: true, UsesNonExemptEncryption: new(false)})
	mustNot(t, addBuilds(t, c, internalOnly, external), "can only be added to internal groups")
	if err := addBuilds(t, c, internalOnly, internal); err != nil {
		t.Fatal(err)
	}
	if _, groups := s.BuildTesters(internalOnly); len(groups) != 1 || groups[0] != internal {
		t.Errorf("build groups = %v", groups)
	}
}

func TestBetaReviewRules(t *testing.T) {
	s, c := newServer(t)
	app := s.AddApp("App", "com.example.app", "SKU")
	missing := s.AddBuild(app, BuildOptions{Version: "1", Marketing: "1.0"})
	mustNot(t, submitReview(t, c, missing), "MISSING_EXPORT_COMPLIANCE")

	var update gen.BuildUpdateRequest
	update.Data.Type, update.Data.Id = gen.BuildUpdateRequestDataTypeBuilds, missing
	asc.Alloc(&update.Data.Attributes).UsesNonExemptEncryption = new(false)
	out, err := c.BuildsUpdateInstanceWithResponse(t.Context(), missing, update)
	if err := asc.Err(out, err); err != nil {
		t.Fatal(err)
	}
	if err := submitReview(t, c, missing); err != nil {
		t.Fatal(err)
	}
	second := s.AddBuild(app, BuildOptions{Version: "2", Marketing: "1.0", UsesNonExemptEncryption: new(false)})
	mustNot(t, submitReview(t, c, second), "already in beta review")
	other := s.AddBuild(app, BuildOptions{Version: "3", Marketing: "1.1", UsesNonExemptEncryption: new(false)})
	if err := submitReview(t, c, other); err != nil {
		t.Errorf("a build of another version: %v", err)
	}

	s.ApproveBetaReview(missing)
	detail, err := c.BuildBetaDetailsGetCollectionWithResponse(t.Context(), &gen.BuildBetaDetailsGetCollectionParams{FilterBuild: &[]string{missing}})
	if err := asc.Err(detail, err); err != nil {
		t.Fatal(err)
	}
	if got := *detail.JSON200.Data[0].Attributes.ExternalBuildState; got != gen.ExternalBetaStateBETAAPPROVED {
		t.Errorf("external state = %s", got)
	}
	if err := submitReview(t, c, second); err != nil {
		t.Errorf("after approval, another build of the version: %v", err)
	}

	// Expiring a build cannot be undone.
	update.Data.Attributes.UsesNonExemptEncryption = nil
	update.Data.Attributes.Expired = new(true)
	out, err = c.BuildsUpdateInstanceWithResponse(t.Context(), missing, update)
	if err := asc.Err(out, err); err != nil {
		t.Fatal(err)
	}
	update.Data.Attributes.Expired = new(false)
	out, err = c.BuildsUpdateInstanceWithResponse(t.Context(), missing, update)
	mustNot(t, asc.Err(out, err), "cannot be made available again")
}

func TestBetaTesterRules(t *testing.T) {
	s, c := newServer(t)
	app := s.AddApp("App", "com.example.app", "SKU")
	group, err := createGroup(t, c, app, "Internal", true, false)
	if err != nil {
		t.Fatal(err)
	}
	create := func(email string, groups ...string) (*gen.BetaTestersCreateInstanceResponse, error) {
		var body gen.BetaTesterCreateRequest
		body.Data.Type = gen.BetaTesterCreateRequestDataTypeBetaTesters
		body.Data.Attributes.Email = openapi_types.Email(email)
		for _, g := range groups {
			e := asc.Append(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).BetaGroups).Data)
			e.Id, e.Type = g, gen.BetaTesterCreateRequestDataRelationshipsBetaGroupsDataTypeBetaGroups
		}
		return c.BetaTestersCreateInstanceWithResponse(t.Context(), body)
	}
	out, err := create("lone@example.com")
	mustNot(t, asc.Err(out, err), "must be assigned to a group or a build")
	out, err = create("ann@example.com", group)
	if err := asc.Err(out, err); err != nil {
		t.Fatal(err)
	}
	tester := out.JSON201.Data.Id
	out, err = create("ANN@example.com", group)
	mustNot(t, asc.Err(out, err), "already exists")

	invite := func(appID string) error {
		var body gen.BetaTesterInvitationCreateRequest
		body.Data.Type = gen.BetaTesterInvitationCreateRequestDataTypeBetaTesterInvitations
		body.Data.Relationships.App.Data.Id = appID
		body.Data.Relationships.App.Data.Type = gen.BetaTesterInvitationCreateRequestDataRelationshipsAppDataTypeApps
		d := asc.Alloc(&asc.Alloc(&body.Data.Relationships.BetaTester).Data)
		d.Id, d.Type = tester, gen.BetaTesterInvitationCreateRequestDataRelationshipsBetaTesterDataTypeBetaTesters
		resp, err := c.BetaTesterInvitationsCreateInstanceWithResponse(t.Context(), body)
		return asc.Err(resp, err)
	}
	if err := invite(app); err != nil {
		t.Fatal(err)
	}
	other := s.AddApp("Other", "com.example.other", "SKU2")
	mustNot(t, invite(other), "not assigned to a group or build")
	if got := s.Invitations(); len(got) != 1 || got[0] != (Invitation{AppID: app, TesterID: tester}) {
		t.Errorf("invitations = %v", got)
	}

	// Removing the tester from the app removes their groups.
	var remove gen.AppBetaTestersLinkagesRequest
	e := push(&remove.Data)
	e.Id, e.Type = tester, gen.AppBetaTestersLinkagesRequestDataTypeBetaTesters
	rm, err := c.AppsBetaTestersDeleteToManyRelationshipWithResponse(t.Context(), app, remove)
	if err := asc.Err(rm, err); err != nil {
		t.Fatal(err)
	}
	links, err := c.BetaTestersBetaGroupsGetToManyRelationshipWithResponse(t.Context(), tester, nil)
	if err := asc.Err(links, err); err != nil {
		t.Fatal(err)
	}
	if len(links.JSON200.Data) != 0 {
		t.Errorf("groups after removal = %s", links.Body)
	}

	del, err := c.BetaTestersDeleteInstanceWithResponse(t.Context(), tester)
	if err := asc.Err(del, err); err != nil {
		t.Fatal(err)
	}
	got, err := c.BetaTestersGetInstanceWithResponse(t.Context(), tester, nil)
	if err := asc.Err(got, err); !asc.IsNotFound(err) {
		t.Errorf("deleted tester: %v", err)
	}
}

func TestBuildIncludes(t *testing.T) {
	s, c := newServer(t)
	app := s.AddApp("App", "com.example.app", "SKU")
	id := s.AddBuild(app, BuildOptions{Version: "7", Marketing: "2.0", Platform: gen.PlatformMACOS, UsesNonExemptEncryption: new(true)})
	out, err := c.BuildsGetInstanceWithResponse(t.Context(), id, &gen.BuildsGetInstanceParams{Include: &[]gen.BuildsGetInstanceParamsInclude{
		gen.BuildsGetInstanceParamsIncludeApp, gen.BuildsGetInstanceParamsIncludePreReleaseVersion, gen.BuildsGetInstanceParamsIncludeBuildBetaDetail,
	}})
	if err := asc.Err(out, err); err != nil {
		t.Fatal(err)
	}
	b := out.JSON200
	if b.Data.Relationships.App.Data.Id != app || len(*b.Included) != 2 {
		t.Fatalf("build = %s", out.Body)
	}
	pre, err := (*b.Included)[0].AsPrereleaseVersion()
	if err != nil || *pre.Attributes.Version != "2.0" || *pre.Attributes.Platform != gen.PlatformMACOS {
		t.Errorf("pre-release version = %+v, %v", pre, err)
	}
	detail, err := (*b.Included)[1].AsBuildBetaDetail()
	if err != nil || *detail.Attributes.InternalBuildState != gen.InternalBetaStateINBETATESTING {
		t.Errorf("build beta detail = %+v, %v", detail, err)
	}
}
