package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func webhookConfig(url, secret, extra string) string {
	return webhookEventsConfig(url, secret, `"BUILD_UPLOAD_STATE_UPDATED"`, extra)
}

func webhookEventsConfig(url, secret, events, extra string) string {
	return `
resource "apple_webhook" "builds" {
  app_id      = local.app_id
  name        = "Builds"
  url         = "` + url + `"
  event_types = [` + events + `]
  secret      = "` + secret + `"
` + extra + `
}

data "apple_webhooks" "app" {
  app_id     = local.app_id
  depends_on = [apple_webhook.builds]
}
`
}

func TestAccWebhook(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + webhookConfig("http://example.com/hook", "s1", ""),
				ExpectError: regexp.MustCompile(`must be an HTTPS URL`),
			},
			{
				Config:      locals + webhookEventsConfig("https://example.com/hook", "s1", `"BUILD_UPLOADED"`, ""),
				ExpectError: regexp.MustCompile(`value must be one of`),
			},
			{
				Config: locals + webhookConfig("https://example.com/hook", "s1", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_webhook.builds", "enabled", "true"),
					resource.TestCheckResourceAttr("data.apple_webhooks.app", "webhooks.#", "1"),
					resource.TestCheckResourceAttr("data.apple_webhooks.app", "webhooks.0.url", "https://example.com/hook"),
					captureAttr("apple_webhook.builds", "id", &id),
					func(*terraform.State) error {
						if got := env.fake.WebhookSecret(id); got != "s1" {
							return fmt.Errorf("secret = %q", got)
						}
						return nil
					},
				),
			},
			{
				// The secret can't be read back.
				ResourceName: "apple_webhook.builds", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"secret"},
			},
			{
				Config:           locals + webhookConfig("https://example.com/v2/hook", "s2", "enabled = false"),
				ConfigPlanChecks: expectAction("apple_webhook.builds", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_webhook.builds", "enabled", "false"),
					resource.TestCheckResourceAttr("apple_webhook.builds", "url", "https://example.com/v2/hook"),
					func(*terraform.State) error {
						if got := env.fake.WebhookSecret(id); got != "s2" {
							return fmt.Errorf("secret = %q, want the new one", got)
						}
						return nil
					},
				),
			},
			{
				// A webhook deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.WebhooksDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           locals + webhookConfig("https://example.com/v2/hook", "s2", "enabled = false"),
				ConfigPlanChecks: expectAction("apple_webhook.builds", plancheck.ResourceActionCreate),
			},
		},
	})
}

func TestAccWebhookActions(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	actions := `
action "apple_ping_webhook" "builds" {
  config {
    webhook_id = apple_webhook.builds.id
  }
}
`
	cfg := func(enabled string) string {
		return locals + actions + webhookConfig("https://example.com/hook", "s1", "enabled = "+enabled)
	}
	deliveries := `
data "apple_webhook_deliveries" "ok" {
  webhook_id = apple_webhook.builds.id
  states     = ["SUCCEEDED"]
  depends_on = [terraform_data.trigger]
}
`
	var webhookID string
	env.actionCase(
		resource.TestStep{
			Config:      cfg("false") + triggerAction("1", "apple_ping_webhook.builds"),
			ExpectError: regexp.MustCompile(`The webhook is disabled`),
		},
		resource.TestStep{
			Config: cfg("true") + triggerAction("2", "apple_ping_webhook.builds") + deliveries,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.apple_webhook_deliveries.ok", "deliveries.#", "1"),
				resource.TestCheckResourceAttr("data.apple_webhook_deliveries.ok", "deliveries.0.http_status_code", "200"),
				resource.TestCheckResourceAttr("data.apple_webhook_deliveries.ok", "deliveries.0.redelivery", "false"),
				captureAttr("apple_webhook.builds", "id", &webhookID),
			),
		},
		resource.TestStep{
			Config: cfg("true") + `
data "apple_webhook_deliveries" "sent" {
  webhook_id = apple_webhook.builds.id
}

action "apple_redeliver_webhook" "first" {
  config {
    delivery_id = data.apple_webhook_deliveries.sent.deliveries[0].id
  }
}
` + triggerAction("3", "apple_redeliver_webhook.first"),
			Check: func(*terraform.State) error {
				out, err := env.client.WebhooksDeliveriesGetToManyRelatedWithResponse(context.Background(),
					webhookID, &gen.WebhooksDeliveriesGetToManyRelatedParams{})
				if err := asc.Err(out, err); err != nil {
					return err
				}
				if n := len(out.JSON200.Data); n != 2 || !*out.JSON200.Data[1].Attributes.Redelivery {
					return fmt.Errorf("deliveries = %d, want the ping and its redelivery", n)
				}
				return nil
			},
		},
		resource.TestStep{
			Config: cfg("true") + `
action "apple_redeliver_webhook" "missing" {
  config {
    delivery_id = "D-missing"
  }
}
` + triggerAction("4", "apple_redeliver_webhook.missing"),
			ExpectError: regexp.MustCompile(`There is no webhookDeliveries`),
		},
	)
}
