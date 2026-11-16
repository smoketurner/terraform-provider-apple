package asctest

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Xcode Cloud: workflows of a product, the repositories, Xcode versions and
// macOS versions they refer to, and build runs
// (https://developer.apple.com/documentation/appstoreconnectapi/xcode-cloud-workflows-and-builds).
// Xcode creates products and connects repositories, so tests add them with
// AddCiProduct and AddScmRepository. The fake requires a workflow's
// repository to belong to its product and its macOS version to run its Xcode
// version, and never starts or finishes the runs it creates.

type ciProduct struct {
	gen.CiProduct
	appID string
	// repositories lists the primary repository, then the additional ones.
	repositories []string
}

type scmReference struct {
	gen.ScmGitReference
	repositoryID string
}

type scmPullRequest struct {
	gen.ScmPullRequest
	repositoryID string
}

type ciWorkflow struct {
	gen.CiWorkflow
	productID, repositoryID string
}

type ciBuildRun struct {
	gen.CiBuildRun
	productID, workflowID string
}

type xcodeCloudState struct {
	products      map[string]*ciProduct
	repositories  map[string]*gen.ScmRepository
	references    map[string]*scmReference
	pullRequests  map[string]*scmPullRequest
	xcodeVersions map[string]*gen.CiXcodeVersion
	macVersions   map[string]*gen.CiMacOsVersion
	// macVersionsOf maps an Xcode version ID to the macOS versions that run it.
	macVersionsOf map[string][]string
	workflows     map[string]*ciWorkflow
	runs          map[string]*ciBuildRun
}

// Xcode Cloud versions the fake offers: Xcode 26 runs on macOS 26, and Xcode
// 16.4 on macOS 15 and 26.
const (
	Xcode26   = "XCODE26"
	Xcode16   = "XCODE16"
	MacOS26   = "MACOS26"
	MacOS15   = "MACOS15"
	ciRunsURL = "ciBuildRuns"
)

func linkageData(kind string, ids ...string) map[string]any {
	data := []map[string]any{}
	for _, id := range ids {
		data = append(data, map[string]any{"type": kind, "id": id})
	}
	return map[string]any{"data": data}
}

func oneLinkage(kind, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": kind, "id": id}}
}

// object fills a generated resource object from its JSON parts.
func object(to any, kind, id string, attributes any, relationships map[string]any) {
	doc := map[string]any{"type": kind, "id": id, "attributes": attributes}
	if relationships != nil {
		doc["relationships"] = relationships
	}
	remarshal(doc, to)
}

func newXcodeCloudState() *xcodeCloudState {
	st := &xcodeCloudState{
		products: map[string]*ciProduct{}, repositories: map[string]*gen.ScmRepository{}, references: map[string]*scmReference{},
		pullRequests: map[string]*scmPullRequest{}, xcodeVersions: map[string]*gen.CiXcodeVersion{}, macVersions: map[string]*gen.CiMacOsVersion{},
		macVersionsOf: map[string][]string{Xcode26: {MacOS26}, Xcode16: {MacOS15, MacOS26}},
		workflows:     map[string]*ciWorkflow{}, runs: map[string]*ciBuildRun{},
	}
	for id, v := range map[string][2]string{Xcode26: {"26.0", "Xcode 26"}, Xcode16: {"16.4", "Xcode 16.4"}} {
		x := &gen.CiXcodeVersion{}
		object(x, "ciXcodeVersions", id, map[string]any{"version": v[0], "name": v[1]}, map[string]any{"macOsVersions": linkageData("ciMacOsVersions", st.macVersionsOf[id]...)})
		st.xcodeVersions[id] = x
	}
	for id, v := range map[string][2]string{MacOS26: {"26.0", "macOS Tahoe 26"}, MacOS15: {"15.5", "macOS Sequoia 15.5"}} {
		var xcodes []string
		for _, xcode := range []string{Xcode16, Xcode26} {
			if slices.Contains(st.macVersionsOf[xcode], id) {
				xcodes = append(xcodes, xcode)
			}
		}
		m := &gen.CiMacOsVersion{}
		object(m, "ciMacOsVersions", id, map[string]any{"version": v[0], "name": v[1]}, map[string]any{"xcodeVersions": linkageData("ciXcodeVersions", xcodes...)})
		st.macVersions[id] = m
	}
	return st
}

