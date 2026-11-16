package asctest

import (
	"net/http"
	"slices"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Product page optimization. An experiment tests up to three treatments
// against an app's product page, with a name of up to 64 characters. An app
// has one experiment at a time; a test can't change once it has started, and
// applying a treatment to the product page ends it
// (https://developer.apple.com/app-store/product-page-optimization/,
// https://developer.apple.com/documentation/appstoreconnectapi/post-v2-appstoreversionexperiments).
// A treatment has a localization per locale, which App Asset Library
// placements target. The fake's states are assumptions: an experiment with
// treatment localizations needs review, starting one makes it APPROVED,
// stopping it STOPPED and applying a treatment COMPLETED; treatments can be
// deleted before an experiment starts and after it ends.

const (
	maxTreatments          = 3
	maxExperimentName      = 64
	experimentsRoute       = "appStoreVersionExperiments"
	treatmentsRoute        = "appStoreVersionExperimentTreatments"
	treatmentLocalizations = "appStoreVersionExperimentTreatmentLocalizations"

	experimentDraft     = gen.AppStoreVersionExperimentV2AttributesStatePREPAREFORSUBMISSION
	experimentReady     = gen.AppStoreVersionExperimentV2AttributesStateREADYFORREVIEW
	experimentWaiting   = gen.AppStoreVersionExperimentV2AttributesStateWAITINGFORREVIEW
	experimentAccepted  = gen.AppStoreVersionExperimentV2AttributesStateACCEPTED
	experimentRunning   = gen.AppStoreVersionExperimentV2AttributesStateAPPROVED
	experimentStopped   = gen.AppStoreVersionExperimentV2AttributesStateSTOPPED
	experimentCompleted = gen.AppStoreVersionExperimentV2AttributesStateCOMPLETED
)

type experiment struct {
	id, appID, name string
	platform        gen.Platform
	traffic         int
	state           gen.AppStoreVersionExperimentV2AttributesState
	start, end      *time.Time
}

type treatment struct {
	id, experimentID, name string
	appIconName            *string
	promoted               *time.Time
}

type treatmentLocalization struct {
	id, treatmentID, locale string
}

type experimentsState struct {
	experiments   map[string]*experiment
	treatments    map[string]*treatment
	localizations map[string]*treatmentLocalization
}

func newExperimentsState() *experimentsState {
	return &experimentsState{experiments: map[string]*experiment{}, treatments: map[string]*treatment{}, localizations: map[string]*treatmentLocalization{}}
}

// ---- experiments ----

func (s *Server) experimentTreatments(experimentID string) []*treatment {
	var out []*treatment
	for _, t := range sortedValues(s.experiments.treatments) {
		if t.experimentID == experimentID {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) treatmentLocalizations(treatmentID string) []*treatmentLocalization {
	var out []*treatmentLocalization
	for _, l := range sortedValues(s.experiments.localizations) {
		if l.treatmentID == treatmentID {
			out = append(out, l)
		}
	}
	return out
}

// reviewRequired reports whether the experiment has new metadata to review.
func (s *Server) reviewRequired(e *experiment) bool {
	for _, t := range s.experimentTreatments(e.id) {
		if len(s.treatmentLocalizations(t.id)) > 0 {
			return true
		}
	}
	return false
}

func dateTime(t *time.Time) *gen.DateTime {
	if t == nil {
		return nil
	}
	return &gen.DateTime{Time: *t}
}

func (s *Server) renderExperiment(e *experiment) gen.AppStoreVersionExperimentV2 {
	out := gen.AppStoreVersionExperimentV2{Id: e.id, Type: gen.AppStoreVersionExperimentV2TypeAppStoreVersionExperiments}
	a := alloc(&out.Attributes)
	a.Name, a.Platform, a.TrafficProportion, a.State = new(e.name), new(e.platform), new(e.traffic), new(e.state)
	a.ReviewRequired, a.StartDate, a.EndDate = new(s.reviewRequired(e)), dateTime(e.start), dateTime(e.end)
	app := alloc(&alloc(&alloc(&out.Relationships).App).Data)
	app.Id, app.Type = e.appID, "apps"
	return out
}

// checkExperiment writes an error and returns false when the name or the
// traffic proportion is invalid.
func checkExperiment(w http.ResponseWriter, name string, traffic int) bool {
	if name == "" || len([]rune(name)) > maxExperimentName {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The name must have 1 to 64 characters.", "/data/attributes/name")
		return false
	}
	if traffic < 1 || traffic > 100 {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The traffic proportion must be 1 to 100.", "/data/attributes/trafficProportion")
		return false
	}
	return true
}

var finishedExperimentStates = []gen.AppStoreVersionExperimentV2AttributesState{experimentStopped, experimentCompleted}

func (s *Server) AppStoreVersionExperimentsV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionExperimentV2CreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, appID := req.Data.Attributes, req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	if !checkExperiment(w, in.Name, in.TrafficProportion) {
		return
	}
	for _, e := range s.experiments.experiments {
		if e.appID == appID && e.platform == in.Platform && !slices.Contains(finishedExperimentStates, e.state) {
			stateError(w, "Cannot create new experiment because another experiment is in draft state")
			return
		}
	}
	e := &experiment{id: s.newID("X"), appID: appID, name: in.Name, platform: in.Platform, traffic: in.TrafficProportion, state: experimentDraft}
	s.experiments.experiments[e.id] = e
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionExperimentV2Response{Data: s.renderExperiment(e), Links: docLinks(r)})
}

func (s *Server) findExperiment(w http.ResponseWriter, id string) *experiment {
	e, ok := s.experiments.experiments[id]
	if !ok {
		notFound(w, experimentsRoute, id)
	}
	return e
}

func (s *Server) AppStoreVersionExperimentsV2GetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionExperimentsV2GetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findExperiment(w, id); e != nil {
		writeJSON(w, http.StatusOK, gen.AppStoreVersionExperimentV2Response{Data: s.renderExperiment(e), Links: docLinks(r)})
	}
}

