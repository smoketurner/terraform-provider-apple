package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// writeImage writes a PNG-signed file of more than one upload part and
// returns its path and checksum.
func writeImage(t *testing.T, dir, name, contents string) (string, string) {
	t.Helper()
	data := []byte("\x89PNG" + strings.Repeat(contents, 40))
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, asc.Checksum(data)
}

func (e *testEnv) checkImageGone(id *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		out, err := e.client.AppAssetLibraryImagesGetInstanceWithResponse(context.Background(), *id, nil)
		if err := asc.Err(out, err); !asc.IsNotFound(err) {
			return fmt.Errorf("image %s still exists (err %w)", *id, err)
		}
		return nil
	}
}

func libraryImageConfig(file, extra string) string {
	return `
resource "apple_app_asset_library_image" "home" {
  app_id   = local.app_id
  file     = "` + file + `"
  category = "APP_SCREENSHOTS_AND_PREVIEWS"
` + extra + `}
`
}

func TestAccLibraryImage(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	file, sum := writeImage(t, dir, "home.png", "home ")
	text, _ := writeMedia(t, dir, "home.txt", "not an image ")
	const address = "apple_app_asset_library_image.home"
	var first, second string
	env.run(resource.TestCase{
		CheckDestroy: env.checkImageGone(&second),
		Steps: []resource.TestStep{
			{
				Config:      locals + libraryImageConfig(filepath.Join(dir, "missing.png"), ""),
				ExpectError: regexp.MustCompile(`Cannot read asset library image`),
			},
			{
				Config:      locals + libraryImageConfig(text, ""),
				ExpectError: regexp.MustCompile(`(?s)Unable to upload asset library image.*IMAGE_INCORRECT_FORMAT`),
			},
			{
				Config: locals + libraryImageConfig(file, `  reference_name = "Home"`+"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "checksum", sum),
					resource.TestCheckResourceAttr(address, "reference_name", "Home"),
					resource.TestCheckResourceAttr(address, "category", "APP_SCREENSHOTS_AND_PREVIEWS"),
					resource.TestCheckResourceAttr(address, "state", "COMPLETE"),
					resource.TestCheckResourceAttr(address, "spec_id", "IMAGE_SPEC"),
					captureAttr(address, "id", &first),
				),
			},
			{
				ResourceName: address, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources[address].Primary
					return rs.Attributes["app_id"] + "/" + rs.ID, nil
				},
				ImportStateVerifyIgnore: []string{"file", "checksum"},
			},
			{
				Config:           locals + libraryImageConfig(file, `  reference_name = "Home screen"`+"\n"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "reference_name", "Home screen"),
					resource.TestCheckResourceAttrPtr(address, "id", &first),
				),
			},
			{
				Config:           locals + libraryImageConfig(file, ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckNoResourceAttr(address, "reference_name"),
			},
			{
				// New contents replace the image; the old one is deleted.
				PreConfig:        func() { file, sum = writeImage(t, dir, "home.png", "new home ") },
				Config:           locals + libraryImageConfig(file, ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "checksum", &sum),
					captureAttr(address, "id", &second),
					env.checkImageGone(&first),
				),
			},
			{
				// An image deleted outside Terraform is uploaded again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppAssetLibraryImagesDeleteInstanceWithResponse(ctx, second)
					return asc.Err(out, err)
				}),
				Config:           locals + libraryImageConfig(file, ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
				Check:            captureAttr(address, "id", &second),
			},
			{
				Config: locals + libraryImageConfig(file, "") + `
data "apple_app_asset_library" "app" {
  app_id     = local.app_id
  depends_on = [apple_app_asset_library_image.home]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.apple_app_asset_library.app", "id"),
					resource.TestCheckResourceAttr("data.apple_app_asset_library.app", "images.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs("data.apple_app_asset_library.app", "images.*",
						map[string]string{"state": "COMPLETE", "category": "APP_SCREENSHOTS_AND_PREVIEWS"}),
					resource.TestCheckTypeSetElemNestedAttrs("data.apple_app_asset_library.app", "images.*",
						map[string]string{"state": "FAILED"}),
					resource.TestCheckResourceAttr("data.apple_app_asset_library.app", "videos.#", "0"),
				),
			},
		},
	})
}

func libraryVideoConfig(file, extra string) string {
	return `
resource "apple_app_asset_library_video" "tour" {
  app_id         = local.app_id
  file           = "` + file + `"
  category       = "APP_SCREENSHOTS_AND_PREVIEWS"
  reference_name = "Tour"
` + extra + `}
`
}

func TestAccLibraryVideo(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	file, sum := writeMedia(t, dir, "tour.mov", "tour ")
	const address = "apple_app_asset_library_video.tour"
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + libraryVideoConfig(file, `  preview_frame_time_code = "00:00:05:00"`+"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "checksum", sum),
					resource.TestCheckResourceAttr(address, "preview_frame_time_code", "00:00:05:00"),
					resource.TestCheckResourceAttr(address, "state", "COMPLETE"),
					resource.TestCheckResourceAttr(address, "spec_id", "VIDEO_SPEC"),
					captureAttr(address, "id", &id),
				),
			},
			{
				ResourceName: address, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources[address].Primary
					return rs.Attributes["app_id"] + "/" + rs.ID, nil
				},
				ImportStateVerifyIgnore: []string{"file", "checksum"},
			},
			{
				Config:           locals + libraryVideoConfig(file, `  preview_frame_time_code = "00:00:02:00"`+"\n"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "preview_frame_time_code", "00:00:02:00"),
					resource.TestCheckResourceAttrPtr(address, "id", &id),
				),
			},
			{
				Config:           locals + libraryVideoConfig(file, ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckNoResourceAttr(address, "preview_frame_time_code"),
			},
			{
				Config: locals + libraryVideoConfig(file, "") + `
data "apple_app_asset_library" "app" {
  app_id     = local.app_id
  depends_on = [apple_app_asset_library_video.tour]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_app_asset_library.app", "videos.#", "1"),
					resource.TestCheckResourceAttrPtr("data.apple_app_asset_library.app", "videos.0.id", &id),
					resource.TestCheckResourceAttr("data.apple_app_asset_library.app", "videos.0.reference_name", "Tour"),
				),
			},
		},
	})
}

