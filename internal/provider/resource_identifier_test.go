package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

func identifierConfig(typeName, value, name string) string {
	return fmt.Sprintf(`
resource "apple_%s" "test" {
  identifier = %q
  name       = %q
}
`, typeName, value, name)
}

func TestAccIdentifiers(t *testing.T) {
	for _, kind := range []identifierKind{merchantIDKind, passTypeIDKind} {
		t.Run(kind.typeName, func(t *testing.T) {
			env := newTestEnv(t)
			address := "apple_" + kind.typeName + ".test"
			var id string
			env.run(resource.TestCase{
				Steps: []resource.TestStep{
					{
						Config: identifierConfig(kind.typeName, kind.example, "Example"),
						Check:  captureAttr(address, "id", &id),
					},
					{ResourceName: address, ImportState: true, ImportStateVerify: true},
					{ResourceName: address, ImportState: true, ImportStateVerify: true, ImportStateId: kind.example},
					{
						ResourceName:  address,
						ImportState:   true,
						ImportStateId: kind.example + ".missing",
						ExpectError:   regexp.MustCompile(`No resource has the ID or identifier`),
					},
					{
						Config: identifierConfig(kind.typeName, kind.example, "Renamed"),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate)},
						},
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrPtr(address, "id", &id),
							resource.TestCheckResourceAttr(address, "name", "Renamed"),
						),
					},
					{
						PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
							_, err := kind.rename(ctx, c, id, "Changed")
							return err
						}),
						Config: identifierConfig(kind.typeName, kind.example, "Renamed"),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate)},
						},
					},
					{
						PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error { return kind.remove(ctx, c, id) }),
						Config:    identifierConfig(kind.typeName, kind.example, "Renamed"),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionCreate)},
						},
					},
					{
						Config: identifierConfig(kind.typeName, kind.example+".other", "Renamed"),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionDestroyBeforeCreate)},
						},
					},
				},
			})
		})
	}
}

// TestAccCertificateRelationships covers Apple Pay and pass type
// certificates, whose merchant ID or pass type ID forces replacement.
func TestAccCertificateRelationships(t *testing.T) {
	env := newTestEnv(t)
	ids := identifierConfig("merchant_id", "merchant.com.example", "Pay") + identifierConfig("pass_type_id", "pass.com.example", "Pass")
	pay := certificateConfig("pay", "APPLE_PAY", testCSR(t, "Pay"), "merchant_id = apple_merchant_id.test.id")
	pass := certificateConfig("pass", "PASS_TYPE_ID", testCSR(t, "Pass"), "pass_type_id = apple_pass_type_id.test.id")
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: ids + certificateConfig("both", "APPLE_PAY", testCSR(t, "x"),
					"merchant_id = apple_merchant_id.test.id\npass_type_id = apple_pass_type_id.test.id"),
				ExpectError: regexp.MustCompile(`Attribute "pass_type_id" cannot be specified when "merchant_id" is specified`),
			},
			{
				Config: ids + pay + pass,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("apple_certificate.pay", "merchant_id", "apple_merchant_id.test", "id"),
					resource.TestCheckResourceAttrPair("apple_certificate.pass", "pass_type_id", "apple_pass_type_id.test", "id"),
				),
			},
			{
				// App Store Connect returns the pass type ID, but not the
				// merchant ID.
				ResourceName:            "apple_certificate.pass",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"csr_content"},
			},
			{
				ResourceName:            "apple_certificate.pay",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"csr_content", "merchant_id"},
			},
			{
				// Moving the certificate to another pass type ID replaces it.
				Config: ids + pay + `
resource "apple_pass_type_id" "two" {
  identifier = "pass.com.example.two"
  name       = "Pass 2"
}` + strings.Replace(pass, "apple_pass_type_id.test.id", "apple_pass_type_id.two.id", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apple_certificate.pass", plancheck.ResourceActionDestroyBeforeCreate)},
				},
			},
		},
	})
}
