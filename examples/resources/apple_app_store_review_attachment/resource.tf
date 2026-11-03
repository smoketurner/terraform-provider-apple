resource "apple_app_store_review_attachment" "demo" {
  review_detail_id = apple_app_store_review_detail.ios.id
  file             = "${path.module}/review/demo.mov"
}
