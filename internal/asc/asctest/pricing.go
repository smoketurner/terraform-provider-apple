package asctest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Pricing and availability. An app is sold at a price point in a base
// territory, from which App Store Connect equalizes prices in the others
// unless they have their own manual price; a price schedule is replaced as a
// whole (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-apppriceschedules).
// An app has no availability until one is created with every territory
// inline; each territory's availability is then updated on its own
// (https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-territoryavailabilities-_id_).

// fakeTerritories is the fake's territory catalog, with currencies.
var fakeTerritories = map[string]string{"CAN": "CAD", "FRA": "EUR", "GBR": "GBP", "JPN": "JPY", "USA": "USD"}

// priceTiers are the customer prices of the fake's price points, in each
// territory's currency; tier 0 is free.
var priceTiers = []string{"0.0", "0.99", "1.99", "2.99", "4.99"}

type pricingState struct {
	availability map[string]*availability  // by app ID
	schedules    map[string]*priceSchedule // by app ID
}

type availability struct {
	availableInNew bool
	territories    map[string]*gen.TerritoryAvailability // by territory ID
}

type priceSchedule struct {
	baseTerritory string
	prices        []gen.AppPriceV2
}

func newPricingState() *pricingState {
	return &pricingState{availability: map[string]*availability{}, schedules: map[string]*priceSchedule{}}
}

// pricePointID encodes a price point; Apple's IDs are opaque.
func pricePointID(appID, territory string, tier int) string {
	return fmt.Sprintf("%s-%s-%d", appID, territory, tier)
}

func parsePricePoint(id string) (appID, territory string, tier int, ok bool) {
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		return "", "", 0, false
	}
	tier, err := strconv.Atoi(parts[2])
	if _, known := fakeTerritories[parts[1]]; err != nil || !known || tier < 0 || tier >= len(priceTiers) {
		return "", "", 0, false
	}
	return parts[0], parts[1], tier, true
}

func renderPricePoint(appID, territory string, tier int) gen.AppPricePointV3 {
	p := gen.AppPricePointV3{Id: pricePointID(appID, territory, tier), Type: gen.AppPricePointV3TypeAppPricePoints}
	a := alloc(&p.Attributes)
	a.CustomerPrice = new(priceTiers[tier])
	price, _ := strconv.ParseFloat(priceTiers[tier], 64)
	a.Proceeds = new(strconv.FormatFloat(price*0.7, 'f', 2, 64))
	data := alloc(&alloc(&alloc(&p.Relationships).Territory).Data)
	data.Id, data.Type = territory, "territories"
	return p
}

func (s *Server) TerritoriesGetCollection(w http.ResponseWriter, r *http.Request, params gen.TerritoriesGetCollectionParams) {
	var items []gen.Territory
	for _, id := range sortedKeys(fakeTerritories) {
		t := gen.Territory{Id: id, Type: gen.TerritoryTypeTerritories}
		alloc(&t.Attributes).Currency = new(fakeTerritories[id])
		items = append(items, t)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.TerritoriesResponse{Data: data, Links: links, Meta: meta})
}

func (s *Server) AppsAppPricePointsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsAppPricePointsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.AppPricePointV3
	for _, territory := range sortedKeys(fakeTerritories) {
		if !matches(params.FilterTerritory, territory) {
			continue
		}
		for tier := range priceTiers {
			items = append(items, renderPricePoint(id, territory, tier))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppPricePointsV3Response{Data: nonNil(data), Links: links, Meta: meta})
}

