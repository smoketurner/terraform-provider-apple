package asctest

import (
	"net/http"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// TestFlight test information: localized beta app and build descriptions,
// and the per-app review detail and license agreement.

func (s *Server) renderAppLocalization(r *http.Request, l *appLocalization, withApp bool) gen.BetaAppLocalization {
	out := l.BetaAppLocalization
	attrs := *l.Attributes
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "betaAppLocalizations", l.Id)
	out.Relationships = nil
	if withApp {
		d := alloc(&alloc(&alloc(&out.Relationships).App).Data)
		d.Id, d.Type = l.appID, "apps"
	}
	return out
}

// BetaAppLocalizationsCreateInstance adds a locale's beta app information. An
// app has at most one localization per locale.
func (s *Server) BetaAppLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaAppLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	appID := req.Data.Relationships.App.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no apps with id '"+appID+"'", "/data/relationships/app")
		return
	}
	for _, l := range s.tf.appLocalizations {
		if l.appID == appID && *l.Attributes.Locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A localization for locale "+in.Locale+" already exists.", "/data/attributes/locale")
			return
		}
	}
	l := &appLocalization{appID: appID, BetaAppLocalization: gen.BetaAppLocalization{Id: s.newID("L"), Type: gen.BetaAppLocalizationTypeBetaAppLocalizations}}
	a := alloc(&l.Attributes)
	a.Locale, a.Description, a.FeedbackEmail = new(in.Locale), in.Description, in.FeedbackEmail
	a.MarketingUrl, a.PrivacyPolicyUrl, a.TvOsPrivacyPolicy = in.MarketingUrl, in.PrivacyPolicyUrl, in.TvOsPrivacyPolicy
	s.tf.appLocalizations[l.Id] = l
	writeJSON(w, http.StatusCreated, gen.BetaAppLocalizationResponse{Data: s.renderAppLocalization(r, l, false), Links: docLinks(r)})
}

func (s *Server) BetaAppLocalizationsGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.BetaAppLocalizationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.tf.appLocalizations[id]
	if !ok {
		notFound(w, "betaAppLocalizations", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaAppLocalizationResponse{Data: s.renderAppLocalization(r, l, includes(params.Include, "app")), Links: docLinks(r)})
}

// setString applies an optional string from an update request; an empty
// string clears the field.
func setString(dst **string, src *string) {
	if src == nil {
		return
	}
	if *src == "" {
		*dst = nil
		return
	}
	*dst = new(*src)
}

func (s *Server) BetaAppLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaAppLocalizationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.tf.appLocalizations[id]
	if !ok {
		notFound(w, "betaAppLocalizations", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := l.Attributes
		setString(&a.Description, in.Description)
		setString(&a.FeedbackEmail, in.FeedbackEmail)
		setString(&a.MarketingUrl, in.MarketingUrl)
		setString(&a.PrivacyPolicyUrl, in.PrivacyPolicyUrl)
		setString(&a.TvOsPrivacyPolicy, in.TvOsPrivacyPolicy)
	}
	writeJSON(w, http.StatusOK, gen.BetaAppLocalizationResponse{Data: s.renderAppLocalization(r, l, false), Links: docLinks(r)})
}

func (s *Server) BetaAppLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tf.appLocalizations[id]; !ok {
		notFound(w, "betaAppLocalizations", id)
		return
	}
	delete(s.tf.appLocalizations, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) renderBuildLocalization(r *http.Request, l *buildLocalization, withBuild bool) gen.BetaBuildLocalization {
	out := l.BetaBuildLocalization
	attrs := *l.Attributes
	out.Attributes = &attrs
	out.Links = resourceLinks(r, "betaBuildLocalizations", l.Id)
	out.Relationships = nil
	if withBuild {
		d := alloc(&alloc(&alloc(&out.Relationships).Build).Data)
		d.Id, d.Type = l.buildID, "builds"
	}
	return out
}

