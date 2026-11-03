package asctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Offer codes for subscriptions and in-app purchases. An offer code defines
// the offer and its prices; customers redeem it with custom codes, which
// the developer names, or batches of one-time-use codes App Store Connect
// generates. Codes and offers can be deactivated, which is permanent
// (https://developer.apple.com/help/app-store-connect/manage-subscriptions/set-up-offer-codes).
// One-time-use code values are downloaded as CSV; the fake's has a "Code"
// header and one code per line.

type offerCode struct {
	product  string // "subscription" or "inAppPurchase"
	parentID string
	sub      *gen.SubscriptionOfferCode
	iap      *gen.InAppPurchaseOfferCode
	prices   []offerPrice
}

func (o *offerCode) id() string {
	if o.sub != nil {
		return o.sub.Id
	}
	return o.iap.Id
}

func (o *offerCode) active() *bool {
	if o.sub != nil {
		return o.sub.Attributes.Active
	}
	return o.iap.Attributes.Active
}

// codeBatch is a custom code or a batch of one-time-use codes.
type codeBatch struct {
	product     string // "subscription" or "inAppPurchase"
	oneTime     bool
	id          string
	offerCodeID string
	customCode  string
	number      int
	expiration  *openapi_types.Date
	environment string
	active      bool
	created     time.Time
	codes       []string
}

type offerCodesState struct {
	codes   map[string]*offerCode
	batches map[string]*codeBatch
}

func newOfferCodesState() *offerCodesState {
	return &offerCodesState{codes: map[string]*offerCode{}, batches: map[string]*codeBatch{}}
}

