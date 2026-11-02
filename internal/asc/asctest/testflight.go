package asctest

import (
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// TestFlight state. Builds and pre-release versions come from uploads, which
// the API cannot do, so tests add them with AddBuild.
type testflight struct {
	groups             map[string]*betaGroup
	testers            map[string]*betaTester
	builds             map[string]*build
	preReleaseVersions map[string]*preReleaseVersion
	appLocalizations   map[string]*appLocalization
	buildLocalizations map[string]*buildLocalization
	reviewDetails      map[string]*gen.BetaAppReviewDetail  // by app ID
	licenses           map[string]*gen.BetaLicenseAgreement // by app ID
	criteria           map[string]*criterion
	submissions        map[string]*submission
	notifications      []string
	invitations        []Invitation
}

type betaGroup struct {
	gen.BetaGroup
	appID string
}

type betaTester struct {
	gen.BetaTester
	groups map[string]bool
	builds map[string]bool
}

type build struct {
	gen.Build
	appID, preReleaseID string
	groups              map[string]bool
	testers             map[string]bool
	detail              gen.BuildBetaDetail
}

type preReleaseVersion struct {
	gen.PrereleaseVersion
	appID string
}

type appLocalization struct {
	gen.BetaAppLocalization
	appID string
}

type buildLocalization struct {
	gen.BetaBuildLocalization
	buildID string
}

type criterion struct {
	gen.BetaRecruitmentCriterion
	groupID string
}

type submission struct {
	gen.BetaAppReviewSubmission
	buildID string
}

// Invitation is a TestFlight invitation sent to a tester for an app.
type Invitation struct {
	AppID, TesterID string
}

func newTestflight() *testflight {
	return &testflight{
		groups:             map[string]*betaGroup{},
		testers:            map[string]*betaTester{},
		builds:             map[string]*build{},
		preReleaseVersions: map[string]*preReleaseVersion{},
		appLocalizations:   map[string]*appLocalization{},
		buildLocalizations: map[string]*buildLocalization{},
		reviewDetails:      map[string]*gen.BetaAppReviewDetail{},
		licenses:           map[string]*gen.BetaLicenseAgreement{},
		criteria:           map[string]*criterion{},
		submissions:        map[string]*submission{},
	}
}

// buildTestingPeriod is how long a build can be tested: "You can test a build
// for up to 90 days" ("TestFlight overview",
// https://developer.apple.com/help/app-store-connect/test-a-beta-version/testflight-overview/).
const buildTestingPeriod = 90 * 24 * time.Hour

// BuildOptions describes an uploaded build.
type BuildOptions struct {
	// Version is the build number, e.g. "42"; Marketing is the pre-release
	// version, e.g. "1.2.0".
	Version, Marketing string
	Platform           gen.Platform
	// InternalOnly marks a build uploaded as "TestFlight Internal Only".
	InternalOnly bool
	// UsesNonExemptEncryption answers the export compliance question; nil
	// leaves the build missing export compliance.
	UsesNonExemptEncryption *bool
}

// AddBuild adds a processed build of an app and returns its ID. The app's
// pre-release version is created when it is new.
func (s *Server) AddBuild(appID string, o BuildOptions) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.Platform == "" {
		o.Platform = gen.PlatformIOS
	}
	var pre *preReleaseVersion
	for _, p := range s.tf.preReleaseVersions {
		if p.appID == appID && *p.Attributes.Version == o.Marketing && *p.Attributes.Platform == o.Platform {
			pre = p
		}
	}
	if pre == nil {
		pre = &preReleaseVersion{appID: appID, PrereleaseVersion: gen.PrereleaseVersion{Id: s.newID("V"), Type: gen.PrereleaseVersionTypePreReleaseVersions}}
		a := alloc(&pre.Attributes)
		a.Version, a.Platform = new(o.Marketing), new(o.Platform)
		s.tf.preReleaseVersions[pre.Id] = pre
	}
	now := s.now().UTC().Truncate(time.Second)
	b := &build{appID: appID, preReleaseID: pre.Id, groups: map[string]bool{}, testers: map[string]bool{}}
	b.Build = gen.Build{Id: s.newID("U"), Type: gen.BuildTypeBuilds}
	a := alloc(&b.Attributes)
	a.Version = new(o.Version)
	a.ProcessingState = new(gen.BuildAttributesProcessingStateVALID)
	a.Expired = new(false)
	a.UploadedDate = new(gen.DateTime{Time: now})
	a.ExpirationDate = new(gen.DateTime{Time: now.Add(buildTestingPeriod)})
	a.UsesNonExemptEncryption = o.UsesNonExemptEncryption
	a.BuildAudienceType = new(gen.BuildAudienceTypeAPPSTOREELIGIBLE)
	if o.InternalOnly {
		a.BuildAudienceType = new(gen.BuildAudienceTypeINTERNALONLY)
	}
	b.detail = gen.BuildBetaDetail{Id: b.Id, Type: gen.BuildBetaDetailTypeBuildBetaDetails}
	alloc(&b.detail.Attributes).AutoNotifyEnabled = new(false)
	s.tf.builds[b.Id] = b
	s.refreshBuildStates(b)
	return b.Id
}