// AddScmRepository connects a repository with a default branch named main,
// and returns the IDs of the repository and the branch.
func (s *Server) AddScmRepository(owner, name string) (repositoryID, branchID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repositoryID = s.newID("SR")
	branchID = s.addGitReference(repositoryID, "main", "BRANCH")
	r := &gen.ScmRepository{}
	object(r, "scmRepositories", repositoryID, map[string]any{
		"ownerName": owner, "repositoryName": name,
		"httpCloneUrl": "https://example.com/" + owner + "/" + name + ".git", "sshCloneUrl": "ssh://git@example.com/" + owner + "/" + name + ".git",
	}, map[string]any{"scmProvider": oneLinkage("scmProviders", "SCM1"), "defaultBranch": oneLinkage("scmGitReferences", branchID)})
	s.xcodeCloud.repositories[repositoryID] = r
	return repositoryID, branchID
}

func (s *Server) addGitReference(repositoryID, name, kind string) string {
	prefix := "refs/heads/"
	if kind == "TAG" {
		prefix = "refs/tags/"
	}
	ref := &scmReference{repositoryID: repositoryID}
	object(&ref.ScmGitReference, "scmGitReferences", s.newID("GR"),
		map[string]any{"name": name, "canonicalName": prefix + name, "kind": kind, "isDeleted": false}, nil)
	s.xcodeCloud.references[ref.Id] = ref
	return ref.Id
}

// AddGitReference adds a branch (kind BRANCH) or tag (kind TAG) to a
// repository and returns its ID.
func (s *Server) AddGitReference(repositoryID, name, kind string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addGitReference(repositoryID, name, kind)
}

// AddPullRequest adds an open pull request into main to a repository and
// returns its ID.
func (s *Server) AddPullRequest(repositoryID string, number int, title, sourceBranch string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr := &scmPullRequest{repositoryID: repositoryID}
	object(&pr.ScmPullRequest, "scmPullRequests", s.newID("PR"), map[string]any{
		"number": number, "title": title, "sourceBranchName": sourceBranch, "destinationBranchName": "main", "isClosed": false,
		"isCrossRepository": false, "webUrl": "https://example.com/pull/" + title,
	}, nil)
	s.xcodeCloud.pullRequests[pr.Id] = pr
	return pr.Id
}

// AddCiProduct adds the Xcode Cloud product of an app, with its primary
// repository and any additional ones, and returns its ID.
func (s *Server) AddCiProduct(appID, name, primaryRepositoryID string, additional ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &ciProduct{appID: appID, repositories: append([]string{primaryRepositoryID}, additional...)}
	object(&p.CiProduct, "ciProducts", s.newID("CP"),
		map[string]any{"name": name, "productType": "APP", "createdDate": s.now().UTC().Truncate(time.Second)},
		map[string]any{"app": oneLinkage("apps", appID), "primaryRepositories": linkageData("scmRepositories", primaryRepositoryID)})
	s.xcodeCloud.products[p.Id] = p
	return p.Id
}

// ---- products, repositories and versions ----

// writePage writes a page of items as a collection response of type R.
func writePage[T, R any](w http.ResponseWriter, r *http.Request, items []T, limit *int) {
	data, links, meta, ok := page(w, r, items, limit, maxLimit)
	if !ok {
		return
	}
	var out R
	remarshal(map[string]any{"data": nonNil(data), "links": links, "meta": meta}, &out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CiProductsGetCollection(w http.ResponseWriter, r *http.Request, params gen.CiProductsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.CiProduct
	for _, p := range sortedValues(s.xcodeCloud.products) {
		if matches(params.FilterApp, p.appID) && matches(params.FilterProductType, string(*p.Attributes.ProductType)) {
			items = append(items, p.CiProduct)
		}
	}
	writePage[gen.CiProduct, gen.CiProductsResponse](w, r, items, params.Limit)
}

// productRepositories writes a product's primary or additional repositories.
func (s *Server) productRepositories(w http.ResponseWriter, r *http.Request, id string, primary bool, limit *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.xcodeCloud.products[id]
	if !ok {
		notFound(w, "ciProducts", id)
		return
	}
	ids := p.repositories[:1]
	if !primary {
		ids = p.repositories[1:]
	}
	var items []gen.ScmRepository
	for _, id := range ids {
		items = append(items, *s.xcodeCloud.repositories[id])
	}
	writePage[gen.ScmRepository, gen.ScmRepositoriesResponse](w, r, items, limit)
}

func (s *Server) CiProductsPrimaryRepositoriesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.CiProductsPrimaryRepositoriesGetToManyRelatedParams) {
	s.productRepositories(w, r, id, true, params.Limit)
}

func (s *Server) CiProductsAdditionalRepositoriesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.CiProductsAdditionalRepositoriesGetToManyRelatedParams) {
	s.productRepositories(w, r, id, false, params.Limit)
}

