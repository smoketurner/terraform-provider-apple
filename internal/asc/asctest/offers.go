package asctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Promoted in-app purchases and subscription offers. An app promotes
// purchases and subscriptions on its App Store page in an order it sets
// (https://developer.apple.com/documentation/appstoreconnectapi/promoted-purchases).
// A subscription has an introductory offer per territory, promotional
// offers the app presents, and win-back offers for lapsed subscribers; paid
// offers price each territory with one of the subscription's price points
// (https://developer.apple.com/documentation/appstoreconnectapi/subscription-offers).

type promotedPurchase struct {
	gen.PromotedPurchase
	appID string
}

type introOffer struct {
	gen.SubscriptionIntroductoryOffer
	subscriptionID string
}

// offerPrice is a territory's price of a promotional or win-back offer.
type offerPrice struct {
	id, territory, pricePointID string
}

type promoOffer struct {
	gen.SubscriptionPromotionalOffer
	subscriptionID string
	prices         []offerPrice
}

type winBackOffer struct {
	gen.WinBackOffer
	subscriptionID string
	prices         []offerPrice
}

type offersState struct {
	promoted    map[string]*promotedPurchase
	order       map[string][]string // promoted purchase IDs by app ID
	intro       map[string]*introOffer
	promotional map[string]*promoOffer
	winBack     map[string]*winBackOffer
}

func newOffersState() *offersState {
	return &offersState{promoted: map[string]*promotedPurchase{}, order: map[string][]string{}, intro: map[string]*introOffer{},
		promotional: map[string]*promoOffer{}, winBack: map[string]*winBackOffer{}}
}

// subscriptionApp returns the app of a subscription, or "".
func (s *Server) subscriptionApp(subID string) string {
	if sub, ok := s.subs.subscriptions[subID]; ok {
		return s.subs.groups[sub.groupID].appID
	}
	return ""
}

// ---- promoted purchases ----

func (s *Server) PromotedPurchasesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.PromotedPurchaseCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	appID := rel.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	p := &promotedPurchase{appID: appID, PromotedPurchase: gen.PromotedPurchase{Id: s.newID("R"), Type: gen.PromotedPurchaseTypePromotedPurchases}}
	prel := alloc(&p.Relationships)
	switch {
	case rel.InAppPurchaseV2 != nil && rel.InAppPurchaseV2.Data != nil && (rel.Subscription == nil || rel.Subscription.Data == nil):
		iapID := rel.InAppPurchaseV2.Data.Id
		if purchase, ok := s.iap.iaps[iapID]; !ok || purchase.appID != appID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no in-app purchase '"+iapID+"' of this app.", "/data/relationships/inAppPurchaseV2")
			return
		}
		d := alloc(&alloc(&prel.InAppPurchaseV2).Data)
		d.Id, d.Type = iapID, "inAppPurchases"
	case rel.Subscription != nil && rel.Subscription.Data != nil && (rel.InAppPurchaseV2 == nil || rel.InAppPurchaseV2.Data == nil):
		subID := rel.Subscription.Data.Id
		if s.subscriptionApp(subID) != appID {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscription '"+subID+"' of this app.", "/data/relationships/subscription")
			return
		}
		d := alloc(&alloc(&prel.Subscription).Data)
		d.Id, d.Type = subID, "subscriptions"
	default:
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "A promoted purchase needs an in-app purchase or a subscription.", "/data/relationships")
		return
	}
	for _, other := range s.offers.promoted {
		if other.appID == appID && promotedTarget(other) == promotedTarget(p) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The product is already promoted.", "/data/relationships")
			return
		}
	}
	a := alloc(&p.Attributes)
	a.VisibleForAllUsers, a.Enabled = new(req.Data.Attributes.VisibleForAllUsers), new(true)
	if req.Data.Attributes.Enabled != nil {
		a.Enabled = new(*req.Data.Attributes.Enabled)
	}
	a.State = new(gen.PromotedPurchaseAttributesStatePREPAREFORSUBMISSION)
	s.offers.promoted[p.Id] = p
	s.offers.order[appID] = append(s.offers.order[appID], p.Id)
	writeJSON(w, http.StatusCreated, gen.PromotedPurchaseResponse{Data: p.PromotedPurchase, Links: docLinks(r)})
}

