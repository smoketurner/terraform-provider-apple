package asctest

import (
	"net/http"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Subscription groups and auto-renewable subscriptions. A group's display
// name and custom app name, and a subscription's display name, description
// and promotional image, belong to versions, which work as in-app purchase
// versions do (see iap.go): edited in Prepare for Submission, read-only
// after review, and the next version starts with copies of the previous
// one's localizations and image. A subscription's App Review screenshot
// belongs to the subscription.

// metaVersion is a version of a group's or subscription's metadata. Its
// localizations hold attribute values by JSON name.
type metaVersion struct {
	id, parentID  string
	number        int
	state         string
	localizations map[string]*metaLocalization
}

type metaLocalization struct {
	id, locale string
	values     map[string]*string
}

type subscriptionGroup struct {
	gen.SubscriptionGroup
	appID    string
	versions []string
}

type subscription struct {
	gen.Subscription
	groupID  string
	versions []string
}

const (
	subscriptionImage            fileKind = "subscriptionImages"
	subscriptionReviewScreenshot fileKind = "subscriptionAppStoreReviewScreenshots"
)

type subscriptionsState struct {
	groups        map[string]*subscriptionGroup
	subscriptions map[string]*subscription
	versions      map[string]*metaVersion
}

func newSubscriptionsState() *subscriptionsState {
	return &subscriptionsState{groups: map[string]*subscriptionGroup{}, subscriptions: map[string]*subscription{}, versions: map[string]*metaVersion{}}
}

func (s *Server) latestMetaVersion(ids []string) *metaVersion {
	if len(ids) == 0 {
		return nil
	}
	return s.subs.versions[ids[len(ids)-1]]
}

// startMetaVersion starts a group's or subscription's next version, copying
// the latest one's localizations; it reports false when one is open.
func (s *Server) startMetaVersion(w http.ResponseWriter, parentID string, versions *[]string, prefix string) *metaVersion {
	previous := s.latestMetaVersion(*versions)
	if previous != nil && (previous.state == "PREPARE_FOR_SUBMISSION" || slices.Contains([]string{"READY_FOR_REVIEW", "WAITING_FOR_REVIEW", "IN_REVIEW"}, previous.state)) {
		stateError(w, "The product already has an open version.")
		return nil
	}
	v := &metaVersion{id: s.newID(prefix), parentID: parentID, number: len(*versions) + 1, state: "PREPARE_FOR_SUBMISSION", localizations: map[string]*metaLocalization{}}
	if previous != nil {
		for _, l := range previous.localizations {
			c := &metaLocalization{id: s.newID("L"), locale: l.locale, values: map[string]*string{}}
			for k, val := range l.values {
				c.values[k] = val
			}
			v.localizations[c.id] = c
		}
		for _, f := range s.files {
			if f.kind == subscriptionImage && f.parentID == previous.id {
				c := s.newFile(subscriptionImage, v.id, f.fileName, f.size)
				c.state, c.checksum = f.state, f.checksum
				break
			}
		}
	}
	s.subs.versions[v.id] = v
	*versions = append(*versions, v.id)
	return v
}

func (s *Server) editableMetaVersion(w http.ResponseWriter, id string) *metaVersion {
	v, ok := s.subs.versions[id]
	if !ok {
		notFound(w, "versions", id)
		return nil
	}
	if v.state != "PREPARE_FOR_SUBMISSION" {
		stateError(w, "The version is read-only in its current state: "+v.state+".")
		return nil
	}
	return v
}

// addMetaLocalization adds a localization to an editable version.
func (s *Server) addMetaLocalization(w http.ResponseWriter, versionID, locale string, values map[string]*string) *metaLocalization {
	v := s.editableMetaVersion(w, versionID)
	if v == nil {
		return nil
	}
	for _, l := range v.localizations {
		if l.locale == locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A localization for locale "+locale+" already exists.", "/data/attributes/locale")
			return nil
		}
	}
	l := &metaLocalization{id: s.newID("L"), locale: locale, values: values}
	v.localizations[l.id] = l
	return l
}

func (s *Server) findMetaLocalization(w http.ResponseWriter, id string) (*metaLocalization, *metaVersion) {
	for _, v := range s.subs.versions {
		if l, ok := v.localizations[id]; ok {
			if s.editableMetaVersion(w, v.id) == nil {
				return nil, nil
			}
			return l, v
		}
	}
	notFound(w, "localizations", id)
	return nil, nil
}

