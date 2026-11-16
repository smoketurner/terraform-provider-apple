package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func appEventConfig(event, rest string) string {
	return `
resource "apple_app_event" "summer" {
  app_id = local.app_id
` + event + `
}

data "apple_app_events" "drafts" {
  app_id     = local.app_id
  states     = ["DRAFT"]
  depends_on = [apple_app_event.summer]
}
` + rest
}

func appEventLocalizationConfig(text string) string {
	return `
resource "apple_app_event_localization" "en_US" {
  app_event_id = apple_app_event.summer.id
  locale       = "en-US"
` + text + `
}
`
}

const summerSchedule = `
  territory_schedules = [{
    territories   = ["USA", "CAN"]
    publish_start = "2099-06-01T16:00:00Z"
    event_start   = "2099-06-15T16:00:00Z"
    event_end     = "2099-06-30T16:00:00Z"
  }]
`

func (e *testEnv) checkAppEventState(id *string, want gen.AppEventAttributesEventState) resource.TestCheckFunc {
	return func(*terraform.State) error {
		out, err := e.client.AppEventsGetInstanceWithResponse(context.Background(), *id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		if got := asc.Value(asc.Value(out.JSON200.Data.Attributes).EventState); got != want {
			return fmt.Errorf("in-app event is %s, want %s", got, want)
		}
		return nil
	}
}

func TestAccAppEvent(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	dir := t.TempDir()
	writeImage(t, dir, "card.png", "card ")
	const address = "apple_app_event.summer"
	full := `reference_name = "Summer Cup"
  badge                = "COMPETITION"
  deep_link            = "https://example.com/events/summer"
  purchase_requirement = "NO_COST_ASSOCIATED"
  primary_locale       = "en-US"
  priority             = "HIGH"
  purpose              = "ATTRACT_NEW_USERS"` + summerSchedule
	text := appEventLocalizationConfig(`name = "Summer Cup"
  short_description = "Compete for the summer title."
  long_description  = "Win matches all month to climb the leaderboard and take the cup."`)
	placement := `
resource "apple_app_asset_library_image" "card" {
  app_id   = local.app_id
  file     = "` + filepath.Join(dir, "card.png") + `"
  category = "CREATIVE_ASSETS"
}

resource "apple_app_asset_library_placement" "card" {
  image_id                  = apple_app_asset_library_image.card.id
  app_event_localization_id = apple_app_event_localization.en_US.id
  placement_type            = "EVENT_CARD_ASSET"
  placement_group           = "DEFAULT_PROFILE"
}
`
	var eventID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + appEventConfig(`reference_name = "Summer Cup"`, appEventLocalizationConfig(`name = "A name that is far too long for an event"`)),
				ExpectError: regexp.MustCompile(`string length must be between 1 and 30`),
			},
			{
				// Unset options take App Store Connect's defaults.
				Config: locals + appEventConfig(`reference_name = "Summer Cup"`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &eventID),
					resource.TestCheckResourceAttr(address, "state", "DRAFT"),
					resource.TestCheckResourceAttr(address, "priority", "NORMAL"),
					resource.TestCheckResourceAttr(address, "purpose", "APPROPRIATE_FOR_ALL_USERS"),
					resource.TestCheckNoResourceAttr(address, "badge"),
					resource.TestCheckNoResourceAttr(address, "territory_schedules"),
					resource.TestCheckResourceAttr("data.apple_app_events.drafts", "app_events.#", "1"),
					resource.TestCheckResourceAttrPtr("data.apple_app_events.drafts", "app_events.0.id", &eventID),
				),
			},
			{
				// The event changes in place, and its media are placements on
				// a localization.
				Config:           locals + appEventConfig(full, text+placement),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &eventID),
					resource.TestCheckResourceAttr(address, "badge", "COMPETITION"),
					resource.TestCheckResourceAttr(address, "priority", "HIGH"),
					resource.TestCheckResourceAttr(address, "territory_schedules.#", "1"),
					resource.TestCheckResourceAttr(address, "territory_schedules.0.territories.#", "2"),
					resource.TestCheckResourceAttr(address, "territory_schedules.0.event_end", "2099-06-30T16:00:00Z"),
					resource.TestCheckResourceAttrPair("apple_app_asset_library_placement.card", "app_event_localization_id", "apple_app_event_localization.en_US", "id"),
				),
			},
			{
				ResourceName: address, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID + "/" + eventID, nil },
			},
			{ResourceName: "apple_app_event_localization.en_US", ImportState: true, ImportStateVerify: true},
			{
				ResourceName: "apple_app_event_localization.en_US", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return eventID + "/en-US", nil },
			},
			{
				ResourceName: "apple_app_event_localization.en_US", ImportState: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return eventID + "/fr-FR", nil },
				ExpectError:       regexp.MustCompile(`No resource has the ID or in-app event ID and locale`),
			},
			{
				// A change made outside Terraform is reverted.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.AppEventUpdateRequest
					body.Data.Type, body.Data.Id = gen.AppEventUpdateRequestDataTypeAppEvents, eventID
					asc.Alloc(&body.Data.Attributes).ReferenceName = new("Renamed")
					out, err := c.AppEventsUpdateInstanceWithResponse(ctx, eventID, body)
					return asc.Err(out, err)
				}),
				Config:           locals + appEventConfig(full, text+placement),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(address, "reference_name", "Summer Cup"),
			},
			{
				// Removed options keep their values; removed schedules and
				// localized text clear.
				Config: locals + appEventConfig(`reference_name = "Summer Cup"`, appEventLocalizationConfig(`name = "Summer Cup"`)+placement),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "badge", "COMPETITION"),
					resource.TestCheckNoResourceAttr(address, "territory_schedules"),
					resource.TestCheckNoResourceAttr("apple_app_event_localization.en_US", "short_description"),
					resource.TestCheckNoResourceAttr("apple_app_event_localization.en_US", "long_description"),
				),
			},
			{
				// An event has one localization per locale.
				Config: locals + appEventConfig(`reference_name = "Summer Cup"`, appEventLocalizationConfig(`name = "Summer Cup"`)+placement+`
resource "apple_app_event_localization" "again" {
  app_event_id = apple_app_event.summer.id
  locale       = "en-US"
}
`),
				ExpectError: regexp.MustCompile(`already has a localization\s+for\s+en-US`),
			},
			{
				// An event deleted outside Terraform is created again, with
				// its localization and placement.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppEventsDeleteInstanceWithResponse(ctx, eventID)
					return asc.Err(out, err)
				}),
				Config: locals + appEventConfig(`reference_name = "Summer Cup"`, appEventLocalizationConfig(`name = "Summer Cup"`)+placement),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction("apple_app_event_localization.en_US", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

func TestAccAppEventDraftLimit(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{{
			Config: locals + `
resource "apple_app_event" "many" {
  count          = 51
  app_id         = local.app_id
  reference_name = "Event ${count.index}"
}
`,
			ExpectError: regexp.MustCompile(`already has 50 draft in-app events`),
		}},
	})
}