// offerCodeRequest is how the fake reads offer code creation: attributes by
// kind, the parent, and inline prices whose price point relationship is
// named per kind.
type offerCodeRequest struct {
	Data struct {
		Attributes    json.RawMessage `json:"attributes"`
		Relationships struct {
			Subscription  *jsonLinkage `json:"subscription"`
			InAppPurchase *jsonLinkage `json:"inAppPurchase"`
			Prices        struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"prices"`
		} `json:"relationships"`
	} `json:"data"`
	Included []struct {
		ID            string `json:"id"`
		Relationships struct {
			Territory              *jsonLinkage `json:"territory"`
			SubscriptionPricePoint *jsonLinkage `json:"subscriptionPricePoint"`
			PricePoint             *jsonLinkage `json:"pricePoint"`
		} `json:"relationships"`
	} `json:"included"`
}

type jsonLinkage struct {
	Data *struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (l *jsonLinkage) id() string {
	if l == nil || l.Data == nil {
		return ""
	}
	return l.Data.ID
}

func (s *Server) createOfferCode(w http.ResponseWriter, r *http.Request, product string) {
	body, err := io.ReadAll(r.Body)
	var req offerCodeRequest
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ENTITY_UNPROCESSABLE", "The request entity is not valid JSON", fmt.Sprint(err), "")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := req.Data.Relationships.Subscription.id()
	ownerExists := s.subs.subscriptions[parent] != nil
	if product == "inAppPurchase" {
		parent = req.Data.Relationships.InAppPurchase.id()
		_, ownerExists = s.iap.iaps[parent]
	}
	if !ownerExists {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no "+product+" with id '"+parent+"'", "/data/relationships")
		return
	}
	// Check the prices' price points: the subscription's, or the purchase's.
	var prices []offerPrice
	seen := map[string]bool{}
	for i, ref := range req.Data.Relationships.Prices.Data {
		pointer := fmt.Sprintf("/data/relationships/prices/data/%d", i)
		var point, territory string
		for _, inc := range req.Included {
			if inc.ID == ref.ID {
				point = inc.Relationships.SubscriptionPricePoint.id() + inc.Relationships.PricePoint.id()
				territory = inc.Relationships.Territory.id()
			}
		}
		owner, pointTerritory, _, ok := parsePricePoint(point)
		if !ok || owner != parent || !localID(ref.ID) {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The price '"+ref.ID+"' has no price point of this product.", pointer)
			return
		}
		if territory != pointTerritory || seen[territory] {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The price point is for another territory, or the territory is repeated.", pointer)
			return
		}
		seen[territory] = true
		prices = append(prices, offerPrice{id: s.newID("Y"), territory: territory, pricePointID: point})
	}
	o := &offerCode{product: product, parentID: parent, prices: prices}
	if product == "subscription" {
		var in gen.SubscriptionOfferCodeCreateRequest
		_ = json.Unmarshal(body, &in)
		a := in.Data.Attributes
		if len(a.CustomerEligibilities) == 0 {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "customerEligibilities is required.", "/data/attributes/customerEligibilities")
			return
		}
		if a.OfferMode != gen.SubscriptionOfferModeFREETRIAL && len(prices) == 0 {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "A paid offer needs prices.", "/data/relationships/prices")
			return
		}
		o.sub = &gen.SubscriptionOfferCode{Id: s.newID("C"), Type: gen.SubscriptionOfferCodeTypeSubscriptionOfferCodes}
		oa := alloc(&o.sub.Attributes)
		oa.Name, oa.CustomerEligibilities, oa.OfferEligibility = new(a.Name), new(a.CustomerEligibilities), new(a.OfferEligibility)
		oa.Duration, oa.OfferMode, oa.NumberOfPeriods = new(a.Duration), new(a.OfferMode), new(a.NumberOfPeriods)
		oa.AutoRenewEnabled, oa.Active = new(a.AutoRenewEnabled == nil || *a.AutoRenewEnabled), new(true)
		oa.TargetSubscriptionPlanType = new(gen.SubscriptionPlanTypeUPFRONT)
		if a.TargetSubscriptionPlanType != nil {
			oa.TargetSubscriptionPlanType = new(*a.TargetSubscriptionPlanType)
		}
		d := alloc(&alloc(&alloc(&o.sub.Relationships).Subscription).Data)
		d.Id, d.Type = parent, "subscriptions"
	} else {
		var in gen.InAppPurchaseOfferCodeCreateRequest
		_ = json.Unmarshal(body, &in)
		a := in.Data.Attributes
		if len(a.CustomerEligibilities) == 0 || len(prices) == 0 {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "customerEligibilities and prices are required.", "/data")
			return
		}
		o.iap = &gen.InAppPurchaseOfferCode{Id: s.newID("C"), Type: gen.InAppPurchaseOfferCodeTypeInAppPurchaseOfferCodes}
		oa := alloc(&o.iap.Attributes)
		var eligibilities []gen.InAppPurchaseOfferCodeAttributesCustomerEligibilities
		for _, e := range a.CustomerEligibilities {
			eligibilities = append(eligibilities, gen.InAppPurchaseOfferCodeAttributesCustomerEligibilities(e))
		}
		oa.Name, oa.CustomerEligibilities, oa.Active = new(a.Name), new(eligibilities), new(true)
	}
	s.offerCodes.codes[o.id()] = o
	s.writeOfferCode(w, r, http.StatusCreated, o)
}

func (s *Server) writeOfferCode(w http.ResponseWriter, r *http.Request, status int, o *offerCode) {
	if o.sub != nil {
		writeJSON(w, status, gen.SubscriptionOfferCodeResponse{Data: *o.sub, Links: docLinks(r)})
		return
	}
	writeJSON(w, status, gen.InAppPurchaseOfferCodeResponse{Data: *o.iap, Links: docLinks(r)})
}

func (s *Server) findOfferCode(w http.ResponseWriter, product, id string) *offerCode {
	o, ok := s.offerCodes.codes[id]
	if !ok || o.product != product {
		notFound(w, product+"OfferCodes", id)
		return nil
	}
	return o
}

// deactivate applies an "active" change: an offer or code can be
// deactivated, but not reactivated.
func deactivate(w http.ResponseWriter, active *bool, requested *bool) bool {
	if requested == nil {
		return true
	}
	if *requested && !*active {
		stateError(w, "A deactivated offer or code can't be reactivated.")
		return false
	}
	*active = *requested
	return true
}

func (s *Server) updateOfferCode(w http.ResponseWriter, r *http.Request, product, id string) {
	var req struct {
		Data struct {
			Attributes struct {
				Active *bool `json:"active"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.findOfferCode(w, product, id)
	if o == nil || !deactivate(w, o.active(), req.Data.Attributes.Active) {
		return
	}
	s.writeOfferCode(w, r, http.StatusOK, o)
}

func (s *Server) SubscriptionOfferCodesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createOfferCode(w, r, "subscription")
}

func (s *Server) InAppPurchaseOfferCodesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createOfferCode(w, r, "inAppPurchase")
}

func (s *Server) SubscriptionOfferCodesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionOfferCodesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.findOfferCode(w, "subscription", id); o != nil {
		s.writeOfferCode(w, r, http.StatusOK, o)
	}
}

func (s *Server) InAppPurchaseOfferCodesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchaseOfferCodesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.findOfferCode(w, "inAppPurchase", id); o != nil {
		s.writeOfferCode(w, r, http.StatusOK, o)
	}
}

func (s *Server) SubscriptionOfferCodesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateOfferCode(w, r, "subscription", id)
}

func (s *Server) InAppPurchaseOfferCodesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateOfferCode(w, r, "inAppPurchase", id)
}

func (s *Server) SubscriptionOfferCodesPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionOfferCodesPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.findOfferCode(w, "subscription", id)
	if o == nil {
		return
	}
	var items []gen.SubscriptionOfferCodePrice
	for _, p := range o.prices {
		price := gen.SubscriptionOfferCodePrice{Id: p.id, Type: gen.SubscriptionOfferCodePriceTypeSubscriptionOfferCodePrices}
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
	writeJSON(w, http.StatusOK, gen.SubscriptionOfferCodePricesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) InAppPurchaseOfferCodesPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.InAppPurchaseOfferCodesPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.findOfferCode(w, "inAppPurchase", id)
	if o == nil {
		return
	}
	var items []gen.InAppPurchaseOfferPrice
	for _, p := range o.prices {
		price := gen.InAppPurchaseOfferPrice{Id: p.id, Type: gen.InAppPurchaseOfferPriceTypeInAppPurchaseOfferPrices}
		rel := alloc(&price.Relationships)
		t := alloc(&alloc(&rel.Territory).Data)
		t.Id, t.Type = p.territory, "territories"
		pp := alloc(&alloc(&rel.PricePoint).Data)
		pp.Id, pp.Type = p.pricePointID, "inAppPurchasePricePoints"
		items = append(items, price)
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.InAppPurchaseOfferPricesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- code batches ----

type batchRequest struct {
	Data struct {
		Attributes struct {
			CustomCode     string              `json:"customCode"`
			NumberOfCodes  int                 `json:"numberOfCodes"`
			ExpirationDate *openapi_types.Date `json:"expirationDate"`
			Environment    string              `json:"environment"`
			Active         *bool               `json:"active"`
		} `json:"attributes"`
		Relationships struct {
			OfferCode jsonLinkage `json:"offerCode"`
		} `json:"relationships"`
	} `json:"data"`
}

func (s *Server) createBatch(w http.ResponseWriter, r *http.Request, product string, oneTime bool) {
	var req batchRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.offerCodes.codes[req.Data.Relationships.OfferCode.id()]
	if o == nil || o.product != product {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no offer code '"+req.Data.Relationships.OfferCode.id()+"'", "/data/relationships/offerCode")
		return
	}
	switch {
	case in.NumberOfCodes < 1:
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "numberOfCodes must be at least 1.", "/data/attributes/numberOfCodes")
		return
	case oneTime && (in.ExpirationDate == nil || !in.ExpirationDate.After(s.now())):
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A one-time-use batch needs an expiration date in the future.", "/data/attributes/expirationDate")
		return
	case !oneTime && strings.TrimSpace(in.CustomCode) == "":
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "customCode is required.", "/data/attributes/customCode")
		return
	}
	if !oneTime {
		for _, b := range s.offerCodes.batches {
			if !b.oneTime && strings.EqualFold(b.customCode, in.CustomCode) {
				conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The custom code "+in.CustomCode+" is already used.", "/data/attributes/customCode")
				return
			}
		}
	}
	b := &codeBatch{product: product, oneTime: oneTime, id: s.newID("B"), offerCodeID: o.id(), customCode: in.CustomCode, number: in.NumberOfCodes,
		expiration: in.ExpirationDate, environment: in.Environment, active: true, created: s.now().UTC().Truncate(time.Second)}
	if oneTime {
		if b.environment == "" {
			b.environment = "PRODUCTION"
		}
		for i := range b.number {
			b.codes = append(b.codes, fmt.Sprintf("%s%04d", strings.ToUpper(b.id), i))
		}
	}
	s.offerCodes.batches[b.id] = b
	s.writeBatch(w, r, http.StatusCreated, b)
}

func (s *Server) writeBatch(w http.ResponseWriter, r *http.Request, status int, b *codeBatch) {
	created := gen.DateTime{Time: b.created}
	switch {
	case b.product == "subscription" && b.oneTime:
		out := gen.SubscriptionOfferCodeOneTimeUseCode{Id: b.id, Type: gen.SubscriptionOfferCodeOneTimeUseCodeTypeSubscriptionOfferCodeOneTimeUseCodes}
		a := alloc(&out.Attributes)
		a.NumberOfCodes, a.ExpirationDate, a.Active, a.CreatedDate = new(b.number), b.expiration, new(b.active), new(created)
		a.Environment = new(gen.OfferCodeEnvironment(b.environment))
		writeJSON(w, status, gen.SubscriptionOfferCodeOneTimeUseCodeResponse{Data: out, Links: docLinks(r)})
	case b.product == "subscription":
		out := gen.SubscriptionOfferCodeCustomCode{Id: b.id, Type: gen.SubscriptionOfferCodeCustomCodeTypeSubscriptionOfferCodeCustomCodes}
		a := alloc(&out.Attributes)
		a.CustomCode, a.NumberOfCodes, a.ExpirationDate, a.Active, a.CreatedDate = new(b.customCode), new(b.number), b.expiration, new(b.active), new(created)
		writeJSON(w, status, gen.SubscriptionOfferCodeCustomCodeResponse{Data: out, Links: docLinks(r)})
	case b.oneTime:
		out := gen.InAppPurchaseOfferCodeOneTimeUseCode{Id: b.id, Type: gen.InAppPurchaseOfferCodeOneTimeUseCodeTypeInAppPurchaseOfferCodeOneTimeUseCodes}
		a := alloc(&out.Attributes)
		a.NumberOfCodes, a.ExpirationDate, a.Active, a.CreatedDate = new(b.number), b.expiration, new(b.active), new(created)
		a.Environment = new(gen.OfferCodeEnvironment(b.environment))
		writeJSON(w, status, gen.InAppPurchaseOfferCodeOneTimeUseCodeResponse{Data: out, Links: docLinks(r)})
	default:
		out := gen.InAppPurchaseOfferCodeCustomCode{Id: b.id, Type: gen.InAppPurchaseOfferCodeCustomCodeTypeInAppPurchaseOfferCodeCustomCodes}
		a := alloc(&out.Attributes)
		a.CustomCode, a.NumberOfCodes, a.ExpirationDate, a.Active, a.CreatedDate = new(b.customCode), new(b.number), b.expiration, new(b.active), new(created)
		writeJSON(w, status, gen.InAppPurchaseOfferCodeCustomCodeResponse{Data: out, Links: docLinks(r)})
	}
}

