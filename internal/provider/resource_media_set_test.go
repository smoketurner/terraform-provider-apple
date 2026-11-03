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

func TestMediaTypes(t *testing.T) {
	for _, v := range screenshotDisplayTypes {
		if !gen.ScreenshotDisplayType(v).Valid() {
			t.Errorf("%s is not a ScreenshotDisplayType", v)
		}
	}
	for _, v := range previewTypes {
		if !gen.PreviewType(v).Valid() {
			t.Errorf("%s is not a PreviewType", v)
		}
	}
	if mediaFailure("COMPLETE", nil) != "" || mediaFailure("FAILED", nil) != "processing failed" {
		t.Error("mediaFailure without errors")
	}
	errs := []gen.AppMediaStateError{{Code: new("IMAGE_TOO_SMALL"), Description: new("too small")}}
	if got := mediaFailure("FAILED", &errs); got != "IMAGE_TOO_SMALL: too small" {
		t.Errorf("mediaFailure = %q", got)
	}
}

// writeMedia writes a file of more than one upload part and returns its
// path and checksum.
func writeMedia(t *testing.T, dir, name, contents string) (string, string) {
	t.Helper()
	data := []byte(strings.Repeat(contents, 40))
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, asc.Checksum(data)
}

func screenshotSetConfig(displayType string, files ...string) string {
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = fmt.Sprintf("%q", f)
	}
	return versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id = apple_app_store_version.next.id
  locale     = "en-US"
}