func promotedTarget(p *promotedPurchase) string {
	if rel := p.Relationships; rel != nil {
		if rel.InAppPurchaseV2 != nil && rel.InAppPurchaseV2.Data != nil {
			return "iap:" + rel.InAppPurchaseV2.Data.Id
		}
		if rel.Subscription != nil && rel.Subscription.Data != nil {
			return "sub:" + rel.Subscription.Data.Id
		}
	}
	return ""
}

func (s *Server) PromotedPurchasesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.PromotedPurchasesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.offers.promoted[id]
	if !ok {
		notFound(w, "promotedPurchases", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.PromotedPurchaseResponse{Data: p.PromotedPurchase, Links: docLinks(r)})
}

func (s *Server) PromotedPurchasesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.PromotedPurchaseUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.offers.promoted[id]
	if !ok {
		notFound(w, "promotedPurchases", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		if in.Enabled != nil {
			p.Attributes.Enabled = new(*in.Enabled)
		}
		if in.VisibleForAllUsers != nil {
			p.Attributes.VisibleForAllUsers = new(*in.VisibleForAllUsers)
		}
	}
	writeJSON(w, http.StatusOK, gen.PromotedPurchaseResponse{Data: p.PromotedPurchase, Links: docLinks(r)})
}

func (s *Server) PromotedPurchasesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.offers.promoted[id]
	if !ok {
		notFound(w, "promotedPurchases", id)
		return
	}
	s.offers.order[p.appID] = slices.DeleteFunc(s.offers.order[p.appID], func(o string) bool { return o == id })
	delete(s.offers.promoted, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsPromotedPurchasesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsPromotedPurchasesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.PromotedPurchase
	for _, pid := range s.offers.order[id] {
		items = append(items, s.offers.promoted[pid].PromotedPurchase)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.PromotedPurchasesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// AppsPromotedPurchasesReplaceToManyRelationship sets the order of an app's
// promoted purchases, which must list each of them once.
func (s *Server) AppsPromotedPurchasesReplaceToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.AppPromotedPurchasesLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	var ids []string
	for _, e := range req.Data {
		ids = append(ids, e.Id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.offers.order[id]
	if len(ids) != len(current) || slices.ContainsFunc(ids, func(p string) bool { return !slices.Contains(current, p) }) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The order must list each of the app's promoted purchases once.", "/data")
		return
	}
	s.offers.order[id] = ids
	w.WriteHeader(http.StatusNoContent)
}

// ---- introductory offers ----

func (s *Server) checkSubscriptionPoint(w http.ResponseWriter, subID, pointID, pointer string) (string, bool) {
	owner, territory, _, ok := parsePricePoint(pointID)
	if !ok || owner != subID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no price point '"+pointID+"' of this subscription.", pointer)
		return "", false
	}
	return territory, true
}

func (s *Server) SubscriptionIntroductoryOffersCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionIntroductoryOfferCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, rel := req.Data.Attributes, req.Data.Relationships
	subID := rel.Subscription.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[subID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptions with id '"+subID+"'", "/data/relationships/subscription")
		return
	}
	var pointID, territory string
	if rel.SubscriptionPricePoint != nil && rel.SubscriptionPricePoint.Data != nil {
		pointID = rel.SubscriptionPricePoint.Data.Id
		t, ok := s.checkSubscriptionPoint(w, subID, pointID, "/data/relationships/subscriptionPricePoint")
		if !ok {
			return
		}
		territory = t
	}
	if rel.Territory != nil && rel.Territory.Data != nil {
		territory = rel.Territory.Data.Id
	}
	switch {
	case in.OfferMode == gen.SubscriptionOfferModeFREETRIAL && pointID != "":
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "A free trial has no price point.", "/data/relationships/subscriptionPricePoint")
		return
	case in.OfferMode != gen.SubscriptionOfferModeFREETRIAL && pointID == "":
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "A paid introductory offer needs a price point.", "/data/relationships/subscriptionPricePoint")
		return
	case territory == "":
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "An introductory offer needs a territory.", "/data/relationships/territory")
		return
	}
	plan := gen.SubscriptionPlanTypeUPFRONT
	if in.TargetSubscriptionPlanType != nil {
		plan = *in.TargetSubscriptionPlanType
	}
	for _, o := range s.offers.intro {
		if o.subscriptionID == subID && o.Relationships.Territory.Data.Id == territory && *o.Attributes.TargetSubscriptionPlanType == plan {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The subscription already has an introductory offer in "+territory+".", "/data/relationships/territory")
			return
		}
	}
	o := &introOffer{subscriptionID: subID, SubscriptionIntroductoryOffer: gen.SubscriptionIntroductoryOffer{Id: s.newID("O"), Type: gen.SubscriptionIntroductoryOfferTypeSubscriptionIntroductoryOffers}}
	a := alloc(&o.Attributes)
	a.Duration, a.OfferMode, a.NumberOfPeriods = new(in.Duration), new(in.OfferMode), new(in.NumberOfPeriods)
	a.StartDate, a.EndDate, a.TargetSubscriptionPlanType = in.StartDate, in.EndDate, new(plan)
	orel := alloc(&o.Relationships)
	t := alloc(&alloc(&orel.Territory).Data)
	t.Id, t.Type = territory, "territories"
	if pointID != "" {
		pp := alloc(&alloc(&orel.SubscriptionPricePoint).Data)
		pp.Id, pp.Type = pointID, "subscriptionPricePoints"
	}
	s.offers.intro[o.Id] = o
	writeJSON(w, http.StatusCreated, gen.SubscriptionIntroductoryOfferResponse{Data: o.SubscriptionIntroductoryOffer, Links: docLinks(r)})
}

func (s *Server) SubscriptionsIntroductoryOffersGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionsIntroductoryOffersGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	var items []gen.SubscriptionIntroductoryOffer
	for _, o := range sortedValues(s.offers.intro) {
		if o.subscriptionID == id && matches(params.FilterTerritory, o.Relationships.Territory.Data.Id) {
			items = append(items, o.SubscriptionIntroductoryOffer)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionIntroductoryOffersResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) SubscriptionIntroductoryOffersUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionIntroductoryOfferUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers.intro[id]
	if !ok {
		notFound(w, "subscriptionIntroductoryOffers", id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.EndDate != nil || nulls["endDate"] {
		o.Attributes.EndDate = asc.Value(req.Data.Attributes).EndDate
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionIntroductoryOfferResponse{Data: o.SubscriptionIntroductoryOffer, Links: docLinks(r)})
}

func (s *Server) SubscriptionIntroductoryOffersDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.offers.intro[id]; !ok {
		notFound(w, "subscriptionIntroductoryOffers", id)
		return
	}
	delete(s.offers.intro, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- offer prices, created inline ----

// inlinePrice is an inline offer price: a local ID, territory and price point.
type inlinePrice struct {
	ID            string `json:"id"`
	Relationships struct {
		Territory struct {
			Data *struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"territory"`
		SubscriptionPricePoint struct {
			Data *struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"subscriptionPricePoint"`
	} `json:"relationships"`
}

// offerPrices resolves the inline prices that refs list, checking that each
// price point is the subscription's and that territories aren't repeated.
func (s *Server) offerPrices(w http.ResponseWriter, subID string, refs []string, included []inlinePrice) ([]offerPrice, bool) {
	inline := map[string]inlinePrice{}
	for i, p := range included {
		if !localID(p.ID) {
			conflict(w, "ENTITY_ERROR.INCLUDED.INVALID_ID", "For inline creation, the id must be a local id with the format '${local-id}'.", fmt.Sprintf("/included/%d/id", i))
			return nil, false
		}
		inline[p.ID] = p
	}
	var prices []offerPrice
	seen := map[string]bool{}
	for i, ref := range refs {
		pointer := fmt.Sprintf("/data/relationships/prices/data/%d", i)
		p, ok := inline[ref]
		if !ok || p.Relationships.SubscriptionPricePoint.Data == nil {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The price '"+ref+"' is not included, or has no price point.", pointer)
			return nil, false
		}
		territory, ok := s.checkSubscriptionPoint(w, subID, p.Relationships.SubscriptionPricePoint.Data.ID, pointer)
		if !ok {
			return nil, false
		}
		if t := p.Relationships.Territory.Data; t != nil && t.ID != territory {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The price point is for another territory.", pointer)
			return nil, false
		}
		if seen[territory] {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The offer already has a price in "+territory+".", pointer)
			return nil, false
		}
		seen[territory] = true
		prices = append(prices, offerPrice{id: s.newID("Y"), territory: territory, pricePointID: p.Relationships.SubscriptionPricePoint.Data.ID})
	}
	return prices, true
}

// offerRequest is the shape the fake reads promotional and win-back offer
// requests in: the generated win-back inline price type has no
// relationships.
type offerRequest struct {
	Data struct {
		Relationships struct {
			Prices *struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"prices"`
		} `json:"relationships"`
	} `json:"data"`
	Included []inlinePrice `json:"included"`
}

func priceRefs(req offerRequest) []string {
	var refs []string
	if req.Data.Relationships.Prices != nil {
		for _, e := range req.Data.Relationships.Prices.Data {
			refs = append(refs, e.ID)
		}
	}
	return refs
}

// readOffer decodes a request both as v and as an offerRequest.
func readOffer(w http.ResponseWriter, r *http.Request, v any) (offerRequest, bool) {
	var req offerRequest
	body, err := io.ReadAll(r.Body)
	if err == nil {
		err = json.Unmarshal(body, v)
	}
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ENTITY_UNPROCESSABLE", "The request entity is not valid JSON", err.Error(), "")
		return req, false
	}
	return req, true
}

// ---- promotional offers ----

func (s *Server) SubscriptionPromotionalOffersCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.SubscriptionPromotionalOfferCreateRequest
	raw, ok := readOffer(w, r, &req)
	if !ok {
		return
	}
	in := req.Data.Attributes
	subID := req.Data.Relationships.Subscription.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[subID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptions with id '"+subID+"'", "/data/relationships/subscription")
		return
	}
	for _, o := range s.offers.promotional {
		if o.subscriptionID == subID && *o.Attributes.OfferCode == in.OfferCode {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The offer code "+in.OfferCode+" is already used.", "/data/attributes/offerCode")
			return
		}
	}
	prices, ok := s.offerPrices(w, subID, priceRefs(raw), raw.Included)
	if !ok {
		return
	}
	if in.OfferMode != gen.SubscriptionOfferModeFREETRIAL && len(prices) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "A paid offer needs prices.", "/data/relationships/prices")
		return
	}
	o := &promoOffer{subscriptionID: subID, prices: prices, SubscriptionPromotionalOffer: gen.SubscriptionPromotionalOffer{Id: s.newID("O"), Type: gen.SubscriptionPromotionalOfferTypeSubscriptionPromotionalOffers}}
	a := alloc(&o.Attributes)
	a.Name, a.OfferCode, a.Duration, a.OfferMode, a.NumberOfPeriods = new(in.Name), new(in.OfferCode), new(in.Duration), new(in.OfferMode), new(in.NumberOfPeriods)
	a.TargetSubscriptionPlanType = new(gen.SubscriptionPlanTypeUPFRONT)
	if in.TargetSubscriptionPlanType != nil {
		a.TargetSubscriptionPlanType = new(*in.TargetSubscriptionPlanType)
	}
	d := alloc(&alloc(&alloc(&o.Relationships).Subscription).Data)
	d.Id, d.Type = subID, "subscriptions"
	s.offers.promotional[o.Id] = o
	writeJSON(w, http.StatusCreated, gen.SubscriptionPromotionalOfferResponse{Data: o.SubscriptionPromotionalOffer, Links: docLinks(r)})
}

func (s *Server) SubscriptionPromotionalOffersGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionPromotionalOffersGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers.promotional[id]
	if !ok {
		notFound(w, "subscriptionPromotionalOffers", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPromotionalOfferResponse{Data: o.SubscriptionPromotionalOffer, Links: docLinks(r)})
}

func renderPromoPrices(prices []offerPrice) []gen.SubscriptionPromotionalOfferPrice {
	var out []gen.SubscriptionPromotionalOfferPrice
	for _, p := range prices {
		price := gen.SubscriptionPromotionalOfferPrice{Id: p.id, Type: gen.SubscriptionPromotionalOfferPriceTypeSubscriptionPromotionalOfferPrices}
		rel := alloc(&price.Relationships)
		t := alloc(&alloc(&rel.Territory).Data)
		t.Id, t.Type = p.territory, "territories"
		pp := alloc(&alloc(&rel.SubscriptionPricePoint).Data)
		pp.Id, pp.Type = p.pricePointID, "subscriptionPricePoints"
		out = append(out, price)
	}
	return out
}

func (s *Server) SubscriptionPromotionalOffersPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionPromotionalOffersPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers.promotional[id]
	if !ok {
		notFound(w, "subscriptionPromotionalOffers", id)
		return
	}
	data, links, meta, ok := page(w, r, renderPromoPrices(o.prices), params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPromotionalOfferPricesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// SubscriptionPromotionalOffersUpdateInstance replaces an offer's prices.
func (s *Server) SubscriptionPromotionalOffersUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionPromotionalOfferUpdateRequest
	raw, ok := readOffer(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers.promotional[id]
	if !ok {
		notFound(w, "subscriptionPromotionalOffers", id)
		return
	}
	if raw.Data.Relationships.Prices != nil {
		prices, ok := s.offerPrices(w, o.subscriptionID, priceRefs(raw), raw.Included)
		if !ok {
			return
		}
		o.prices = prices
	}
	writeJSON(w, http.StatusOK, gen.SubscriptionPromotionalOfferResponse{Data: o.SubscriptionPromotionalOffer, Links: docLinks(r)})
}

func (s *Server) SubscriptionPromotionalOffersDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.offers.promotional[id]; !ok {
		notFound(w, "subscriptionPromotionalOffers", id)
		return
	}
	delete(s.offers.promotional, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- win-back offers ----

func (s *Server) WinBackOffersCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.WinBackOfferCreateRequest
	raw, ok := readOffer(w, r, &req)
	if !ok {
		return
	}
	in := req.Data.Attributes
	subID := req.Data.Relationships.Subscription.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[subID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no subscriptions with id '"+subID+"'", "/data/relationships/subscription")
		return
	}
	for _, o := range s.offers.winBack {
		if s.subscriptionApp(o.subscriptionID) == s.subscriptionApp(subID) && *o.Attributes.OfferId == in.OfferId {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The offer ID "+in.OfferId+" is already used.", "/data/attributes/offerId")
			return
		}
	}
	if r := in.CustomerEligibilityTimeSinceLastSubscribedInMonths; r.Minimum != nil && r.Maximum != nil && *r.Minimum > *r.Maximum {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The minimum months since last subscribed is above the maximum.", "/data/attributes/customerEligibilityTimeSinceLastSubscribedInMonths")
		return
	}
	prices, ok := s.offerPrices(w, subID, priceRefs(raw), raw.Included)
	if !ok {
		return
	}
	o := &winBackOffer{subscriptionID: subID, prices: prices, WinBackOffer: gen.WinBackOffer{Id: s.newID("O"), Type: gen.WinBackOfferTypeWinBackOffers}}
	a := alloc(&o.Attributes)
	a.ReferenceName, a.OfferId, a.Duration, a.OfferMode, a.PeriodCount = new(in.ReferenceName), new(in.OfferId), new(in.Duration), new(in.OfferMode), new(in.PeriodCount)
	a.CustomerEligibilityPaidSubscriptionDurationInMonths = new(in.CustomerEligibilityPaidSubscriptionDurationInMonths)
	a.CustomerEligibilityTimeSinceLastSubscribedInMonths = new(in.CustomerEligibilityTimeSinceLastSubscribedInMonths)
	a.CustomerEligibilityWaitBetweenOffersInMonths = in.CustomerEligibilityWaitBetweenOffersInMonths
	a.StartDate, a.EndDate = new(in.StartDate), in.EndDate
	a.Priority = new(gen.WinBackOfferAttributesPriority(in.Priority))
	a.PromotionIntent = new(gen.WinBackOfferAttributesPromotionIntentNOTPROMOTED)
	if in.PromotionIntent != nil {
		a.PromotionIntent = new(gen.WinBackOfferAttributesPromotionIntent(*in.PromotionIntent))
	}
	a.TargetSubscriptionPlanType = new(gen.SubscriptionPlanTypeUPFRONT)
	if in.TargetSubscriptionPlanType != nil {
		a.TargetSubscriptionPlanType = new(*in.TargetSubscriptionPlanType)
	}
	s.offers.winBack[o.Id] = o
	writeJSON(w, http.StatusCreated, gen.WinBackOfferResponse{Data: o.WinBackOffer, Links: docLinks(r)})
}

func (s *Server) WinBackOffersGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.WinBackOffersGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers.winBack[id]
	if !ok {
		notFound(w, "winBackOffers", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.WinBackOfferResponse{Data: o.WinBackOffer, Links: docLinks(r)})
}

func (s *Server) WinBackOffersPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.WinBackOffersPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers.winBack[id]
	if !ok {
		notFound(w, "winBackOffers", id)
		return
	}
	var items []gen.WinBackOfferPrice
	for _, p := range o.prices {
		price := gen.WinBackOfferPrice{Id: p.id, Type: gen.WinBackOfferPriceTypeWinBackOfferPrices}
		rel := alloc(&price.Relationships)
		t := alloc(&alloc(&rel.Territory).Data)
		t.Id, t.Type = p.territory, "territories"
		pp := alloc(&alloc(&rel.SubscriptionPricePoint).Data)
		pp.Id, pp.Type = p.pricePointID, "subscriptionPricePoints"
		items = append(items, price)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.WinBackOfferPricesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) WinBackOffersUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.WinBackOfferUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers.winBack[id]
	if !ok {
		notFound(w, "winBackOffers", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := o.Attributes
		if in.CustomerEligibilityPaidSubscriptionDurationInMonths != nil {
			a.CustomerEligibilityPaidSubscriptionDurationInMonths = new(*in.CustomerEligibilityPaidSubscriptionDurationInMonths)
		}
		if in.CustomerEligibilityTimeSinceLastSubscribedInMonths != nil {
			a.CustomerEligibilityTimeSinceLastSubscribedInMonths = new(*in.CustomerEligibilityTimeSinceLastSubscribedInMonths)
		}
		if in.CustomerEligibilityWaitBetweenOffersInMonths != nil || nulls["customerEligibilityWaitBetweenOffersInMonths"] {
			a.CustomerEligibilityWaitBetweenOffersInMonths = in.CustomerEligibilityWaitBetweenOffersInMonths
		}
		if in.StartDate != nil {
			a.StartDate = new(*in.StartDate)
		}
		if in.EndDate != nil || nulls["endDate"] {
			a.EndDate = in.EndDate
		}
		if in.Priority != nil {
			a.Priority = new(gen.WinBackOfferAttributesPriority(*in.Priority))
		}
		if in.PromotionIntent != nil {
			a.PromotionIntent = new(gen.WinBackOfferAttributesPromotionIntent(*in.PromotionIntent))
		}
	}
	writeJSON(w, http.StatusOK, gen.WinBackOfferResponse{Data: o.WinBackOffer, Links: docLinks(r)})
}

func (s *Server) WinBackOffersDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.offers.winBack[id]; !ok {
		notFound(w, "winBackOffers", id)
		return
	}
	delete(s.offers.winBack, id)
	w.WriteHeader(http.StatusNoContent)
}