func (s *Server) listMetaVersions(ids []string) []*metaVersion {
	var out []*metaVersion
	for _, id := range ids {
		out = append(out, s.subs.versions[id])
	}
	return out
}

func approvedOnce(vs []*metaVersion) bool {
	return slices.ContainsFunc(vs, func(v *metaVersion) bool { return v.state == "APPROVED" || v.state == "REPLACED_WITH_NEW_VERSION" })
}

// approveLatest approves the latest version and replaces the previously
// approved one.
func (s *Server) approveLatest(ids []string) {
	latest := s.latestMetaVersion(ids)
	for _, v := range s.listMetaVersions(ids) {
		if v != latest && v.state == "APPROVED" {
			v.state = "REPLACED_WITH_NEW_VERSION"
		}
	}
	latest.state = "APPROVED"
}

// ---- groups ----

func (s *Server) SubscriptionGroupsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionGroupCreateRequest
	if !decode(w, r, &req) {
		return
	}
	appID, name := req.Data.Relationships.App.Data.Id, req.Data.Attributes.ReferenceName
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	for _, g := range s.subs.groups {
		if g.appID == appID && *g.Attributes.ReferenceName == name {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The reference name "+name+" is already used.", "/data/attributes/referenceName")
			return
		}
	}
	g := &subscriptionGroup{appID: appID, SubscriptionGroup: gen.SubscriptionGroup{Id: s.newID("G"), Type: gen.SubscriptionGroupTypeSubscriptionGroups}}
	alloc(&g.Attributes).ReferenceName = new(name)
	s.subs.groups[g.Id] = g
	writeJSON(w, http.StatusCreated, gen.SubscriptionGroupResponse{Data: g.SubscriptionGroup, Links: docLinks(r)})
}

func (s *Server) SubscriptionGroupsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionGroupsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.subs.groups[id]
	if !ok {
		notFound(w, "subscriptionGroups", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGroupResponse{Data: g.SubscriptionGroup, Links: docLinks(r)})
}

func (s *Server) AppsSubscriptionGroupsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsSubscriptionGroupsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.SubscriptionGroup
	for _, g := range sortedValues(s.subs.groups) {
		if g.appID == id && matches(params.FilterReferenceName, *g.Attributes.ReferenceName) {
			items = append(items, g.SubscriptionGroup)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGroupsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionGroupsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionGroupUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.subs.groups[id]
	if !ok {
		notFound(w, "subscriptionGroups", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		setString(&g.Attributes.ReferenceName, in.ReferenceName)
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGroupResponse{Data: g.SubscriptionGroup, Links: docLinks(r)})
}

// SubscriptionGroupsDeleteInstance deletes a group that has no
// subscriptions and was never approved.
func (s *Server) SubscriptionGroupsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.subs.groups[id]
	if !ok {
		notFound(w, "subscriptionGroups", id)
		return
	}
	for _, sub := range s.subs.subscriptions {
		if sub.groupID == id {
			stateError(w, "A subscription group with subscriptions can't be deleted.")
			return
		}
	}
	if approvedOnce(s.listMetaVersions(g.versions)) {
		stateError(w, "An approved subscription group can't be deleted.")
		return
	}
	delete(s.subs.groups, id)
	w.WriteHeader(http.StatusNoContent)
}

func renderGroupVersion(v *metaVersion) gen.SubscriptionGroupVersion {
	out := gen.SubscriptionGroupVersion{Id: v.id, Type: gen.SubscriptionGroupVersionTypeSubscriptionGroupVersions}
	a := alloc(&out.Attributes)
	a.Version, a.State = new(v.number), new(gen.SubscriptionGroupVersionAttributesState(v.state))
	return out
}

func (s *Server) SubscriptionGroupsVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionGroupsVersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.subs.groups[id]
	if !ok {
		notFound(w, "subscriptionGroups", id)
		return
	}
	var items []gen.SubscriptionGroupVersion
	for _, v := range s.listMetaVersions(g.versions) {
		items = append(items, renderGroupVersion(v))
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGroupVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionGroupVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionGroupVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groupID := req.Data.Relationships.SubscriptionGroup.Data.Id
	g, ok := s.subs.groups[groupID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptionGroups with id '"+groupID+"'", "/data/relationships/subscriptionGroup")
		return
	}
	if v := s.startMetaVersion(w, groupID, &g.versions, "W"); v != nil {
		writeJSON(w, http.StatusCreated, gen.SubscriptionGroupVersionResponse{Data: renderGroupVersion(v), Links: docLinks(r)})
	}
}

func renderGroupLocalization(l *metaLocalization) gen.SubscriptionGroupLocalizationV2 {
	out := gen.SubscriptionGroupLocalizationV2{Id: l.id, Type: gen.SubscriptionGroupLocalizationV2TypeSubscriptionGroupLocalizations}
	a := alloc(&out.Attributes)
	a.Locale, a.Name, a.CustomAppName = new(l.locale), l.values["name"], l.values["customAppName"]
	return out
}

func (s *Server) SubscriptionGroupVersionsLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionGroupVersionsLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.subs.versions[id]
	if !ok {
		notFound(w, "subscriptionGroupVersions", id)
		return
	}
	var items []gen.SubscriptionGroupLocalizationV2
	for _, l := range sortedValues(v.localizations) {
		items = append(items, renderGroupLocalization(l))
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGroupLocalizationsV2Response{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionGroupLocalizationsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionGroupLocalizationV2CreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	values := map[string]*string{"name": new(in.Name), "customAppName": in.CustomAppName}
	if l := s.addMetaLocalization(w, req.Data.Relationships.Version.Data.Id, in.Locale, values); l != nil {
		writeJSON(w, http.StatusCreated, gen.SubscriptionGroupLocalizationV2Response{Data: renderGroupLocalization(l), Links: docLinks(r)})
	}
}

func (s *Server) SubscriptionGroupLocalizationsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionGroupLocalizationV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, _ := s.findMetaLocalization(w, id)
	if l == nil {
		return
	}
	if in := req.Data.Attributes; in != nil {
		name, app := l.values["name"], l.values["customAppName"]
		setString(&name, in.Name)
		setString(&app, in.CustomAppName)
		l.values["name"], l.values["customAppName"] = name, app
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGroupLocalizationV2Response{Data: renderGroupLocalization(l), Links: docLinks(r)})
}

func (s *Server) SubscriptionGroupLocalizationsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, v := s.findMetaLocalization(w, id)
	if l == nil {
		return
	}
	delete(v.localizations, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- subscriptions ----

func (s *Server) refreshSubscriptionState(sub *subscription) {
	state := gen.SubscriptionAttributesStateMISSINGMETADATA
	for _, v := range s.listMetaVersions(sub.versions) {
		switch {
		case v.state == "APPROVED":
			state = gen.SubscriptionAttributesStateAPPROVED
		case slices.Contains([]string{"READY_FOR_REVIEW", "WAITING_FOR_REVIEW", "IN_REVIEW"}, v.state):
			state = gen.SubscriptionAttributesStateWAITINGFORREVIEW
		case v.state == "PREPARE_FOR_SUBMISSION" && len(v.localizations) > 0 && state != gen.SubscriptionAttributesStateAPPROVED:
			state = gen.SubscriptionAttributesStateREADYTOSUBMIT
		}
	}
	sub.Attributes.State = new(state)
}

func (s *Server) productIDUsed(appID, productID string) bool {
	for _, p := range s.iap.iaps {
		if p.appID == appID && *p.Attributes.ProductId == productID {
			return true
		}
	}
	for _, sub := range s.subs.subscriptions {
		if s.subs.groups[sub.groupID].appID == appID && *sub.Attributes.ProductId == productID {
			return true
		}
	}
	return false
}

func (s *Server) SubscriptionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	groupID := req.Data.Relationships.Group.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.subs.groups[groupID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptionGroups with id '"+groupID+"'", "/data/relationships/group")
		return
	}
	if s.productIDUsed(g.appID, in.ProductId) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The product ID "+in.ProductId+" is already used.", "/data/attributes/productId")
		return
	}
	level := 1
	for _, sub := range s.subs.subscriptions {
		if sub.groupID == groupID {
			level++
		}
	}
	sub := &subscription{groupID: groupID, Subscription: gen.Subscription{Id: s.newID("S"), Type: gen.SubscriptionTypeSubscriptions}}
	a := alloc(&sub.Attributes)
	a.Name, a.ProductId, a.ReviewNote = new(in.Name), new(in.ProductId), in.ReviewNote
	a.FamilySharable, a.GroupLevel = new(asc.Value(in.FamilySharable)), new(level)
	if in.GroupLevel != nil {
		a.GroupLevel = new(*in.GroupLevel)
	}
	if in.SubscriptionPeriod != nil {
		a.SubscriptionPeriod = new(gen.SubscriptionAttributesSubscriptionPeriod(*in.SubscriptionPeriod))
	}
	g2 := alloc(&alloc(&alloc(&sub.Relationships).Group).Data)
	g2.Id, g2.Type = groupID, "subscriptionGroups"
	s.subs.subscriptions[sub.Id] = sub
	s.refreshSubscriptionState(sub)
	writeJSON(w, http.StatusCreated, gen.SubscriptionResponse{Data: sub.Subscription, Links: docLinks(r)})
}

