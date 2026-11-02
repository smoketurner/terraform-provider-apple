// Command terraform-provider-apple is a Terraform provider for App Store
// Connect: code signing, apps, TestFlight and more.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/smoketurner/terraform-provider-apple/internal/provider"
)

// version is set by GoReleaser at build time.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/smoketurner/apple",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
