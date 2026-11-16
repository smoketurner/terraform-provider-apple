package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Analytics reports: a report request for an app makes Apple generate
// reports, each with instances per granularity and processing date, whose
// segments are the files to download
// (https://developer.apple.com/documentation/appstoreconnectapi/downloading-analytics-reports).
// The fake gives every request the same three reports, allows an app one
// active request per access type, and leaves the instances to
// AddAnalyticsReportInstance, because Apple generates them over days.

type analyticsRequest struct {
	id, appID, accessType string
	stopped               bool
}

type analyticsReport struct {
	gen.AnalyticsReport
	requestID string
}

type analyticsInstance struct {
	gen.AnalyticsReportInstance
	reportID string
	segments []gen.AnalyticsReportSegment
}

type analyticsState struct {
	requests  map[string]*analyticsRequest
	reports   map[string]*analyticsReport
	instances map[string]*analyticsInstance
}

func newAnalyticsState() *analyticsState {
	return &analyticsState{requests: map[string]*analyticsRequest{}, reports: map[string]*analyticsReport{}, instances: map[string]*analyticsInstance{}}
}

// AddAnalyticsReportInstance adds an instance with one segment to a report,
// as Apple does when it generates the report, and returns the instance's ID.
func (s *Server) AddAnalyticsReportInstance(reportID, granularity, processingDate string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := &analyticsInstance{reportID: reportID, segments: make([]gen.AnalyticsReportSegment, 1)}
	object(&i.AnalyticsReportInstance, "analyticsReportInstances", s.newID("AI"), map[string]any{"granularity": granularity, "processingDate": processingDate}, nil)
	segmentID := s.newID("AS")
	object(&i.segments[0], "analyticsReportSegments", segmentID,
		map[string]any{"url": s.URL + "/analytics/" + segmentID + ".csv.gz", "checksum": "md5-" + segmentID, "sizeInBytes": 2048}, nil)
	s.analytics.instances[i.Id] = i
	return i.Id
}

// StopAnalyticsReportRequest marks a request as stopped due to inactivity.
func (s *Server) StopAnalyticsReportRequest(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.analytics.requests[id].stopped = true
}

func renderAnalyticsRequest(q *analyticsRequest) gen.AnalyticsReportRequest {
	var out gen.AnalyticsReportRequest
	object(&out, "analyticsReportRequests", q.id, map[string]any{"accessType": q.accessType, "stoppedDueToInactivity": q.stopped}, nil)
	return out
}

func (s *Server) AnalyticsReportRequestsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AnalyticsReportRequestCreateRequest
	if !decode(w, r, &req) {
		return
	}
	appID, accessType := req.Data.Relationships.App.Data.Id, string(req.Data.Attributes.AccessType)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	for _, q := range s.analytics.requests {
		if q.appID == appID && q.accessType == accessType && !q.stopped {
			conflict(w, "STATE_ERROR", "The app already has an "+accessType+" analytics report request.", "/data/attributes/accessType")
			return
		}
	}
	q := &analyticsRequest{id: s.newID("AQ"), appID: appID, accessType: accessType}
	s.analytics.requests[q.id] = q
	for _, report := range [][2]string{
		{"App Store Discovery and Engagement Standard", "APP_STORE_ENGAGEMENT"}, {"App Sessions Standard", "APP_USAGE"},
		{"App Store Purchases Standard", "COMMERCE"},
	} {
		rep := &analyticsReport{requestID: q.id}
		object(&rep.AnalyticsReport, "analyticsReports", s.newID("AR"), map[string]any{"name": report[0], "category": report[1]}, nil)
		s.analytics.reports[rep.Id] = rep
	}
	writeJSON(w, http.StatusCreated, gen.AnalyticsReportRequestResponse{Data: renderAnalyticsRequest(q), Links: docLinks(r)})
}

func (s *Server) AnalyticsReportRequestsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AnalyticsReportRequestsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.analytics.requests[id]
	if !ok {
		notFound(w, "analyticsReportRequests", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AnalyticsReportRequestResponse{Data: renderAnalyticsRequest(q), Links: docLinks(r)})
}

// AnalyticsReportRequestsDeleteInstance deletes a request with its reports.
func (s *Server) AnalyticsReportRequestsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.analytics.requests[id]; !ok {
		notFound(w, "analyticsReportRequests", id)
		return
	}
	for reportID, report := range s.analytics.reports {
		if report.requestID != id {
			continue
		}
		for instanceID, instance := range s.analytics.instances {
			if instance.reportID == reportID {
				delete(s.analytics.instances, instanceID)
			}
		}
		delete(s.analytics.reports, reportID)
	}
	delete(s.analytics.requests, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsAnalyticsReportRequestsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAnalyticsReportRequestsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AnalyticsReportRequest
	for _, q := range sortedValues(s.analytics.requests) {
		if q.appID == id && matches(params.FilterAccessType, q.accessType) {
			items = append(items, renderAnalyticsRequest(q))
		}
	}
	writePage[gen.AnalyticsReportRequest, gen.AnalyticsReportRequestsResponse](w, r, items, params.Limit)
}

func (s *Server) AnalyticsReportRequestsReportsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AnalyticsReportRequestsReportsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.analytics.requests[id]; !ok {
		notFound(w, "analyticsReportRequests", id)
		return
	}
	var items []gen.AnalyticsReport
	for _, report := range sortedValues(s.analytics.reports) {
		a := report.Attributes
		if report.requestID == id && matches(params.FilterName, *a.Name) && matches(params.FilterCategory, string(*a.Category)) {
			items = append(items, report.AnalyticsReport)
		}
	}
	writePage[gen.AnalyticsReport, gen.AnalyticsReportsResponse](w, r, items, params.Limit)
}

func (s *Server) AnalyticsReportsInstancesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AnalyticsReportsInstancesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.analytics.reports[id]; !ok {
		notFound(w, "analyticsReports", id)
		return
	}
	var items []gen.AnalyticsReportInstance
	for _, instance := range sortedValues(s.analytics.instances) {
		a := instance.Attributes
		if instance.reportID == id && matches(params.FilterGranularity, string(*a.Granularity)) && matches(params.FilterProcessingDate, a.ProcessingDate.Format("2006-01-02")) {
			items = append(items, instance.AnalyticsReportInstance)
		}
	}
	writePage[gen.AnalyticsReportInstance, gen.AnalyticsReportInstancesResponse](w, r, items, params.Limit)
}

func (s *Server) AnalyticsReportInstancesSegmentsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AnalyticsReportInstancesSegmentsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, ok := s.analytics.instances[id]
	if !ok {
		notFound(w, "analyticsReportInstances", id)
		return
	}
	writePage[gen.AnalyticsReportSegment, gen.AnalyticsReportSegmentsResponse](w, r, instance.segments, params.Limit)
}

// BuildBundlesBuildBundleFileSizesGetToManyRelated returns the same two
// sizes for every bundle: a universal one and one for a device model.
func (s *Server) BuildBundlesBuildBundleFileSizesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.BuildBundlesBuildBundleFileSizesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.advanced.bundles[id]; !ok {
		notFound(w, buildBundlesRoute, id)
		return
	}
	items := make([]gen.BuildBundleFileSize, 2)
	object(&items[0], "buildBundleFileSizes", id+"-universal", map[string]any{"deviceModel": "Universal", "osVersion": "Universal", "downloadBytes": 52428800, "installBytes": 104857600}, nil)
	object(&items[1], "buildBundleFileSizes", id+"-iphone", map[string]any{"deviceModel": "iPhone18,1", "osVersion": "26.0", "downloadBytes": 20971520, "installBytes": 41943040}, nil)
	writePage[gen.BuildBundleFileSize, gen.BuildBundleFileSizesResponse](w, r, items, params.Limit)
}
