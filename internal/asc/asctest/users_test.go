package asctest

import (
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func invite(t *testing.T, c *asc.Client, email string, allApps *bool, roles ...gen.UserRole) error {
	t.Helper()
	var body gen.UserInvitationCreateRequest
	body.Data.Type = gen.UserInvitationCreateRequestDataTypeUserInvitations
	a := &body.Data.Attributes
	a.Email, a.FirstName, a.LastName, a.Roles, a.AllAppsVisible = openapi_types.Email(email), "First", "Last", roles, allApps
	out, err := c.UserInvitationsCreateInstanceWithResponse(t.Context(), body)
	return asc.Err(out, err)
}

func TestUserRules(t *testing.T) {
	s, c := newServer(t)
	mustNot(t, invite(t, c, "boss@example.com", nil, gen.UserRoleACCOUNTHOLDER), "Account Holder role cannot be assigned")
	mustNot(t, invite(t, c, "admin@example.com", new(false), gen.UserRoleADMIN), "cannot be limited")
	mustNot(t, invite(t, c, "nobody@example.com", nil), "at least one role")
	mustNot(t, invite(t, c, AccountHolder, nil, gen.UserRoleDEVELOPER), "already a member")
	if err := invite(t, c, "dev@example.com", new(false), gen.UserRoleDEVELOPER); err != nil {
		t.Fatal(err)
	}
	mustNot(t, invite(t, c, "DEV@example.com", new(false), gen.UserRoleDEVELOPER), "already has a pending invitation")
	s.ExpireInvitation("dev@example.com")
	if s.AcceptInvitation("dev@example.com") != "" {
		t.Error("an expired invitation was accepted")
	}
	if err := invite(t, c, "dev@example.com", new(false), gen.UserRoleDEVELOPER); err != nil {
		t.Errorf("inviting again after expiry: %v", err)
	}
	dev := s.AcceptInvitation("dev@example.com")
	if dev == "" {
		t.Fatal("invitation was not accepted")
	}

	// App access can only be limited for users who do not see all apps.
	app := s.AddApp("App", "com.example.app", "SKU")
	var body gen.UserVisibleAppsLinkagesRequest
	e := push(&body.Data)
	e.Id, e.Type = app, gen.UserVisibleAppsLinkagesRequestDataTypeApps
	add, err := c.UsersVisibleAppsCreateToManyRelationshipWithResponse(t.Context(), dev, body)
	if err := asc.Err(add, err); err != nil {
		t.Fatal(err)
	}
	add, err = c.UsersVisibleAppsCreateToManyRelationshipWithResponse(t.Context(), "U00000000", body)
	mustNot(t, asc.Err(add, err), "can see all apps")

	// Becoming an Admin makes every app visible.
	var update gen.UserUpdateRequest
	update.Data.Type, update.Data.Id = gen.UserUpdateRequestDataTypeUsers, dev
	asc.Alloc(&update.Data.Attributes).Roles = &[]gen.UserRole{gen.UserRoleADMIN}
	up, err := c.UsersUpdateInstanceWithResponse(t.Context(), dev, update)
	if err := asc.Err(up, err); err != nil {
		t.Fatal(err)
	}
	if !*up.JSON200.Data.Attributes.AllAppsVisible {
		t.Error("an Admin's app access is limited")
	}

	del, err := c.UsersDeleteInstanceWithResponse(t.Context(), "U00000000")
	mustNot(t, asc.Err(del, err), "Account Holder cannot be removed")
}