func (s *Server) ScmRepositoriesGetCollection(w http.ResponseWriter, r *http.Request, params gen.ScmRepositoriesGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.ScmRepository
	for _, repository := range sortedValues(s.xcodeCloud.repositories) {
		if matches(params.FilterId, repository.Id) {
			items = append(items, *repository)
		}
	}
	writePage[gen.ScmRepository, gen.ScmRepositoriesResponse](w, r, items, params.Limit)
}

func (s *Server) ScmRepositoriesGitReferencesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.ScmRepositoriesGitReferencesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.xcodeCloud.repositories[id]; !ok {
		notFound(w, "scmRepositories", id)
		return
	}
	var items []gen.ScmGitReference
	for _, ref := range sortedValues(s.xcodeCloud.references) {
		if ref.repositoryID == id {
			items = append(items, ref.ScmGitReference)
		}
	}
	writePage[gen.ScmGitReference, gen.ScmGitReferencesResponse](w, r, items, params.Limit)
}

func (s *Server) ScmRepositoriesPullRequestsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.ScmRepositoriesPullRequestsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.xcodeCloud.repositories[id]; !ok {
		notFound(w, "scmRepositories", id)
		return
	}
	var items []gen.ScmPullRequest
	for _, pr := range sortedValues(s.xcodeCloud.pullRequests) {
		if pr.repositoryID == id {
			items = append(items, pr.ScmPullRequest)
		}
	}
	writePage[gen.ScmPullRequest, gen.ScmPullRequestsResponse](w, r, items, params.Limit)
}

func (s *Server) CiXcodeVersionsGetCollection(w http.ResponseWriter, r *http.Request, params gen.CiXcodeVersionsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.CiXcodeVersion
	for _, v := range sortedValues(s.xcodeCloud.xcodeVersions) {
		items = append(items, *v)
	}
	writePage[gen.CiXcodeVersion, gen.CiXcodeVersionsResponse](w, r, items, params.Limit)
}

func (s *Server) CiMacOsVersionsGetCollection(w http.ResponseWriter, r *http.Request, params gen.CiMacOsVersionsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.CiMacOsVersion
	for _, v := range sortedValues(s.xcodeCloud.macVersions) {
		items = append(items, *v)
	}
	writePage[gen.CiMacOsVersion, gen.CiMacOsVersionsResponse](w, r, items, params.Limit)
}

// ---- workflows ----

// checkWorkflowVersions writes an error and returns false unless the macOS
// version runs the Xcode version.
func (s *Server) checkWorkflowVersions(w http.ResponseWriter, xcodeID, macID string) bool {
	if _, ok := s.xcodeCloud.xcodeVersions[xcodeID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no ciXcodeVersions with id '"+xcodeID+"'", "/data/relationships/xcodeVersion")
		return false
	}
	if !slices.Contains(s.xcodeCloud.macVersionsOf[xcodeID], macID) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The macOS version '"+macID+"' doesn't run the Xcode version '"+xcodeID+"'.", "/data/relationships/macOsVersion")
		return false
	}
	return true
}

// setWorkflowRelationships records the workflow's relationships in the
// object it is read as.
func (wf *ciWorkflow) setWorkflowRelationships(xcodeID, macID string) {
	wf.Relationships = nil
	remarshal(map[string]any{
		"product": oneLinkage("ciProducts", wf.productID), "repository": oneLinkage("scmRepositories", wf.repositoryID),
		"xcodeVersion": oneLinkage("ciXcodeVersions", xcodeID), "macOsVersion": oneLinkage("ciMacOsVersions", macID),
	}, &wf.Relationships)
}