func TestAccSubmitAppEventAction(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	actions := `
action "apple_submit_app_event" "summer" {
  config {
    app_id       = local.app_id
    app_event_id = apple_app_event.summer.id
  }
}
action "apple_submit_app_event" "prepared" {
  config {
    app_id       = local.app_id
    app_event_id = apple_app_event.summer.id
    platform     = "IOS"
    submit       = false
  }
}
`
	event := `reference_name = "Summer Cup"`
	text := appEventLocalizationConfig(`name = "Summer Cup"`)
	var eventID string
	env.actionCase(
		resource.TestStep{
			// An event without localizations can't be submitted.
			Config:      locals + actions + appEventConfig(event, "") + triggerAction("1", "apple_submit_app_event.summer"),
			ExpectError: regexp.MustCompile(`in-app event has no localizations`),
		},
		resource.TestStep{
			// Added without submitting, the event is ready for review.
			Config: locals + actions + appEventConfig(event, text) + triggerAction("2", "apple_submit_app_event.prepared"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_event.summer", "id", &eventID),
				env.checkAppEventState(&eventID, gen.AppEventAttributesEventStateREADYFORREVIEW),
			),
		},
		resource.TestStep{
			// An event already in a submission can't be submitted again.
			Config:      locals + actions + appEventConfig(event, text) + triggerAction("3", "apple_submit_app_event.summer"),
			ExpectError: regexp.MustCompile(`is READY_FOR_REVIEW, and\s+can't be submitted`),
		},
		resource.TestStep{
			// The state App Review sets is read back.
			PreConfig: func() { env.fake.ApproveAppEvent(eventID) },
			Config:    locals + actions + appEventConfig(event, text),
			Check:     resource.TestCheckResourceAttr("apple_app_event.summer", "state", "APPROVED"),
		},
	)
}

func TestAccSubmitAppEventForReview(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	config := locals + `
action "apple_submit_app_event" "summer" {
  config {
    app_id       = local.app_id
    app_event_id = apple_app_event.summer.id
  }
}
` + appEventConfig(`reference_name = "Summer Cup"`, appEventLocalizationConfig(`name = "Summer Cup"`))
	var eventID string
	env.actionCase(
		resource.TestStep{
			Config: config + triggerAction("1", "apple_submit_app_event.summer"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_event.summer", "id", &eventID),
				env.checkAppEventState(&eventID, gen.AppEventAttributesEventStateWAITINGFORREVIEW),
			),
		},
	)
}
