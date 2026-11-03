package asctest

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// In-app purchase pricing and availability, and sandbox testers. A
// purchase's price schedule works like an app's: a base territory price,
// equalized elsewhere, plus manual prices, replaced as a whole
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-inapppurchasepriceschedules).
// Availability lists the territories where the purchase is sold, replaced as
// a whole. Sandbox testers are created in App Store Connect; the API lists
// and changes them.

type iapSchedule struct {
	base   string
	prices []gen.InAppPurchasePrice
}

type iapAvailability struct {
	availableInNew bool
	territories    []string
}

type iapPricingState struct {
	schedules    map[string]*iapSchedule     // by purchase ID
	availability map[string]*iapAvailability // by purchase ID
	testers      map[string]*gen.SandboxTesterV2
	cleared      []string
}

func newIAPPricingState() *iapPricingState {
	return &iapPricingState{schedules: map[string]*iapSchedule{}, availability: map[string]*iapAvailability{}, testers: map[string]*gen.SandboxTesterV2{}}
}

func renderIAPPricePoint(iapID, territory string, tier int) gen.InAppPurchasePricePoint {
	p := gen.InAppPurchasePricePoint{Id: pricePointID(iapID, territory, tier), Type: gen.InAppPurchasePricePointTypeInAppPurchasePricePoints}
	a := alloc(&p.Attributes)
	a.CustomerPrice = new(priceTiers[tier])
	price, _ := strconv.ParseFloat(priceTiers[tier], 64)
	a.Proceeds = new(strconv.FormatFloat(price*0.7, 'f', 2, 64))
	d := alloc(&alloc(&alloc(&p.Relationships).Territory).Data)
	d.Id, d.Type = territory, "territories"
	return p
}

func (s *Server) InAppPurchasesV2PricePointsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.InAppPurchasesV2PricePointsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.iap.iaps[id]; !ok {
		notFound(w, "inAppPurchases", id)
		return
	}
	var items []gen.InAppPurchasePricePoint
	for _, territory := range sortedKeys(fakeTerritories) {
		if matches(params.FilterTerritory, territory) {
			for tier := range priceTiers {
				items = append(items, renderIAPPricePoint(id, territory, tier))
			}
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchasePricePointsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) InAppPurchasePricePointsEqualizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.InAppPurchasePricePointsEqualizationsGetToManyRelatedParams) {
	iapID, territory, tier, ok := parsePricePoint(id)
	if !ok {
		notFound(w, "inAppPurchasePricePoints", id)
		return
	}
	var items []gen.InAppPurchasePricePoint
	for _, other := range sortedKeys(fakeTerritories) {
		if other != territory && matches(params.FilterTerritory, other) {
			items = append(items, renderIAPPricePoint(iapID, other, tier))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchasePricePointsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// InAppPurchasePriceSchedulesCreateInstance replaces a purchase's price
// schedule; the manual prices are inline, referenced by local IDs.
func (s *Server) InAppPurchasePriceSchedulesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.InAppPurchasePriceScheduleCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	iapID, base := rel.InAppPurchase.Data.Id, rel.BaseTerritory.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.iap.iaps[iapID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no inAppPurchases with id '"+iapID+"'", "/data/relationships/inAppPurchase")
		return
	}
	if _, ok := fakeTerritories[base]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no territories with id '"+base+"'", "/data/relationships/baseTerritory")
		return
	}
	inline := map[string]gen.InAppPurchasePriceInlineCreate{}
	for i, item := range asc.Value(req.Included) {
		p, err := item.AsInAppPurchasePriceInlineCreate()
		if err != nil || p.Type != gen.InAppPurchasePriceInlineCreateTypeInAppPurchasePrices {
			continue
		}
		if p.Id == nil || !localID(*p.Id) {
			conflict(w, "ENTITY_ERROR.INCLUDED.INVALID_ID", "For inline creation, the id must be a local id with the format '${local-id}'.", fmt.Sprintf("/included/%d/id", i))
			return
		}
		inline[*p.Id] = p
	}
	sched := &iapSchedule{base: base}
	undated := map[string]bool{}
	for i, ref := range rel.ManualPrices.Data {
		pointer := fmt.Sprintf("/data/relationships/manualPrices/data/%d", i)
		p, ok := inline[ref.Id]
		if !ok || p.Relationships == nil || p.Relationships.InAppPurchasePricePoint == nil || p.Relationships.InAppPurchasePricePoint.Data == nil {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The manual price '"+ref.Id+"' is not included, or has no price point.", pointer)
			return
		}
		pointID := p.Relationships.InAppPurchasePricePoint.Data.Id
		owner, territory, _, ok := parsePricePoint(pointID)
		if !ok || owner != iapID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no price point '"+pointID+"' of this in-app purchase.", pointer)
			return
		}
		price := gen.InAppPurchasePrice{Id: s.newID("Y"), Type: gen.InAppPurchasePriceTypeInAppPurchasePrices}
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
		pp := alloc(&alloc(&rels.InAppPurchasePricePoint).Data)
		pp.Id, pp.Type = pointID, "inAppPurchasePricePoints"
		t := alloc(&alloc(&rels.Territory).Data)
		t.Id, t.Type = territory, "territories"
		sched.prices = append(sched.prices, price)
	}
	if !slices.ContainsFunc(sched.prices, func(p gen.InAppPurchasePrice) bool { return p.Relationships.Territory.Data.Id == base }) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The base territory "+base+" needs a manual price.", "/data/relationships/manualPrices")
		return
	}
	s.iapPricing.schedules[iapID] = sched
	writeJSON(w, http.StatusCreated, gen.InAppPurchasePriceScheduleResponse{
		Data: gen.InAppPurchasePriceSchedule{Id: iapID, Type: gen.InAppPurchasePriceScheduleTypeInAppPurchasePriceSchedules}, Links: docLinks(r),
	})
}