func (s *Server) CiWorkflowsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.CiWorkflowCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.xcodeCloud.products[rel.Product.Data.Id]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no ciProducts with id '"+rel.Product.Data.Id+"'", "/data/relationships/product")
		return
	}
	if !slices.Contains(p.repositories, rel.Repository.Data.Id) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The product has no repository '"+rel.Repository.Data.Id+"'.", "/data/relationships/repository")
		return
	}
	if !s.checkWorkflowVersions(w, rel.XcodeVersion.Data.Id, rel.MacOsVersion.Data.Id) {
		return
	}
	if len(req.Data.Attributes.Actions) == 0 {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.REQUIRED", "A workflow needs an action.", "/data/attributes/actions")
		return
	}
	wf := &ciWorkflow{productID: p.Id, repositoryID: rel.Repository.Data.Id}
	wf.Id, wf.Type = s.newID("WF"), "ciWorkflows"
	remarshal(req.Data.Attributes, &wf.Attributes)
	wf.Attributes.LastModifiedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	if wf.Attributes.IsLockedForEditing == nil {
		wf.Attributes.IsLockedForEditing = new(false)
	}
	wf.setWorkflowRelationships(rel.XcodeVersion.Data.Id, rel.MacOsVersion.Data.Id)
	s.xcodeCloud.workflows[wf.Id] = wf
	writeJSON(w, http.StatusCreated, gen.CiWorkflowResponse{Data: wf.CiWorkflow, Links: docLinks(r)})
}

func (s *Server) CiWorkflowsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.CiWorkflowsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, ok := s.xcodeCloud.workflows[id]
	if !ok {
		notFound(w, "ciWorkflows", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.CiWorkflowResponse{Data: wf.CiWorkflow, Links: docLinks(r)})
}

// CiWorkflowsUpdateInstance merges the request's attributes into the
// workflow's; a null attribute removes a start condition.
func (s *Server) CiWorkflowsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.CiWorkflowUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, ok := s.xcodeCloud.workflows[id]
	if !ok {
		notFound(w, "ciWorkflows", id)
		return
	}
	xcodeID, macID := wf.Relationships.XcodeVersion.Data.Id, wf.Relationships.MacOsVersion.Data.Id
	if rel := req.Data.Relationships; rel != nil {
		if rel.XcodeVersion != nil && rel.XcodeVersion.Data != nil {
			xcodeID = rel.XcodeVersion.Data.Id
		}
		if rel.MacOsVersion != nil && rel.MacOsVersion.Data != nil {
			macID = rel.MacOsVersion.Data.Id
		}
	}
	if !s.checkWorkflowVersions(w, xcodeID, macID) {
		return
	}
	if in := req.Data.Attributes; in != nil && in.Actions != nil && len(*in.Actions) == 0 {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.REQUIRED", "A workflow needs an action.", "/data/attributes/actions")
		return
	}
	var current, changes map[string]json.RawMessage
	remarshal(wf.Attributes, &current)
	remarshal(req.Data.Attributes, &changes)
	for name, value := range changes {
		current[name] = value
	}
	for name := range nulls {
		delete(current, name)
	}
	wf.Attributes = nil
	remarshal(current, &wf.Attributes)
	wf.Attributes.LastModifiedDate = new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	wf.setWorkflowRelationships(xcodeID, macID)
	writeJSON(w, http.StatusOK, gen.CiWorkflowResponse{Data: wf.CiWorkflow, Links: docLinks(r)})
}

func (s *Server) CiWorkflowsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.xcodeCloud.workflows[id]; !ok {
		notFound(w, "ciWorkflows", id)
		return
	}
	delete(s.xcodeCloud.workflows, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) CiProductsWorkflowsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.CiProductsWorkflowsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.xcodeCloud.products[id]; !ok {
		notFound(w, "ciProducts", id)
		return
	}
	var items []gen.CiWorkflow
	for _, wf := range sortedValues(s.xcodeCloud.workflows) {
		if wf.productID == id {
			items = append(items, wf.CiWorkflow)
		}
	}
	writePage[gen.CiWorkflow, gen.CiWorkflowsResponse](w, r, items, params.Limit)
}

// ---- build runs ----

