---
page_title: "Provider: Apple"
description: |-
  Manage App Store Connect with an App Store Connect API key.
---

# Apple Provider

Manage App Store Connect as code through the [App Store Connect API](https://developer.apple.com/documentation/appstoreconnectapi).

## Setup

1. In App Store Connect, open **Users and Access**, then **Integrations**, and generate a team API key with a role
   that allows the changes you declare. Copy the **Issuer ID** shown above the key list and the key's **Key ID**, and
   download the `.p8` private key, which App Store Connect lets you download only once.
2. Pass the issuer ID, key ID and the contents of the `.p8` file as `issuer_id`, `key_id` and `private_key`, or set
   `APP_STORE_CONNECT_ISSUER_ID`, `APP_STORE_CONNECT_KEY_ID` and `APP_STORE_CONNECT_PRIVATE_KEY`.
3. To use an individual API key from your user profile instead, leave `issuer_id` unset.

```terraform
terraform {
  required_providers {
    apple = {
      source = "smoketurner/apple"
    }
  }
}

# Each argument can also be set with an environment variable:
# APP_STORE_CONNECT_ISSUER_ID, APP_STORE_CONNECT_KEY_ID and
# APP_STORE_CONNECT_PRIVATE_KEY.
provider "apple" {
  issuer_id   = "57246542-96fe-1a63-e053-0824d011072a"
  key_id      = "2X9R4HXF34"
  private_key = file("AuthKey_2X9R4HXF34.p8")
}
```

## Behavior

* **Authentication.** The provider signs a JSON Web Token with the private key and renews it before it expires.
  Tokens never live longer than the 20 minutes App Store Connect allows.
* **Rate limits.** App Store Connect allows a number of requests per API key per rolling hour. The provider retries a
  request that hits the limit a few times, then fails with the remaining quota from the `X-Rate-Limit` header.
* **Retries.** Server errors are retried for reads, updates and deletes. Creates are not retried after a server
  error, because the object may already exist.
* **Imports by natural key.** Resources import by their App Store Connect ID or by a natural key: the bundle identifier,
  certificate serial number, device UDID, profile name, or merchant or pass type identifier. On Terraform 1.12+ they
  also import by identity.
* **Certificates are revoked on destroy.** App Store Connect cannot renew a certificate, so `apple_certificate`
  replaces it, by the first apply within `early_renewal_hours` of its expiration date. Distribution certificates are
  limited to one of each type per team, so replace them without `create_before_destroy`.
* **Invalid profiles are replaced.** A provisioning profile becomes invalid when one of its certificates is revoked or
  expires, one of its devices is disabled, or its bundle ID's capabilities change. The next plan replaces it.
* **TestFlight builds are distributed with actions.** Builds come from uploads, so adding them to groups and testers,
  submitting them for beta review, notifying testers and expiring them are actions (Terraform 1.14+), typically
  triggered when `data.apple_build` returns a new build. Groups, testers and test information are resources.
* **Testers are managed by group membership.** Testers belong to the whole account, so `apple_beta_group_tester`
  adds a tester to one group and destroying it removes them from that group only. Several configurations can add the
  same tester to their own apps' groups. The `apple_delete_beta_tester` action deletes a tester everywhere.
* **Per-app and per-build settings are adopted.** App Store Connect creates an app's beta app review detail and beta
  license agreement, and a build's beta detail. Their resources manage the existing object, and destroying one leaves
  the current settings in place.
* **Users join by invitation.** `apple_user` sends an invitation, which expires after 3 days (the next apply sends a new
  one), and manages the user's roles once they accept. `apple_user_visible_app` grants one app at a time, once the
  invitation is accepted.
* **App information follows the version being prepared.** App info, its localizations and the age rating apply to the
  app info of the version in preparation and take effect when it is released. They need a version in an editable state;
  otherwise applying fails and names the problem. The resources are keyed by app and locale, because App Store Connect
  creates new records with each version.
* **A released version is replaced by the next one.** `apple_app_store_version` manages the platform's version in
  preparation, adopting it if it exists. Once a version is approved it can't be edited, so changing its configuration
  creates the next version; destroying it deletes only a version that was never submitted. Its localizations, review
  details and phased release are keyed by version, so they follow it. Submitting, canceling and releasing are actions.
* **Availability is set per territory.** An app isn't sold anywhere until it has an availability. Creating
  `apple_app_availability` creates one with every territory unavailable; each `apple_app_territory_availability` then
  makes the app available in one territory, and destroying it makes the app unavailable there. Use `for_each` over
  `data.apple_territories` to sell everywhere.
* **Devices are disabled, not deleted.** App Store Connect cannot delete devices. Destroying `apple_device` disables
  the device, which still counts toward the team's device limit until the membership year renews.

## Debugging

Set `TF_LOG=DEBUG` to see each request's status and rate limit. Errors from App Store Connect include the HTTP
method, path, error code and the request field the error refers to.

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `base_url` (String) App Store Connect API base URL. Defaults to `https://api.appstoreconnect.apple.com`. Can also be set with the `APP_STORE_CONNECT_BASE_URL` environment variable. Intended for testing.
- `issuer_id` (String) Issuer ID of a team API key, shown on the Integrations tab of Users and Access in App Store Connect. Leave it unset for an individual API key. Can also be set with the `APP_STORE_CONNECT_ISSUER_ID` environment variable.
- `key_id` (String) ID of the API key, for example `2X9R4HXF34`. Can also be set with the `APP_STORE_CONNECT_KEY_ID` environment variable.
- `private_key` (String, Sensitive) Contents of the API key's `.p8` file, for example `file("AuthKey_2X9R4HXF34.p8")`. Can also be set with the `APP_STORE_CONNECT_PRIVATE_KEY` environment variable.