// InAppPurchasesV2IapPriceScheduleGetToOneRelated returns a purchase's price
// schedule. The schedule and availability IDs are the purchase's ID.
func (s *Server) InAppPurchasesV2IapPriceScheduleGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchasesV2IapPriceScheduleGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.iapPricing.schedules[id]; !ok {
		notFound(w, "inAppPurchasePriceSchedules", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchasePriceScheduleResponse{
		Data: gen.InAppPurchasePriceSchedule{Id: id, Type: gen.InAppPurchasePriceScheduleTypeInAppPurchasePriceSchedules}, Links: docLinks(r),
	})
}

func (s *Server) InAppPurchasePriceSchedulesBaseTerritoryGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchasePriceSchedulesBaseTerritoryGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sched, ok := s.iapPricing.schedules[id]
	if !ok {
		notFound(w, "inAppPurchasePriceSchedules", id)
		return
	}
	t := gen.Territory{Id: sched.base, Type: gen.TerritoryTypeTerritories}
	alloc(&t.Attributes).Currency = new(fakeTerritories[sched.base])
	writeJSON(w, http.StatusOK, gen.TerritoryResponse{Data: t, Links: docLinks(r)})
}

func (s *Server) InAppPurchasePriceSchedulesManualPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.InAppPurchasePriceSchedulesManualPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sched, ok := s.iapPricing.schedules[id]
	if !ok {
		notFound(w, "inAppPurchasePriceSchedules", id)
		return
	}
	var items []gen.InAppPurchasePrice
	for _, p := range sched.prices {
		if matches(params.FilterTerritory, p.Relationships.Territory.Data.Id) {
			items = append(items, p)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchasePricesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// InAppPurchaseAvailabilitiesCreateInstance replaces where a purchase is
// sold.
func (s *Server) InAppPurchaseAvailabilitiesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.InAppPurchaseAvailabilityCreateRequest
	if !decode(w, r, &req) {
		return
	}
	iapID := req.Data.Relationships.InAppPurchase.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.iap.iaps[iapID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no inAppPurchases with id '"+iapID+"'", "/data/relationships/inAppPurchase")
		return
	}
	av := &iapAvailability{availableInNew: req.Data.Attributes.AvailableInNewTerritories}
	for i, t := range req.Data.Relationships.AvailableTerritories.Data {
		if _, ok := fakeTerritories[t.Id]; !ok {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no territories with id '"+t.Id+"'", fmt.Sprintf("/data/relationships/availableTerritories/data/%d", i))
			return
		}
		av.territories = append(av.territories, t.Id)
	}
	slices.Sort(av.territories)
	s.iapPricing.availability[iapID] = av
	writeJSON(w, http.StatusCreated, gen.InAppPurchaseAvailabilityResponse{Data: renderIAPAvailability(iapID, av), Links: docLinks(r)})
}

func renderIAPAvailability(id string, av *iapAvailability) gen.InAppPurchaseAvailability {
	out := gen.InAppPurchaseAvailability{Id: id, Type: gen.InAppPurchaseAvailabilityTypeInAppPurchaseAvailabilities}
	alloc(&out.Attributes).AvailableInNewTerritories = new(av.availableInNew)
	return out
}

func (s *Server) InAppPurchasesV2InAppPurchaseAvailabilityGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchasesV2InAppPurchaseAvailabilityGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	av, ok := s.iapPricing.availability[id]
	if !ok {
		notFound(w, "inAppPurchaseAvailabilities", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseAvailabilityResponse{Data: renderIAPAvailability(id, av), Links: docLinks(r)})
}

func (s *Server) InAppPurchaseAvailabilitiesAvailableTerritoriesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.InAppPurchaseAvailabilitiesAvailableTerritoriesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	av, ok := s.iapPricing.availability[id]
	if !ok {
		notFound(w, "inAppPurchaseAvailabilities", id)
		return
	}
	var items []gen.Territory
	for _, t := range av.territories {
		territory := gen.Territory{Id: t, Type: gen.TerritoryTypeTerritories}
		alloc(&territory.Attributes).Currency = new(fakeTerritories[t])
		items = append(items, territory)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.TerritoriesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- sandbox testers ----

// AddSandboxTester adds a sandbox tester, as App Store Connect's Users and
// Access does, and returns its ID.
func (s *Server) AddSandboxTester(email, firstName, lastName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &gen.SandboxTesterV2{Id: s.newID("X"), Type: gen.SandboxTesterV2TypeSandboxTesters}
	a := alloc(&t.Attributes)
	a.AcAccountName, a.FirstName, a.LastName = new(email), new(firstName), new(lastName)
	a.Territory, a.InterruptPurchases, a.ApplePayCompatible = new(gen.TerritoryCodeUSA), new(false), new(false)
	a.SubscriptionRenewalRate = new(gen.SandboxTesterV2AttributesSubscriptionRenewalRateMONTHLYRENEWALEVERYONEHOUR)
	s.iapPricing.testers[t.Id] = t
	return t.Id
}

func (s *Server) SandboxTestersV2GetCollection(w http.ResponseWriter, r *http.Request, params gen.SandboxTestersV2GetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.SandboxTesterV2
	for _, t := range sortedValues(s.iapPricing.testers) {
		items = append(items, *t)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SandboxTestersV2Response{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SandboxTestersV2UpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SandboxTesterV2UpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.iapPricing.testers[id]
	if !ok {
		notFound(w, "sandboxTesters", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.Territory != nil {
			if !in.Territory.Valid() {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "territory is invalid.", "/data/attributes/territory")
				return
			}
			t.Attributes.Territory = new(*in.Territory)
		}
		if in.InterruptPurchases != nil {
			t.Attributes.InterruptPurchases = new(*in.InterruptPurchases)
		}
		if in.SubscriptionRenewalRate != nil {
			t.Attributes.SubscriptionRenewalRate = new(gen.SandboxTesterV2AttributesSubscriptionRenewalRate(*in.SubscriptionRenewalRate))
		}
	}
	writeJSON(w, http.StatusOK, gen.SandboxTesterV2Response{Data: *t, Links: docLinks(r)})
}

func (s *Server) SandboxTestersClearPurchaseHistoryRequestV2CreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SandboxTestersClearPurchaseHistoryRequestV2CreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range req.Data.Relationships.SandboxTesters.Data {
		if _, ok := s.iapPricing.testers[t.Id]; !ok {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no sandboxTesters with id '"+t.Id+"'", fmt.Sprintf("/data/relationships/sandboxTesters/data/%d", i))
			return
		}
	}
	for _, t := range req.Data.Relationships.SandboxTesters.Data {
		s.iapPricing.cleared = append(s.iapPricing.cleared, t.Id)
	}
	writeJSON(w, http.StatusCreated, gen.SandboxTestersClearPurchaseHistoryRequestV2Response{
		Data:  gen.SandboxTestersClearPurchaseHistoryRequestV2{Id: s.newID("C"), Type: gen.SandboxTestersClearPurchaseHistoryRequestV2TypeSandboxTestersClearPurchaseHistoryRequest},
		Links: docLinks(r),
	})
}

// ClearedSandboxTesters returns the testers whose purchase history was
// cleared, in order.
func (s *Server) ClearedSandboxTesters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.iapPricing.cleared)
}
