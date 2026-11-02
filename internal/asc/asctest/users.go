package asctest

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Users and invitations. Rules from "Add and edit users"
// (https://developer.apple.com/help/app-store-connect/manage-your-team/add-and-edit-users)
// and "Role permissions"
// (https://developer.apple.com/help/app-store-connect/reference/account-management/role-permissions/):
// people join by accepting an invitation, which expires 3 days after it is
// sent; there is one Account Holder; and Admin, Finance and Access to
// Reports users see every app, so their app access cannot be limited.

// AccountHolder is the email of the Account Holder the fake is seeded with.
const AccountHolder = "holder@example.com"

// invitationLifetime is how long an invitation can be accepted.
const invitationLifetime = 3 * 24 * time.Hour

type user struct {
	gen.User
	apps map[string]bool
}

type usersState struct {
	users       map[string]*user
	invitations map[string]*gen.UserInvitation
}

func newUsersState() *usersState {
	holder := &user{apps: map[string]bool{}, User: gen.User{Id: "U00000000", Type: gen.UserTypeUsers}}
	a := alloc(&holder.Attributes)
	a.Username, a.FirstName, a.LastName = new(AccountHolder), new("Account"), new("Holder")
	a.Roles = &[]gen.UserRole{gen.UserRoleACCOUNTHOLDER, gen.UserRoleADMIN}
	a.AllAppsVisible, a.ProvisioningAllowed = new(true), new(true)
	return &usersState{users: map[string]*user{holder.Id: holder}, invitations: map[string]*gen.UserInvitation{}}
}

// seesAllApps reports whether roles make every app visible.
func seesAllApps(roles []gen.UserRole) bool {
	return slices.ContainsFunc(roles, func(r gen.UserRole) bool {
		return r == gen.UserRoleACCOUNTHOLDER || r == gen.UserRoleADMIN || r == gen.UserRoleFINANCE || r == gen.UserRoleACCESSTOREPORTS
	})
}

// checkRoles validates roles and app visibility, writing a 409 when invalid.
func checkRoles(w http.ResponseWriter, roles []gen.UserRole, allAppsVisible *bool) bool {
	if len(roles) == 0 {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A user needs at least one role.", "/data/attributes/roles")
		return false
	}
	for _, r := range roles {
		if !r.Valid() {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Unknown role "+string(r)+".", "/data/attributes/roles")
			return false
		}
		if r == gen.UserRoleACCOUNTHOLDER {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The Account Holder role cannot be assigned.", "/data/attributes/roles")
			return false
		}
	}
	if seesAllApps(roles) && allAppsVisible != nil && !*allAppsVisible {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Admin, Finance and Access to Reports users can see all apps; their app access cannot be limited.", "/data/attributes/allAppsVisible")
		return false
	}
	return true
}

func (s *Server) renderUser(r *http.Request, u *user) gen.User {
	out := u.User
	attrs := *u.Attributes
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "users", u.Id)
	return out
}

func (s *Server) UsersGetCollection(w http.ResponseWriter, r *http.Request, params gen.UsersGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.User
	for _, u := range sortedValues(s.users.users) {
		a := u.Attributes
		hasRole := params.FilterRoles == nil
		for _, role := range asc.Value(params.FilterRoles) {
			hasRole = hasRole || slices.Contains(*a.Roles, gen.UserRole(role))
		}
		if hasRole && matches(params.FilterUsername, asc.Value(a.Username)) && anyIn(params.FilterVisibleApps, u.apps) {
			items = append(items, s.renderUser(r, u))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.UsersResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) UsersGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.UsersGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users.users[id]
	if !ok {
		notFound(w, "users", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.UserResponse{Data: s.renderUser(r, u), Links: docLinks(r)})
}

// UsersUpdateInstance changes roles and app visibility
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-users-_id_).
func (s *Server) UsersUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.UserUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users.users[id]
	if !ok {
		notFound(w, "users", id)
		return
	}
	a := u.Attributes
	if slices.Contains(*a.Roles, gen.UserRoleACCOUNTHOLDER) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The Account Holder cannot be changed through the API.", "/data/id")
		return
	}
	in := asc.Value(req.Data.Attributes)
	roles := *a.Roles
	if in.Roles != nil {
		roles = *in.Roles
	}
	visible := in.AllAppsVisible
	if visible == nil && in.Roles != nil && seesAllApps(roles) {
		visible = new(true)
	}
	if !checkRoles(w, roles, visible) {
		return
	}
	a.Roles = new(slices.Clone(roles))
	if visible != nil {
		a.AllAppsVisible = new(*visible)
	}
	if in.ProvisioningAllowed != nil {
		a.ProvisioningAllowed = new(*in.ProvisioningAllowed)
	}
	writeJSON(w, http.StatusOK, gen.UserResponse{Data: s.renderUser(r, u), Links: docLinks(r)})
}

// UsersDeleteInstance removes a user from the team
// (https://developer.apple.com/documentation/appstoreconnectapi/delete-v1-users-_id_).
func (s *Server) UsersDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users.users[id]
	if !ok {
		notFound(w, "users", id)
		return
	}
	if slices.Contains(*u.Attributes.Roles, gen.UserRoleACCOUNTHOLDER) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The Account Holder cannot be removed.", "/data/id")
		return
	}
	delete(s.users.users, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) appExists(id string) bool { _, ok := s.apps[id]; return ok }

