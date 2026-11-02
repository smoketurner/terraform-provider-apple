// Package provider implements the Apple (App Store Connect) Terraform
// provider.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

var _ provider.Provider = &appleProvider{}

type appleProvider struct {
	version string
}

type providerModel struct {
	IssuerID   types.String `tfsdk:"issuer_id"`
	KeyID      types.String `tfsdk:"key_id"`
	PrivateKey types.String `tfsdk:"private_key"`
	BaseURL    types.String `tfsdk:"base_url"`
}

// New returns a constructor for the provider at the given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &appleProvider{version: version}
	}
}

func (p *appleProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "apple"
	resp.Version = p.version
}

func (p *appleProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage App Store Connect with an App Store Connect API key.",
		Attributes: map[string]schema.Attribute{
			"issuer_id": schema.StringAttribute{
				MarkdownDescription: "Issuer ID of a team API key, shown on the Integrations tab of Users and Access in App " +
					"Store Connect. Leave it unset for an individual API key. Can also be set with the " +
					"`APP_STORE_CONNECT_ISSUER_ID` environment variable.",
				Optional: true,
			},
			"key_id": schema.StringAttribute{
				MarkdownDescription: "ID of the API key, for example `2X9R4HXF34`. Can also be set with the " +
					"`APP_STORE_CONNECT_KEY_ID` environment variable.",
				Optional: true,
			},
			"private_key": schema.StringAttribute{
				MarkdownDescription: "Contents of the API key's `.p8` file, for example `file(\"AuthKey_2X9R4HXF34.p8\")`. " +
					"Can also be set with the `APP_STORE_CONNECT_PRIVATE_KEY` environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"base_url": schema.StringAttribute{
				MarkdownDescription: "App Store Connect API base URL. Defaults to `" + asc.DefaultBaseURL + "`. Can also be " +
					"set with the `APP_STORE_CONNECT_BASE_URL` environment variable. Intended for testing.",
				Optional: true,
			},
		},
	}
}

// configValue returns the attribute's value, or the environment variable's
// when the attribute is not set.
func configValue(v types.String, env string) string {
	if !v.IsNull() {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func (p *appleProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.IssuerID.IsUnknown() || cfg.KeyID.IsUnknown() || cfg.PrivateKey.IsUnknown() || cfg.BaseURL.IsUnknown() {
		resp.Diagnostics.AddWarning("Provider configuration unknown",
			"The Apple provider configuration depends on values not known until apply; resources cannot be read during this plan.")
		return
	}

	keyID := configValue(cfg.KeyID, "APP_STORE_CONNECT_KEY_ID")
	if keyID == "" {
		resp.Diagnostics.AddAttributeError(path.Root("key_id"), "Missing API key ID",
			"Set the provider's key_id attribute or the APP_STORE_CONNECT_KEY_ID environment variable.")
	}
	p8 := configValue(cfg.PrivateKey, "APP_STORE_CONNECT_PRIVATE_KEY")
	if p8 == "" {
		resp.Diagnostics.AddAttributeError(path.Root("private_key"), "Missing API private key",
			"Set the provider's private_key attribute or the APP_STORE_CONNECT_PRIVATE_KEY environment variable to the contents of the .p8 file.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	key, err := asc.ParsePrivateKey(p8)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("private_key"), "Invalid API private key", err.Error())
		return
	}

	ts := asc.NewTokenSource(asc.Credentials{
		IssuerID:   configValue(cfg.IssuerID, "APP_STORE_CONNECT_ISSUER_ID"),
		KeyID:      keyID,
		PrivateKey: key,
	})
	client, err := asc.New(configValue(cfg.BaseURL, "APP_STORE_CONNECT_BASE_URL"), ts, p.version)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("base_url"), "Invalid App Store Connect API base URL", err.Error())
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *appleProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newBundleIDResource,
		newBundleIDCapabilityResource,
		newCertificateResource,
		newDeviceResource,
		newProfileResource,
		newIdentifierResource(merchantIDKind),
		newIdentifierResource(passTypeIDKind),
	}
}

func (p *appleProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
