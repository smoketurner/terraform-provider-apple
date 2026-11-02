// Command apispec downloads Apple's App Store Connect OpenAPI spec, reports
// spec changes for the coverage watcher workflow, and writes the client
// generator configuration.
//
//	apispec fetch -o FILE    download the latest spec; print its version and checksum
//	apispec watch -pinned FILE -latest FILE [-manifest FILE]
//	                         print the changes that need issues as JSON
//	apispec gencfg -o FILE [-manifest FILE]
//	                         write the oapi-codegen configuration for coverage.yaml
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/smoketurner/terraform-provider-apple/internal/apispec"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "apispec:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: apispec fetch|watch|gencfg [flags]")
	}
	switch args[0] {
	case "fetch":
		return fetch(ctx, args[1:], stdout, apispec.SpecURL)
	case "watch":
		return watch(args[1:], stdout)
	case "gencfg":
		return gencfg(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func fetch(ctx context.Context, args []string, stdout io.Writer, url string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	out := fs.String("o", "", "write the spec to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("fetch: -o is required")
	}

	c := &http.Client{Timeout: 2 * time.Minute}
	b, err := apispec.FetchSpec(ctx, c, url)
	if err != nil {
		return err
	}
	s, err := apispec.ParseSpec(b)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		return fmt.Errorf("writing spec: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "version=%s\nsha256=%s\n", s.Version, apispec.SHA256(b))
	return err
}

func watch(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	pinnedPath := fs.String("pinned", "", "the pinned spec")
	latestPath := fs.String("latest", "", "the latest spec")
	manifestPath := fs.String("manifest", "coverage.yaml", "the coverage manifest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pinnedPath == "" || *latestPath == "" {
		return errors.New("watch: -pinned and -latest are required")
	}

	pinned, err := apispec.LoadSpec(*pinnedPath)
	if err != nil {
		return err
	}
	latest, err := apispec.LoadSpec(*latestPath)
	if err != nil {
		return err
	}
	m, err := apispec.LoadManifest(*manifestPath)
	if err != nil {
		return err
	}
	findings := apispec.Watch(pinned, latest, m)
	if findings == nil {
		findings = []apispec.Finding{}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(findings)
}

func gencfg(args []string) error {
	fs := flag.NewFlagSet("gencfg", flag.ContinueOnError)
	out := fs.String("o", "", "write the configuration to this file")
	manifestPath := fs.String("manifest", "coverage.yaml", "the coverage manifest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("gencfg: -o is required")
	}
	m, err := apispec.LoadManifest(*manifestPath)
	if err != nil {
		return err
	}
	b, err := apispec.GeneratorConfig(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		return fmt.Errorf("writing configuration: %w", err)
	}
	return nil
}
