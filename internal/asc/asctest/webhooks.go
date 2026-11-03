package asctest

import (
	"net/http"
	"net/url"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Webhooks notify a URL of an app's events, signed with a secret that the
// API accepts but never returns (https://developer.apple.com/documentation/appstoreconnectapi/webhooks).
// A ping sends a test event; a delivery can be sent again.

type webhook struct {
	gen.Webhook
	appID      string
	secret     string
	deliveries []gen.WebhookDelivery
}

func newWebhooks() map[string]*webhook { return map[string]*webhook{} }

func renderWebhook(h *webhook) gen.Webhook {
	out := h.Webhook
	attrs := *h.Attributes
	out.Attributes = &attrs
	data := alloc(&alloc(&alloc(&out.Relationships).App).Data)
	data.Id, data.Type = h.appID, "apps"
	return out
}

func validWebhookURL(w http.ResponseWriter, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "The webhook URL must be an HTTPS URL.", "/data/attributes/url")
		return false
	}
	return true
}

func validEventTypes(w http.ResponseWriter, types []gen.WebhookEventType) bool {
	if len(types) == 0 {
		conflict(w, "ENTITY_ERROR.ATTRIBUTE.REQUIRED", "A webhook needs at least one event type.", "/data/attributes/eventTypes")
		return false
	}
	for _, t := range types {
		if !t.Valid() {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "'"+string(t)+"' is not a valid event type.", "/data/attributes/eventTypes")
			return false
		}
	}
	return true
}

func (s *Server) WebhooksCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.WebhookCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	appID := req.Data.Relationships.App.Data.Id
	if !validWebhookURL(w, in.Url) || !validEventTypes(w, in.EventTypes) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	h := &webhook{appID: appID, secret: in.Secret, Webhook: gen.Webhook{Id: s.newID("H"), Type: gen.WebhookTypeWebhooks}}
	a := alloc(&h.Attributes)
	a.Name, a.Url, a.Enabled, a.EventTypes = new(in.Name), new(in.Url), new(in.Enabled), new(in.EventTypes)
	s.webhooks[h.Id] = h
	writeJSON(w, http.StatusCreated, gen.WebhookResponse{Data: renderWebhook(h), Links: docLinks(r)})
}

func (s *Server) WebhooksGetInstance(w http.ResponseWriter, r *http.Request, id string, _ gen.WebhooksGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.webhooks[id]
	if !ok {
		notFound(w, "webhooks", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.WebhookResponse{Data: renderWebhook(h), Links: docLinks(r)})
}

func (s *Server) AppsWebhooksGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.AppsWebhooksGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		notFound(w, "apps", id)
		return
	}
	var items []gen.Webhook
	for _, h := range sortedValues(s.webhooks) {
		if h.appID == id {
			items = append(items, renderWebhook(h))
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.WebhooksResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) WebhooksUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.WebhookUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	in := asc.Value(req.Data.Attributes)
	if in.Url != nil && !validWebhookURL(w, *in.Url) || in.EventTypes != nil && !validEventTypes(w, *in.EventTypes) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.webhooks[id]
	if !ok {
		notFound(w, "webhooks", id)
		return
	}
	a := h.Attributes
	setString(&a.Name, in.Name)
	setString(&a.Url, in.Url)
	if in.Enabled != nil {
		a.Enabled = new(*in.Enabled)
	}
	if in.EventTypes != nil {
		a.EventTypes = new(*in.EventTypes)
	}
	if in.Secret != nil {
		h.secret = *in.Secret
	}
	writeJSON(w, http.StatusOK, gen.WebhookResponse{Data: renderWebhook(h), Links: docLinks(r)})
}

func (s *Server) WebhooksDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.webhooks[id]; !ok {
		notFound(w, "webhooks", id)
		return
	}
	delete(s.webhooks, id)
	w.WriteHeader(http.StatusNoContent)
}

// deliver records a successful delivery to h.
func (s *Server) deliver(h *webhook, redelivery bool) gen.WebhookDelivery {
	d := gen.WebhookDelivery{Id: s.newID("D"), Type: gen.WebhookDeliveryTypeWebhookDeliveries}
	a := alloc(&d.Attributes)
	now := gen.DateTime{Time: s.now().UTC().Truncate(time.Second)}
	a.CreatedDate, a.SentDate = new(now), new(now)
	a.DeliveryState, a.Redelivery = new(gen.WebhookDeliveryAttributesDeliveryStateSUCCEEDED), new(redelivery)
	alloc(&a.Request).Url = new(*h.Attributes.Url)
	alloc(&a.Response).HttpStatusCode = new(http.StatusOK)
	h.deliveries = append(h.deliveries, d)
	return d
}

// WebhookPingsCreateInstance sends a test event to an enabled webhook.
func (s *Server) WebhookPingsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.WebhookPingCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := req.Data.Relationships.Webhook.Data.Id
	h, ok := s.webhooks[id]
	if !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no webhooks with id '"+id+"'", "/data/relationships/webhook")
		return
	}
	if !*h.Attributes.Enabled {
		stateError(w, "The webhook is disabled.")
		return
	}
	s.deliver(h, false)
	writeJSON(w, http.StatusCreated, gen.WebhookPingResponse{
		Data: gen.WebhookPing{Id: s.newID("G"), Type: gen.WebhookPingTypeWebhookPings}, Links: docLinks(r),
	})
}

// WebhookDeliveriesCreateInstance sends a past delivery again.
func (s *Server) WebhookDeliveriesCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.WebhookDeliveryCreateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	template := req.Data.Relationships.Template.Data.Id
	for _, h := range s.webhooks {
		for _, d := range h.deliveries {
			if d.Id == template {
				writeJSON(w, http.StatusCreated, gen.WebhookDeliveryResponse{Data: s.deliver(h, true), Links: docLinks(r)})
				return
			}
		}
	}
	conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no webhookDeliveries with id '"+template+"'", "/data/relationships/template")
}

func (s *Server) WebhooksDeliveriesGetToManyRelated(w http.ResponseWriter, r *http.Request, id string, params gen.WebhooksDeliveriesGetToManyRelatedParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.webhooks[id]
	if !ok {
		notFound(w, "webhooks", id)
		return
	}
	var items []gen.WebhookDelivery
	for _, d := range h.deliveries {
		if matches(params.FilterDeliveryState, string(*d.Attributes.DeliveryState)) {
			items = append(items, d)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.WebhookDeliveriesResponse{Data: nonNil(data), Links: links, Meta: meta})
}

// WebhookSecret returns the secret App Store Connect signs a webhook's
// events with.
func (s *Server) WebhookSecret(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.webhooks[id].secret
}
