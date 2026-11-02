package asctest

import (
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// buildIncludes are the relationships a build response can include.
type buildIncludes struct{ app, preRelease, detail bool }

func buildIncludesOf[T ~string](include *[]T) buildIncludes {
	return buildIncludes{app: includes(include, "app"), preRelease: includes(include, "preReleaseVersion"), detail: includes(include, "buildBetaDetail")}
}

// renderBuild returns the build with the requested relationships, and the
// included objects to send with it.
func (s *Server) renderBuild(r *http.Request, b *build, inc buildIncludes) (gen.Build, []any) {
	out := b.Build
	attrs := *b.Attributes
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "builds", b.Id)
	out.Relationships = nil
	var included []any
	if inc.app {
		d := alloc(&alloc(&alloc(&out.Relationships).App).Data)
		d.Id, d.Type = b.appID, "apps"
	}
	if inc.preRelease {
		d := alloc(&alloc(&alloc(&out.Relationships).PreReleaseVersion).Data)
		d.Id, d.Type = b.preReleaseID, "preReleaseVersions"
		included = append(included, s.tf.preReleaseVersions[b.preReleaseID].PrereleaseVersion)
	}
	if inc.detail {
		d := alloc(&alloc(&alloc(&out.Relationships).BuildBetaDetail).Data)
		d.Id, d.Type = b.detail.Id, "buildBetaDetails"
		included = append(included, b.detail)
	}
	return out, included
}

// includedItems converts included objects to a generated union slice.
func includedItems[I any](objects []any, from func(*I, any) error) *[]I {
	if len(objects) == 0 {
		return nil
	}
	out := make([]I, len(objects))
	for i, o := range objects {
		if err := from(&out[i], o); err != nil {
			panic(err)
		}
	}
	return &out
}

func buildIncluded(item *gen.BuildResponse_Included_Item, o any) error {
	switch v := o.(type) {
	case gen.PrereleaseVersion:
		return item.FromPrereleaseVersion(v)
	case gen.BuildBetaDetail:
		return item.FromBuildBetaDetail(v)
	}
	panic("unexpected included object")
}

func buildsIncluded(item *gen.BuildsResponse_Included_Item, o any) error {
	switch v := o.(type) {
	case gen.PrereleaseVersion:
		return item.FromPrereleaseVersion(v)
	case gen.BuildBetaDetail:
		return item.FromBuildBetaDetail(v)
	}
	panic("unexpected included object")
}

func (s *Server) BuildsGetCollection(w http.ResponseWriter, r *http.Request, params gen.BuildsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.Build
	var included []any
	seen := map[string]bool{}
	inc := buildIncludesOf(params.Include)
	for _, b := range sortedValues(s.tf.builds) {
		a := b.Attributes
		pre := s.tf.preReleaseVersions[b.preReleaseID].Attributes
		if !matches(params.FilterId, b.Id) || !matches(params.FilterApp, b.appID) || !matches(params.FilterVersion, asc.Value(a.Version)) ||
			!matches(params.FilterExpired, boolString(a.Expired)) || !matches(params.FilterProcessingState, string(asc.Value(a.ProcessingState))) ||
			!matches(params.FilterPreReleaseVersion, b.preReleaseID) || !matches(params.FilterPreReleaseVersionVersion, asc.Value(pre.Version)) ||
			!matches(params.FilterPreReleaseVersionPlatform, string(asc.Value(pre.Platform))) ||
			!matches(params.FilterBuildAudienceType, string(asc.Value(a.BuildAudienceType))) || !anyIn(params.FilterBetaGroups, b.groups) {
			continue
		}
		item, inc := s.renderBuild(r, b, inc)
		items = append(items, item)
		for _, o := range inc {
			var id string
			switch v := o.(type) {
			case gen.PrereleaseVersion:
				id = "v" + v.Id
			case gen.BuildBetaDetail:
				id = "d" + v.Id
			}
			if !seen[id] {
				seen[id] = true
				included = append(included, o)
			}
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BuildsResponse{Data: nonNil(data), Included: includedItems(included, buildsIncluded), Links: links, Meta: meta})
}

func (s *Server) BuildsGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.BuildsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "builds", id)
		return
	}
	item, included := s.renderBuild(r, b, buildIncludesOf(params.Include))
	writeJSON(w, http.StatusOK, gen.BuildResponse{Data: item, Included: includedItems(included, buildIncluded), Links: docLinks(r)})
}

// BuildsUpdateInstance expires a build or answers its export compliance
// question (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-builds-_id_).
// An expired build cannot be made testable again.
func (s *Server) BuildsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BuildUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "builds", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.Expired != nil {
			if !*in.Expired && *b.Attributes.Expired {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "An expired build cannot be made available again.", "/data/attributes/expired")
				return
			}
			b.Attributes.Expired = new(*in.Expired)
		}
		if in.UsesNonExemptEncryption != nil {
			b.Attributes.UsesNonExemptEncryption = new(*in.UsesNonExemptEncryption)
		}
	}
	s.refreshBuildStates(b)
	item, _ := s.renderBuild(r, b, buildIncludes{})
	writeJSON(w, http.StatusOK, gen.BuildResponse{Data: item, Links: docLinks(r)})
}

func (s *Server) buildExists(id string) bool { _, ok := s.tf.builds[id]; return ok }

