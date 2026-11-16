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

func experimentConfig(experiment, rest string) string {
	return `
resource "apple_app_store_version_experiment" "icons" {
  app_id   = local.app_id
  platform = "IOS"
` + experiment + `
}

data "apple_app_store_version_experiments" "drafts" {
  app_id     = local.app_id
  states     = ["PREPARE_FOR_SUBMISSION"]
  depends_on = [apple_app_store_version_experiment.icons]
}
` + rest
}

func treatmentConfig(name, extra string) string {
	return `
resource "apple_app_store_version_experiment_treatment" "` + name + `" {
  experiment_id = apple_app_store_version_experiment.icons.id
  name          = "` + name + `"
` + extra + `
}
`
}

const treatmentLocalizationConfig = `
resource "apple_app_store_version_experiment_treatment_localization" "en_US" {
  treatment_id = apple_app_store_version_experiment_treatment.orange.id
  locale       = "en-US"
}
`

func (e *testEnv) checkExperimentState(id *string, want gen.AppStoreVersionExperimentV2AttributesState) resource.TestCheckFunc {
	return func(*terraform.State) error {
		out, err := e.client.AppStoreVersionExperimentsV2GetInstanceWithResponse(context.Background(), *id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		if got := asc.Value(asc.Value(out.JSON200.Data.Attributes).State); got != want {
			return fmt.Errorf("experiment is %s, want %s", got, want)
		}
		return nil
	}
}

func TestAccExperiment(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	writeImage(t, dir, "orange.png", "orange ")
	const address = "apple_app_store_version_experiment.icons"
	const orange = "apple_app_store_version_experiment_treatment.orange"
	const english = "apple_app_store_version_experiment_treatment_localization.en_US"
	experiment := `name = "Icon colors"
  traffic_proportion = 40`
	media := treatmentLocalizationConfig + `
resource "apple_app_asset_library_image" "orange" {
  app_id   = local.app_id
  file     = "` + filepath.Join(dir, "orange.png") + `"
  category = "APP_SCREENSHOTS_AND_PREVIEWS"
}

resource "apple_app_asset_library_placement" "orange" {
  image_id                             = apple_app_asset_library_image.orange.id
  experiment_treatment_localization_id = apple_app_store_version_experiment_treatment_localization.en_US.id
  placement_type                       = "APP_SCREENSHOT"
  placement_group                      = "` + iphoneGroup + `"
}
`
	treatments := treatmentConfig("orange", `app_icon_name = "OrangeIcon"`) + treatmentConfig("robots", "")
	var experimentID, treatmentID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + experimentConfig(`name = "Icon colors"`+"\n  traffic_proportion = 0", ""),
				ExpectError: regexp.MustCompile(`traffic_proportion`),
			},
			{
				Config: locals + experimentConfig(experiment, treatments+media),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &experimentID),
					captureAttr(orange, "id", &treatmentID),
					resource.TestCheckResourceAttr(address, "state", "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckNoResourceAttr(address, "start_date"),
					resource.TestCheckResourceAttr(orange, "app_icon_name", "OrangeIcon"),
					resource.TestCheckResourceAttrPair("apple_app_asset_library_placement.orange", "experiment_treatment_localization_id", english, "id"),
					resource.TestCheckResourceAttr("data.apple_app_store_version_experiments.drafts", "experiments.#", "1"),
					resource.TestCheckResourceAttr("data.apple_app_store_version_experiments.drafts", "experiments.0.traffic_proportion", "40"),
				),
			},
			{
				// A treatment localization makes the experiment need review.
				Config: locals + experimentConfig(experiment, treatments+media),
				Check:  resource.TestCheckResourceAttr(address, "review_required", "true"),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{ResourceName: orange, ImportState: true, ImportStateVerify: true},
			{ResourceName: english, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: english, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return treatmentID + "/en-US", nil },
			},
			{
				ResourceName: english, ImportState: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return treatmentID + "/fr-FR", nil },
				ExpectError:       regexp.MustCompile(`No resource has the ID or treatment ID and locale`),
			},
			{
				// An app has one experiment at a time per platform.
				Config: locals + experimentConfig(experiment, treatments+media) + `
resource "apple_app_store_version_experiment" "second" {
  app_id             = local.app_id
  platform           = "IOS"
  name               = "Second"
  traffic_proportion = 10
}
`,
				ExpectError: regexp.MustCompile(`another experiment is\s+in\s+draft\s+state`),
			},
			{
				// An experiment has up to three treatments.
				Config:      locals + experimentConfig(experiment, treatments+media+treatmentConfig("brain", "")+treatmentConfig("extra", "")),
				ExpectError: regexp.MustCompile(`already has 3 treatments`),
			},
			{
				// The experiment and its treatments change in place, and a
				// removed icon name clears.
				Config: locals + experimentConfig(`name = "Icon styles"
  traffic_proportion = 60`, treatmentConfig("orange", "")+treatmentConfig("robots", "")+media),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(orange, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "name", "Icon styles"),
					resource.TestCheckResourceAttr(address, "traffic_proportion", "60"),
					resource.TestCheckNoResourceAttr(orange, "app_icon_name"),
					resource.TestCheckResourceAttrPtr(orange, "id", &treatmentID),
				),
			},
			{
				// A treatment deleted outside Terraform is created again, with
				// its localization and placement.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppStoreVersionExperimentTreatmentsDeleteInstanceWithResponse(ctx, treatmentID)
					return asc.Err(out, err)
				}),
				Config: locals + experimentConfig(`name = "Icon styles"
  traffic_proportion = 60`, treatmentConfig("orange", "")+treatmentConfig("robots", "")+media),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(orange, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(english, plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

const experimentActions = `
action "apple_start_app_store_version_experiment" "icons" {
  config {
    experiment_id = apple_app_store_version_experiment.icons.id
  }
}
action "apple_stop_app_store_version_experiment" "icons" {
  config {
    experiment_id = apple_app_store_version_experiment.icons.id
  }
}
action "apple_submit_app_store_version_experiment" "icons" {
  config {
    experiment_id = apple_app_store_version_experiment.icons.id
  }
}
action "apple_submit_app_store_version_experiment" "prepared" {
  config {
    experiment_id = apple_app_store_version_experiment.icons.id
    submit        = false
  }
}
`

const promoteAction = `
action "apple_promote_app_store_version_experiment_treatment" "orange" {
  config {
    version_id   = apple_app_store_version.next.id
    treatment_id = apple_app_store_version_experiment_treatment.orange.id
  }
}
`

// An experiment without metadata of its own starts without review, can't
// change once started, and ends when a treatment is applied.
func TestAccStartExperimentAction(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	experiment := `name = "Icon colors"
  traffic_proportion = 40`
	base := locals + experimentActions + versionConfig("IOS", "1.0.0", "")
	withTreatment := promoteAction + experimentConfig(experiment, treatmentConfig("orange", `app_icon_name = "OrangeIcon"`))
	var experimentID string
	env.actionCase(
		resource.TestStep{
			// An experiment without treatments can't start.
			Config:      base + experimentConfig(experiment, "") + triggerAction("1", "apple_start_app_store_version_experiment.icons"),
			ExpectError: regexp.MustCompile(`Only an experiment with treatments`),
		},
		resource.TestStep{
			// Promoting a treatment needs a started experiment.
			Config:      base + withTreatment + triggerAction("2", "apple_promote_app_store_version_experiment_treatment.orange"),
			ExpectError: regexp.MustCompile(`experiment that has started`),
		},
		// The treatment exists before the experiment starts: the trigger
		// doesn't depend on it.
		resource.TestStep{Config: base + withTreatment},
		resource.TestStep{
			Config: base + withTreatment + triggerAction("3", "apple_start_app_store_version_experiment.icons"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_store_version_experiment.icons", "id", &experimentID),
				env.checkExperimentState(&experimentID, gen.AppStoreVersionExperimentV2AttributesStateAPPROVED),
			),
		},
		resource.TestStep{
			Config:      base + promoteAction + experimentConfig(`name = "Renamed"`+"\n  traffic_proportion = 40", treatmentConfig("orange", `app_icon_name = "OrangeIcon"`)),
			ExpectError: regexp.MustCompile(`can't change once it has\s+started`),
		},
		resource.TestStep{
			Config: base + withTreatment + triggerAction("4", "apple_promote_app_store_version_experiment_treatment.orange"),
			Check:  env.checkExperimentState(&experimentID, gen.AppStoreVersionExperimentV2AttributesStateCOMPLETED),
		},
		resource.TestStep{
			// The dates and the promotion are read back.
			Config: base + withTreatment,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("apple_app_store_version_experiment.icons", "state", "COMPLETED"),
				resource.TestCheckResourceAttrSet("apple_app_store_version_experiment.icons", "start_date"),
				resource.TestCheckResourceAttrSet("apple_app_store_version_experiment.icons", "end_date"),
				resource.TestCheckResourceAttrSet("apple_app_store_version_experiment_treatment.orange", "promoted_date"),
			),
		},
	)
}

// An experiment with treatment metadata is reviewed before it starts.
func TestAccSubmitExperimentAction(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	experiment := `name = "Screenshots"
  traffic_proportion = 40`
	base := locals + experimentActions + versionConfig("IOS", "1.0.0", "")
	reviewed := experimentConfig(experiment, treatmentConfig("orange", "")+treatmentLocalizationConfig)
	var experimentID string
	env.actionCase(
		resource.TestStep{
			// An experiment without treatments can't be submitted.
			Config:      base + experimentConfig(experiment, "") + triggerAction("1", "apple_submit_app_store_version_experiment.icons"),
			ExpectError: regexp.MustCompile(`experiment has no treatments`),
		},
		resource.TestStep{Config: base + reviewed},
		resource.TestStep{
			// It can't start before review.
			Config:      base + reviewed + triggerAction("2", "apple_start_app_store_version_experiment.icons"),
			ExpectError: regexp.MustCompile(`reviewed if it needs review`),
		},
		resource.TestStep{
			Config: base + reviewed + triggerAction("3", "apple_submit_app_store_version_experiment.prepared"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_store_version_experiment.icons", "id", &experimentID),
				env.checkExperimentState(&experimentID, gen.AppStoreVersionExperimentV2AttributesStateREADYFORREVIEW),
			),
		},
		resource.TestStep{
			// An experiment already in a submission can't be submitted again.
			Config:      base + reviewed + triggerAction("4", "apple_submit_app_store_version_experiment.icons"),
			ExpectError: regexp.MustCompile(`is\s+READY_FOR_REVIEW, and\s+can't\s+be\s+submitted`),
		},
		resource.TestStep{
			// A running experiment can't be stopped twice.
			PreConfig: func() { env.fake.AcceptExperiment(experimentID) },
			Config:    base + reviewed + triggerAction("5", "apple_start_app_store_version_experiment.icons"),
			Check:     env.checkExperimentState(&experimentID, gen.AppStoreVersionExperimentV2AttributesStateAPPROVED),
		},
		resource.TestStep{
			Config: base + reviewed + triggerAction("6", "apple_stop_app_store_version_experiment.icons"),
			Check:  env.checkExperimentState(&experimentID, gen.AppStoreVersionExperimentV2AttributesStateSTOPPED),
		},
		resource.TestStep{
			Config:      base + reviewed + triggerAction("7", "apple_stop_app_store_version_experiment.icons"),
			ExpectError: regexp.MustCompile(`Only a running experiment can stop`),
		},
	)
}

func TestAccSubmitExperimentForReview(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	config := locals + experimentActions + versionConfig("IOS", "1.0.0", "") + experimentConfig(`name = "Screenshots"
  traffic_proportion = 40`, treatmentConfig("orange", "")+treatmentLocalizationConfig)
	var experimentID string
	env.actionCase(
		resource.TestStep{Config: config},
		resource.TestStep{
			Config: config + triggerAction("1", "apple_submit_app_store_version_experiment.icons"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_store_version_experiment.icons", "id", &experimentID),
				env.checkExperimentState(&experimentID, gen.AppStoreVersionExperimentV2AttributesStateWAITINGFORREVIEW),
			),
		},
	)
}
