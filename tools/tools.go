//go:build generate

// Package tools pins the code and documentation generators as Go tool
// dependencies of this module.
package tools

// Format Terraform code for use in documentation.
//go:generate terraform fmt -recursive ../examples/

// Generate documentation.
//go:generate go tool tfplugindocs generate --provider-dir .. --provider-name apple