// BetaBuildLocalizationsCreateInstance adds a locale's "What to Test" text
// to a build. A build has at most one localization per locale.
func (s *Server) BetaBuildLocalizationsCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req gen.BetaBuildLocalizationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	in := req.Data.Attributes
	buildID := req.Data.Relationships.Build.Data.Id
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.buildExists(buildID) {
		conflict(w, "ENTITY_ERROR.RELATIONSHIP.INVALID", "There is no builds with id '"+buildID+"'", "/data/relationships/build")
		return
	}
	for _, l := range s.tf.buildLocalizations {
		if l.buildID == buildID && *l.Attributes.Locale == in.Locale {
			conflict(w, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A localization for locale "+in.Locale+" already exists.", "/data/attributes/locale")
			return
		}
	}
	l := &buildLocalization{buildID: buildID, BetaBuildLocalization: gen.BetaBuildLocalization{Id: s.newID("W"), Type: gen.BetaBuildLocalizationTypeBetaBuildLocalizations}}
	a := alloc(&l.Attributes)
	a.Locale, a.WhatsNew = new(in.Locale), in.WhatsNew
	s.tf.buildLocalizations[l.Id] = l
	writeJSON(w, http.StatusCreated, gen.BetaBuildLocalizationResponse{Data: s.renderBuildLocalization(r, l, false), Links: docLinks(r)})
}

func (s *Server) BetaBuildLocalizationsGetInstance(w http.ResponseWriter, r *http.Request, id string, params gen.BetaBuildLocalizationsGetInstanceParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.tf.buildLocalizations[id]
	if !ok {
		notFound(w, "betaBuildLocalizations", id)
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaBuildLocalizationResponse{Data: s.renderBuildLocalization(r, l, includes(params.Include, "build")), Links: docLinks(r)})
}

func (s *Server) BetaBuildLocalizationsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaBuildLocalizationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.tf.buildLocalizations[id]
	if !ok {
		notFound(w, "betaBuildLocalizations", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		setString(&l.Attributes.WhatsNew, in.WhatsNew)
	}
	writeJSON(w, http.StatusOK, gen.BetaBuildLocalizationResponse{Data: s.renderBuildLocalization(r, l, false), Links: docLinks(r)})
}

func (s *Server) BetaBuildLocalizationsDeleteInstance(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tf.buildLocalizations[id]; !ok {
		notFound(w, "betaBuildLocalizations", id)
		return
	}
	delete(s.tf.buildLocalizations, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BetaAppReviewDetailsGetCollection(w http.ResponseWriter, r *http.Request, params gen.BetaAppReviewDetailsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.BetaAppReviewDetail
	for _, appID := range params.FilterApp {
		if d, ok := s.tf.reviewDetails[appID]; ok {
			items = append(items, *d)
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaAppReviewDetailsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) BetaAppReviewDetailsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaAppReviewDetailUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tf.reviewDetails[id]
	if !ok {
		notFound(w, "betaAppReviewDetails", id)
		return
	}
	if in := req.Data.Attributes; in != nil {
		a := d.Attributes
		setString(&a.ContactEmail, in.ContactEmail)
		setString(&a.ContactFirstName, in.ContactFirstName)
		setString(&a.ContactLastName, in.ContactLastName)
		setString(&a.ContactPhone, in.ContactPhone)
		setString(&a.DemoAccountName, in.DemoAccountName)
		setString(&a.DemoAccountPassword, in.DemoAccountPassword)
		setString(&a.Notes, in.Notes)
		if in.DemoAccountRequired != nil {
			a.DemoAccountRequired = new(*in.DemoAccountRequired)
		}
	}
	writeJSON(w, http.StatusOK, gen.BetaAppReviewDetailResponse{Data: *d, Links: docLinks(r)})
}

func (s *Server) BetaLicenseAgreementsGetCollection(w http.ResponseWriter, r *http.Request, params gen.BetaLicenseAgreementsGetCollectionParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []gen.BetaLicenseAgreement
	for _, appID := range sortedKeys(s.tf.licenses) {
		if matches(params.FilterApp, appID) {
			items = append(items, *s.tf.licenses[appID])
		}
	}
	data, links, meta, ok := page(w, r, items, params.Limit, maxLimit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.BetaLicenseAgreementsResponse{Data: nonNil(data), Links: links, Meta: meta})
}

func (s *Server) BetaLicenseAgreementsUpdateInstance(w http.ResponseWriter, r *http.Request, id string) {
	var req gen.BetaLicenseAgreementUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.tf.licenses[id]
	if !ok {
		notFound(w, "betaLicenseAgreements", id)
		return
	}
	if in := req.Data.Attributes; in != nil && in.AgreementText != nil {
		l.Attributes.AgreementText = new(*in.AgreementText)
	}
	writeJSON(w, http.StatusOK, gen.BetaLicenseAgreementResponse{Data: *l, Links: docLinks(r)})
}
