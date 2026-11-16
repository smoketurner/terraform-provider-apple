package asctest

import (
	"net/http"
	"slices"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Retention messaging. An app has at most 10 retention messages, which
// people see when they start to cancel a subscription; a message's reference
// name can't change. A message holds its content in versions, which can't be
// changed or deleted and start in PREPARE_FOR_SUBMISSION. A version has a
// localization per locale, with a header of up to 66 characters and a body
// of up to 144, and an approved version takes no new localizations. A subscription
// has at most one retention message, which assigning replaces. Retention
// offers belong to a subscription, price each territory with one of its
// price points, and change only in their eligibility rules: paid months of 1
// to 24, 36, 48 or 60, and a wait between offers of 2 to 24 months. A
// subscription has at most 10 offers per plan type, and presents the ones
// added to its retention message offers
// (https://developer.apple.com/documentation/appstoreconnectapi/setting-up-retention-messaging).

const (
	maxRetentionMessages      = 10
	maxRetentionOffersPerPlan = 10
	maxRetentionReferenceName = 64
	maxRetentionHeader        = 66
	maxRetentionBody          = 144

	retentionVersionTarget      = "retentionMessageVersion"
	retentionPlacementType      = "RETENTION_MESSAGE_ASSET"
	retentionMessagesCollection = "retentionMessages"

	retentionVersionInitial   = gen.RetentionMessageVersionAttributesStatePREPAREFORSUBMISSION
	retentionVersionReady     = gen.RetentionMessageVersionAttributesStateREADYFORREVIEW
	retentionVersionWithdrawn = gen.RetentionMessageVersionAttributesStateDEVELOPERREJECTED
	retentionVersionRejected  = gen.RetentionMessageVersionAttributesStateREJECTED
	retentionVersionApproved  = gen.RetentionMessageVersionAttributesStateAPPROVED
)

type retentionMessage struct {
	id, appID, referenceName string
}

type retentionVersion struct {
	id, messageID, placementID string
	state                      gen.RetentionMessageVersionAttributesState
}

type retentionLocalization struct {
	id, versionID, locale string
	header, body          *string
}

type retentionOffer struct {
	gen.RetentionOffer
	subscriptionID string
	prices         []offerPrice
}

type retentionState struct {
	messages      map[string]*retentionMessage
	versions      map[string]*retentionVersion
	localizations map[string]*retentionLocalization
	offers        map[string]*retentionOffer
	// assigned maps a subscription ID to its retention message's ID.
	assigned map[string]string
	// messageOffers maps a subscription ID to the IDs of the offers its
	// retention message presents.
	messageOffers map[string][]string
}

func newRetentionState() *retentionState {
	return &retentionState{
		messages: map[string]*retentionMessage{}, versions: map[string]*retentionVersion{},
		localizations: map[string]*retentionLocalization{}, offers: map[string]*retentionOffer{},
		assigned: map[string]string{}, messageOffers: map[string][]string{},
	}
}

// ---- messages ----

func renderRetentionMessage(m *retentionMessage) gen.RetentionMessage {
	out := gen.RetentionMessage{Id: m.id, Type: gen.RetentionMessageTypeRetentionMessages}
	alloc(&out.Attributes).ReferenceName = new(m.referenceName)
	return out
}

func (s *Server) RetentionMessagesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.RetentionMessageCreateRequest
	if !decode(w, r, &req) {
		return
	}
	name, appID := req.Data.Attributes.ReferenceName, req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	if name == "" || len([]rune(name)) > maxRetentionReferenceName {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The reference name must have 1 to 64 characters.", "/data/attributes/referenceName")
		return
	}
	n := 0
	for _, m := range s.retention.messages {
		if m.appID == appID {
			n++
		}
	}
	if n >= maxRetentionMessages {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app already has 10 retention messages.", "/data/relationships/app")
		return
	}
	m := &retentionMessage{id: s.newID("RM"), appID: appID, referenceName: name}
	s.retention.messages[m.id] = m
	writeJSON(w, http.StatusCreated, gen.RetentionMessageResponse{Data: renderRetentionMessage(m), Links: docLinks(r)})
}

func (s *Server) RetentionMessagesGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.RetentionMessagesGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.retention.messages[id]
	if !ok {
		notFound(w, retentionMessagesCollection, id)
		return
	}
	writeJSON(w, http.StatusOK, gen.RetentionMessageResponse{Data: renderRetentionMessage(m), Links: docLinks(r)})
}

