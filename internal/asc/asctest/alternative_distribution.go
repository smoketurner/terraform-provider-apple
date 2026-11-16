package asctest

import (
	"net/http"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Alternative marketplaces and web distribution in the EU: distribution
// domains and keys, an alternative marketplace's search detail, and the
// alternative distribution package of an App Store version
// (https://developer.apple.com/documentation/appstoreconnectapi/alternative-marketplaces-and-web-distribution).
// One key applies to every app of the account, and an app can have its own.
// The fake allows one account key, one key per app, one search detail per
// app and one package per version, and returns only an app's own key for
// the app. Apple builds a package's versions, so tests add them with
// AddPackageVersion.

const (
	distributionDomainsRoute  = "alternativeDistributionDomains"
	distributionKeysRoute     = "alternativeDistributionKeys"
	distributionPackagesRoute = "alternativeDistributionPackages"
	packageVersionsRoute      = "alternativeDistributionPackageVersions"
	searchDetailsRoute        = "marketplaceSearchDetails"
)

type distributionKey struct {
	id, publicKey string
	// appID is "" for the account's key.
	appID string
}

type searchDetail struct {
	id, appID, catalogURL string
}

type distributionPackage struct {
	id, versionID string
}

type packageVersion struct {
	gen.AlternativeDistributionPackageVersion
	packageID string
	variants  []gen.AlternativeDistributionPackageVariant
	deltas    []gen.AlternativeDistributionPackageDelta
}

type distributionState struct {
	domains  map[string]*gen.AlternativeDistributionDomain
	keys     map[string]*distributionKey
	details  map[string]*searchDetail
	packages map[string]*distributionPackage
	versions map[string]*packageVersion
}

func newDistributionState() *distributionState {
	return &distributionState{
		domains: map[string]*gen.AlternativeDistributionDomain{}, keys: map[string]*distributionKey{}, details: map[string]*searchDetail{},
		packages: map[string]*distributionPackage{}, versions: map[string]*packageVersion{},
	}
}

// AddPackageVersion adds a version with one variant and one delta to an
// alternative distribution package, as Apple does after it builds the
// package, and returns the version's ID. Earlier versions become REPLACED.
func (s *Server) AddPackageVersion(packageID, version string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.distribution.versions {
		if v.packageID == packageID {
			v.Attributes.State = new(gen.AlternativeDistributionPackageVersionAttributesStateREPLACED)
		}
	}
	expires := gen.DateTime{Time: s.now().UTC().Truncate(time.Second).Add(time.Hour)}
	v := &packageVersion{packageID: packageID}
	v.Id, v.Type = s.newID("PV"), gen.AlternativeDistributionPackageVersionTypeAlternativeDistributionPackageVersions
	a := alloc(&v.Attributes)
	a.Version, a.State = new(version), new(gen.AlternativeDistributionPackageVersionAttributesStateCOMPLETED)
	a.Url, a.UrlExpirationDate, a.FileChecksum = new(s.URL+"/packages/"+v.Id), new(expires), new("sha256-"+v.Id)
	variant := gen.AlternativeDistributionPackageVariant{Id: s.newID("PA"), Type: gen.AlternativeDistributionPackageVariantTypeAlternativeDistributionPackageVariants}
	va := alloc(&variant.Attributes)
	va.Url, va.UrlExpirationDate, va.FileChecksum, va.AlternativeDistributionKeyBlob = new(s.URL+"/packages/"+variant.Id), new(expires), new("sha256-"+variant.Id), new("blob-"+variant.Id)
	delta := gen.AlternativeDistributionPackageDelta{Id: s.newID("PD"), Type: gen.AlternativeDistributionPackageDeltaTypeAlternativeDistributionPackageDeltas}
	da := alloc(&delta.Attributes)
	da.Url, da.UrlExpirationDate, da.FileChecksum, da.AlternativeDistributionKeyBlob = new(s.URL+"/packages/"+delta.Id), new(expires), new("sha256-"+delta.Id), new("blob-"+delta.Id)
	v.variants, v.deltas = []gen.AlternativeDistributionPackageVariant{variant}, []gen.AlternativeDistributionPackageDelta{delta}
	s.distribution.versions[v.Id] = v
	return v.Id
}

// ---- domains ----

func (s *Server) AlternativeDistributionDomainsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AlternativeDistributionDomainCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.distribution.domains {
		if *d.Attributes.Domain == in.Domain {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The domain "+in.Domain+" is already added.", "/data/attributes/domain")
			return
		}
	}
	d := &gen.AlternativeDistributionDomain{Id: s.newID("DD"), Type: gen.AlternativeDistributionDomainTypeAlternativeDistributionDomains}
	a := alloc(&d.Attributes)
	a.Domain, a.ReferenceName, a.CreatedDate = new(in.Domain), new(in.ReferenceName), new(gen.DateTime{Time: s.now().UTC().Truncate(time.Second)})
	s.distribution.domains[d.Id] = d
	writeJSON(w, http.StatusCreated, gen.AlternativeDistributionDomainResponse{Data: *d, Links: docLinks(r)})
}

func (s *Server) AlternativeDistributionDomainsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AlternativeDistributionDomainsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.distribution.domains[id]
	if !ok {
		notFound(w, distributionDomainsRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionDomainResponse{Data: *d, Links: docLinks(r)})
}

func (s *Server) AlternativeDistributionDomainsGetCollection(w http.ResponseWriter, r *http.Request, params gen.AlternativeDistributionDomainsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.AlternativeDistributionDomain
	for _, d := range sortedValues(s.distribution.domains) {
		items = append(items, *d)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionDomainsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AlternativeDistributionDomainsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.distribution.domains[id]; !ok {
		notFound(w, distributionDomainsRoute, id)
		return
	}
	delete(s.distribution.domains, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- keys ----

func renderDistributionKey(k *distributionKey) gen.AlternativeDistributionKey {
	out := gen.AlternativeDistributionKey{Id: k.id, Type: gen.AlternativeDistributionKeyTypeAlternativeDistributionKeys}
	alloc(&out.Attributes).PublicKey = new(k.publicKey)
	return out
}

// appDistributionKey returns the key of an app, or of the account for "".
func (s *Server) appDistributionKey(appID string) *distributionKey {
	for _, k := range s.distribution.keys {
		if k.appID == appID {
			return k
		}
	}
	return nil
}

func (s *Server) AlternativeDistributionKeysCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AlternativeDistributionKeyCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &distributionKey{publicKey: req.Data.Attributes.PublicKey}
	if rel := req.Data.Relationships; rel != nil && rel.App != nil && rel.App.Data != nil {
		if _, ok := s.apps[rel.App.Data.Id]; !ok {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+rel.App.Data.Id+"'", "/data/relationships/app")
			return
		}
		k.appID = rel.App.Data.Id
	}
	if s.appDistributionKey(k.appID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "An alternative distribution key already exists.", "/data/relationships/app")
		return
	}
	k.id = s.newID("DK")
	s.distribution.keys[k.id] = k
	writeJSON(w, http.StatusCreated, gen.AlternativeDistributionKeyResponse{Data: renderDistributionKey(k), Links: docLinks(r)})
}

func (s *Server) AlternativeDistributionKeysGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AlternativeDistributionKeysGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.distribution.keys[id]
	if !ok {
		notFound(w, distributionKeysRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionKeyResponse{Data: renderDistributionKey(k), Links: docLinks(r)})
}