func (s *Server) SubscriptionsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs.subscriptions[id]
	if !ok {
		notFound(w, "subscriptions", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionResponse{Data: sub.Subscription, Links: docLinks(r)})
}

func (s *Server) SubscriptionGroupsSubscriptionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionGroupsSubscriptionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.groups[id]; !ok {
		notFound(w, "subscriptionGroups", id)
		return
	}
	var items []gen.Subscription
	for _, sub := range sortedValues(s.subs.subscriptions) {
		a := sub.Attributes
		if sub.groupID == id && matches(params.FilterProductId, *a.ProductId) && matches(params.FilterState, string(*a.State)) {
			items = append(items, sub.Subscription)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs.subscriptions[id]
	if !ok {
		notFound(w, "subscriptions", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := sub.Attributes
		if in.FamilySharable != nil && *a.FamilySharable && !*in.FamilySharable {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Family Sharing can't be turned off once it's on.", "/data/attributes/familySharable")
			return
		}
		setString(&a.Name, in.Name)
		setString(&a.ReviewNote, in.ReviewNote)
		if in.FamilySharable != nil {
			a.FamilySharable = new(*in.FamilySharable)
		}
		if in.GroupLevel != nil {
			a.GroupLevel = new(*in.GroupLevel)
		}
		if in.SubscriptionPeriod != nil {
			a.SubscriptionPeriod = new(gen.SubscriptionAttributesSubscriptionPeriod(*in.SubscriptionPeriod))
		}
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionResponse{Data: sub.Subscription, Links: docLinks(r)})
}

// SubscriptionsDeleteInstance deletes a subscription that was never
// approved.
func (s *Server) SubscriptionsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs.subscriptions[id]
	if !ok {
		notFound(w, "subscriptions", id)
		return
	}
	if approvedOnce(s.listMetaVersions(sub.versions)) {
		stateError(w, "An approved subscription can't be deleted.")
		return
	}
	for _, f := range s.files {
		if f.kind == subscriptionReviewScreenshot && f.parentID == id {
			s.removeFile(f)
		}
	}
	delete(s.subs.subscriptions, id)
	w.WriteHeader(http.StatusNoContent)
}

func renderSubscriptionVersion(v *metaVersion) gen.SubscriptionVersion {
	out := gen.SubscriptionVersion{Id: v.id, Type: gen.SubscriptionVersionTypeSubscriptionVersions}
	a := alloc(&out.Attributes)
	a.Version, a.State = new(v.number), new(gen.SubscriptionVersionAttributesState(v.state))
	return out
}

func (s *Server) SubscriptionsVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionsVersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs.subscriptions[id]
	if !ok {
		notFound(w, "subscriptions", id)
		return
	}
	var items []gen.SubscriptionVersion
	for _, v := range s.listMetaVersions(sub.versions) {
		items = append(items, renderSubscriptionVersion(v))
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	subID := req.Data.Relationships.Subscription.Data.Id
	sub, ok := s.subs.subscriptions[subID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptions with id '"+subID+"'", "/data/relationships/subscription")
		return
	}
	if v := s.startMetaVersion(w, subID, &sub.versions, "V"); v != nil {
		s.refreshSubscriptionState(sub)
		writeJSON(w, http.StatusCreated, gen.SubscriptionVersionResponse{Data: renderSubscriptionVersion(v), Links: docLinks(r)})
	}
}

func renderSubscriptionLocalization(l *metaLocalization) gen.SubscriptionLocalizationV2 {
	out := gen.SubscriptionLocalizationV2{Id: l.id, Type: gen.SubscriptionLocalizationV2TypeSubscriptionLocalizations}
	a := alloc(&out.Attributes)
	a.Locale, a.Name, a.Description = new(l.locale), l.values["name"], l.values["description"]
	return out
}

func (s *Server) SubscriptionVersionsLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionVersionsLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.subs.versions[id]
	if !ok {
		notFound(w, "subscriptionVersions", id)
		return
	}
	var items []gen.SubscriptionLocalizationV2
	for _, l := range sortedValues(v.localizations) {
		items = append(items, renderSubscriptionLocalization(l))
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionLocalizationsV2Response{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionLocalizationsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionLocalizationV2CreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	values := map[string]*string{"name": new(in.Name), "description": in.Description}
	if l := s.addMetaLocalization(w, req.Data.Relationships.Version.Data.Id, in.Locale, values); l != nil {
		v := s.subs.versions[req.Data.Relationships.Version.Data.Id]
		if sub, ok := s.subs.subscriptions[v.parentID]; ok {
			s.refreshSubscriptionState(sub)
		}
		writeJSON(w, http.StatusCreated, gen.SubscriptionLocalizationV2Response{Data: renderSubscriptionLocalization(l), Links: docLinks(r)})
	}
}

func (s *Server) SubscriptionLocalizationsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionLocalizationV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, _ := s.findMetaLocalization(w, id)
	if l == nil {
		return
	}
	if in := req.Data.Attributes; in != nil {
		name, desc := l.values["name"], l.values["description"]
		setString(&name, in.Name)
		setString(&desc, in.Description)
		l.values["name"], l.values["description"] = name, desc
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionLocalizationV2Response{Data: renderSubscriptionLocalization(l), Links: docLinks(r)})
}

func (s *Server) SubscriptionLocalizationsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, v := s.findMetaLocalization(w, id)
	if l == nil {
		return
	}
	delete(v.localizations, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- subscription images and review screenshots ----

func (s *Server) renderSubscriptionImage(r *http.Request, f *uploadedFile) gen.SubscriptionImageV2 {
	out := gen.SubscriptionImageV2{Id: f.id, Type: gen.SubscriptionImageV2TypeSubscriptionImages}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, _ = s.fileAttributes(r, f)
	return out
}

func (s *Server) SubscriptionImagesV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionImageV2CreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.editableMetaVersion(w, req.Data.Relationships.Version.Data.Id)
	if v == nil {
		return
	}
	for _, f := range s.files {
		if f.kind == subscriptionImage && f.parentID == v.id {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has an image.", "/data/relationships/version")
			return
		}
	}
	f := s.newFile(subscriptionImage, v.id, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.SubscriptionImageV2Response{Data: s.renderSubscriptionImage(r, f), Links: docLinks(r)})
}

func (s *Server) SubscriptionImagesV2GetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionImagesV2GetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, subscriptionImage, id); f != nil {
		writeJSON(w, http.StatusOK, gen.SubscriptionImageV2Response{Data: s.renderSubscriptionImage(r, f), Links: docLinks(r)})
	}
}

func (s *Server) SubscriptionVersionsImageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionVersionsImageGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.versions[id]; !ok {
		notFound(w, "subscriptionVersions", id)
		return
	}
	for _, f := range s.files {
		if f.kind == subscriptionImage && f.parentID == id {
			writeJSON(w, http.StatusOK, gen.SubscriptionImageV2Response{Data: s.renderSubscriptionImage(r, f), Links: docLinks(r)})
			return
		}
	}
	notFound(w, "subscriptionImages", id)
}

// SubscriptionImagesV2UpdateInstance commits an image; the v2 request has
// no checksum, so the fake checks only that every byte arrived.
func (s *Server) SubscriptionImagesV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionImageV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, subscriptionImage, id)
	if f == nil {
		return
	}
	if in := asc.Value(req.Data.Attributes); asc.Value(in.Uploaded) && f.state == "AWAITING_UPLOAD" {
		var data []byte
		for i := 0; i*uploadPartSize < f.size; i++ {
			data = append(data, f.parts[i]...)
		}
		f.commit(asc.Checksum(data))
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionImageV2Response{Data: s.renderSubscriptionImage(r, f), Links: docLinks(r)})
}

func (s *Server) SubscriptionImagesV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, subscriptionImage, id)
	if f == nil || s.editableMetaVersion(w, f.parentID) == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) renderSubscriptionReviewScreenshot(r *http.Request, f *uploadedFile) gen.SubscriptionAppStoreReviewScreenshot {
	out := gen.SubscriptionAppStoreReviewScreenshot{Id: f.id, Type: gen.SubscriptionAppStoreReviewScreenshotTypeSubscriptionAppStoreReviewScreenshots}
	a := alloc(&out.Attributes)
	a.FileName, a.FileSize = new(f.fileName), new(f.size)
	a.AssetDeliveryState, a.UploadOperations, a.SourceFileChecksum = s.fileAttributes(r, f)
	return out
}