// AppStoreVersionExperimentsV2UpdateInstance renames an experiment that
// hasn't started or changes its traffic, and starts or stops it.
func (s *Server) AppStoreVersionExperimentsV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreVersionExperimentV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.findExperiment(w, id)
	if e == nil {
		return
	}
	in := asc.Value(req.Data.Attributes)
	if in.Name != nil || in.TrafficProportion != nil {
		if e.start != nil {
			stateError(w, "An experiment can't change once it has started.")
			return
		}
		name, traffic := e.name, e.traffic
		if in.Name != nil {
			name = *in.Name
		}
		if in.TrafficProportion != nil {
			traffic = *in.TrafficProportion
		}
		if !checkExperiment(w, name, traffic) {
			return
		}
		e.name, e.traffic = name, traffic
	}
	now := s.now().UTC().Truncate(time.Second)
	switch {
	case in.Started == nil:
	case *in.Started:
		ready := e.state == experimentAccepted || e.state == experimentDraft && !s.reviewRequired(e)
		if !ready || len(s.experimentTreatments(e.id)) == 0 {
			stateError(w, "Only an experiment with treatments, reviewed if it needs review, can start.")
			return
		}
		e.state, e.start = experimentRunning, &now
	default:
		if e.state != experimentRunning {
			stateError(w, "Only a running experiment can stop.")
			return
		}
		e.state, e.end = experimentStopped, &now
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionExperimentV2Response{Data: s.renderExperiment(e), Links: docLinks(r)})
}

// AppStoreVersionExperimentsV2DeleteInstance deletes an experiment with its
// treatments and their localizations.
func (s *Server) AppStoreVersionExperimentsV2DeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findExperiment(w, id) == nil {
		return
	}
	for _, t := range s.experimentTreatments(id) {
		s.removeTreatment(t.id)
	}
	delete(s.experiments.experiments, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsAppStoreVersionExperimentsV2GetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppStoreVersionExperimentsV2GetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppStoreVersionExperimentV2
	for _, e := range sortedValues(s.experiments.experiments) {
		if e.appID == id && matches(params.FilterState, string(e.state)) {
			items = append(items, s.renderExperiment(e))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionExperimentsV2Response{Data: nonNil(data), Links: links, Meta: meta})
}

// addExperimentItem adds an experiment being prepared to a review
// submission of its app and platform.
func (s *Server) addExperimentItem(w http.ResponseWriter, sub *reviewSubmission, experimentID string) bool {
	e, ok := s.experiments.experiments[experimentID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersionExperiments with id '"+experimentID+"'", "/data/relationships/appStoreVersionExperimentV2")
		return false
	}
	if e.appID != sub.appID || e.platform != *sub.Attributes.Platform {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The experiment belongs to another app or platform.", "/data/relationships/appStoreVersionExperimentV2")
		return false
	}
	if e.state != experimentDraft {
		stateError(w, "The experiment is already in review, reviewed or finished.")
		return false
	}
	if len(s.experimentTreatments(experimentID)) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The experiment has no treatments.", "/data/relationships/appStoreVersionExperimentV2")
		return false
	}
	e.state = experimentReady
	sub.experiments = append(sub.experiments, experimentID)
	return true
}

