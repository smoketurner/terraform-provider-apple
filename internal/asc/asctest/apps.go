package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// AddApp adds an app record, which the API cannot create, and returns its ID.
func (s *Server) AddApp(name, bundleID, sku string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := &gen.App{Id: s.newID("A"), Type: gen.AppTypeApps}
	attrs := alloc(&a.Attributes)
	attrs.Name, attrs.BundleId, attrs.Sku, attrs.PrimaryLocale = new(name), new(bundleID), new(sku), new("en-US")
	s.apps[a.Id] = a
	return a.Id
}

func (s *Server) AppsGetCollection(w http.ResponseWriter, r *http.Request, params gen.AppsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.App
	for _, a := range sortedValues(s.apps) {
		attrs := a.Attributes
		if matches(params.FilterId, a.Id) && matches(params.FilterName, asc.Value(attrs.Name)) &&
			matches(params.FilterBundleId, asc.Value(attrs.BundleId)) && matches(params.FilterSku, asc.Value(attrs.Sku)) {
			app := *a
			app.Links = resourceLinks(r, "apps", a.Id)
			items = append(items, app)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) AppsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.apps[id]
	if !ok {
		notFound(w, "apps", id)
		return
	}
	app := *a
	app.Links = resourceLinks(r, "apps", a.Id)
	writeJSON(w, http.StatusOK, gen.AppResponse{Data: app, Links: docLinks(r)})
}