func (s *Server) findBatch(w http.ResponseWriter, product string, oneTime bool, id string) *codeBatch {
	b, ok := s.offerCodes.batches[id]
	if !ok || b.product != product || b.oneTime != oneTime {
		notFound(w, "offerCodeBatches", id)
		return nil
	}
	return b
}

func (s *Server) getBatch(w http.ResponseWriter, r *http.Request, product string, oneTime bool, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.findBatch(w, product, oneTime, id); b != nil {
		s.writeBatch(w, r, http.StatusOK, b)
	}
}

func (s *Server) updateBatch(w http.ResponseWriter, r *http.Request, product string, oneTime bool, id string) {
	var req batchRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.findBatch(w, product, oneTime, id)
	if b == nil || !deactivate(w, &b.active, req.Data.Attributes.Active) {
		return
	}
	s.writeBatch(w, r, http.StatusOK, b)
}

// batchValues writes a one-time-use batch's codes as CSV.
func (s *Server) batchValues(w http.ResponseWriter, product, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.findBatch(w, product, true, id)
	if b == nil {
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "Code\n"+strings.Join(b.codes, "\n")+"\n")
}

func (s *Server) SubscriptionOfferCodeCustomCodesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createBatch(w, r, "subscription", false)
}

func (s *Server) SubscriptionOfferCodeCustomCodesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionOfferCodeCustomCodesGetInstanceParams) {
	s.getBatch(w, r, "subscription", false, id)
}

func (s *Server) SubscriptionOfferCodeCustomCodesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateBatch(w, r, "subscription", false, id)
}

func (s *Server) SubscriptionOfferCodeOneTimeUseCodesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createBatch(w, r, "subscription", true)
}

func (s *Server) SubscriptionOfferCodeOneTimeUseCodesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.SubscriptionOfferCodeOneTimeUseCodesGetInstanceParams) {
	s.getBatch(w, r, "subscription", true, id)
}

func (s *Server) SubscriptionOfferCodeOneTimeUseCodesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateBatch(w, r, "subscription", true, id)
}

func (s *Server) SubscriptionOfferCodeOneTimeUseCodesValuesGetToOneRelated(w http.ResponseWriter, _ *http.Request, id string) {
	s.batchValues(w, "subscription", id)
}

func (s *Server) InAppPurchaseOfferCodeCustomCodesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createBatch(w, r, "inAppPurchase", false)
}

func (s *Server) InAppPurchaseOfferCodeCustomCodesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchaseOfferCodeCustomCodesGetInstanceParams) {
	s.getBatch(w, r, "inAppPurchase", false, id)
}

func (s *Server) InAppPurchaseOfferCodeCustomCodesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateBatch(w, r, "inAppPurchase", false, id)
}

func (s *Server) InAppPurchaseOfferCodeOneTimeUseCodesCreateInstance(w http.ResponseWriter, r *http.Request) {
	s.createBatch(w, r, "inAppPurchase", true)
}

func (s *Server) InAppPurchaseOfferCodeOneTimeUseCodesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.InAppPurchaseOfferCodeOneTimeUseCodesGetInstanceParams) {
	s.getBatch(w, r, "inAppPurchase", true, id)
}

func (s *Server) InAppPurchaseOfferCodeOneTimeUseCodesUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	s.updateBatch(w, r, "inAppPurchase", true, id)
}

func (s *Server) InAppPurchaseOfferCodeOneTimeUseCodesValuesGetToOneRelated(w http.ResponseWriter, _ *http.Request, id string) {
	s.batchValues(w, "inAppPurchase", id)
}