func TestAccLibraryAssetErrors(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	file, _ := writeImage(t, dir, "home.png", "home ")
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + `
resource "apple_app_asset_library_image" "bad" {
  app_id   = "missing"
  file     = "` + file + `"
  category = "CREATIVE_ASSETS"
}
`,
				ExpectError: regexp.MustCompile(`Unable to upload asset library image`),
			},
			{
				PreConfig:   func() { env.fake.FailNext("PUT", "/upload", 403) },
				Config:      locals + libraryImageConfig(file, ""),
				ExpectError: regexp.MustCompile(`Unable to upload asset library image`),
			},
			{
				Config: locals + libraryImageConfig(file, ""),
				Check:  captureAttr("apple_app_asset_library_image.home", "id", &id),
			},
			{
				PreConfig:   func() { env.fake.FailNext("PATCH", "/v1/appAssetLibraryImages/"+id, 409) },
				Config:      locals + libraryImageConfig(file, `  reference_name = "Home"`+"\n"),
				ExpectError: regexp.MustCompile(`Unable to update asset library image`),
			},
			{
				PreConfig:   func() { env.fake.FailNext("GET", "/v1/appAssetLibraryImages/"+id, 403) },
				Config:      locals + libraryImageConfig(file, ""),
				ExpectError: regexp.MustCompile(`Unable to read asset library image`),
			},
			{
				PreConfig:   func() { env.fake.FailNext("DELETE", "/v1/appAssetLibraryImages/"+id, 409) },
				Config:      locals,
				ExpectError: regexp.MustCompile(`Unable to delete asset library image`),
			},
			{
				Config: locals + `
data "apple_app_asset_library" "missing" {
  app_id = "missing"
}
`,
				ExpectError: regexp.MustCompile(`Unable to read asset library`),
			},
		},
	})
}