// UsersVisibleAppsCreateToManyRelationship gives a user access to apps
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-users-_id_-relationships-visibleapps).
func (s *Server) UsersVisibleAppsCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users.users[id]
	if !ok {
		notFound(w, "users", id)
		return
	}
	ids, ok := linkageIDs(w, r, "apps", s.appExists)
	if !ok {
		return
	}
	if asc.Value(u.Attributes.AllAppsVisible) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The user can see all apps; set allAppsVisible to false to limit their app access.", "/data")
		return
	}
	for _, a := range ids {
		u.apps[a] = true
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) UsersVisibleAppsDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users.users[id]
	if !ok {
		notFound(w, "users", id)
		return
	}
	ids, ok := linkageIDs(w, r, "apps", s.appExists)
	if !ok {
		return
	}
	for _, a := range ids {
		delete(u.apps, a)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) UsersVisibleAppsGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.UsersVisibleAppsGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users.users[id]
	if !ok {
		notFound(w, "users", id)
		return
	}
	ids, links, meta, ok := page(w, r, sortedKeys(u.apps), params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	resp := gen.UserVisibleAppsLinkagesResponse{Links: links, Meta: meta}
	for _, a := range ids {
		e := push(&resp.Data)
		e.Id, e.Type = a, "apps"
	}
	resp.Data = nonNil(resp.Data)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) UserInvitationsGetCollection(w http.ResponseWriter, r *http.Request, params gen.UserInvitationsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.UserInvitation
	for _, inv := range sortedValues(s.users.invitations) {
		a := inv.Attributes
		if matches(params.FilterEmail, string(asc.Value(a.Email))) {
			item := *inv
			item.Links = resourceLinks(r, "userInvitations", inv.Id)
			items = append(items, item)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.UserInvitationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// UserInvitationsCreateInstance invites someone to the team
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-userinvitations).
// An email that already belongs to a user, or has a pending invitation,
// cannot be invited.
func (s *Server) UserInvitationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.UserInvitationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	if !checkRoles(w, in.Roles, in.AllAppsVisible) {
		return
	}
	email := string(in.Email)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, u := range s.users.users {
		if strings.EqualFold(asc.Value(u.Attributes.Username), email) {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", email+" is already a member of the team.", "/data/attributes/email")
			return
		}
	}
	for _, inv := range s.users.invitations {
		if strings.EqualFold(string(asc.Value(inv.Attributes.Email)), email) && now.Before(inv.Attributes.ExpirationDate.Time) {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", email+" already has a pending invitation.", "/data/attributes/email")
			return
		}
	}
	inv := &gen.UserInvitation{Id: s.newID("J"), Type: gen.UserInvitationTypeUserInvitations}
	a := alloc(&inv.Attributes)
	a.Email, a.FirstName, a.LastName = new(in.Email), new(in.FirstName), new(in.LastName)
	a.Roles = new(slices.Clone(in.Roles))
	a.AllAppsVisible = new(asc.Value(in.AllAppsVisible) || seesAllApps(in.Roles))
	a.ProvisioningAllowed = new(asc.Value(in.ProvisioningAllowed))
	a.ExpirationDate = new(gen.DateTime{Time: now.Add(invitationLifetime).UTC().Truncate(time.Second)})
	s.users.invitations[inv.Id] = inv
	writeJSON(w, http.StatusCreated, gen.UserInvitationResponse{Data: *inv, Links: docLinks(r)})
}

// UserInvitationsDeleteInstance cancels a pending invitation
// (https://developer.apple.com/documentation/appstoreconnectapi/delete-v1-userinvitations-_id_).
func (s *Server) UserInvitationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users.invitations[id]; !ok {
		notFound(w, "userInvitations", id)
		return
	}
	delete(s.users.invitations, id)
	w.WriteHeader(http.StatusNoContent)
}

// AcceptInvitation accepts the pending invitation for email, as the invitee
// does, and returns the new user's ID. It returns "" when there is no
// pending invitation.
func (s *Server) AcceptInvitation(email string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, inv := range s.users.invitations {
		a := inv.Attributes
		if !strings.EqualFold(string(asc.Value(a.Email)), email) || !s.now().Before(a.ExpirationDate.Time) {
			continue
		}
		u := &user{apps: map[string]bool{}, User: gen.User{Id: s.newID("U"), Type: gen.UserTypeUsers}}
		ua := alloc(&u.Attributes)
		ua.Username, ua.FirstName, ua.LastName = new(string(*a.Email)), a.FirstName, a.LastName
		ua.Roles, ua.AllAppsVisible, ua.ProvisioningAllowed = a.Roles, a.AllAppsVisible, a.ProvisioningAllowed
		s.users.users[u.Id] = u
		delete(s.users.invitations, id)
		return u.Id
	}
	return ""
}

// ExpireInvitation makes the pending invitation for email expire now.
func (s *Server) ExpireInvitation(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inv := range s.users.invitations {
		if strings.EqualFold(string(asc.Value(inv.Attributes.Email)), email) {
			inv.Attributes.ExpirationDate = new(gen.DateTime{Time: s.now().Add(-time.Minute).UTC().Truncate(time.Second)})
		}
	}
}
