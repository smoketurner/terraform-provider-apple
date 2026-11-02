package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

func testP8(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// configure runs the provider's Configure with the given attribute values;
// absent attributes are null.
func configure(t *testing.T, attrs map[string]tftypes.Value) *provider.ConfigureResponse {
	t.Helper()
	ctx := context.Background()
	p := New("test")()
	var schemaResp provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &schemaResp)
	typ := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object) //nolint:forcetypeassert // A schema's type is always an object.
	values := map[string]tftypes.Value{}
	for name, attrType := range typ.AttributeTypes {
		values[name] = tftypes.NewValue(attrType, nil)
	}
	for name, v := range attrs {
		values[name] = v
	}
	req := provider.ConfigureRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(typ, values)}}
	var resp provider.ConfigureResponse
	p.Configure(ctx, req, &resp)
	return &resp
}

func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"APP_STORE_CONNECT_ISSUER_ID", "APP_STORE_CONNECT_KEY_ID", "APP_STORE_CONNECT_PRIVATE_KEY", "APP_STORE_CONNECT_BASE_URL"} {
		t.Setenv(name, "")
	}
}

func TestConfigureFromAttributes(t *testing.T) {
	clearEnv(t)
	resp := configure(t, map[string]tftypes.Value{
		"issuer_id": str("issuer"), "key_id": str("KEY"), "private_key": str(testP8(t)), "base_url": str("https://example.com"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if _, ok := resp.ResourceData.(*asc.Client); !ok {
		t.Errorf("ResourceData is %T", resp.ResourceData)
	}
	if _, ok := resp.DataSourceData.(*asc.Client); !ok {
		t.Errorf("DataSourceData is %T", resp.DataSourceData)
	}
}

func TestConfigureFromEnvironment(t *testing.T) {
	clearEnv(t)
	// An individual API key has no issuer ID.
	t.Setenv("APP_STORE_CONNECT_KEY_ID", "KEY")
	t.Setenv("APP_STORE_CONNECT_PRIVATE_KEY", testP8(t))
	resp := configure(t, nil)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if resp.ResourceData == nil {
		t.Error("no client configured")
	}
}

func TestConfigureErrors(t *testing.T) {
	p8 := testP8(t)
	for name, tc := range map[string]struct {
		attrs map[string]tftypes.Value
		want  string
	}{
		"missing key ID":      {map[string]tftypes.Value{"private_key": str(p8)}, "Missing API key ID"},
		"missing private key": {map[string]tftypes.Value{"key_id": str("K")}, "Missing API private key"},
		"invalid private key": {map[string]tftypes.Value{"key_id": str("K"), "private_key": str("nope")}, "Invalid API private key"},
		"invalid base URL":    {map[string]tftypes.Value{"key_id": str("K"), "private_key": str(p8), "base_url": str("example.com")}, "Invalid App Store Connect API base URL"},
	} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			resp := configure(t, tc.attrs)
			if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), tc.want) {
				t.Errorf("diagnostics = %v, want %q", resp.Diagnostics, tc.want)
			}
			if resp.ResourceData != nil {
				t.Error("a client was configured")
			}
		})
	}
}

func TestConfigureUnknown(t *testing.T) {
	clearEnv(t)
	resp := configure(t, map[string]tftypes.Value{"private_key": tftypes.NewValue(tftypes.String, tftypes.UnknownValue)})
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Errorf("diagnostics = %v, want one warning", resp.Diagnostics)
	}
	if resp.ResourceData != nil {
		t.Error("a client was configured")
	}
}
