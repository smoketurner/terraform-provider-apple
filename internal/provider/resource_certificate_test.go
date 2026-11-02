package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

func testCSR(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func certificateConfig(name, certType, csr, extra string) string {
	return fmt.Sprintf(`
resource "apple_certificate" %q {
  certificate_type = %q
  csr_content      = <<-EOT
%sEOT
%s
}
`, name, certType, csr, extra)
}

// checkCertificateContent checks that certificate_content is a DER
// certificate whose serial number matches serial_number.
func checkCertificateContent(name string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs := s.RootModule().Resources[name].Primary.Attributes
		der, err := base64.StdEncoding.DecodeString(attrs["certificate_content"])
		if err != nil {
			return err
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return err
		}
		if !strings.EqualFold(cert.SerialNumber.Text(16), attrs["serial_number"]) {
			return fmt.Errorf("serial_number %s does not match the certificate's %s", attrs["serial_number"], cert.SerialNumber.Text(16))
		}
		return nil
	}
}

func TestAccCertificate(t *testing.T) {
	env := newTestEnv(t)
	csr := testCSR(t, "Example Dev")
	var id, serial string
	ignore := []string{"csr_content", "early_renewal_hours"}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: certificateConfig("test", "DEVELOPMENT", csr, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_certificate.test", "id", &id),
					captureAttr("apple_certificate.test", "serial_number", &serial),
					resource.TestCheckResourceAttr("apple_certificate.test", "activated", "true"),
					resource.TestCheckResourceAttr("apple_certificate.test", "ready_for_renewal", "false"),
					resource.TestCheckResourceAttr("apple_certificate.test", "display_name", "Example Dev"),
					resource.TestCheckResourceAttrSet("apple_certificate.test", "expiration_date"),
					resource.TestCheckNoResourceAttr("apple_certificate.test", "pass_type_id"),
					checkCertificateContent("apple_certificate.test"),
				),
			},
			{
				ResourceName:            "apple_certificate.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: ignore,
			},
			{
				// Import by serial number, in either case.
				ResourceName:            "apple_certificate.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: ignore,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return strings.ToLower(serial), nil
				},
			},
			{
				// activated changes in place.
				Config: certificateConfig("test", "DEVELOPMENT", csr, "activated = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_certificate.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("apple_certificate.test", "id", &id),
					resource.TestCheckResourceAttr("apple_certificate.test", "activated", "false"),
				),
			},
			{
				// early_renewal_hours alone changes nothing at Apple.
				Config: certificateConfig("test", "DEVELOPMENT", csr, "activated = false\nearly_renewal_hours = 24"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_certificate.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttrPtr("apple_certificate.test", "id", &id),
			},
			{
				// Within early_renewal_hours of expiring, the certificate is
				// replaced, the new one first with create_before_destroy.
				Config: certificateConfig("test", "DEVELOPMENT", csr,
					"activated = false\nearly_renewal_hours = 9000\nlifecycle {\n  create_before_destroy = true\n}"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_certificate.test", plancheck.ResourceActionCreateBeforeDestroy)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						if s.RootModule().Resources["apple_certificate.test"].Primary.ID == id {
							return fmt.Errorf("certificate %s was not replaced", id)
						}
						return nil
					},
					resource.TestCheckResourceAttr("apple_certificate.test", "activated", "false"),
				),
				// The replacement is also within 9000 hours of expiring.
				ExpectNonEmptyPlan: true,
			},
			{
				// A certificate revoked outside Terraform is created again.
				Config: certificateConfig("test", "DEVELOPMENT", csr, ""),
				Check:  captureAttr("apple_certificate.test", "id", &id),
			},
			{
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.CertificatesDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config: certificateConfig("test", "DEVELOPMENT", csr, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_certificate.test", plancheck.ResourceActionCreate)},
				},
			},
			{
				// A new CSR forces replacement.
				Config: certificateConfig("test", "DEVELOPMENT", testCSR(t, "Other"), ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_certificate.test", plancheck.ResourceActionDestroyBeforeCreate)},
				},
			},
		},
	})
}

// TestAccCertificateImportThenManage checks that adopting an imported
// certificate, whose CSR App Store Connect does not return, updates it in
// place instead of replacing it.
func TestAccCertificateImportThenManage(t *testing.T) {
	env := newTestEnv(t)
	csr := testCSR(t, "Imported")
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: certificateConfig("test", "DEVELOPMENT", csr, ""),
				Check:  captureAttr("apple_certificate.test", "id", &id),
			},
			{
				// Forget the certificate without revoking it.
				Config: `
removed {
  from = apple_certificate.test
  lifecycle {
    destroy = false
  }
}`,
			},
			{
				Config:             certificateConfig("test", "DEVELOPMENT", csr, ""),
				ResourceName:       "apple_certificate.test",
				ImportState:        true,
				ImportStatePersist: true,
				ImportStateIdFunc:  func(*terraform.State) (string, error) { return id, nil },
			},
			{
				Config: certificateConfig("test", "DEVELOPMENT", csr, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_certificate.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttrPtr("apple_certificate.test", "id", &id),
			},
		},
	})
}

func TestAccCertificateDistributionLimit(t *testing.T) {
	env := newTestEnv(t)
	first := certificateConfig("first", "IOS_DISTRIBUTION", testCSR(t, "Dist 1"), "")
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{Config: first},
			{
				Config:      first + certificateConfig("second", "IOS_DISTRIBUTION", testCSR(t, "Dist 2"), ""),
				ExpectError: regexp.MustCompile(`(?s)HTTP 409.*You\s+already\s+have\s+a\s+current\s+IOS_DISTRIBUTION`),
			},
		},
	})
}

func TestReadyForRenewal(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	exp := types.StringValue("2027-01-02T00:00:00Z")
	for hours, want := range map[int64]bool{0: false, 23: false, 24: true, 48: true} {
		if got := readyForRenewal(now, exp, types.Int64Value(hours)); got != want {
			t.Errorf("readyForRenewal(%d hours) = %v, want %v", hours, got, want)
		}
	}
	if readyForRenewal(now, exp, types.Int64Null()) {
		t.Error("a certificate a day from expiring is ready with no early_renewal_hours")
	}
	if !readyForRenewal(now, types.StringValue("2026-12-31T00:00:00Z"), types.Int64Null()) {
		t.Error("an expired certificate is not ready for renewal")
	}
	if readyForRenewal(now, types.StringNull(), types.Int64Value(48)) {
		t.Error("an unknown expiration date is ready for renewal")
	}
}