// CiBuildRunsCreateInstance starts a run of a workflow for a branch, tag or
// pull request of its repository, or runs an earlier run again.
func (s *Server) CiBuildRunsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.CiBuildRunCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := req.Data.Relationships
	var wf *ciWorkflow
	reason, source := "MANUAL", map[string]any{}
	switch {
	case rel != nil && rel.BuildRun != nil && rel.BuildRun.Data != nil:
		earlier, ok := s.xcodeCloud.runs[rel.BuildRun.Data.Id]
		if !ok {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no ciBuildRuns with id '"+rel.BuildRun.Data.Id+"'", "/data/relationships/buildRun")
			return
		}
		wf, reason = s.xcodeCloud.workflows[earlier.workflowID], "MANUAL_REBUILD"
		remarshal(earlier.Relationships, &source)
	case rel != nil && rel.Workflow != nil && rel.Workflow.Data != nil:
		wf = s.xcodeCloud.workflows[rel.Workflow.Data.Id]
		switch {
		case rel.SourceBranchOrTag != nil && rel.SourceBranchOrTag.Data != nil:
			ref, ok := s.xcodeCloud.references[rel.SourceBranchOrTag.Data.Id]
			if !ok || wf != nil && ref.repositoryID != wf.repositoryID {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The workflow's repository has no git reference '"+rel.SourceBranchOrTag.Data.Id+"'.", "/data/relationships/sourceBranchOrTag")
				return
			}
			source["sourceBranchOrTag"] = oneLinkage("scmGitReferences", ref.Id)
		case rel.PullRequest != nil && rel.PullRequest.Data != nil:
			pr, ok := s.xcodeCloud.pullRequests[rel.PullRequest.Data.Id]
			if !ok || wf != nil && pr.repositoryID != wf.repositoryID {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The workflow's repository has no pull request '"+rel.PullRequest.Data.Id+"'.", "/data/relationships/pullRequest")
				return
			}
			source["pullRequest"] = oneLinkage("scmPullRequests", pr.Id)
		default:
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "A run needs a branch, tag or pull request.", "/data/relationships/sourceBranchOrTag")
			return
		}
	}
	if wf == nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The run needs an existing workflow or build run.", "/data/relationships/workflow")
		return
	}
	if !*wf.Attributes.IsEnabled {
		stateError(w, "The workflow is disabled.")
		return
	}
	number := 1
	for _, run := range s.xcodeCloud.runs {
		if run.productID == wf.productID {
			number++
		}
	}
	source["workflow"], source["product"] = oneLinkage("ciWorkflows", wf.Id), oneLinkage("ciProducts", wf.productID)
	run := &ciBuildRun{productID: wf.productID, workflowID: wf.Id}
	object(&run.CiBuildRun, ciRunsURL, s.newID("BR"), map[string]any{
		"number": number, "createdDate": s.now().UTC().Truncate(time.Second), "executionProgress": "PENDING", "startReason": reason,
		"isPullRequestBuild": source["pullRequest"] != nil,
	}, source)
	s.xcodeCloud.runs[run.Id] = run
	writeJSON(w, http.StatusCreated, gen.CiBuildRunResponse{Data: run.CiBuildRun, Links: docLinks(r)})
}

// buildRuns writes the runs that match.
func (s *Server) buildRuns(w http.ResponseWriter, r *http.Request, limit *int, match func(*ciBuildRun) bool) {
	var items []gen.CiBuildRun
	for _, run := range sortedValues(s.xcodeCloud.runs) {
		if match(run) {
			items = append(items, run.CiBuildRun)
		}
	}
	slices.SortStableFunc(items, func(a, b gen.CiBuildRun) int { return *b.Attributes.Number - *a.Attributes.Number })
	writePage[gen.CiBuildRun, gen.CiBuildRunsResponse](w, r, items, limit)
}

func (s *Server) CiWorkflowsBuildRunsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.CiWorkflowsBuildRunsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.xcodeCloud.workflows[id]; !ok {
		notFound(w, "ciWorkflows", id)
		return
	}
	s.buildRuns(w, r, params.Limit, func(run *ciBuildRun) bool { return run.workflowID == id })
}

func (s *Server) CiProductsBuildRunsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.CiProductsBuildRunsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.xcodeCloud.products[id]; !ok {
		notFound(w, "ciProducts", id)
		return
	}
	s.buildRuns(w, r, params.Limit, func(run *ciBuildRun) bool { return run.productID == id })
}