// RetentionMessagesDeleteInstance deletes a message with its versions and
// their localizations, and unassigns it from its subscriptions.
func (s *Server) RetentionMessagesDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.retention.messages[id]; !ok {
		notFound(w, retentionMessagesCollection, id)
		return
	}
	for vid, v := range s.retention.versions {
		if v.messageID != id {
			continue
		}
		for lid, l := range s.retention.localizations {
			if l.versionID == vid {
				delete(s.retention.localizations, lid)
			}
		}
		if p, ok := s.lib.placements[v.placementID]; ok {
			p.targetKind, p.targetID = "", ""
		}
		delete(s.retention.versions, vid)
	}
	for subID, messageID := range s.retention.assigned {
		if messageID == id {
			delete(s.retention.assigned, subID)
		}
	}
	delete(s.retention.messages, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) AppsRetentionMessagesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsRetentionMessagesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.RetentionMessage
	for _, m := range sortedValues(s.retention.messages) {
		if m.appID == id {
			items = append(items, renderRetentionMessage(m))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.RetentionMessagesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- versions ----

func renderRetentionVersion(v *retentionVersion) gen.RetentionMessageVersion {
	out := gen.RetentionMessageVersion{Id: v.id, Type: gen.RetentionMessageVersionTypeRetentionMessageVersions}
	alloc(&out.Attributes).State = new(v.state)
	rel := alloc(&out.Relationships)
	m := alloc(&alloc(&rel.RetentionMessage).Data)
	m.Id, m.Type = v.messageID, "retentionMessages"
	if v.placementID != "" {
		p := alloc(&alloc(&rel.Placement).Data)
		p.Id, p.Type = v.placementID, "appAssetLibraryPlacements"
	}
	return out
}

// RetentionMessageVersionsCreateInstance creates a version, optionally
// showing a placement that no localization or version uses yet.
func (s *Server) RetentionMessageVersionsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.RetentionMessageVersionCreateRequest
	if !decode(w, r, &req) {
		return
	}
	rel := req.Data.Relationships
	messageID := rel.RetentionMessage.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.retention.messages[messageID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no retentionMessages with id '"+messageID+"'", "/data/relationships/retentionMessage")
		return
	}
	v := &retentionVersion{id: s.newID("RV"), messageID: messageID, state: retentionVersionInitial}
	if rel.Placement != nil && rel.Placement.Data != nil {
		p, ok := s.lib.placements[rel.Placement.Data.Id]
		if !ok || p.placementType != retentionPlacementType || p.targetID != "" {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The placement must be an unused "+retentionPlacementType+" placement.", "/data/relationships/placement")
			return
		}
		p.targetKind, p.targetID = retentionVersionTarget, v.id
		v.placementID = p.id
	}
	s.retention.versions[v.id] = v
	writeJSON(w, http.StatusCreated, gen.RetentionMessageVersionResponse{Data: renderRetentionVersion(v), Links: docLinks(r)})
}

func (s *Server) RetentionMessageVersionsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.RetentionMessageVersionsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.retention.versions[id]
	if !ok {
		notFound(w, "retentionMessageVersions", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.RetentionMessageVersionResponse{Data: renderRetentionVersion(v), Links: docLinks(r)})
}

// addRetentionVersionItem adds a version to a review submission of its app.
// The version needs a localization.
func (s *Server) addRetentionVersionItem(w http.ResponseWriter, sub *reviewSubmission, versionID string) bool {
	v, ok := s.retention.versions[versionID]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no retentionMessageVersions with id '"+versionID+"'", "/data/relationships/retentionMessageVersion")
		return false
	}
	if s.retention.messages[v.messageID].appID != sub.appID {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version belongs to another app.", "/data/relationships/retentionMessageVersion")
		return false
	}
	if !slices.Contains([]gen.RetentionMessageVersionAttributesState{retentionVersionInitial, retentionVersionWithdrawn, retentionVersionRejected}, v.state) {
		stateError(w, "The retention message version is already in review or approved.")
		return false
	}
	if len(s.retentionLocalizations(versionID)) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The version has no localizations.", "/data/relationships/retentionMessageVersion")
		return false
	}
	v.state = retentionVersionReady
	sub.retentionVersions = append(sub.retentionVersions, versionID)
	return true
}