// checkPlacementOrder checks the display order of the iPhone placements of
// the en-US version localization.
func (e *testEnv) checkPlacementOrder(want ...*string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		locID := s.RootModule().Resources["apple_app_store_version_localization.en_US"].Primary.ID
		ids, err := orderedPlacements(context.Background(), e.client, versionLocalizationAttr, locID, iphoneGroup)
		if err != nil {
			return err
		}
		wantIDs := make([]string, len(want))
		for i, w := range want {
			wantIDs[i] = *w
		}
		if !slices.Equal(ids, wantIDs) {
			return fmt.Errorf("placement order = %v, want %v", ids, wantIDs)
		}
		return nil
	}
}

const iphoneGroup = "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"

func placementConfig(dir, order string) string {
	return versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id = apple_app_store_version.next.id
  locale     = "en-US"
}

resource "apple_app_asset_library_image" "home" {
  app_id   = local.app_id
  file     = "` + filepath.Join(dir, "home.png") + `"
  category = "APP_SCREENSHOTS_AND_PREVIEWS"
}

resource "apple_app_asset_library_image" "share" {
  app_id   = local.app_id
  file     = "` + filepath.Join(dir, "share.png") + `"
  category = "APP_SCREENSHOTS_AND_PREVIEWS"
}

resource "apple_app_asset_library_video" "tour" {
  app_id   = local.app_id
  file     = "` + filepath.Join(dir, "tour.mov") + `"
  category = "APP_SCREENSHOTS_AND_PREVIEWS"
}

resource "apple_app_asset_library_placement" "home" {
  image_id                          = apple_app_asset_library_image.home.id
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_type                    = "APP_SCREENSHOT"
  placement_group                   = "` + iphoneGroup + `"
}

resource "apple_app_asset_library_placement" "share" {
  image_id                          = apple_app_asset_library_image.share.id
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_type                    = "APP_SCREENSHOT"
  placement_group                   = "` + iphoneGroup + `"
}

resource "apple_app_asset_library_placement" "tour" {
  video_id                          = apple_app_asset_library_video.tour.id
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_type                    = "APP_PREVIEW"
  placement_group                   = "` + iphoneGroup + `"
}
` + order
}

func orderConfig(ids ...string) string {
	return `
