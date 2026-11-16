package asctest

import (
	"net/http"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// In-app events. An app has up to 50 draft events
// (https://developer.apple.com/help/app-store-connect/reference/in-app-events/in-app-event-statuses).
// An event has a localization per locale, with a name of up to 30
// characters, a short description of up to 50 and a long description of up
// to 120 (https://developer.apple.com/app-store/in-app-events/). Its media
// are App Asset Library placements on a localization; the event screenshot
// and video clip endpoints are deprecated
// (https://developer.apple.com/documentation/appstoreconnectapi/app-event-localizations).
// An event is reviewed as an item of a review submission.

const (
	maxDraftEvents          = 50
	maxEventName            = 30
	maxEventShortText       = 50
	maxEventLongText        = 120
	appEventsCollection     = "appEvents"
	eventLocalizationsRoute = "appEventLocalizations"
)

type appEvent struct {
	gen.AppEvent
	appID string
}

type eventsState struct {
	events        map[string]*appEvent
	localizations map[string]*gen.AppEventLocalization
}

func newEventsState() *eventsState {
	return &eventsState{events: map[string]*appEvent{}, localizations: map[string]*gen.AppEventLocalization{}}
}

// ---- events ----

func (s *Server) AppEventsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppEventCreateRequest
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
	drafts := 0
	for _, e := range s.events.events {
		if e.appID == appID && *e.Attributes.EventState == gen.AppEventAttributesEventStateDRAFT {
			drafts++
		}
	}
	if drafts >= maxDraftEvents {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app already has 50 draft in-app events.", "/data/relationships/app")
		return
	}
	e := &appEvent{appID: appID, AppEvent: gen.AppEvent{Id: s.newID("EV"), Type: gen.AppEventTypeAppEvents}}
	a := alloc(&e.Attributes)
	a.ReferenceName, a.EventState = new(in.ReferenceName), new(gen.AppEventAttributesEventStateDRAFT)
	a.DeepLink, a.PurchaseRequirement, a.PrimaryLocale = in.DeepLink, in.PurchaseRequirement, in.PrimaryLocale
	a.Priority, a.Purpose = new(gen.AppEventAttributesPriorityNORMAL), new(gen.AppEventAttributesPurposeAPPROPRIATEFORALLUSERS)
	if in.Badge != nil {
		a.Badge = new(gen.AppEventAttributesBadge(*in.Badge))
	}
	if in.Priority != nil {
		a.Priority = new(gen.AppEventAttributesPriority(*in.Priority))
	}
	if in.Purpose != nil {
		a.Purpose = new(gen.AppEventAttributesPurpose(*in.Purpose))
	}
	if in.TerritorySchedules != nil {
		a.TerritorySchedules = new(*in.TerritorySchedules)
	}
	s.events.events[e.Id] = e
	writeJSON(w, http.StatusCreated, gen.AppEventResponse{Data: e.AppEvent, Links: docLinks(r)})
}

func (s *Server) findEvent(w http.ResponseWriter, id string) *appEvent {
	e, ok := s.events.events[id]
	if !ok {
		notFound(w, appEventsCollection, id)
	}
	return e
}

func (s *Server) AppEventsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppEventsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findEvent(w, id); e != nil {
		writeJSON(w, http.StatusOK, gen.AppEventResponse{Data: e.AppEvent, Links: docLinks(r)})
	}
}

func (s *Server) AppEventsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppEventUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.findEvent(w, id)
	if e == nil {
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := e.Attributes
		if in.ReferenceName != nil {
			a.ReferenceName = new(*in.ReferenceName)
		}
		if in.Badge != nil {
			a.Badge = new(gen.AppEventAttributesBadge(*in.Badge))
		}
		if in.DeepLink != nil {
			a.DeepLink = new(*in.DeepLink)
		}
		if in.PurchaseRequirement != nil {
			a.PurchaseRequirement = new(*in.PurchaseRequirement)
		}
		if in.PrimaryLocale != nil {
			a.PrimaryLocale = new(*in.PrimaryLocale)
		}
		if in.Priority != nil {
			a.Priority = new(gen.AppEventAttributesPriority(*in.Priority))
		}
		if in.Purpose != nil {
			a.Purpose = new(gen.AppEventAttributesPurpose(*in.Purpose))
		}
		if in.TerritorySchedules != nil {
			a.TerritorySchedules = new(*in.TerritorySchedules)
		}
	}
	writeJSON(w, http.StatusOK, gen.AppEventResponse{Data: e.AppEvent, Links: docLinks(r)})
}

// removeEventLocalization deletes a localization with its placements.
func (s *Server) removeEventLocalization(id string) {
	for pid, p := range s.lib.placements {
		if p.targetKind == eventTarget && p.targetID == id {
			key := placementKey(p.targetID, p.group)
			s.lib.order[key] = slices.DeleteFunc(s.lib.order[key], func(other string) bool { return other == pid })
			delete(s.lib.placements, pid)
		}
	}
	delete(s.events.localizations, id)
}

