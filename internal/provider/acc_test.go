package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
)

var protoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"apple": providerserver.NewProtocol6WithError(New("test")()),
}

// testEnv runs acceptance tests against the in-memory fake App Store Connect
// API. There are no live tests.
type testEnv struct {
	t      *testing.T
	fake   *asctest.Server
	client *asc.Client
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	fake := asctest.NewServer()
	t.Cleanup(fake.Close)
	t.Setenv("APP_STORE_CONNECT_ISSUER_ID", asctest.IssuerID)
	t.Setenv("APP_STORE_CONNECT_KEY_ID", asctest.KeyID)
	t.Setenv("APP_STORE_CONNECT_PRIVATE_KEY", fake.PrivateKey)
	t.Setenv("APP_STORE_CONNECT_BASE_URL", fake.URL)
	return &testEnv{t: t, fake: fake, client: fake.Client()}
}

func (e *testEnv) run(tc resource.TestCase) {
	e.t.Helper()
	if tc.ProtoV6ProviderFactories == nil {
		tc.ProtoV6ProviderFactories = protoV6ProviderFactories
	}
	resource.UnitTest(e.t, tc)
}

// outside runs a client call in a PreConfig hook, failing the test on error,
// to simulate changes made outside Terraform.
func (e *testEnv) outside(f func(ctx context.Context, c *asc.Client) error) func() {
	return func() {
		if err := f(context.Background(), e.client); err != nil {
			e.t.Fatalf("changing state outside Terraform: %v", err)
		}
	}
}

// captureAttr stores a resource attribute from state for use in later steps.
func captureAttr(name, attr string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("resource %s not found in state", name)
		}
		*dst = rs.Primary.Attributes[attr]
		if *dst == "" {
			return fmt.Errorf("%s.%s is empty", name, attr)
		}
		return nil
	}
}

// attrIDFunc returns an ImportStateIdFunc that imports by an attribute.
func attrIDFunc(name, attr string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", name)
		}
		return rs.Primary.Attributes[attr], nil
	}
}
