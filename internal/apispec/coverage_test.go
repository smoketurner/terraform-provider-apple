package apispec

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/smoketurner/terraform-provider-apple/internal/provider"
)

const (
	specPath      = "../../api/openapi.oas.json"
	manifestPath  = "../../coverage.yaml"
	genConfigPath = "../asc/gen/oapi-codegen.yaml"
	genClientPath = "../asc/gen/asc.gen.go"
	providerDir   = "../provider"
)

// pinnedSpec loads the vendored spec after checking it is the pinned one.
func pinnedSpec(t *testing.T) *Spec {
	t.Helper()
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(b, PinnedSHA256); err != nil {
		t.Fatalf("%s is not the pinned spec: %v", specPath, err)
	}
	s, err := ParseSpec(b)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != PinnedVersion {
		t.Errorf("%s is version %s, but pin.go says %s", specPath, s.Version, PinnedVersion)
	}
	return s
}

func loadManifest(t *testing.T) *Manifest {
	t.Helper()
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestMapsEveryOperation(t *testing.T) {
	s := pinnedSpec(t)
	if err := loadManifest(t).Check(s); err != nil {
		t.Errorf("coverage.yaml does not match the spec:\n%v", err)
	}
}

// TestGeneratorConfigIsCurrent checks that the client is generated for
// exactly the covered and in_progress operations.
func TestGeneratorConfigIsCurrent(t *testing.T) {
	want, err := GeneratorConfig(loadManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(genConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is stale; run make generate", genConfigPath)
	}
}

// TestCoveredOperationsAreCalled checks that the provider calls the
// generated client method of every covered operation. in_progress operations
// are generated but need not be called yet.
func TestCoveredOperationsAreCalled(t *testing.T) {
	called := providerCalls(t)
	for _, id := range loadManifest(t).Covered() {
		method := goName(id)
		if !called[method] && !called[method+"WithResponse"] {
			t.Errorf("coverage.yaml marks %s covered, but internal/provider never calls %sWithResponse", id, method)
		}
	}
}

// goName returns oapi-codegen's Go name for an operationId, e.g.
// BundleIdsGetCollection for bundleIds_getCollection.
func goName(id string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(id, "_") {
		if part == "" {
			continue
		}
		r := []rune(part)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// TestGeneratedClientMethods checks goName against the generated client:
// every generated operation has a ClientWithResponses method by that name.
func TestGeneratedClientMethods(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), genClientPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok && id.Name == "ClientWithResponses" {
				methods[fn.Name.Name] = true
			}
		}
	}
	for _, id := range loadManifest(t).Generated() {
		if !methods[goName(id)+"WithResponse"] {
			t.Errorf("the generated client has no %sWithResponse for %s; run make generate", goName(id), id)
		}
	}
}

func TestGoName(t *testing.T) {
	for id, want := range map[string]string{
		"bundleIds_getCollection":                     "BundleIdsGetCollection",
		"profiles_certificates_getToManyRelated":      "ProfilesCertificatesGetToManyRelated",
		"apps_appStoreVersionExperimentsV2_getToMany": "AppsAppStoreVersionExperimentsV2GetToMany",
	} {
		if got := goName(id); got != want {
			t.Errorf("goName(%q) = %q, want %q", id, got, want)
		}
	}
}

// providerCalls returns the names of every method called on a selector in
// the provider's non-test sources.
func providerCalls(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(providerDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	called := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					called[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	return called
}

// TestManifestNamesExist checks every "by" entry against the provider's
// registered resources, data sources and actions.
func TestManifestNamesExist(t *testing.T) {
	names := providerNames(t)
	for id, e := range loadManifest(t).Operations {
		for _, by := range e.By {
			if !names[by] {
				t.Errorf("%s: %q is not a resource, data source or action of the provider", id, by)
			}
		}
	}
}

func providerNames(t *testing.T) map[string]bool {
	t.Helper()
	ctx := context.Background()
	p := provider.New("test")()
	var meta fwprovider.MetadataResponse
	p.Metadata(ctx, fwprovider.MetadataRequest{}, &meta)
	prefix := meta.TypeName

	names := map[string]bool{}
	for _, f := range p.Resources(ctx) {
		var resp resource.MetadataResponse
		f().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: prefix}, &resp)
		names[resp.TypeName] = true
	}
	for _, f := range p.DataSources(ctx) {
		var resp datasource.MetadataResponse
		f().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: prefix}, &resp)
		names["data."+resp.TypeName] = true
	}
	if pa, ok := p.(fwprovider.ProviderWithActions); ok {
		for _, f := range pa.Actions(ctx) {
			var resp action.MetadataResponse
			f().Metadata(ctx, action.MetadataRequest{ProviderTypeName: prefix}, &resp)
			names["action."+resp.TypeName] = true
		}
	}
	return names
}