// BuildsBetaGroupsCreateToManyRelationship adds a build to groups
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-builds-_id_-relationships-betagroups).
// Internal-only builds can be added only to internal groups.
func (s *Server) BuildsBetaGroupsCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "builds", id)
		return
	}
	ids, ok := linkageIDs(w, r, "betaGroups", s.groupExists)
	if !ok {
		return
	}
	for _, g := range ids {
		group := s.tf.groups[g]
		if group.appID != b.appID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The beta group belongs to another app.", "/data")
			return
		}
		if *b.Attributes.BuildAudienceType == gen.BuildAudienceTypeINTERNALONLY && !*group.Attributes.IsInternalGroup {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "Builds uploaded as TestFlight Internal Only can only be added to internal groups.", "/data")
			return
		}
	}
	for _, g := range ids {
		b.groups[g] = true
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BuildsBetaGroupsDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "builds", id)
		return
	}
	ids, ok := linkageIDs(w, r, "betaGroups", s.groupExists)
	if !ok {
		return
	}
	for _, g := range ids {
		delete(b.groups, g)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BuildsIndividualTestersCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "builds", id)
		return
	}
	ids, ok := linkageIDs(w, r, "betaTesters", s.testerExists)
	if !ok {
		return
	}
	for _, t := range ids {
		b.testers[t] = true
		s.tf.testers[t].builds[id] = true
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BuildsIndividualTestersDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "builds", id)
		return
	}
	ids, ok := linkageIDs(w, r, "betaTesters", s.testerExists)
	if !ok {
		return
	}
	for _, t := range ids {
		delete(b.testers, t)
		delete(s.tf.testers[t].builds, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// BuildTesters returns the IDs of the testers a build was given to
// directly, and of the groups it was added to.
func (s *Server) BuildTesters(buildID string) (testers, groups []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.tf.builds[buildID]
	for t := range b.testers {
		testers = append(testers, t)
	}
	for g := range b.groups {
		groups = append(groups, g)
	}
	slices.Sort(testers)
	slices.Sort(groups)
	return testers, groups
}

func (s *Server) PreReleaseVersionsGetCollection(w http.ResponseWriter, r *http.Request, params gen.PreReleaseVersionsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.PrereleaseVersion
	for _, p := range sortedValues(s.tf.preReleaseVersions) {
		a := p.Attributes
		if matches(params.FilterApp, p.appID) && matches(params.FilterVersion, asc.Value(a.Version)) &&
			matches(params.FilterPlatform, string(asc.Value(a.Platform))) {
			item := p.PrereleaseVersion
			item.Links = resourceLinks(r, "preReleaseVersions", p.Id)
			items = append(items, item)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.PreReleaseVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) BuildBetaDetailsGetCollection(w http.ResponseWriter, r *http.Request, params gen.BuildBetaDetailsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.BuildBetaDetail
	for _, b := range sortedValues(s.tf.builds) {
		if matches(params.FilterBuild, b.Id) && matches(params.FilterId, b.detail.Id) {
			items = append(items, b.detail)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BuildBetaDetailsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) BuildBetaDetailsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BuildBetaDetailUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[id]
	if !ok {
		notFound(w, "buildBetaDetails", id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.AutoNotifyEnabled != nil {
		b.detail.Attributes.AutoNotifyEnabled = new(*in.AutoNotifyEnabled)
	}
	writeJSON(w, http.StatusOK, gen.BuildBetaDetailResponse{Data: b.detail, Links: docLinks(r)})
}

// BuildBetaNotificationsCreateInstance notifies a build's testers that it is
// available (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-buildbetanotifications).
func (s *Server) BuildBetaNotificationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BuildBetaNotificationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	buildID := req.Data.Relationships.Build.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[buildID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no builds with id '"+buildID+"'", "/data/relationships/build")
		return
	}
	if *b.Attributes.Expired {
		conflict(w, "STATE_ERROR", "The build has expired.", "/data/relationships/build")
		return
	}
	s.tf.notifications = append(s.tf.notifications, buildID)
	writeJSON(w, http.StatusCreated, gen.BuildBetaNotificationResponse{
		Data:  gen.BuildBetaNotification{Id: s.newID("N"), Type: gen.BuildBetaNotificationTypeBuildBetaNotifications},
		Links: docLinks(r),
	})
}

// Beta app review submissions. "You can only have one build of each version
// in review at a time" ("Invite external testers",
// https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers/).

func (s *Server) BetaAppReviewSubmissionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaAppReviewSubmissionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	buildID := req.Data.Relationships.Build.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.tf.builds[buildID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no builds with id '"+buildID+"'", "/data/relationships/build")
		return
	}
	if state := *b.detail.Attributes.ExternalBuildState; state != gen.ExternalBetaStateREADYFORBETASUBMISSION {
		conflict(w, "STATE_ERROR", "The build is not ready for beta review: its external build state is "+string(state)+".", "/data/relationships/build")
		return
	}
	for _, sub := range s.tf.submissions {
		other := s.tf.builds[sub.buildID]
		inReview := *sub.Attributes.BetaReviewState == gen.BetaReviewStateWAITINGFORREVIEW || *sub.Attributes.BetaReviewState == gen.BetaReviewStateINREVIEW
		if inReview && other.preReleaseID == b.preReleaseID {
			conflict(w, "STATE_ERROR", "Another build of this version is already in beta review.", "/data/relationships/build")
			return
		}
	}
	sub := &submission{buildID: buildID, BetaAppReviewSubmission: gen.BetaAppReviewSubmission{Id: s.newID("S"), Type: gen.BetaAppReviewSubmissionTypeBetaAppReviewSubmissions}}
	a := alloc(&sub.Attributes)
	a.BetaReviewState = new(gen.BetaReviewStateWAITINGFORREVIEW)
	a.SubmittedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	s.tf.submissions[sub.Id] = sub
	s.refreshBuildStates(b)
	writeJSON(w, http.StatusCreated, gen.BetaAppReviewSubmissionResponse{Data: sub.BetaAppReviewSubmission, Links: docLinks(r)})
}
