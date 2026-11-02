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
