# The private key never leaves Terraform: App Store Connect signs the CSR.
resource "tls_private_key" "distribution" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_cert_request" "distribution" {
  private_key_pem = tls_private_key.distribution.private_key_pem
  subject {
    common_name = "Example Distribution"
  }
}

resource "apple_certificate" "distribution" {
  certificate_type = "DISTRIBUTION"
  csr_content      = tls_cert_request.distribution.cert_request_pem

  # Replace the certificate 30 days before it expires.
  early_renewal_hours = 720
}

# For a type without a per-team limit, such as DEVELOPMENT, create the new
# certificate before revoking the old one. Distribution certificates are
# limited to one of each type per team, so they must be revoked first.
resource "apple_certificate" "development" {
  certificate_type    = "DEVELOPMENT"
  csr_content         = tls_cert_request.distribution.cert_request_pem
  early_renewal_hours = 720

  lifecycle {
    create_before_destroy = true
  }
}