func (s *Server) setExperimentStates(sub *reviewSubmission, state gen.AppStoreVersionExperimentV2AttributesState) {
	for _, id := range sub.experiments {
		if e, ok := s.experiments.experiments[id]; ok {
			e.state = state
		}
	}
}

// AcceptExperiment accepts an experiment in review, as App Review does,
// after which it can start.
func (s *Server) AcceptExperiment(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.experiments.experiments[id].state = experimentAccepted
}

// ---- treatments ----

func renderTreatment(t *treatment) gen.AppStoreVersionExperimentTreatment {
	out := gen.AppStoreVersionExperimentTreatment{Id: t.id, Type: gen.AppStoreVersionExperimentTreatmentTypeAppStoreVersionExperimentTreatments}
	a := alloc(&out.Attributes)
	a.Name, a.AppIconName, a.PromotedDate = new(t.name), t.appIconName, dateTime(t.promoted)
	e := alloc(&alloc(&alloc(&out.Relationships).AppStoreVersionExperimentV2).Data)
	e.Id, e.Type = t.experimentID, experimentsRoute
	return out
}

// removableFrom writes an error and returns false while the experiment is
// running: its treatments can be deleted before it starts and after it ends.
func (s *Server) removableFrom(w http.ResponseWriter, experimentID string) bool {
	if s.experiments.experiments[experimentID].state == experimentRunning {
		stateError(w, "A running experiment can't change.")
		return false
	}
	return true
}

// unstartedExperiment writes an error and returns false once the experiment
// has started.
func (s *Server) unstartedExperiment(w http.ResponseWriter, id string) bool {
	if s.experiments.experiments[id].start != nil {
		stateError(w, "An experiment can't change once it has started.")
		return false
	}
	return true
}

func (s *Server) AppStoreVersionExperimentTreatmentsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionExperimentTreatmentCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := asc.Value(req.Data.Relationships)
	if rel.AppStoreVersionExperimentV2 == nil || rel.AppStoreVersionExperimentV2.Data == nil {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "The fake supports only version 2 experiments.", "/data/relationships/appStoreVersionExperimentV2")
		return
	}
	experimentID := rel.AppStoreVersionExperimentV2.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.experiments.experiments[experimentID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersionExperiments with id '"+experimentID+"'", "/data/relationships/appStoreVersionExperimentV2")
		return
	}
	if !s.unstartedExperiment(w, experimentID) {
		return
	}
	if len(s.experimentTreatments(experimentID)) >= maxTreatments {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The experiment already has 3 treatments.", "/data/relationships/appStoreVersionExperimentV2")
		return
	}
	t := &treatment{id: s.newID("TR"), experimentID: experimentID, name: req.Data.Attributes.Name, appIconName: req.Data.Attributes.AppIconName}
	s.experiments.treatments[t.id] = t
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionExperimentTreatmentResponse{Data: renderTreatment(t), Links: docLinks(r)})
}

func (s *Server) findTreatment(w http.ResponseWriter, id string) *treatment {
	t, ok := s.experiments.treatments[id]
	if !ok {
		notFound(w, treatmentsRoute, id)
	}
	return t
}

func (s *Server) AppStoreVersionExperimentTreatmentsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionExperimentTreatmentsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.findTreatment(w, id); t != nil {
		writeJSON(w, http.StatusOK, gen.AppStoreVersionExperimentTreatmentResponse{Data: renderTreatment(t), Links: docLinks(r)})
	}
}

func (s *Server) AppStoreVersionExperimentTreatmentsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppStoreVersionExperimentTreatmentUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.findTreatment(w, id)
	if t == nil || !s.unstartedExperiment(w, t.experimentID) {
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.Name != nil {
			t.name = *in.Name
		}
		if in.AppIconName != nil || nulls["appIconName"] {
			t.appIconName = in.AppIconName
		}
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionExperimentTreatmentResponse{Data: renderTreatment(t), Links: docLinks(r)})
}

// removeTreatment deletes a treatment with its localizations and their
// placements.
func (s *Server) removeTreatment(id string) {
	for _, l := range s.treatmentLocalizations(id) {
		s.removeTreatmentLocalization(l.id)
	}
	delete(s.experiments.treatments, id)
}

func (s *Server) AppStoreVersionExperimentTreatmentsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.findTreatment(w, id)
	if t == nil || !s.removableFrom(w, t.experimentID) {
		return
	}
	s.removeTreatment(id)
	w.WriteHeader(http.StatusNoContent)
}

