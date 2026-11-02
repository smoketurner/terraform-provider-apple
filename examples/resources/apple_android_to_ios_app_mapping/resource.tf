resource "apple_android_to_ios_app_mapping" "example" {
  app_id       = data.apple_app.example.id
  package_name = "com.example.android"
  signing_certificate_sha256_fingerprints = [
    "14:6D:E9:83:C5:73:06:50:D8:EE:B9:95:2F:34:FC:64:16:A0:83:42:E6:1D:BE:A8:8A:04:96:B2:3F:CF:44:E5",
  ]
}