resource "apple_app_asset_library_placement_order" "iphone" {
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_group                   = "` + iphoneGroup + `"
  placement_ids                     = [` + strings.Join(ids, ", ") + `]
}
`
}

func TestAccPlacement(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	writeImage(t, dir, "home.png", "home ")
	writeImage(t, dir, "share.png", "share ")
	writeMedia(t, dir, "tour.mov", "tour ")
	const home, share, tour = "apple_app_asset_library_placement.home.id", "apple_app_asset_library_placement.share.id", "apple_app_asset_library_placement.tour.id"
	var homeID, shareID, tourID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + placementConfig(dir, orderConfig(share, tour, home)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("apple_app_asset_library_placement.home", "image_id", "apple_app_asset_library_image.home", "id"),
					resource.TestCheckNoResourceAttr("apple_app_asset_library_placement.home", "video_id"),
					resource.TestCheckResourceAttrPair("apple_app_asset_library_placement.tour", "video_id", "apple_app_asset_library_video.tour", "id"),
					resource.TestCheckResourceAttr("apple_app_asset_library_placement.home", "state", "PARENT_PREPARE_FOR_SUBMISSION"),
					captureAttr("apple_app_asset_library_placement.home", "id", &homeID),
					captureAttr("apple_app_asset_library_placement.share", "id", &shareID),
					captureAttr("apple_app_asset_library_placement.tour", "id", &tourID),
					env.checkPlacementOrder(&shareID, &tourID, &homeID),
				),
			},
			{ResourceName: "apple_app_asset_library_placement.tour", ImportState: true, ImportStateVerify: true},
			{ResourceName: "apple_app_asset_library_placement_order.iphone", ImportState: true, ImportStateVerify: true},
			{
				Config:           locals + placementConfig(dir, orderConfig(home, share, tour)),
				ConfigPlanChecks: expectAction("apple_app_asset_library_placement_order.iphone", plancheck.ResourceActionUpdate),
				Check:            env.checkPlacementOrder(&homeID, &shareID, &tourID),
			},
			{
				// Reordered outside Terraform, the order is set again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.AppAssetLibraryPlacementOrderingRequestCreateRequest
					body.Data.Type = gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataTypeAppAssetLibraryPlacementOrderingRequests
					asc.Alloc(&body.Data.Attributes).PlacementGroup = new(iphoneGroup)
					rel := asc.Alloc(&body.Data.Relationships)
					for _, id := range []string{tourID, shareID, homeID} {
						d := asc.Push(asc.Alloc(&asc.Alloc(&rel.OrderedPlacements).Data))
						d.Id, d.Type = id, gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataRelationshipsOrderedPlacementsDataTypeAppAssetLibraryPlacements
					}
					locID, err := env.placementLocalization(ctx, homeID)
					if err != nil {
						return err
					}
					d := asc.Alloc(&asc.Alloc(&rel.AppStoreVersionLocalization).Data)
					d.Id, d.Type = locID, gen.AppAssetLibraryPlacementOrderingRequestCreateRequestDataRelationshipsAppStoreVersionLocalizationDataTypeAppStoreVersionLocalizations
					out, err := c.AppAssetLibraryPlacementOrderingRequestsCreateInstanceWithResponse(ctx, nil, body)
					return asc.Err(out, err)
				}),
				Config:           locals + placementConfig(dir, orderConfig(home, share, tour)),
				ConfigPlanChecks: expectAction("apple_app_asset_library_placement_order.iphone", plancheck.ResourceActionUpdate),
				Check:            env.checkPlacementOrder(&homeID, &shareID, &tourID),
			},
			{
				// The order must list every placement of the group.
				Config:      locals + placementConfig(dir, orderConfig(home, share)),
				ExpectError: regexp.MustCompile(`(?s)Unable to order placements.*each placement`),
			},
			{
				// A new image replaces its placements, which the order lists.
				PreConfig: func() { writeImage(t, dir, "home.png", "new home ") },
				Config:    locals + placementConfig(dir, orderConfig(home, share, tour)),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_asset_library_placement.home", "id", &homeID),
					env.checkPlacementOrder(&homeID, &shareID, &tourID),
				),
			},
			{
				Config: locals + placementConfig(dir, orderConfig(home, share, tour)) + `
data "apple_app_asset_library_placements" "share" {
  image_id   = apple_app_asset_library_image.share.id
  depends_on = [apple_app_asset_library_placement.share]
}

data "apple_app_asset_library_placements" "tour" {
  video_id   = apple_app_asset_library_video.tour.id
  depends_on = [apple_app_asset_library_placement.tour]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_app_asset_library_placements.share", "placements.#", "1"),
					resource.TestCheckResourceAttrPtr("data.apple_app_asset_library_placements.share", "placements.0.id", &shareID),
					resource.TestCheckResourceAttr("data.apple_app_asset_library_placements.share", "placements.0.placement_type", "APP_SCREENSHOT"),
					resource.TestCheckResourceAttr("data.apple_app_asset_library_placements.share", "placements.0.placement_group", iphoneGroup),
					resource.TestCheckResourceAttr("data.apple_app_asset_library_placements.share", "placements.0.state", "PARENT_PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttrPtr("data.apple_app_asset_library_placements.tour", "placements.0.id", &tourID),
				),
			},
			{
				// A placement deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppAssetLibraryPlacementsDeleteInstanceWithResponse(ctx, tourID)
					return asc.Err(out, err)
				}),
				Config:           locals + placementConfig(dir, ""),
				ConfigPlanChecks: expectAction("apple_app_asset_library_placement.tour", plancheck.ResourceActionCreate),
			},
		},
	})
}