// AppStoreVersionPromotionsCreateInstance applies a treatment to a version
// of the experiment's app, which ends the experiment.
func (s *Server) AppStoreVersionPromotionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionPromotionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.experiments.treatments[rel.AppStoreVersionExperimentTreatment.Data.Id]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersionExperimentTreatments with id '"+rel.AppStoreVersionExperimentTreatment.Data.Id+"'", "/data/relationships/appStoreVersionExperimentTreatment")
		return
	}
	e := s.experiments.experiments[t.experimentID]
	v, ok := s.versions.versions[rel.AppStoreVersion.Data.Id]
	if !ok || v.appID != e.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app has no appStoreVersions with id '"+rel.AppStoreVersion.Data.Id+"'", "/data/relationships/appStoreVersion")
		return
	}
	if e.start == nil {
		stateError(w, "Only a treatment of an experiment that has started can be applied.")
		return
	}
	now := s.now().UTC().Truncate(time.Second)
	t.promoted = &now
	if e.end == nil {
		e.end = &now
	}
	e.state = experimentCompleted
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionPromotionResponse{
		Data:  gen.AppStoreVersionPromotion{Id: s.newID("PR"), Type: gen.AppStoreVersionPromotionTypeAppStoreVersionPromotions},
		Links: docLinks(r),
	})
}

// ---- treatment localizations ----

func renderTreatmentLocalization(l *treatmentLocalization) gen.AppStoreVersionExperimentTreatmentLocalization {
	out := gen.AppStoreVersionExperimentTreatmentLocalization{Id: l.id, Type: gen.AppStoreVersionExperimentTreatmentLocalizationTypeAppStoreVersionExperimentTreatmentLocalizations}
	alloc(&out.Attributes).Locale = new(l.locale)
	t := alloc(&alloc(&alloc(&out.Relationships).AppStoreVersionExperimentTreatment).Data)
	t.Id, t.Type = l.treatmentID, treatmentsRoute
	return out
}

// removeTreatmentLocalization deletes a localization with its placements.
func (s *Server) removeTreatmentLocalization(id string) {
	for pid, p := range s.lib.placements {
		if p.targetKind == treatmentLocalizationTarget && p.targetID == id {
			key := placementKey(p.targetID, p.group)
			s.lib.order[key] = slices.DeleteFunc(s.lib.order[key], func(other string) bool { return other == pid })
			delete(s.lib.placements, pid)
		}
	}
	delete(s.experiments.localizations, id)
}

func (s *Server) AppStoreVersionExperimentTreatmentLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppStoreVersionExperimentTreatmentLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	locale, treatmentID := req.Data.Attributes.Locale, req.Data.Relationships.AppStoreVersionExperimentTreatment.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.experiments.treatments[treatmentID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appStoreVersionExperimentTreatments with id '"+treatmentID+"'", "/data/relationships/appStoreVersionExperimentTreatment")
		return
	}
	if !s.unstartedExperiment(w, t.experimentID) {
		return
	}
	for _, l := range s.treatmentLocalizations(treatmentID) {
		if l.locale == locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The treatment already has a localization for "+locale+".", "/data/attributes/locale")
			return
		}
	}
	l := &treatmentLocalization{id: s.newID("TL"), treatmentID: treatmentID, locale: locale}
	s.experiments.localizations[l.id] = l
	writeJSON(w, http.StatusCreated, gen.AppStoreVersionExperimentTreatmentLocalizationResponse{Data: renderTreatmentLocalization(l), Links: docLinks(r)})
}

func (s *Server) AppStoreVersionExperimentTreatmentLocalizationsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppStoreVersionExperimentTreatmentLocalizationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.experiments.localizations[id]
	if !ok {
		notFound(w, treatmentLocalizations, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionExperimentTreatmentLocalizationResponse{Data: renderTreatmentLocalization(l), Links: docLinks(r)})
}

func (s *Server) AppStoreVersionExperimentTreatmentLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.experiments.localizations[id]
	if !ok {
		notFound(w, treatmentLocalizations, id)
		return
	}
	if !s.removableFrom(w, s.experiments.treatments[l.treatmentID].experimentID) {
		return
	}
	s.removeTreatmentLocalization(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppStoreVersionExperimentTreatmentsAppStoreVersionExperimentTreatmentLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppStoreVersionExperimentTreatmentsAppStoreVersionExperimentTreatmentLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findTreatment(w, id) == nil {
		return
	}
	var items []gen.AppStoreVersionExperimentTreatmentLocalization
	for _, l := range s.treatmentLocalizations(id) {
		if matches(params.FilterLocale, l.locale) {
			items = append(items, renderTreatmentLocalization(l))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppStoreVersionExperimentTreatmentLocalizationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}