func (s *Server) setRetentionVersionStates(sub *reviewSubmission, state gen.RetentionMessageVersionAttributesState) {
	for _, id := range sub.retentionVersions {
		if v, ok := s.retention.versions[id]; ok {
			v.state = state
		}
	}
}

// ApproveRetentionMessageVersion approves a version, as App Review does.
func (s *Server) ApproveRetentionMessageVersion(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retention.versions[id].state = retentionVersionApproved
}

// ---- localizations ----

func (s *Server) retentionLocalizations(versionID string) []*retentionLocalization {
	var out []*retentionLocalization
	for _, l := range sortedValues(s.retention.localizations) {
		if l.versionID == versionID {
			out = append(out, l)
		}
	}
	return out
}

func renderRetentionLocalization(l *retentionLocalization) gen.RetentionMessageLocalization {
	out := gen.RetentionMessageLocalization{Id: l.id, Type: gen.RetentionMessageLocalizationTypeRetentionMessageLocalizations}
	a := alloc(&out.Attributes)
	a.Locale, a.Header, a.Body = new(l.locale), l.header, l.body
	v := alloc(&alloc(&alloc(&out.Relationships).RetentionMessageVersion).Data)
	v.Id, v.Type = l.versionID, "retentionMessageVersions"
	return out
}

// checkRetentionText writes an error and returns false when the header or
// body is too long.
func checkRetentionText(w http.ResponseWriter, header, body *string) bool {
	if len([]rune(asc.Value(header))) > maxRetentionHeader {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.TOO_LONG", "The header can have up to 66 characters.", "/data/attributes/header")
		return false
	}
	if len([]rune(asc.Value(body))) > maxRetentionBody {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.TOO_LONG", "The body can have up to 144 characters.", "/data/attributes/body")
		return false
	}
	return true
}

func (s *Server) RetentionMessageLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.RetentionMessageLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in, versionID := req.Data.Attributes, req.Data.Relationships.RetentionMessageVersion.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.retention.versions[versionID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no retentionMessageVersions with id '"+versionID+"'", "/data/relationships/retentionMessageVersion")
		return
	}
	if s.retention.versions[versionID].state == retentionVersionApproved {
		stateError(w, "An approved retention message version takes no new localizations.")
		return
	}
	if !checkRetentionText(w, in.Header, in.Body) {
		return
	}
	for _, l := range s.retentionLocalizations(versionID) {
		if l.locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The version already has a localization for "+in.Locale+".", "/data/attributes/locale")
			return
		}
	}
	l := &retentionLocalization{id: s.newID("RL"), versionID: versionID, locale: in.Locale, header: in.Header, body: in.Body}
	s.retention.localizations[l.id] = l
	writeJSON(w, http.StatusCreated, gen.RetentionMessageLocalizationResponse{Data: renderRetentionLocalization(l), Links: docLinks(r)})
}

func (s *Server) RetentionMessageLocalizationsGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.RetentionMessageLocalizationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.retention.localizations[id]
	if !ok {
		notFound(w, "retentionMessageLocalizations", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.RetentionMessageLocalizationResponse{Data: renderRetentionLocalization(l), Links: docLinks(r)})
}

func (s *Server) RetentionMessageLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.RetentionMessageLocalizationUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.retention.localizations[id]
	if !ok {
		notFound(w, "retentionMessageLocalizations", id)
		return
	}
	header, body := l.header, l.body
	if in := req.Data.Attributes; in != nil {
		if in.Header != nil || nulls["header"] {
			header = in.Header
		}
		if in.Body != nil || nulls["body"] {
			body = in.Body
		}
	}
	if !checkRetentionText(w, header, body) {
		return
	}
	l.header, l.body = header, body
	writeJSON(w, http.StatusOK, gen.RetentionMessageLocalizationResponse{Data: renderRetentionLocalization(l), Links: docLinks(r)})
}