// refreshBuildStates derives a build's beta states from its export
// compliance, expiration and beta review.
func (s *Server) refreshBuildStates(b *build) {
	a := b.detail.Attributes
	switch {
	case *b.Attributes.Expired:
		a.InternalBuildState = new(gen.InternalBetaStateEXPIRED)
		a.ExternalBuildState = new(gen.ExternalBetaStateEXPIRED)
		return
	case b.Attributes.UsesNonExemptEncryption == nil:
		a.InternalBuildState = new(gen.InternalBetaStateMISSINGEXPORTCOMPLIANCE)
		a.ExternalBuildState = new(gen.ExternalBetaStateMISSINGEXPORTCOMPLIANCE)
		return
	}
	a.InternalBuildState = new(gen.InternalBetaStateINBETATESTING)
	a.ExternalBuildState = new(gen.ExternalBetaStateREADYFORBETASUBMISSION)
	if *b.Attributes.BuildAudienceType == gen.BuildAudienceTypeINTERNALONLY {
		a.ExternalBuildState = new(gen.ExternalBetaStateNOTAPPLICABLE)
	}
	for _, sub := range s.tf.submissions {
		if sub.buildID != b.Id {
			continue
		}
		switch *sub.Attributes.BetaReviewState {
		case gen.BetaReviewStateWAITINGFORREVIEW:
			a.ExternalBuildState = new(gen.ExternalBetaStateWAITINGFORBETAREVIEW)
		case gen.BetaReviewStateINREVIEW:
			a.ExternalBuildState = new(gen.ExternalBetaStateINBETAREVIEW)
		case gen.BetaReviewStateAPPROVED:
			a.ExternalBuildState = new(gen.ExternalBetaStateBETAAPPROVED)
		case gen.BetaReviewStateREJECTED:
			a.ExternalBuildState = new(gen.ExternalBetaStateBETAREJECTED)
		}
	}
}

// ApproveBetaReview approves a build's beta app review, as App Review does.
func (s *Server) ApproveBetaReview(buildID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.tf.submissions {
		if sub.buildID == buildID {
			sub.Attributes.BetaReviewState = new(gen.BetaReviewStateAPPROVED)
		}
	}
	if b, ok := s.tf.builds[buildID]; ok {
		s.refreshBuildStates(b)
	}
}

// Notifications returns the IDs of the builds testers were notified about.
func (s *Server) Notifications() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tf.notifications...)
}

// Invitations returns the invitations sent.
func (s *Server) Invitations() []Invitation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Invitation(nil), s.tf.invitations...)
}

// addAppTestFlight creates the per-app TestFlight objects App Store Connect
// creates with every app: its beta app review detail and beta license
// agreement, which the API can read and update but not create.
func (s *Server) addAppTestFlight(appID string) {
	d := &gen.BetaAppReviewDetail{Id: appID, Type: gen.BetaAppReviewDetailTypeBetaAppReviewDetails}
	alloc(&d.Attributes).DemoAccountRequired = new(false)
	s.tf.reviewDetails[appID] = d
	l := &gen.BetaLicenseAgreement{Id: appID, Type: gen.BetaLicenseAgreementTypeBetaLicenseAgreements}
	alloc(&l.Attributes).AgreementText = new("")
	s.tf.licenses[appID] = l
}