func (s *Server) SubscriptionAppStoreReviewScreenshotsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionAppStoreReviewScreenshotCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	subID := req.Data.Relationships.Subscription.Data.Id
	if _, ok := s.subs.subscriptions[subID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptions with id '"+subID+"'", "/data/relationships/subscription")
		return
	}
	for _, f := range s.files {
		if f.kind == subscriptionReviewScreenshot && f.parentID == subID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The subscription already has a review screenshot.", "/data/relationships/subscription")
			return
		}
	}
	f := s.newFile(subscriptionReviewScreenshot, subID, req.Data.Attributes.FileName, req.Data.Attributes.FileSize)
	writeJSON(w, http.StatusCreated, gen.SubscriptionAppStoreReviewScreenshotResponse{Data: s.renderSubscriptionReviewScreenshot(r, f), Links: docLinks(r)})
}

func (s *Server) SubscriptionAppStoreReviewScreenshotsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionAppStoreReviewScreenshotsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFile(w, subscriptionReviewScreenshot, id); f != nil {
		writeJSON(w, http.StatusOK, gen.SubscriptionAppStoreReviewScreenshotResponse{Data: s.renderSubscriptionReviewScreenshot(r, f), Links: docLinks(r)})
	}
}

func (s *Server) SubscriptionsAppStoreReviewScreenshotGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionsAppStoreReviewScreenshotGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	for _, f := range s.files {
		if f.kind == subscriptionReviewScreenshot && f.parentID == id {
			writeJSON(w, http.StatusOK, gen.SubscriptionAppStoreReviewScreenshotResponse{Data: s.renderSubscriptionReviewScreenshot(r, f), Links: docLinks(r)})
			return
		}
	}
	notFound(w, "subscriptionAppStoreReviewScreenshots", id)
}

func (s *Server) SubscriptionAppStoreReviewScreenshotsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionAppStoreReviewScreenshotUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, subscriptionReviewScreenshot, id)
	if f == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	commitFile(f, in.Uploaded, in.SourceFileChecksum)
	writeJSON(w, http.StatusOK, gen.SubscriptionAppStoreReviewScreenshotResponse{Data: s.renderSubscriptionReviewScreenshot(r, f), Links: docLinks(r)})
}

func (s *Server) SubscriptionAppStoreReviewScreenshotsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.findFile(w, subscriptionReviewScreenshot, id)
	if f == nil {
		return
	}
	s.removeFile(f)
	w.WriteHeader(http.StatusNoContent)
}

// ---- review ----

// addMetaVersionItem adds a group or subscription version to a review
// submission of the same app.
func (s *Server) addMetaVersionItem(w http.ResponseWriter, sub *reviewSubmission, versionID string) bool {
	v := s.editableMetaVersion(w, versionID)
	if v == nil {
		return false
	}
	appID := ""
	if g, ok := s.subs.groups[v.parentID]; ok {
		appID = g.appID
	} else if subscription, ok := s.subs.subscriptions[v.parentID]; ok {
		appID = s.subs.groups[subscription.groupID].appID
	}
	if appID != sub.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version belongs to another app.", "/data/relationships")
		return false
	}
	if len(v.localizations) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version has no localizations.", "/data/relationships")
		return false
	}
	v.state = "READY_FOR_REVIEW"
	sub.metaVersions = append(sub.metaVersions, v.id)
	s.refreshMetaParent(v)
	return true
}

func (s *Server) refreshMetaParent(v *metaVersion) {
	if sub, ok := s.subs.subscriptions[v.parentID]; ok {
		s.refreshSubscriptionState(sub)
	}
}

func (s *Server) setMetaVersionStates(sub *reviewSubmission, state string) {
	for _, id := range sub.metaVersions {
		v := s.subs.versions[id]
		v.state = state
		s.refreshMetaParent(v)
	}
}

// ApproveSubscription approves a subscription's latest version, and its
// group's, as App Review does.
func (s *Server) ApproveSubscription(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subs.subscriptions[id]
	s.approveLatest(sub.versions)
	if g := s.subs.groups[sub.groupID]; len(g.versions) > 0 {
		s.approveLatest(g.versions)
	}
	s.refreshSubscriptionState(sub)
}

// MetaVersionStates returns the states of a group's or subscription's
// versions, oldest first.
func (s *Server) MetaVersionStates(parentID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	if g, ok := s.subs.groups[parentID]; ok {
		ids = g.versions
	} else {
		ids = s.subs.subscriptions[parentID].versions
	}
	var out []string
	for _, v := range s.listMetaVersions(ids) {
		out = append(out, v.state)
	}
	return out
}