func (s *Server) RetentionMessageLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.retention.localizations[id]; !ok {
		notFound(w, "retentionMessageLocalizations", id)
		return
	}
	delete(s.retention.localizations, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RetentionMessageVersionsLocalizationsGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.RetentionMessageVersionsLocalizationsGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.retention.versions[id]; !ok {
		notFound(w, "retentionMessageVersions", id)
		return
	}
	var items []gen.RetentionMessageLocalization
	for _, l := range s.retentionLocalizations(id) {
		items = append(items, renderRetentionLocalization(l))
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.RetentionMessageLocalizationsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// ---- offers ----

// checkRetentionEligibility writes an error and returns false when the paid
// months or the wait between offers is outside what the API accepts.
func checkRetentionEligibility(w http.ResponseWriter, paid int, wait *int) bool {
	if (paid < 1 || paid > 24) && paid != 36 && paid != 48 && paid != 60 {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The paid subscription duration must be 1 to 24, 36, 48 or 60 months.",
			"/data/attributes/customerEligibilityPaidSubscriptionDurationInMonths")
		return false
	}
	if wait != nil && (*wait < 2 || *wait > 24) {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The wait between offers must be 2 to 24 months.",
			"/data/attributes/customerEligibilityWaitBetweenOffersInMonths")
		return false
	}
	return true
}

func (s *Server) RetentionOffersCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.RetentionOfferCreateRequest
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
	plan := gen.SubscriptionPlanTypeUPFRONT
	if in.TargetSubscriptionPlanType != nil {
		plan = *in.TargetSubscriptionPlanType
	}
	samePlan := 0
	for _, o := range s.retention.offers {
		if o.subscriptionID != subID {
			continue
		}
		if *o.Attributes.OfferId == in.OfferId {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "The offer ID "+in.OfferId+" is already used.", "/data/attributes/offerId")
			return
		}
		if *o.Attributes.TargetSubscriptionPlanType == plan {
			samePlan++
		}
	}
	if samePlan >= maxRetentionOffersPerPlan {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The subscription already has 10 retention offers for the plan type.", "/data/relationships/subscription")
		return
	}
	if !checkRetentionEligibility(w, in.CustomerEligibilityPaidSubscriptionDurationInMonths, in.CustomerEligibilityWaitBetweenOffersInMonths) {
		return
	}
	prices, ok := s.offerPrices(w, subID, priceRefs(raw), raw.Included)
	if !ok {
		return
	}
	if in.OfferMode != gen.SubscriptionOfferModeFREETRIAL && len(prices) == 0 {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.REQUIRED", "A paid offer needs prices.", "/data/relationships/prices")
		return
	}
	o := &retentionOffer{subscriptionID: subID, prices: prices, RetentionOffer: gen.RetentionOffer{Id: s.newID("RO"), Type: gen.RetentionOfferTypeRetentionOffers}}
	a := alloc(&o.Attributes)
	a.ReferenceName, a.OfferId, a.Duration, a.OfferMode, a.PeriodCount = new(in.ReferenceName), new(in.OfferId), new(in.Duration), new(in.OfferMode), new(in.PeriodCount)
	a.CustomerEligibilityPaidSubscriptionDurationInMonths = new(in.CustomerEligibilityPaidSubscriptionDurationInMonths)
	a.CustomerEligibilityWaitBetweenOffersInMonths = in.CustomerEligibilityWaitBetweenOffersInMonths
	a.TargetSubscriptionPlanType = new(plan)
	d := alloc(&alloc(&alloc(&o.Relationships).Subscription).Data)
	d.Id, d.Type = subID, "subscriptions"
	s.retention.offers[o.Id] = o
	writeJSON(w, http.StatusCreated, gen.RetentionOfferResponse{Data: o.RetentionOffer, Links: docLinks(r)})
}

func (s *Server) RetentionOffersGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.RetentionOffersGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.retention.offers[id]
	if !ok {
		notFound(w, "retentionOffers", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.RetentionOfferResponse{Data: o.RetentionOffer, Links: docLinks(r)})
}