func (e *testEnv) placementLocalization(ctx context.Context, placementID string) (string, error) {
	out, err := e.client.AppAssetLibraryPlacementsGetInstanceWithResponse(ctx, placementID, nil)
	if err := asc.Err(out, err); err != nil {
		return "", err
	}
	rel, err := out.JSON200.Data.Relationships.AsAppAssetLibraryPlacementImageRelationships()
	if err != nil {
		return "", err
	}
	return rel.AppStoreVersionLocalization.Data.Id, nil
}

// TestAccPlacementTargets places assets on the localizations the fake keeps
// as bare IDs.
func TestAccPlacementTargets(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	card, _ := writeImage(t, dir, "card.png", "card ")
	event := env.fake.AddPlacementTarget("appEventLocalization")
	page := env.fake.AddPlacementTarget("appCustomProductPageLocalization")
	treatment := env.fake.AddPlacementTarget("appStoreVersionExperimentTreatmentLocalization")
	var cardID string
	cfg := locals + `
resource "apple_app_asset_library_image" "card" {
  app_id   = local.app_id
  file     = "` + card + `"
  category = "CREATIVE_ASSETS"
}

resource "apple_app_asset_library_placement" "event" {
  image_id                  = apple_app_asset_library_image.card.id
  app_event_localization_id = "` + event + `"
  placement_type            = "EVENT_CARD_ASSET"
  placement_group           = "DEFAULT_PROFILE"
}

resource "apple_app_asset_library_placement" "page" {
  image_id                            = apple_app_asset_library_image.card.id
  custom_product_page_localization_id = "` + page + `"
  placement_type                      = "PRODUCT_PAGE_HEADER_ASSET"
  placement_group                     = "DEFAULT_PROFILE"
}

resource "apple_app_asset_library_placement" "treatment" {
  image_id                             = apple_app_asset_library_image.card.id
  experiment_treatment_localization_id = "` + treatment + `"
  placement_type                       = "PRODUCT_PAGE_HEADER_ASSET"
  placement_group                      = "DEFAULT_PROFILE"
}

resource "apple_app_asset_library_placement_order" "page" {
  custom_product_page_localization_id = "` + page + `"
  placement_group                     = "DEFAULT_PROFILE"
  placement_ids                       = [apple_app_asset_library_placement.page.id]
}

resource "apple_app_asset_library_placement_order" "treatment" {
  experiment_treatment_localization_id = "` + treatment + `"
  placement_group                      = "DEFAULT_PROFILE"
  placement_ids                        = [apple_app_asset_library_placement.treatment.id]
}
`
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_asset_library_placement.event", "app_event_localization_id", event),
					resource.TestCheckNoResourceAttr("apple_app_asset_library_placement.event", "app_store_version_localization_id"),
					resource.TestCheckResourceAttr("apple_app_asset_library_placement.page", "custom_product_page_localization_id", page),
					resource.TestCheckResourceAttr("apple_app_asset_library_placement.treatment", "experiment_treatment_localization_id", treatment),
					resource.TestCheckResourceAttr("apple_app_asset_library_placement_order.page", "id", page+"/DEFAULT_PROFILE"),
					captureAttr("apple_app_asset_library_image.card", "id", &cardID),
				),
			},
			{ResourceName: "apple_app_asset_library_placement.event", ImportState: true, ImportStateVerify: true},
			{ResourceName: "apple_app_asset_library_placement.page", ImportState: true, ImportStateVerify: true},
			{ResourceName: "apple_app_asset_library_placement_order.page", ImportState: true, ImportStateVerify: true},
			{ResourceName: "apple_app_asset_library_placement_order.treatment", ImportState: true, ImportStateVerify: true},
			{
				// With its group emptied outside Terraform, the order is gone
				// and is set again once the placement is back.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					placements, err := c.AppAssetLibraryImagesPlacementsGetToManyRelatedWithResponse(ctx, cardID, nil)
					if err := asc.Err(placements, err); err != nil {
						return err
					}
					for _, p := range placements.JSON200.Data {
						if asc.Value(p.Attributes.PlacementType) == gen.AppAssetLibraryPlacementTypePRODUCTPAGEHEADERASSET {
							out, err := c.AppAssetLibraryPlacementsDeleteInstanceWithResponse(ctx, p.Id)
							if err := asc.Err(out, err); err != nil {
								return err
							}
						}
					}
					return nil
				}),
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("apple_app_asset_library_placement_order.page", plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction("apple_app_asset_library_placement_order.treatment", plancheck.ResourceActionCreate),
				}},
			},
			{
				ResourceName: "apple_app_asset_library_placement_order.page", ImportState: true, ImportStateId: "missing/DEFAULT_PROFILE",
				ExpectError: regexp.MustCompile(`No localization has the ID missing`),
			},
			{
				ResourceName: "apple_app_asset_library_placement_order.page", ImportState: true, ImportStateId: "no-group",
				ExpectError: regexp.MustCompile(`Invalid import ID`),
			},
		},
	})
}