// AppEventsDeleteInstance deletes an event with its localizations.
func (s *Server) AppEventsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findEvent(w, id) == nil {
		return
	}
	for lid, l := range s.events.localizations {
		if l.Relationships.AppEvent.Data.Id == id {
			s.removeEventLocalization(lid)
		}
	}
	delete(s.events.events, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsAppEventsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppEventsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppEvent
	for _, e := range sortedValues(s.events.events) {
		if e.appID == id && matches(params.FilterEventState, string(*e.Attributes.EventState)) && matches(params.FilterId, e.Id) {
			items = append(items, e.AppEvent)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppEventsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- review ----

// addEventItem adds a draft or rejected event of the submission's app to a
// review submission.
func (s *Server) addEventItem(w http.ResponseWriter, sub *reviewSubmission, eventID string) bool {
	e, ok := s.events.events[eventID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appEvents with id '"+eventID+"'", "/data/relationships/appEvent")
		return false
	}
	if e.appID != sub.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The in-app event belongs to another app.", "/data/relationships/appEvent")
		return false
	}
	if state := *e.Attributes.EventState; state != gen.AppEventAttributesEventStateDRAFT && state != gen.AppEventAttributesEventStateREJECTED {
		stateError(w, "The in-app event is already in review or approved.")
		return false
	}
	if len(s.eventLocalizations(eventID)) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The in-app event has no localizations.", "/data/relationships/appEvent")
		return false
	}
	e.Attributes.EventState = new(gen.AppEventAttributesEventStateREADYFORREVIEW)
	sub.events = append(sub.events, eventID)
	return true
}

func (s *Server) setEventStates(sub *reviewSubmission, state gen.AppEventAttributesEventState) {
	for _, id := range sub.events {
		if e, ok := s.events.events[id]; ok {
			e.Attributes.EventState = new(state)
		}
	}
}

// ApproveAppEvent approves an in-app event, as App Review does.
func (s *Server) ApproveAppEvent(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events.events[id].Attributes.EventState = new(gen.AppEventAttributesEventStateAPPROVED)
}

// ---- localizations ----

func (s *Server) eventLocalizations(eventID string) []*gen.AppEventLocalization {
	var out []*gen.AppEventLocalization
	for _, l := range sortedValues(s.events.localizations) {
		if l.Relationships.AppEvent.Data.Id == eventID {
			out = append(out, l)
		}
	}
	return out
}

// checkEventText writes an error and returns false when a localized text is
// too long.
func checkEventText(w http.ResponseWriter, name, short, long *string) bool {
	for _, f := range []struct {
		field string
		value *string
		limit int
	}{{"name", name, maxEventName}, {"shortDescription", short, maxEventShortText}, {"longDescription", long, maxEventLongText}} {
		if len([]rune(asc.Value(f.value))) > f.limit {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.TOO_LONG", "The "+f.field+" is too long.", "/data/attributes/"+f.field)
			return false
		}
	}
	return true
}

func (s *Server) AppEventLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppEventLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, eventID := req.Data.Attributes, req.Data.Relationships.AppEvent.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events.events[eventID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no appEvents with id '"+eventID+"'", "/data/relationships/appEvent")
		return
	}
	if !checkEventText(w, in.Name, in.ShortDescription, in.LongDescription) {
		return
	}
	for _, l := range s.eventLocalizations(eventID) {
		if *l.Attributes.Locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The in-app event already has a localization for "+in.Locale+".", "/data/attributes/locale")
			return
		}
	}
	l := &gen.AppEventLocalization{Id: s.newID("EL"), Type: gen.AppEventLocalizationTypeAppEventLocalizations}
	a := alloc(&l.Attributes)
	a.Locale, a.Name, a.ShortDescription, a.LongDescription = new(in.Locale), in.Name, in.ShortDescription, in.LongDescription
	e := alloc(&alloc(&alloc(&l.Relationships).AppEvent).Data)
	e.Id, e.Type = eventID, "appEvents"
	s.events.localizations[l.Id] = l
	writeJSON(w, http.StatusCreated, gen.AppEventLocalizationResponse{Data: *l, Links: docLinks(r)})
}

func (s *Server) findEventLocalization(w http.ResponseWriter, id string) *gen.AppEventLocalization {
	l, ok := s.events.localizations[id]
	if !ok {
		notFound(w, eventLocalizationsRoute, id)
	}
	return l
}

func (s *Server) AppEventLocalizationsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.AppEventLocalizationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.findEventLocalization(w, id); l != nil {
		writeJSON(w, http.StatusOK, gen.AppEventLocalizationResponse{Data: *l, Links: docLinks(r)})
	}
}

// AppEventLocalizationsUpdateInstance changes a localization's text; an
// empty string clears a value.
func (s *Server) AppEventLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppEventLocalizationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.findEventLocalization(w, id)
	if l == nil {
		return
	}
	if in := req.Data.Attributes; in != nil {
		if !checkEventText(w, in.Name, in.ShortDescription, in.LongDescription) {
			return
		}
		for _, f := range []struct{ dst, src **string }{
			{&l.Attributes.Name, &in.Name}, {&l.Attributes.ShortDescription, &in.ShortDescription}, {&l.Attributes.LongDescription, &in.LongDescription},
		} {
			switch {
			case *f.src == nil:
			case **f.src == "":
				*f.dst = nil
			default:
				*f.dst = new(**f.src)
			}
		}
	}
	writeJSON(w, http.StatusOK, gen.AppEventLocalizationResponse{Data: *l, Links: docLinks(r)})
}

// AppEventLocalizationsDeleteInstance deletes a localization with its
// placements.
func (s *Server) AppEventLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findEventLocalization(w, id) == nil {
		return
	}
	s.removeEventLocalization(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppEventsLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppEventsLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findEvent(w, id) == nil {
		return
	}
	var items []gen.AppEventLocalization
	for _, l := range s.eventLocalizations(id) {
		items = append(items, *l)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppEventLocalizationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}