func (s *Server) RetentionOffersPricesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.RetentionOffersPricesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.retention.offers[id]
	if !ok {
		notFound(w, "retentionOffers", id)
		return
	}
	var items []gen.RetentionOfferPrice
	for _, p := range o.prices {
		price := gen.RetentionOfferPrice{Id: p.id, Type: gen.RetentionOfferPriceTypeRetentionOfferPrices}
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
	writeJSON(w, http.StatusOK, gen.RetentionOfferPricesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// RetentionOffersUpdateInstance changes an offer's eligibility rules.
func (s *Server) RetentionOffersUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.RetentionOfferUpdateRequest
	nulls, ok := readBody(w, r, &req)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.retention.offers[id]
	if !ok {
		notFound(w, "retentionOffers", id)
		return
	}
	a := o.Attributes
	paid, wait := *a.CustomerEligibilityPaidSubscriptionDurationInMonths, a.CustomerEligibilityWaitBetweenOffersInMonths
	if in := req.Data.Attributes; in != nil {
		if in.CustomerEligibilityPaidSubscriptionDurationInMonths != nil {
			paid = *in.CustomerEligibilityPaidSubscriptionDurationInMonths
		}
		if in.CustomerEligibilityWaitBetweenOffersInMonths != nil || nulls["customerEligibilityWaitBetweenOffersInMonths"] {
			wait = in.CustomerEligibilityWaitBetweenOffersInMonths
		}
	}
	if !checkRetentionEligibility(w, paid, wait) {
		return
	}
	a.CustomerEligibilityPaidSubscriptionDurationInMonths, a.CustomerEligibilityWaitBetweenOffersInMonths = new(paid), wait
	writeJSON(w, http.StatusOK, gen.RetentionOfferResponse{Data: o.RetentionOffer, Links: docLinks(r)})
}

// RetentionOffersDeleteInstance deletes an offer and stops presenting it.
func (s *Server) RetentionOffersDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.retention.offers[id]
	if !ok {
		notFound(w, "retentionOffers", id)
		return
	}
	s.retention.messageOffers[o.subscriptionID] = slices.DeleteFunc(s.retention.messageOffers[o.subscriptionID], func(other string) bool { return other == id })
	delete(s.retention.offers, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- a subscription's retention message and the offers it presents ----

func (s *Server) SubscriptionsRetentionMessageGetToOneRelationship(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	var data any
	if messageID, ok := s.retention.assigned[id]; ok {
		data = map[string]string{"type": retentionMessagesCollection, "id": messageID}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "links": docLinks(r)})
}

// SubscriptionsRetentionMessageUpdateToOneRelationship assigns one of the
// app's messages to a subscription, replacing the one it has.
func (s *Server) SubscriptionsRetentionMessageUpdateToOneRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionRetentionMessageLinkageRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	m, ok := s.retention.messages[req.Data.Id]
	if !ok || m.appID != s.subscriptionApp(id) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The app has no retentionMessages with id '"+req.Data.Id+"'", "/data")
		return
	}
	s.retention.assigned[id] = m.id
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SubscriptionsRetentionMessageOffersGetToManyRelationship(w http.ResponseWriter, r *http.Request, id string, params gen.SubscriptionsRetentionMessageOffersGetToManyRelationshipParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	ids, links, meta, ok := page(w, r, s.retention.messageOffers[id], params.Limit, maxLinkageLimit)
	if !ok {
		return
	}
	out := gen.SubscriptionRetentionMessageOffersLinkagesResponse{Links: links, Meta: meta}
	out.Data = nonNil(out.Data)
	for _, offerID := range ids {
		e := push(&out.Data)
		e.Id, e.Type = offerID, "retentionOffers"
	}
	writeJSON(w, http.StatusOK, out)
}

// SubscriptionsRetentionMessageOffersCreateToManyRelationship presents the
// subscription's own offers with its retention message.
func (s *Server) SubscriptionsRetentionMessageOffersCreateToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionRetentionMessageOffersLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	for _, e := range req.Data {
		if o, ok := s.retention.offers[e.Id]; !ok || o.subscriptionID != id {
			conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "The subscription has no retentionOffers with id '"+e.Id+"'", "/data")
			return
		}
	}
	for _, e := range req.Data {
		if !slices.Contains(s.retention.messageOffers[id], e.Id) {
			s.retention.messageOffers[id] = append(s.retention.messageOffers[id], e.Id)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SubscriptionsRetentionMessageOffersDeleteToManyRelationship(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.SubscriptionRetentionMessageOffersLinkagesRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs.subscriptions[id]; !ok {
		notFound(w, "subscriptions", id)
		return
	}
	for _, e := range req.Data {
		s.retention.messageOffers[id] = slices.DeleteFunc(s.retention.messageOffers[id], func(other string) bool { return other == e.Id })
	}
	w.WriteHeader(http.StatusNoContent)
}