func TestAccPlacementErrors(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	card, _ := writeImage(t, dir, "card.png", "card ")
	image := locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id = apple_app_store_version.next.id
  locale     = "en-US"
}

resource "apple_app_asset_library_image" "card" {
  app_id   = local.app_id
  file     = "` + card + `"
  category = "CREATIVE_ASSETS"
}
`
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: image + `
resource "apple_app_asset_library_placement" "both" {
  image_id                          = apple_app_asset_library_image.card.id
  video_id                          = apple_app_asset_library_image.card.id
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_type                    = "APP_SCREENSHOT"
  placement_group                   = "` + iphoneGroup + `"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{
				Config: image + `
resource "apple_app_asset_library_placement" "event" {
  image_id                          = apple_app_asset_library_image.card.id
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_type                    = "EVENT_CARD_ASSET"
  placement_group                   = "DEFAULT_PROFILE"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Unable to create placement.*event`),
			},
			{
				Config: image + `
data "apple_app_asset_library_placements" "missing" {
  image_id = "missing"
}
`,
				ExpectError: regexp.MustCompile(`Unable to list placements`),
			},
			{
				Config: image + `
resource "apple_app_asset_library_placement_order" "missing" {
  app_store_version_localization_id = "missing"
  placement_group                   = "DEFAULT_PROFILE"
  placement_ids                     = ["missing"]
}
`,
				ExpectError: regexp.MustCompile(`Unable to order placements`),
			},
		},
	})
}

func TestAccReferenceData(t *testing.T) {
	env := newTestEnv(t)
	const address = "data.apple_app_asset_library_reference_data.all"
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: `data "apple_app_asset_library_reference_data" "all" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "placement_groups.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs(address, "placement_groups.*", map[string]string{
						"id": iphoneGroup, "platform": "IPHONE_APP_STORE", "display_class": "IPHONE_DYNAMIC_ISLAND_LARGE_DISPLAY",
					}),
					resource.TestCheckResourceAttr(address, "image_specs.0.spec_id", "IMAGE_SPEC"),
					resource.TestCheckResourceAttr(address, "image_specs.0.min_width", "1290"),
					resource.TestCheckResourceAttr(address, "image_specs.0.max_height", "2868"),
					resource.TestCheckResourceAttr(address, "image_specs.0.max_file_size", "10000000"),
					resource.TestCheckTypeSetElemAttr(address, "image_specs.0.file_extensions.*", "png"),
					resource.TestCheckTypeSetElemAttr(address, "image_specs.0.compatible_placement_types.*", "APP_SCREENSHOT"),
					resource.TestCheckResourceAttr(address, "video_specs.0.spec_id", "VIDEO_SPEC"),
					resource.TestCheckResourceAttr(address, "placement_policies.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs(address, "placement_policies.*", map[string]string{
						"feature": "APP_STORE_VERSIONS", "placement_type": "APP_PREVIEW", "max_count": "3",
					}),
				),
			},
			{
				PreConfig:   func() { env.fake.FailNext("GET", "/v1/appAssetLibraryRefData", 403) },
				Config:      `data "apple_app_asset_library_reference_data" "all" {}`,
				ExpectError: regexp.MustCompile(`Unable to read asset library reference data`),
			},
		},
	})
}