resource "apple_app_screenshot_set" "iphone" {
  localization_id = apple_app_store_version_localization.en_US.id
  display_type    = "` + displayType + `"
  files           = [` + strings.Join(quoted, ", ") + `]
}
`
}

func TestAccScreenshotSet(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	dir := t.TempDir()
	a, sumA := writeMedia(t, dir, "1-home.png", "home ")
	b, sumB := writeMedia(t, dir, "2-editor.png", "editor ")
	c, sumC := writeMedia(t, dir, "3-share.png", "share ")
	var setID string
	ids := map[string]string{}
	files := func(want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := env.fake.MediaFiles(setID); !slices.Equal(got, want) {
				return fmt.Errorf("set files = %v, want %v", got, want)
			}
			return nil
		}
	}
	// keep records file IDs by checksum, and fails if a file was uploaded
	// again although its contents didn't change.
	keep := func(sums ...string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			attrs := s.RootModule().Resources["apple_app_screenshot_set.iphone"].Primary.Attributes
			for i, sum := range strings.Split(attrs["checksums.0"]+","+attrs["checksums.1"]+","+attrs["checksums.2"], ",") {
				id := attrs[fmt.Sprintf("file_ids.%d", i)]
				if prev, ok := ids[sum]; ok && slices.Contains(sums, sum) && prev != id {
					return fmt.Errorf("file %s was uploaded again", sum)
				}
				ids[sum] = id
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + screenshotSetConfig("APP_IPHONE_99", a),
				ExpectError: regexp.MustCompile(`display_type value must be one of`),
			},
			{
				Config:      locals + screenshotSetConfig("APP_IPHONE_67", slices.Repeat([]string{a}, 11)...),
				ExpectError: regexp.MustCompile(`list must contain at least 1 elements and at most 10`),
			},
			{
				Config:      locals + screenshotSetConfig("APP_IPHONE_67", filepath.Join(dir, "missing.png")),
				ExpectError: regexp.MustCompile(`Cannot read screenshot`),
			},
			{
				Config: locals + screenshotSetConfig("APP_IPHONE_67", a, b, c),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_screenshot_set.iphone", "id", &setID),
					resource.TestCheckResourceAttr("apple_app_screenshot_set.iphone", "checksums.1", sumB),
					resource.TestCheckResourceAttr("apple_app_screenshot_set.iphone", "file_ids.#", "3"),
					files(sumA, sumB, sumC),
					keep(),
				),
			},
			{
				ResourceName: "apple_app_screenshot_set.iphone", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["apple_app_store_version_localization.en_US"].Primary.ID + "/APP_IPHONE_67", nil
				},
				ImportStateVerifyIdentifierAttribute: "display_type",
				// The API doesn't return the configured paths.
				ImportStateVerifyIgnore: []string{"files"},
			},
			{
				// Reordering keeps the uploaded files.
				Config:           locals + screenshotSetConfig("APP_IPHONE_67", c, a, b),
				ConfigPlanChecks: expectAction("apple_app_screenshot_set.iphone", plancheck.ResourceActionUpdate),
				Check:            resource.ComposeAggregateTestCheckFunc(files(sumC, sumA, sumB), keep(sumA, sumB, sumC)),
			},
			{
				// Changing a file's contents uploads it again, and only it.
				PreConfig:        func() { b, sumB = writeMedia(t, dir, "2-editor.png", "new editor ") },
				Config:           locals + screenshotSetConfig("APP_IPHONE_67", c, a, b),
				ConfigPlanChecks: expectAction("apple_app_screenshot_set.iphone", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error { return files(sumC, sumA, sumB)(s) },
					keep(sumA, sumC),
				),
			},
			{
				// A screenshot deleted outside Terraform is uploaded again.
				PreConfig: env.outside(func(ctx context.Context, cl *asc.Client) error {
					out, err := cl.AppScreenshotsDeleteInstanceWithResponse(ctx, ids[sumA])
					return asc.Err(out, err)
				}),
				Config:           locals + screenshotSetConfig("APP_IPHONE_67", c, a),
				ConfigPlanChecks: expectAction("apple_app_screenshot_set.iphone", plancheck.ResourceActionUpdate),
				Check:            files(sumC, sumA),
			},
			{
				// A rejected upload fails the apply; the next apply cleans up
				// and uploads the file again.
				PreConfig:   func() { env.fake.FailNext("PUT", "/upload", 403) },
				Config:      locals + screenshotSetConfig("APP_IPHONE_67", c, a, b),
				ExpectError: regexp.MustCompile(`(?s)uploading.*2-editor.png:\s+upload operation 0: HTTP 403`),
			},
			{
				Config: locals + screenshotSetConfig("APP_IPHONE_67", c, a, b),
				Check:  func(s *terraform.State) error { return files(sumC, sumA, sumB)(s) },
			},
			{
				// Once the version is released, its screenshots stay.
				PreConfig: func() { env.fake.ReleaseApp(appID) },
				Config:    locals + versionConfig("IOS", "1.0.0", ""),
				Check:     func(s *terraform.State) error { return files(sumC, sumA, sumB)(s) },
			},
		},
	})
}

func TestAccPreviewSet(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	a, sumA := writeMedia(t, dir, "intro.mov", "intro ")
	b, sumB := writeMedia(t, dir, "tour.mov", "tour ")
	cfg := func(files ...string) string {
		quoted := make([]string, len(files))
		for i, f := range files {
			quoted[i] = fmt.Sprintf("%q", f)
		}
		return locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id = apple_app_store_version.next.id
  locale     = "en-US"
}

resource "apple_app_preview_set" "iphone" {
  localization_id = apple_app_store_version_localization.en_US.id
  display_type    = "IPHONE_67"
  files           = [` + strings.Join(quoted, ", ") + `]
}
`
	}
	var setID string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.AppPreviewSetsGetInstanceWithResponse(context.Background(), setID, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("preview set %s still exists: %w", setID, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      cfg(a, a, a, a),
				ExpectError: regexp.MustCompile(`at most 3`),
			},
			{
				Config: cfg(a, b),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_app_preview_set.iphone", "id", &setID),
					func(*terraform.State) error {
						if got := env.fake.MediaFiles(setID); !slices.Equal(got, []string{sumA, sumB}) {
							return fmt.Errorf("previews = %v", got)
						}
						return nil
					},
				),
			},
			{
				Config: cfg(b),
				Check: func(*terraform.State) error {
					if got := env.fake.MediaFiles(setID); !slices.Equal(got, []string{sumB}) {
						return fmt.Errorf("previews = %v", got)
					}
					return nil
				},
			},
		},
	})
}
