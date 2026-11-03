package asctest

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Subscription prices, availability and billing grace periods. A
// subscription has a price per territory and plan type, plus scheduled
// price changes, each added and deleted on its own; only a price that
// hasn't started can be deleted
// (https://developer.apple.com/documentation/appstoreconnectapi/post-v1-subscriptionprices).
// Plan availabilities list the territories where each plan type is sold.
// An app has one grace period setting.

type subscriptionPlanAvailability struct {
	id, subscriptionID string
	planType           string
	availableInNew     bool
	territories        []string
}

type subscriptionPricingState struct {
	prices     map[string]*gen.SubscriptionPrice // by price ID
	priceOwner map[string]string                 // price ID to subscription ID
	plans      map[string]*subscriptionPlanAvailability
	grace      map[string]*gen.SubscriptionGracePeriod // by app ID
}

func newSubscriptionPricingState() *subscriptionPricingState {
	return &subscriptionPricingState{prices: map[string]*gen.SubscriptionPrice{}, priceOwner: map[string]string{},
		plans: map[string]*subscriptionPlanAvailability{}, grace: map[string]*gen.SubscriptionGracePeriod{}}
}

func renderSubscriptionPricePoint(subID, territory string, tier int) gen.SubscriptionPricePoint {
	p := gen.SubscriptionPricePoint{Id: pricePointID(subID, territory, tier), Type: gen.SubscriptionPricePointTypeSubscriptionPricePoints}
	a := alloc(&p.Attributes)
	a.CustomerPrice = new(priceTiers[tier])
	price, _ := strconv.ParseFloat(priceTiers[tier], 64)
	a.Proceeds = new(strconv.FormatFloat(price*0.7, 'f', 2, 64))
	a.ProceedsYear2 = new(strconv.FormatFloat(price*0.85, 'f', 2, 64))
	d := alloc(&alloc(&alloc(&p.Relationships).Territory).Data)
	d.Id, d.Type = territory, "territories"
	return p
}

func (s *Server) SubscriptionsPricePointsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionsPricePointsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	var items []gen.SubscriptionPricePoint
	for _, territory := range sortedKeys(fakeTerritories) {
		if matches(params.FilterTerritory, territory) {
			for tier := range priceTiers {
				items = append(items, renderSubscriptionPricePoint(id, territory, tier))
			}
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPricePointsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionPricePointsEqualizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionPricePointsEqualizationsGetToManyRelatedParams) {
	subID, territory, tier, ok := parsePricePoint(id)
	if !ok {
		notFound(w, "subscriptionPricePoints", id)
		return
	}
	var items []gen.SubscriptionPricePoint
	for _, other := range sortedKeys(fakeTerritories) {
		if other != territory && matches(params.FilterTerritory, other) {
			items = append(items, renderSubscriptionPricePoint(subID, other, tier))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPricePointsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionPricesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionPriceCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	subID, pointID := rel.Subscription.Data.Id, rel.SubscriptionPricePoint.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[subID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptions with id '"+subID+"'", "/data/relationships/subscription")
		return
	}
	owner, territory, _, ok := parsePricePoint(pointID)
	if !ok || owner != subID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no price point '"+pointID+"' of this subscription.", "/data/relationships/subscriptionPricePoint")
		return
	}
	price := &gen.SubscriptionPrice{Id: s.newID("Y"), Type: gen.SubscriptionPriceTypeSubscriptionPrices}
	a := alloc(&price.Attributes)
	a.PlanType, a.Preserved = new(gen.SubscriptionPlanTypeUPFRONT), new(false)
	if in := req.Data.Attributes; in != nil {
		if in.PlanType != nil {
			a.PlanType = new(*in.PlanType)
		}
		a.StartDate = in.StartDate
		if in.PreserveCurrentPrice != nil {
			a.Preserved = new(*in.PreserveCurrentPrice)
		}
	}
	for id, p := range s.subPricing.prices {
		if s.subPricing.priceOwner[id] == subID && p.Relationships.Territory.Data.Id == territory && *p.Attributes.PlanType == *a.PlanType &&
			fmt.Sprint(p.Attributes.StartDate) == fmt.Sprint(a.StartDate) {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The subscription already has a price in "+territory+" starting then.", "/data/attributes/startDate")
			return
		}
	}
	rels := alloc(&price.Relationships)
	pp := alloc(&alloc(&rels.SubscriptionPricePoint).Data)
	pp.Id, pp.Type = pointID, "subscriptionPricePoints"
	t := alloc(&alloc(&rels.Territory).Data)
	t.Id, t.Type = territory, "territories"
	s.subPricing.prices[price.Id] = price
	s.subPricing.priceOwner[price.Id] = subID
	writeJSON(w, http.StatusCreated, gen.SubscriptionPriceResponse{Data: *price, Links: docLinks(r)})
}

func (s *Server) SubscriptionsPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionsPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	var items []gen.SubscriptionPrice
	for _, pid := range sortedKeys(s.subPricing.prices) {
		p := s.subPricing.prices[pid]
		if s.subPricing.priceOwner[pid] == id && matches(params.FilterTerritory, p.Relationships.Territory.Data.Id) &&
			matches(params.FilterSubscriptionPricePoint, p.Relationships.SubscriptionPricePoint.Data.Id) && matches(params.FilterPlanType, string(*p.Attributes.PlanType)) {
			items = append(items, *p)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPricesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// SubscriptionPricesDeleteInstance deletes a price that hasn't started.
func (s *Server) SubscriptionPricesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.subPricing.prices[id]
	if !ok {
		notFound(w, "subscriptionPrices", id)
		return
	}
	if start := p.Attributes.StartDate; start == nil || !start.After(s.now()) {
		stateError(w, "Only a price that hasn't started can be deleted.")
		return
	}
	delete(s.subPricing.prices, id)
	delete(s.subPricing.priceOwner, id)
	w.WriteHeader(http.StatusNoContent)
}

func territoriesResponse(w http.ResponseWriter, r *http.Request, ids []string, limit *int) {
	var items []gen.Territory
	for _, t := range ids {
		territory := gen.Territory{Id: t, Type: gen.TerritoryTypeTerritories}
		alloc(&territory.Attributes).Currency = new(fakeTerritories[t])
		items = append(items, territory)
	}
	data, links, meta, ok := page(w, r, items, limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.TerritoriesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- plan availabilities ----

func renderPlanAvailability(p *subscriptionPlanAvailability) gen.SubscriptionPlanAvailability {
	out := gen.SubscriptionPlanAvailability{Id: p.id, Type: gen.SubscriptionPlanAvailabilityTypeSubscriptionPlanAvailabilities}
	a := alloc(&out.Attributes)
	a.AvailableInNewTerritories, a.PlanType = new(p.availableInNew), new(gen.SubscriptionPlanType(p.planType))
	return out
}

func checkTerritories(w http.ResponseWriter, ids []string) bool {
	for i, t := range ids {
		if _, ok := fakeTerritories[t]; !ok {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no territories with id '"+t+"'", fmt.Sprintf("/data/%d", i))
			return false
		}
	}
	return true
}

func (s *Server) SubscriptionPlanAvailabilitiesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionPlanAvailabilityCreateRequest
	if !decode(w, r, &req) {
		return
	}
	subID, plan := req.Data.Relationships.Subscription.Data.Id, string(req.Data.Attributes.PlanType)
	var territories []string
	for _, t := range req.Data.Relationships.AvailableTerritories.Data {
		territories = append(territories, t.Id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[subID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptions with id '"+subID+"'", "/data/relationships/subscription")
		return
	}
	if !checkTerritories(w, territories) {
		return
	}
	for _, p := range s.subPricing.plans {
		if p.subscriptionID == subID && p.planType == plan {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The subscription already has an availability for "+plan+".", "/data/attributes/planType")
			return
		}
	}
	slices.Sort(territories)
	p := &subscriptionPlanAvailability{id: s.newID("A"), subscriptionID: subID, planType: plan, territories: territories}
	if in := req.Data.Attributes.AvailableInNewTerritories; in != nil {
		p.availableInNew = *in
	}
	s.subPricing.plans[p.id] = p
	writeJSON(w, http.StatusCreated, gen.SubscriptionPlanAvailabilityResponse{Data: renderPlanAvailability(p), Links: docLinks(r)})
}

func (s *Server) SubscriptionsPlanAvailabilitiesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionsPlanAvailabilitiesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	var items []gen.SubscriptionPlanAvailability
	for _, p := range sortedValues(s.subPricing.plans) {
		if p.subscriptionID == id {
			items = append(items, renderPlanAvailability(p))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPlanAvailabilitiesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionPlanAvailabilitiesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionPlanAvailabilityUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.subPricing.plans[id]
	if !ok {
		notFound(w, "subscriptionPlanAvailabilities", id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.AvailableInNewTerritories != nil {
		p.availableInNew = *in.AvailableInNewTerritories
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPlanAvailabilityResponse{Data: renderPlanAvailability(p), Links: docLinks(r)})
}

func (s *Server) SubscriptionPlanAvailabilitiesAvailableTerritoriesReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionPlanAvailabilityAvailableTerritoriesLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	var territories []string
	for _, t := range req.Data {
		territories = append(territories, t.Id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.subPricing.plans[id]
	if !ok {
		notFound(w, "subscriptionPlanAvailabilities", id)
		return
	}
	if !checkTerritories(w, territories) {
		return
	}
	slices.Sort(territories)
	p.territories = territories
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SubscriptionPlanAvailabilitiesAvailableTerritoriesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionPlanAvailabilitiesAvailableTerritoriesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.subPricing.plans[id]
	if !ok {
		notFound(w, "subscriptionPlanAvailabilities", id)
		return
	}
	territoriesResponse(w, r, p.territories, params.Limit)
}

// ---- grace period ----

// gracePeriod returns an app's grace period setting, off until changed.
func (s *Server) gracePeriod(appID string) *gen.SubscriptionGracePeriod {
	g, ok := s.subPricing.grace[appID]
	if !ok {
		g = &gen.SubscriptionGracePeriod{Id: "GP-" + appID, Type: gen.SubscriptionGracePeriodTypeSubscriptionGracePeriods}
		a := alloc(&g.Attributes)
		a.OptIn, a.SandboxOptIn = new(false), new(false)
		a.Duration = new(gen.SubscriptionGracePeriodDurationSIXTEENDAYS)
		a.RenewalType = new(gen.SubscriptionGracePeriodAttributesRenewalTypeALLRENEWALS)
		s.subPricing.grace[appID] = g
	}
	return g
}

func (s *Server) AppsSubscriptionGracePeriodGetToOneRelated(w http.ResponseWriter, r *http.Request, id string, _ gen.AppsSubscriptionGracePeriodGetToOneRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGracePeriodResponse{Data: *s.gracePeriod(id), Links: docLinks(r)})
}

func (s *Server) SubscriptionGracePeriodsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionGracePeriodUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var g *gen.SubscriptionGracePeriod
	for _, candidate := range s.subPricing.grace {
		if candidate.Id == id {
			g = candidate
		}
	}
	if g == nil {
		notFound(w, "subscriptionGracePeriods", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.Duration != nil {
			if !in.Duration.Valid() {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "duration is invalid.", "/data/attributes/duration")
				return
			}
			g.Attributes.Duration = new(*in.Duration)
		}
		if in.OptIn != nil {
			g.Attributes.OptIn = new(*in.OptIn)
		}
		if in.SandboxOptIn != nil {
			g.Attributes.SandboxOptIn = new(*in.SandboxOptIn)
		}
		if in.RenewalType != nil {
			g.Attributes.RenewalType = new(gen.SubscriptionGracePeriodAttributesRenewalType(*in.RenewalType))
		}
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionGracePeriodResponse{Data: *g, Links: docLinks(r)})
}
