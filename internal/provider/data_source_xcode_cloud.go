package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Data sources for what an Xcode Cloud workflow refers to, the runs of a
// workflow, and the action that starts one.

func dsRequired(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Required: true}
}

func dsBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{MarkdownDescription: desc, Computed: true}
}

func dsInt(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{MarkdownDescription: desc, Computed: true}
}

// ---- data.apple_ci_products ----

type ciProductData struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	ProductType         types.String `tfsdk:"product_type"`
	CreatedDate         types.String `tfsdk:"created_date"`
	AppID               types.String `tfsdk:"app_id"`
	PrimaryRepositoryID types.String `tfsdk:"primary_repository_id"`
}

type ciProductsData struct {
	AppID    types.String    `tfsdk:"app_id"`
	Products []ciProductData `tfsdk:"products"`
}

var newCiProductsDataSource = listDataSource("ci_products",
	"Lists the account's Xcode Cloud products. Xcode creates a product when Xcode Cloud is set up for an app or framework.",
	map[string]schema.Attribute{
		"app_id": schema.StringAttribute{MarkdownDescription: "App Store Connect ID of an app, to list only its product.", Optional: true},
		"products": listOf("The products.", map[string]schema.Attribute{
			"id":                    dsComputed("App Store Connect ID of the product."),
			"name":                  dsComputed("Name of the product."),
			"product_type":          dsComputed("Kind of product: `APP` or `FRAMEWORK`."),
			"created_date":          dsComputed("When the product was created."),
			"app_id":                dsComputed("App Store Connect ID of the product's app. Null for a framework."),
			"primary_repository_id": dsComputed("App Store Connect ID of the product's primary repository."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *ciProductsData, diags *diag.Diagnostics) {
		params := &gen.CiProductsGetCollectionParams{
			Include: &[]gen.CiProductsGetCollectionParamsInclude{
				gen.CiProductsGetCollectionParamsIncludeApp, gen.CiProductsGetCollectionParamsIncludePrimaryRepositories,
			},
			Limit: new(listAll),
		}
		if id := m.AppID.ValueString(); id != "" {
			params.FilterApp = &[]string{id}
		}
		out, err := c.CiProductsGetCollectionWithResponse(ctx, params)
		var all []gen.CiProduct
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.CiProductsResponse) []gen.CiProduct { return p.Data },
				func(p *gen.CiProductsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list Xcode Cloud products", err)
			return
		}
		m.Products = []ciProductData{}
		for _, p := range all {
			a := asc.Value(p.Attributes)
			item := ciProductData{
				ID: types.StringValue(p.Id), Name: stringValue(a.Name), ProductType: stringValue(a.ProductType), CreatedDate: timeValue(a.CreatedDate),
				AppID: types.StringNull(), PrimaryRepositoryID: types.StringNull(),
			}
			if rel := p.Relationships; rel != nil {
				if rel.App != nil && rel.App.Data != nil {
					item.AppID = types.StringValue(rel.App.Data.Id)
				}
				if rel.PrimaryRepositories != nil && len(asc.Value(rel.PrimaryRepositories.Data)) > 0 {
					item.PrimaryRepositoryID = types.StringValue((*rel.PrimaryRepositories.Data)[0].Id)
				}
			}
			if id := m.AppID.ValueString(); id == "" || item.AppID.ValueString() == id {
				m.Products = append(m.Products, item)
			}
		}
	})

// ---- data.apple_scm_repositories ----

type scmRepositoryData struct {
	ID              types.String `tfsdk:"id"`
	OwnerName       types.String `tfsdk:"owner_name"`
	RepositoryName  types.String `tfsdk:"repository_name"`
	HTTPCloneURL    types.String `tfsdk:"http_clone_url"`
	SSHCloneURL     types.String `tfsdk:"ssh_clone_url"`
	ProviderID      types.String `tfsdk:"scm_provider_id"`
	DefaultBranchID types.String `tfsdk:"default_branch_id"`
	Primary         types.Bool   `tfsdk:"primary"`
}

type scmRepositoriesData struct {
	ProductID    types.String        `tfsdk:"product_id"`
	Repositories []scmRepositoryData `tfsdk:"repositories"`
}

func scmRepositoryValue(r *gen.ScmRepository, primary types.Bool) scmRepositoryData {
	a := asc.Value(r.Attributes)
	item := scmRepositoryData{
		ID: types.StringValue(r.Id), OwnerName: stringValue(a.OwnerName), RepositoryName: stringValue(a.RepositoryName),
		HTTPCloneURL: stringValue(a.HttpCloneUrl), SSHCloneURL: stringValue(a.SshCloneUrl),
		ProviderID: types.StringNull(), DefaultBranchID: types.StringNull(), Primary: primary,
	}
	if rel := r.Relationships; rel != nil {
		if rel.ScmProvider != nil && rel.ScmProvider.Data != nil {
			item.ProviderID = types.StringValue(rel.ScmProvider.Data.Id)
		}
		if rel.DefaultBranch != nil && rel.DefaultBranch.Data != nil {
			item.DefaultBranchID = types.StringValue(rel.DefaultBranch.Data.Id)
		}
	}
	return item
}

func allRepositories(ctx context.Context, c *asc.Client, first *gen.ScmRepositoriesResponse) ([]gen.ScmRepository, error) {
	return asc.All(ctx, c, first,
		func(p *gen.ScmRepositoriesResponse) []gen.ScmRepository { return p.Data },
		func(p *gen.ScmRepositoriesResponse) *string { return p.Links.Next })
}

var newScmRepositoriesDataSource = listDataSource("scm_repositories",
	"Lists the source code repositories connected to Xcode Cloud: every one, or those of a product.",
	map[string]schema.Attribute{
		"product_id": schema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of an Xcode Cloud product, to list its primary and additional repositories.", Optional: true,
		},
		"repositories": listOf("The repositories.", map[string]schema.Attribute{
			"id":                dsComputed("App Store Connect ID of the repository."),
			"owner_name":        dsComputed("Owner of the repository, e.g. the organization."),
			"repository_name":   dsComputed("Name of the repository."),
			"http_clone_url":    dsComputed("URL the repository clones from over HTTPS."),
			"ssh_clone_url":     dsComputed("URL the repository clones from over SSH."),
			"scm_provider_id":   dsComputed("App Store Connect ID of the source control provider."),
			"default_branch_id": dsComputed("App Store Connect ID of the default branch's git reference."),
			"primary":           dsBool("With `product_id`: whether this is the product's primary repository. Null otherwise."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *scmRepositoriesData, diags *diag.Diagnostics) {
		m.Repositories = []scmRepositoryData{}
		productID := m.ProductID.ValueString()
		if productID == "" {
			out, err := c.ScmRepositoriesGetCollectionWithResponse(ctx, &gen.ScmRepositoriesGetCollectionParams{
				Include: &[]gen.ScmRepositoriesGetCollectionParamsInclude{
					gen.ScmRepositoriesGetCollectionParamsIncludeScmProvider, gen.ScmRepositoriesGetCollectionParamsIncludeDefaultBranch,
				},
				Limit: new(listAll),
			})
			var all []gen.ScmRepository
			if err = asc.Err(out, err); err == nil {
				all, err = allRepositories(ctx, c, out.JSON200)
			}
			if err != nil {
				apiError(diags, "list repositories", err)
				return
			}
			for _, r := range all {
				m.Repositories = append(m.Repositories, scmRepositoryValue(&r, types.BoolNull()))
			}
			return
		}
		primary, err := c.CiProductsPrimaryRepositoriesGetToManyRelatedWithResponse(ctx, productID, &gen.CiProductsPrimaryRepositoriesGetToManyRelatedParams{
			Include: &[]gen.CiProductsPrimaryRepositoriesGetToManyRelatedParamsInclude{
				gen.CiProductsPrimaryRepositoriesGetToManyRelatedParamsIncludeScmProvider, gen.CiProductsPrimaryRepositoriesGetToManyRelatedParamsIncludeDefaultBranch,
			},
			Limit: new(listAll),
		})
		var primaries, additionals []gen.ScmRepository
		if err = asc.Err(primary, err); err == nil {
			primaries, err = allRepositories(ctx, c, primary.JSON200)
		}
		if err == nil {
			additional, e := c.CiProductsAdditionalRepositoriesGetToManyRelatedWithResponse(ctx, productID, &gen.CiProductsAdditionalRepositoriesGetToManyRelatedParams{
				Include: &[]gen.CiProductsAdditionalRepositoriesGetToManyRelatedParamsInclude{
					gen.CiProductsAdditionalRepositoriesGetToManyRelatedParamsIncludeScmProvider, gen.CiProductsAdditionalRepositoriesGetToManyRelatedParamsIncludeDefaultBranch,
				},
				Limit: new(listAll),
			})
			if err = asc.Err(additional, e); err == nil {
				additionals, err = allRepositories(ctx, c, additional.JSON200)
			}
		}
		if err != nil {
			lookupError(diags, "Xcode Cloud product", err)
			return
		}
		for _, r := range primaries {
			m.Repositories = append(m.Repositories, scmRepositoryValue(&r, types.BoolValue(true)))
		}
		for _, r := range additionals {
			m.Repositories = append(m.Repositories, scmRepositoryValue(&r, types.BoolValue(false)))
		}
	})

// ---- data.apple_scm_git_references and data.apple_scm_pull_requests ----

type scmGitReferenceData struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	CanonicalName types.String `tfsdk:"canonical_name"`
	Kind          types.String `tfsdk:"kind"`
	IsDeleted     types.Bool   `tfsdk:"is_deleted"`
}

type scmGitReferencesData struct {
	RepositoryID types.String          `tfsdk:"repository_id"`
	Kinds        types.Set             `tfsdk:"kinds"`
	References   []scmGitReferenceData `tfsdk:"git_references"`
}

var newScmGitReferencesDataSource = listDataSource("scm_git_references",
	"Lists the branches and tags of a repository connected to Xcode Cloud, which `apple_start_ci_build_run` builds.",
	map[string]schema.Attribute{
		"repository_id": dsRequired("App Store Connect ID of the repository; see `data.apple_scm_repositories`."),
		"kinds":         dsFilter("Kinds of reference to list.", "BRANCH", "TAG"),
		"git_references": listOf("The references.", map[string]schema.Attribute{
			"id":             dsComputed("App Store Connect ID of the git reference."),
			"name":           dsComputed("Name of the branch or tag, e.g. `main`."),
			"canonical_name": dsComputed("Full name of the reference, e.g. `refs/heads/main`."),
			"kind":           dsComputed("Kind of reference: `BRANCH` or `TAG`."),
			"is_deleted":     dsBool("Whether the reference is deleted in the repository."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *scmGitReferencesData, diags *diag.Diagnostics) {
		out, err := c.ScmRepositoriesGitReferencesGetToManyRelatedWithResponse(ctx, m.RepositoryID.ValueString(),
			&gen.ScmRepositoriesGitReferencesGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.ScmGitReference
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.ScmGitReferencesResponse) []gen.ScmGitReference { return p.Data },
				func(p *gen.ScmGitReferencesResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "repository", err)
			return
		}
		m.References = []scmGitReferenceData{}
		for _, ref := range all {
			a := asc.Value(ref.Attributes)
			if !passes(ctx, m.Kinds, string(asc.Value(a.Kind)), diags) {
				continue
			}
			m.References = append(m.References, scmGitReferenceData{
				ID: types.StringValue(ref.Id), Name: stringValue(a.Name), CanonicalName: stringValue(a.CanonicalName), Kind: stringValue(a.Kind),
				IsDeleted: types.BoolPointerValue(a.IsDeleted),
			})
		}
	})

type scmPullRequestData struct {
	ID                    types.String `tfsdk:"id"`
	Number                types.Int64  `tfsdk:"number"`
	Title                 types.String `tfsdk:"title"`
	WebURL                types.String `tfsdk:"web_url"`
	SourceBranchName      types.String `tfsdk:"source_branch_name"`
	DestinationBranchName types.String `tfsdk:"destination_branch_name"`
	IsClosed              types.Bool   `tfsdk:"is_closed"`
	IsCrossRepository     types.Bool   `tfsdk:"is_cross_repository"`
}

type scmPullRequestsData struct {
	RepositoryID types.String         `tfsdk:"repository_id"`
	PullRequests []scmPullRequestData `tfsdk:"pull_requests"`
}

var newScmPullRequestsDataSource = listDataSource("scm_pull_requests",
	"Lists the pull requests of a repository connected to Xcode Cloud, which `apple_start_ci_build_run` builds.",
	map[string]schema.Attribute{
		"repository_id": dsRequired("App Store Connect ID of the repository; see `data.apple_scm_repositories`."),
		"pull_requests": listOf("The pull requests.", map[string]schema.Attribute{
			"id":                      dsComputed("App Store Connect ID of the pull request."),
			"number":                  dsInt("Number of the pull request at the source control provider."),
			"title":                   dsComputed("Title of the pull request."),
			"web_url":                 dsComputed("URL of the pull request at the source control provider."),
			"source_branch_name":      dsComputed("Branch the pull request comes from."),
			"destination_branch_name": dsComputed("Branch the pull request merges into."),
			"is_closed":               dsBool("Whether the pull request is closed."),
			"is_cross_repository":     dsBool("Whether the pull request comes from another repository, such as a fork."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *scmPullRequestsData, diags *diag.Diagnostics) {
		out, err := c.ScmRepositoriesPullRequestsGetToManyRelatedWithResponse(ctx, m.RepositoryID.ValueString(),
			&gen.ScmRepositoriesPullRequestsGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.ScmPullRequest
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.ScmPullRequestsResponse) []gen.ScmPullRequest { return p.Data },
				func(p *gen.ScmPullRequestsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "repository", err)
			return
		}
		m.PullRequests = []scmPullRequestData{}
		for _, pr := range all {
			a := asc.Value(pr.Attributes)
			m.PullRequests = append(m.PullRequests, scmPullRequestData{
				ID: types.StringValue(pr.Id), Number: intValue(a.Number), Title: stringValue(a.Title), WebURL: stringValue(a.WebUrl),
				SourceBranchName: stringValue(a.SourceBranchName), DestinationBranchName: stringValue(a.DestinationBranchName),
				IsClosed: types.BoolPointerValue(a.IsClosed), IsCrossRepository: types.BoolPointerValue(a.IsCrossRepository),
			})
		}
	})

// ---- data.apple_ci_xcode_versions and data.apple_ci_mac_os_versions ----

type ciRuntimeData struct {
	RuntimeName       types.String `tfsdk:"runtime_name"`
	RuntimeIdentifier types.String `tfsdk:"runtime_identifier"`
}

type ciTestDestinationData struct {
	Kind                 types.String    `tfsdk:"kind"`
	DeviceTypeName       types.String    `tfsdk:"device_type_name"`
	DeviceTypeIdentifier types.String    `tfsdk:"device_type_identifier"`
	AvailableRuntimes    []ciRuntimeData `tfsdk:"available_runtimes"`
}

type ciXcodeVersionData struct {
	ID               types.String            `tfsdk:"id"`
	Version          types.String            `tfsdk:"version"`
	Name             types.String            `tfsdk:"name"`
	MacOSVersionIDs  types.List              `tfsdk:"mac_os_version_ids"`
	TestDestinations []ciTestDestinationData `tfsdk:"test_destinations"`
}

type ciXcodeVersionsData struct {
	Versions []ciXcodeVersionData `tfsdk:"xcode_versions"`
}

func dsStringList(desc string) schema.ListAttribute {
	return schema.ListAttribute{MarkdownDescription: desc, Computed: true, ElementType: types.StringType}
}

var newCiXcodeVersionsDataSource = listDataSource("ci_xcode_versions",
	"Lists the Xcode versions Xcode Cloud offers, with the macOS versions that run each and the destinations it tests on.",
	map[string]schema.Attribute{
		"xcode_versions": listOf("The Xcode versions.", map[string]schema.Attribute{
			"id":                 dsComputed("App Store Connect ID of the Xcode version."),
			"version":            dsComputed("Version, e.g. `26.0`."),
			"name":               dsComputed("Name, e.g. `Xcode 26`."),
			"mac_os_version_ids": dsStringList("App Store Connect IDs of the macOS versions that run this Xcode version."),
			"test_destinations": listOf("Simulators and Macs a test action can run on.", map[string]schema.Attribute{
				"kind":                   dsComputed("Kind of destination: `SIMULATOR` or `MAC`."),
				"device_type_name":       dsComputed("Name of the device type."),
				"device_type_identifier": dsComputed("Identifier of the device type."),
				"available_runtimes": listOf("Runtimes the device type runs.", map[string]schema.Attribute{
					"runtime_name":       dsComputed("Name of the runtime."),
					"runtime_identifier": dsComputed("Identifier of the runtime."),
				}),
			}),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *ciXcodeVersionsData, diags *diag.Diagnostics) {
		out, err := c.CiXcodeVersionsGetCollectionWithResponse(ctx, &gen.CiXcodeVersionsGetCollectionParams{
			Include: &[]gen.CiXcodeVersionsGetCollectionParamsInclude{gen.CiXcodeVersionsGetCollectionParamsIncludeMacOsVersions},
			Limit:   new(listAll), LimitMacOsVersions: new(50),
		})
		var all []gen.CiXcodeVersion
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.CiXcodeVersionsResponse) []gen.CiXcodeVersion { return p.Data },
				func(p *gen.CiXcodeVersionsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list Xcode versions", err)
			return
		}
		m.Versions = []ciXcodeVersionData{}
		for _, v := range all {
			a := asc.Value(v.Attributes)
			item := ciXcodeVersionData{ID: types.StringValue(v.Id), Version: stringValue(a.Version), Name: stringValue(a.Name), TestDestinations: []ciTestDestinationData{}}
			var macs []string
			if rel := v.Relationships; rel != nil && rel.MacOsVersions != nil {
				for _, d := range asc.Value(rel.MacOsVersions.Data) {
					macs = append(macs, d.Id)
				}
			}
			item.MacOSVersionIDs = stringsList(macs)
			for _, d := range asc.Value(a.TestDestinations) {
				destination := ciTestDestinationData{
					Kind: stringValue(d.Kind), DeviceTypeName: stringValue(d.DeviceTypeName), DeviceTypeIdentifier: stringValue(d.DeviceTypeIdentifier),
					AvailableRuntimes: []ciRuntimeData{},
				}
				for _, runtime := range asc.Value(d.AvailableRuntimes) {
					destination.AvailableRuntimes = append(destination.AvailableRuntimes, ciRuntimeData{
						RuntimeName: stringValue(runtime.RuntimeName), RuntimeIdentifier: stringValue(runtime.RuntimeIdentifier),
					})
				}
				item.TestDestinations = append(item.TestDestinations, destination)
			}
			m.Versions = append(m.Versions, item)
		}
	})

type ciMacOSVersionData struct {
	ID              types.String `tfsdk:"id"`
	Version         types.String `tfsdk:"version"`
	Name            types.String `tfsdk:"name"`
	XcodeVersionIDs types.List   `tfsdk:"xcode_version_ids"`
}

type ciMacOSVersionsData struct {
	Versions []ciMacOSVersionData `tfsdk:"mac_os_versions"`
}

var newCiMacOSVersionsDataSource = listDataSource("ci_mac_os_versions",
	"Lists the macOS versions Xcode Cloud offers, with the Xcode versions each runs.",
	map[string]schema.Attribute{
		"mac_os_versions": listOf("The macOS versions.", map[string]schema.Attribute{
			"id":                dsComputed("App Store Connect ID of the macOS version."),
			"version":           dsComputed("Version, e.g. `26.0`."),
			"name":              dsComputed("Name, e.g. `macOS Tahoe 26`."),
			"xcode_version_ids": dsStringList("App Store Connect IDs of the Xcode versions this macOS version runs."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *ciMacOSVersionsData, diags *diag.Diagnostics) {
		out, err := c.CiMacOsVersionsGetCollectionWithResponse(ctx, &gen.CiMacOsVersionsGetCollectionParams{
			Include: &[]gen.CiMacOsVersionsGetCollectionParamsInclude{gen.CiMacOsVersionsGetCollectionParamsIncludeXcodeVersions},
			Limit:   new(listAll), LimitXcodeVersions: new(50),
		})
		var all []gen.CiMacOsVersion
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.CiMacOsVersionsResponse) []gen.CiMacOsVersion { return p.Data },
				func(p *gen.CiMacOsVersionsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			apiError(diags, "list macOS versions", err)
			return
		}
		m.Versions = []ciMacOSVersionData{}
		for _, v := range all {
			a := asc.Value(v.Attributes)
			var xcodes []string
			if rel := v.Relationships; rel != nil && rel.XcodeVersions != nil {
				for _, d := range asc.Value(rel.XcodeVersions.Data) {
					xcodes = append(xcodes, d.Id)
				}
			}
			m.Versions = append(m.Versions, ciMacOSVersionData{
				ID: types.StringValue(v.Id), Version: stringValue(a.Version), Name: stringValue(a.Name), XcodeVersionIDs: stringsList(xcodes),
			})
		}
	})

// ---- data.apple_ci_workflows ----

type ciWorkflowData struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
}

type ciWorkflowsData struct {
	ProductID types.String     `tfsdk:"product_id"`
	Workflows []ciWorkflowData `tfsdk:"workflows"`
}

var newCiWorkflowsDataSource = listDataSource("ci_workflows",
	"Lists the workflows of an Xcode Cloud product.",
	map[string]schema.Attribute{
		"product_id": dsRequired("App Store Connect ID of the Xcode Cloud product; see `data.apple_ci_products`."),
		"workflows": listOf("The workflows.", map[string]schema.Attribute{
			"id":          dsComputed("App Store Connect ID of the workflow."),
			"name":        dsComputed("Name of the workflow."),
			"description": dsComputed("Description of the workflow."),
			"enabled":     dsBool("Whether the workflow starts builds."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *ciWorkflowsData, diags *diag.Diagnostics) {
		out, err := c.CiProductsWorkflowsGetToManyRelatedWithResponse(ctx, m.ProductID.ValueString(), &gen.CiProductsWorkflowsGetToManyRelatedParams{Limit: new(listAll)})
		var all []gen.CiWorkflow
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.CiWorkflowsResponse) []gen.CiWorkflow { return p.Data },
				func(p *gen.CiWorkflowsResponse) *string { return p.Links.Next })
		}
		if err != nil {
			lookupError(diags, "Xcode Cloud product", err)
			return
		}
		m.Workflows = []ciWorkflowData{}
		for _, w := range all {
			a := asc.Value(w.Attributes)
			m.Workflows = append(m.Workflows, ciWorkflowData{
				ID: types.StringValue(w.Id), Name: stringValue(a.Name), Description: stringValue(a.Description), Enabled: types.BoolPointerValue(a.IsEnabled),
			})
		}
	})

// ---- data.apple_ci_build_runs ----

type ciBuildRunData struct {
	ID                types.String `tfsdk:"id"`
	Number            types.Int64  `tfsdk:"number"`
	CreatedDate       types.String `tfsdk:"created_date"`
	StartedDate       types.String `tfsdk:"started_date"`
	FinishedDate      types.String `tfsdk:"finished_date"`
	ExecutionProgress types.String `tfsdk:"execution_progress"`
	CompletionStatus  types.String `tfsdk:"completion_status"`
	StartReason       types.String `tfsdk:"start_reason"`
	CancelReason      types.String `tfsdk:"cancel_reason"`
	PullRequestBuild  types.Bool   `tfsdk:"is_pull_request_build"`
	SourceCommitSHA   types.String `tfsdk:"source_commit_sha"`
}

type ciBuildRunsData struct {
	WorkflowID types.String     `tfsdk:"workflow_id"`
	ProductID  types.String     `tfsdk:"product_id"`
	Limit      types.Int64      `tfsdk:"limit"`
	Runs       []ciBuildRunData `tfsdk:"build_runs"`
}

var newCiBuildRunsDataSource = listDataSource("ci_build_runs",
	"Lists the most recent Xcode Cloud builds of a workflow or a product, newest first, with their progress and result.",
	map[string]schema.Attribute{
		"workflow_id": schema.StringAttribute{MarkdownDescription: "App Store Connect ID of a workflow. Set this or `product_id`.", Optional: true},
		"product_id":  schema.StringAttribute{MarkdownDescription: "App Store Connect ID of a product. Set this or `workflow_id`.", Optional: true},
		"limit": schema.Int64Attribute{
			MarkdownDescription: "How many builds to list, up to 200. Defaults to 10.", Optional: true,
		},
		"build_runs": listOf("The builds.", map[string]schema.Attribute{
			"id":                    dsComputed("App Store Connect ID of the build run."),
			"number":                dsInt("Number of the build."),
			"created_date":          dsComputed("When the build was created."),
			"started_date":          dsComputed("When the build started. Null until it does."),
			"finished_date":         dsComputed("When the build finished. Null until it does."),
			"execution_progress":    dsComputed("Progress of the build: `PENDING`, `RUNNING` or `COMPLETE`."),
			"completion_status":     dsComputed("Result of a complete build: `SUCCEEDED`, `FAILED`, `ERRORED`, `CANCELED` or `SKIPPED`."),
			"start_reason":          dsComputed("Why the build started, e.g. `GIT_REF_CHANGE`, `MANUAL` or `SCHEDULE`."),
			"cancel_reason":         dsComputed("Why the build was canceled: `AUTOMATICALLY_BY_NEWER_BUILD` or `MANUALLY_BY_USER`."),
			"is_pull_request_build": dsBool("Whether the build is of a pull request."),
			"source_commit_sha":     dsComputed("Commit the build is of."),
		}),
	},
	func(ctx context.Context, c *asc.Client, m *ciBuildRunsData, diags *diag.Diagnostics) {
		limit := 10
		if !m.Limit.IsNull() {
			limit = int(m.Limit.ValueInt64())
		}
		var runs []gen.CiBuildRun
		var err error
		if id := m.WorkflowID.ValueString(); id != "" {
			out, e := c.CiWorkflowsBuildRunsGetToManyRelatedWithResponse(ctx, id, &gen.CiWorkflowsBuildRunsGetToManyRelatedParams{
				Sort: &[]gen.CiWorkflowsBuildRunsGetToManyRelatedParamsSort{gen.CiWorkflowsBuildRunsGetToManyRelatedParamsSortMinusNumber}, Limit: &limit,
			})
			if err = asc.Err(out, e); err == nil {
				runs = out.JSON200.Data
			}
		} else {
			out, e := c.CiProductsBuildRunsGetToManyRelatedWithResponse(ctx, m.ProductID.ValueString(), &gen.CiProductsBuildRunsGetToManyRelatedParams{
				Sort: &[]gen.CiProductsBuildRunsGetToManyRelatedParamsSort{gen.CiProductsBuildRunsGetToManyRelatedParamsSortMinusNumber}, Limit: &limit,
			})
			if err = asc.Err(out, e); err == nil {
				runs = out.JSON200.Data
			}
		}
		if err != nil {
			lookupError(diags, "Xcode Cloud workflow or product", err)
			return
		}
		m.Runs = []ciBuildRunData{}
		for _, run := range runs {
			a := asc.Value(run.Attributes)
			m.Runs = append(m.Runs, ciBuildRunData{
				ID: types.StringValue(run.Id), Number: intValue(a.Number), CreatedDate: timeValue(a.CreatedDate), StartedDate: timeValue(a.StartedDate),
				FinishedDate: timeValue(a.FinishedDate), ExecutionProgress: stringValue(a.ExecutionProgress), CompletionStatus: stringValue(a.CompletionStatus),
				StartReason: stringValue(a.StartReason), CancelReason: stringValue(a.CancelReason), PullRequestBuild: types.BoolPointerValue(a.IsPullRequestBuild),
				SourceCommitSHA: stringValue(asc.Value(a.SourceCommit).CommitSha),
			})
		}
	}, datasourcevalidator.ExactlyOneOf(path.MatchRoot("workflow_id"), path.MatchRoot("product_id")))

// ---- action.apple_start_ci_build_run ----

type startBuildRunModel struct {
	WorkflowID     types.String `tfsdk:"workflow_id"`
	GitReferenceID types.String `tfsdk:"git_reference_id"`
	PullRequestID  types.String `tfsdk:"pull_request_id"`
	Clean          types.Bool   `tfsdk:"clean"`
}

var newStartCiBuildRunAction = newAction("start_ci_build_run",
	"Starts an Xcode Cloud build of a workflow for a branch, a tag or a pull request of its repository. The workflow "+
		"must be enabled. The action returns once the build is queued; `data.apple_ci_build_runs` reports its progress.",
	map[string]actionschema.Attribute{
		"workflow_id": actionString("App Store Connect ID of the workflow, e.g. `apple_ci_workflow.example.id`."),
		"git_reference_id": actionschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of the branch or tag to build; see `data.apple_scm_git_references`. Set this or `pull_request_id`.",
			Optional:            true,
		},
		"pull_request_id": actionschema.StringAttribute{
			MarkdownDescription: "App Store Connect ID of the pull request to build; see `data.apple_scm_pull_requests`. Set this or `git_reference_id`.",
			Optional:            true,
		},
		"clean": actionschema.BoolAttribute{
			MarkdownDescription: "Whether the build starts without the cached data of earlier builds. Unset uses the workflow's setting.",
			Optional:            true,
		},
	},
	func(ctx context.Context, c *asc.Client, m *startBuildRunModel) (string, error) {
		ref, pr := m.GitReferenceID.ValueString(), m.PullRequestID.ValueString()
		if (ref == "") == (pr == "") {
			return "", errors.New("set exactly one of git_reference_id and pull_request_id")
		}
		var body gen.CiBuildRunCreateRequest
		body.Data.Type = gen.CiBuildRunCreateRequestDataTypeCiBuildRuns
		if !m.Clean.IsNull() {
			asc.Alloc(&body.Data.Attributes).Clean = m.Clean.ValueBoolPointer()
		}
		rel := asc.Alloc(&body.Data.Relationships)
		w := asc.Alloc(&asc.Alloc(&rel.Workflow).Data)
		w.Id, w.Type = m.WorkflowID.ValueString(), gen.CiBuildRunCreateRequestDataRelationshipsWorkflowDataTypeCiWorkflows
		if ref != "" {
			d := asc.Alloc(&asc.Alloc(&rel.SourceBranchOrTag).Data)
			d.Id, d.Type = ref, gen.CiBuildRunCreateRequestDataRelationshipsSourceBranchOrTagDataTypeScmGitReferences
		} else {
			d := asc.Alloc(&asc.Alloc(&rel.PullRequest).Data)
			d.Id, d.Type = pr, gen.CiBuildRunCreateRequestDataRelationshipsPullRequestDataTypeScmPullRequests
		}
		out, err := c.CiBuildRunsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return fmt.Sprintf("Started build %d (%s)", asc.Value(asc.Value(out.JSON201.Data.Attributes).Number), out.JSON201.Data.Id), nil
	})