func (s *Server) AlternativeDistributionKeysGetCollection(w http.ResponseWriter, r *http.Request, params gen.AlternativeDistributionKeysGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.AlternativeDistributionKey
	for _, k := range sortedValues(s.distribution.keys) {
		if params.ExistsApp == nil || *params.ExistsApp == (k.appID != "") {
			items = append(items, renderDistributionKey(k))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionKeysResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AlternativeDistributionKeysDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.distribution.keys[id]; !ok {
		notFound(w, distributionKeysRoute, id)
		return
	}
	delete(s.distribution.keys, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsAlternativeDistributionKeyGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsAlternativeDistributionKeyGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.appDistributionKey(id)
	if _, ok := s.apps[id]; !ok || k == nil {
		notFound(w, "apps", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionKeyResponse{Data: renderDistributionKey(k), Links: docLinks(r)})
}

// ---- marketplace search details ----

func renderSearchDetail(d *searchDetail) gen.MarketplaceSearchDetail {
	out := gen.MarketplaceSearchDetail{Id: d.id, Type: gen.MarketplaceSearchDetailTypeMarketplaceSearchDetails}
	alloc(&out.Attributes).CatalogUrl = new(d.catalogURL)
	return out
}

func (s *Server) appSearchDetail(appID string) *searchDetail {
	for _, d := range s.distribution.details {
		if d.appID == appID {
			return d
		}
	}
	return nil
}

func (s *Server) MarketplaceSearchDetailsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.MarketplaceSearchDetailCreateRequest
	if !decode(w, r, &req) {
		return
	}
	appID := req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	if s.appSearchDetail(appID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app already has a marketplace search detail.", "/data/relationships/app")
		return
	}
	d := &searchDetail{id: s.newID("MS"), appID: appID, catalogURL: req.Data.Attributes.CatalogUrl}
	s.distribution.details[d.id] = d
	writeJSON(w, http.StatusCreated, gen.MarketplaceSearchDetailResponse{Data: renderSearchDetail(d), Links: docLinks(r)})
}

func (s *Server) MarketplaceSearchDetailsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.MarketplaceSearchDetailUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.distribution.details[id]
	if !ok {
		notFound(w, searchDetailsRoute, id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.CatalogUrl != nil {
		d.catalogURL = *in.CatalogUrl
	}
	writeJSON(w, http.StatusOK, gen.MarketplaceSearchDetailResponse{Data: renderSearchDetail(d), Links: docLinks(r)})
}

func (s *Server) MarketplaceSearchDetailsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.distribution.details[id]; !ok {
		notFound(w, searchDetailsRoute, id)
		return
	}
	delete(s.distribution.details, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsMarketplaceSearchDetailGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsMarketplaceSearchDetailGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.appSearchDetail(id)
	if _, ok := s.apps[id]; !ok || d == nil {
		notFound(w, "apps", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.MarketplaceSearchDetailResponse{Data: renderSearchDetail(d), Links: docLinks(r)})
}

// ---- packages ----

func renderPackage(p *distributionPackage) gen.AlternativeDistributionPackage {
	return gen.AlternativeDistributionPackage{Id: p.id, Type: gen.AlternativeDistributionPackageTypeAlternativeDistributionPackages}
}

func (s *Server) versionPackage(versionID string) *distributionPackage {
	for _, p := range s.distribution.packages {
		if p.versionID == versionID {
			return p
		}
	}
	return nil
}

func (s *Server) AlternativeDistributionPackagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AlternativeDistributionPackageCreateRequest
	if !decode(w, r, &req) {
		return
	}
	versionID := req.Data.Relationships.AppStoreVersion.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.versions.versions[versionID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersions with id '"+versionID+"'", "/data/relationships/appStoreVersion")
		return
	}
	if s.versionPackage(versionID) != nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version already has an alternative distribution package.", "/data/relationships/appStoreVersion")
		return
	}
	p := &distributionPackage{id: s.newID("DP"), versionID: versionID}
	s.distribution.packages[p.id] = p
	writeJSON(w, http.StatusCreated, gen.AlternativeDistributionPackageResponse{Data: renderPackage(p), Links: docLinks(r)})
}

func (s *Server) AppStoreVersionsAlternativeDistributionPackageGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionsAlternativeDistributionPackageGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.versionPackage(id)
	if p == nil {
		notFound(w, "appStoreVersions", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionPackageResponse{Data: renderPackage(p), Links: docLinks(r)})
}

func (s *Server) AlternativeDistributionPackagesVersionsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AlternativeDistributionPackagesVersionsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.distribution.packages[id]; !ok {
		notFound(w, distributionPackagesRoute, id)
		return
	}
	var items []gen.AlternativeDistributionPackageVersion
	for _, v := range sortedValues(s.distribution.versions) {
		if v.packageID == id && matches(params.FilterState, string(*v.Attributes.State)) {
			items = append(items, v.AlternativeDistributionPackageVersion)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionPackageVersionsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AlternativeDistributionPackageVersionsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AlternativeDistributionPackageVersionsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.distribution.versions[id]
	if !ok {
		notFound(w, packageVersionsRoute, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionPackageVersionResponse{Data: v.AlternativeDistributionPackageVersion, Links: docLinks(r)})
}

func (s *Server) AlternativeDistributionPackageVersionsVariantsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AlternativeDistributionPackageVersionsVariantsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.distribution.versions[id]
	if !ok {
		notFound(w, packageVersionsRoute, id)
		return
	}
	data, links, meta, ok := page(w, r, v.variants, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionPackageVariantsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AlternativeDistributionPackageVersionsDeltasGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AlternativeDistributionPackageVersionsDeltasGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.distribution.versions[id]
	if !ok {
		notFound(w, packageVersionsRoute, id)
		return
	}
	data, links, meta, ok := page(w, r, v.deltas, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AlternativeDistributionPackageDeltasResponse{Data: nonNil(data), Links: links, Meta: meta})
}
