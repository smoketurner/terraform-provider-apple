data "apple_app_clip_domain_status" "clip" {
  build_bundle_id = local.app_clip_bundle_id
}

output "invalid_app_clip_domains" {
  value = [for d in data.apple_app_clip_domain_status.clip.cache_domains : d.domain if !d.is_valid]
}