// AppPricePointsV3EqualizationsGetToManyRelated lists the price points of
// the other territories that are equivalent to a price point.
func (s *Server) AppPricePointsV3EqualizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppPricePointsV3EqualizationsGetToManyRelatedParams) {
	appID, territory, tier, ok := parsePricePoint(id)
	if !ok {
		notFound(w, "appPricePoints", id)
		return
	}
	var items []gen.AppPricePointV3
	for _, other := range sortedKeys(fakeTerritories) {
		if other != territory && matches(params.FilterTerritory, other) {
			items = append(items, renderPricePoint(appID, other, tier))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppPricePointsV3Response{Data: nonNil(data), Links: links, Meta: meta})
}

// AppPriceSchedulesCreateInstance replaces an app's price schedule. The
// manual prices are created inline, referenced by local IDs; the base
// territory needs a manual price.
func (s *Server) AppPriceSchedulesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppPriceScheduleCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	appID, base := rel.App.Data.Id, rel.BaseTerritory.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	if _, ok := fakeTerritories[base]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no territories with id '"+base+"'", "/data/relationships/baseTerritory")
		return
	}
	inline := map[string]gen.AppPriceV2InlineCreate{}
	for i, item := range asc.Value(req.Included) {
		p, err := item.AsAppPriceV2InlineCreate()
		if err != nil || p.Type != gen.AppPriceV2InlineCreateTypeAppPrices {
			continue
		}
		if p.Id == nil || !localID(*p.Id) {
			conflict(w, "ENTITY_ERROR.INCLUDED.INVALID_ID", "For inline creation, the id must be a local id with the format '${local-id}'.", fmt.Sprintf("/included/%d/id", i))
			return
		}
		inline[*p.Id] = p
	}
	sched := &priceSchedule{baseTerritory: base}
	undated := map[string]bool{}
	for i, ref := range rel.ManualPrices.Data {
		p, ok := inline[ref.Id]
		pointer := fmt.Sprintf("/data/relationships/manualPrices/data/%d", i)
		if !ok || p.Relationships == nil || p.Relationships.AppPricePoint == nil || p.Relationships.AppPricePoint.Data == nil {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The manual price '"+ref.Id+"' is not included, or has no price point.", pointer)
			return
		}
		pointApp, territory, _, ok := parsePricePoint(p.Relationships.AppPricePoint.Data.Id)
		if !ok || pointApp != appID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no price point '"+p.Relationships.AppPricePoint.Data.Id+"' of this app.", pointer)
			return
		}
		price := gen.AppPriceV2{Id: s.newID("Y"), Type: gen.AppPriceV2TypeAppPrices}
		a := alloc(&price.Attributes)
		a.Manual = new(true)
		if p.Attributes != nil {
			a.StartDate, a.EndDate = p.Attributes.StartDate, p.Attributes.EndDate
		}
		if a.StartDate == nil && a.EndDate == nil {
			if undated[territory] {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A territory can have only one price without dates.", pointer)
				return
			}
			undated[territory] = true
		}
		rels := alloc(&price.Relationships)
		point := alloc(&alloc(&rels.AppPricePoint).Data)
		point.Id, point.Type = p.Relationships.AppPricePoint.Data.Id, "appPricePoints"
		t := alloc(&alloc(&rels.Territory).Data)
		t.Id, t.Type = territory, "territories"
		sched.prices = append(sched.prices, price)
	}
	if !slices.ContainsFunc(sched.prices, func(p gen.AppPriceV2) bool { return p.Relationships.Territory.Data.Id == base }) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The base territory "+base+" needs a manual price.", "/data/relationships/manualPrices")
		return
	}
	s.pricing.schedules[appID] = sched
	writeJSON(w, http.StatusCreated, gen.AppPriceScheduleResponse{Data: renderSchedule(appID), Links: docLinks(r)})
}

func localID(id string) bool {
	return strings.HasPrefix(id, "${") && strings.HasSuffix(id, "}") && len(id) > 3
}

// The schedule and availability IDs are the app's ID, as Apple's are.
func renderSchedule(appID string) gen.AppPriceSchedule {
	return gen.AppPriceSchedule{Id: appID, Type: gen.AppPriceScheduleTypeAppPriceSchedules}
}

func (s *Server) AppsAppPriceScheduleGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsAppPriceScheduleGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pricing.schedules[id]; !ok {
		notFound(w, "appPriceSchedules", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppPriceScheduleResponse{Data: renderSchedule(id), Links: docLinks(r)})
}

func (s *Server) AppPriceSchedulesBaseTerritoryGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppPriceSchedulesBaseTerritoryGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sched, ok := s.pricing.schedules[id]
	if !ok {
		notFound(w, "appPriceSchedules", id)
		return
	}
	t := gen.Territory{Id: sched.baseTerritory, Type: gen.TerritoryTypeTerritories}
	alloc(&t.Attributes).Currency = new(fakeTerritories[sched.baseTerritory])
	writeJSON(w, http.StatusOK, gen.TerritoryResponse{Data: t, Links: docLinks(r)})
}

func (s *Server) AppPriceSchedulesManualPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppPriceSchedulesManualPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sched, ok := s.pricing.schedules[id]
	if !ok {
		notFound(w, "appPriceSchedules", id)
		return
	}
	var items []gen.AppPriceV2
	for _, p := range sched.prices {
		if matches(params.FilterTerritory, p.Relationships.Territory.Data.Id) {
			items = append(items, p)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.AppPricesV2Response{Data: nonNil(data), Links: links, Meta: meta})
}

// AppAvailabilitiesV2CreateInstance creates an app's availability, with
// every territory's availability inline, or replaces an existing one: then
// the request lists the existing territory availabilities to keep, and the
// others become unavailable.
func (s *Server) AppAvailabilitiesV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.AppAvailabilityV2CreateRequest
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
	inline := map[string]gen.TerritoryAvailabilityInlineCreate{}
	for i, item := range asc.Value(req.Included) {
		if item.Id == nil || !localID(*item.Id) {
			conflict(w, "ENTITY_ERROR.INCLUDED.INVALID_ID", "For inline creation, the id must be a local id with the format '${local-id}'.", fmt.Sprintf("/included/%d/id", i))
			return
		}
		inline[*item.Id] = item
	}
	existing := s.pricing.availability[appID]
	next := &availability{availableInNew: req.Data.Attributes.AvailableInNewTerritories, territories: map[string]*gen.TerritoryAvailability{}}
	for i, ref := range req.Data.Relationships.TerritoryAvailabilities.Data {
		pointer := fmt.Sprintf("/data/relationships/territoryAvailabilities/data/%d", i)
		if item, ok := inline[ref.Id]; ok {
			if item.Relationships == nil || item.Relationships.Territory == nil || item.Relationships.Territory.Data == nil {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "The territory availability has no territory.", pointer)
				return
			}
			territory := item.Relationships.Territory.Data.Id
			if _, known := fakeTerritories[territory]; !known {
				conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no territories with id '"+territory+"'", pointer)
				return
			}
			ta := &gen.TerritoryAvailability{Id: "TA-" + appID + "-" + territory, Type: gen.TerritoryAvailabilityTypeTerritoryAvailabilities}
			a := alloc(&ta.Attributes)
			a.Available, a.PreOrderEnabled = new(false), new(false)
			if in := item.Attributes; in != nil {
				if in.Available != nil {
					a.Available = new(*in.Available)
				}
			}
			t := alloc(&alloc(&alloc(&ta.Relationships).Territory).Data)
			t.Id, t.Type = territory, "territories"
			next.territories[territory] = ta
			continue
		}
		var found *gen.TerritoryAvailability
		if existing != nil {
			for _, ta := range existing.territories {
				if ta.Id == ref.Id {
					found = ta
				}
			}
		}
		if found == nil {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no territoryAvailabilities with id '"+ref.Id+"'", pointer)
			return
		}
		next.territories[found.Relationships.Territory.Data.Id] = found
	}
	if existing != nil {
		for territory, ta := range existing.territories {
			if _, kept := next.territories[territory]; !kept {
				ta.Attributes.Available, ta.Attributes.PreOrderEnabled, ta.Attributes.ReleaseDate = new(false), new(false), nil
				next.territories[territory] = ta
			}
		}
	}
	for _, ta := range next.territories {
		ta.Attributes.ContentStatuses = new(contentStatuses(ta))
	}
	s.pricing.availability[appID] = next
	writeJSON(w, http.StatusCreated, gen.AppAvailabilityV2Response{Data: renderAvailability(appID, next), Links: docLinks(r)})
}

func contentStatuses(ta *gen.TerritoryAvailability) []gen.TerritoryAvailabilityAttributesContentStatuses {
	a := ta.Attributes
	switch {
	case !*a.Available:
		return []gen.TerritoryAvailabilityAttributesContentStatuses{gen.TerritoryAvailabilityAttributesContentStatusesCANNOTSELL}
	case *a.PreOrderEnabled:
		return []gen.TerritoryAvailabilityAttributesContentStatuses{gen.TerritoryAvailabilityAttributesContentStatusesAVAILABLEFORPREORDER}
	}
	return []gen.TerritoryAvailabilityAttributesContentStatuses{gen.TerritoryAvailabilityAttributesContentStatusesAVAILABLE}
}

func renderAvailability(appID string, av *availability) gen.AppAvailabilityV2 {
	out := gen.AppAvailabilityV2{Id: appID, Type: gen.AppAvailabilityV2TypeAppAvailabilities}
	alloc(&out.Attributes).AvailableInNewTerritories = new(av.availableInNew)
	return out
}

func (s *Server) AppsAppAvailabilityV2GetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsAppAvailabilityV2GetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	av, ok := s.pricing.availability[id]
	if !ok {
		notFound(w, "appAvailabilities", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.AppAvailabilityV2Response{Data: renderAvailability(id, av), Links: docLinks(r)})
}

func (s *Server) AppAvailabilitiesV2TerritoryAvailabilitiesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppAvailabilitiesV2TerritoryAvailabilitiesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	av, ok := s.pricing.availability[id]
	if !ok {
		notFound(w, "appAvailabilities", id)
		return
	}
	var items []gen.TerritoryAvailability
	for _, territory := range sortedKeys(av.territories) {
		items = append(items, *av.territories[territory])
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.TerritoryAvailabilitiesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) findTerritoryAvailability(id string) *gen.TerritoryAvailability {
	for _, av := range s.pricing.availability {
		for _, ta := range av.territories {
			if ta.Id == id {
				return ta
			}
		}
	}
	return nil
}

// TerritoryAvailabilitiesUpdateInstance changes a territory's availability.
// A pre-order needs a release date in the future; a null release date
// clears it.
func (s *Server) TerritoryAvailabilitiesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ENTITY_UNPROCESSABLE", "The request entity is not valid JSON", err.Error(), "")
		return
	}
	var req gen.TerritoryAvailabilityUpdateRequest
	var raw struct {
		Data struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !decode(w, r, &req) || json.Unmarshal(body, &raw) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ta := s.findTerritoryAvailability(id)
	if ta == nil {
		notFound(w, "territoryAvailabilities", id)
		return
	}
	next := *ta.Attributes
	if in := req.Data.Attributes; in != nil {
		if in.Available != nil {
			next.Available = new(*in.Available)
		}
		if in.PreOrderEnabled != nil {
			next.PreOrderEnabled = new(*in.PreOrderEnabled)
		}
		if in.ReleaseDate != nil {
			next.ReleaseDate = new(*in.ReleaseDate)
		}
	}
	if string(raw.Data.Attributes["releaseDate"]) == "null" {
		next.ReleaseDate = nil
	}
	if *next.PreOrderEnabled {
		today := s.now().UTC().Truncate(24 * time.Hour)
		if !*next.Available || next.ReleaseDate == nil || !next.ReleaseDate.After(today) {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A pre-order needs the app to be available, with a release date in the future.", "/data/attributes/preOrderEnabled")
			return
		}
		next.PreOrderPublishDate = new(openapi_types.Date{Time: today})
	} else {
		next.PreOrderPublishDate = nil
	}
	*ta.Attributes = next
	ta.Attributes.ContentStatuses = new(contentStatuses(ta))
	writeJSON(w, http.StatusOK, gen.TerritoryAvailabilityResponse{Data: *ta, Links: docLinks(r)})
}

// EndAppAvailabilityPreOrdersCreateInstance ends pre-orders in territories,
// releasing the app there.
func (s *Server) EndAppAvailabilityPreOrdersCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.EndAppAvailabilityPreOrderCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []*gen.TerritoryAvailability
	for i, ref := range req.Data.Relationships.TerritoryAvailabilities.Data {
		ta := s.findTerritoryAvailability(ref.Id)
		if ta == nil || !*ta.Attributes.PreOrderEnabled {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The territory availability '"+ref.Id+"' has no pre-order.",
				fmt.Sprintf("/data/relationships/territoryAvailabilities/data/%d", i))
			return
		}
		found = append(found, ta)
	}
	for _, ta := range found {
		ta.Attributes.PreOrderEnabled, ta.Attributes.ReleaseDate, ta.Attributes.PreOrderPublishDate = new(false), nil, nil
		ta.Attributes.ContentStatuses = new(contentStatuses(ta))
	}
	writeJSON(w, http.StatusCreated, gen.EndAppAvailabilityPreOrderResponse{
		Data:  gen.EndAppAvailabilityPreOrder{Id: s.newID("Z"), Type: gen.EndAppAvailabilityPreOrderTypeEndAppAvailabilityPreOrders},
		Links: docLinks(r),
	})
}
