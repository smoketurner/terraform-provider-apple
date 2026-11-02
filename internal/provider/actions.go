package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// TestFlight build workflow steps are actions: builds come from uploads, so
// distributing, reviewing and expiring them are one-off operations rather
// than state. They need Terraform 1.14 or later.

// actionDef describes an action: its schema attributes, and invoke, which
// reads the configuration into M and calls App Store Connect.
type actionDef[M any] struct {
	typeName    string
	description string
	attributes  map[string]schema.Attribute
	invoke      func(ctx context.Context, c *asc.Client, m *M) (string, error)
	client      *asc.Client
}

func (a *actionDef[M]) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + a.typeName
}

func (a *actionDef[M]) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: a.description, Attributes: a.attributes}
}

func (a *actionDef[M]) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	a.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (a *actionDef[M]) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m M
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	done, err := a.invoke(ctx, a.client, &m)
	if err != nil {
		apiError(&resp.Diagnostics, strings.ReplaceAll(a.typeName, "_", " "), err)
		return
	}
	resp.SendProgress(action.InvokeProgressEvent{Message: done})
}

func newAction[M any](typeName, description string, attributes map[string]schema.Attribute,
	invoke func(ctx context.Context, c *asc.Client, m *M) (string, error),
) func() action.Action {
	return func() action.Action {
		return &actionDef[M]{typeName: typeName, description: description, attributes: attributes, invoke: invoke}
	}
}

func actionString(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Required: true}
}

func actionSet(desc string) schema.SetAttribute {
	return schema.SetAttribute{
		MarkdownDescription: desc,
		Required:            true,
		ElementType:         types.StringType,
		Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
	}
}

// ids reads a set of IDs; the action's schema guarantees it is known.
func ids(ctx context.Context, set types.Set) []string {
	var d diag.Diagnostics
	return setStrings(ctx, set, &d)
}

type buildGroupsModel struct {
	BuildID      types.String `tfsdk:"build_id"`
	BetaGroupIDs types.Set    `tfsdk:"beta_group_ids"`
}

type buildTestersModel struct {
	BuildID       types.String `tfsdk:"build_id"`
	BetaTesterIDs types.Set    `tfsdk:"beta_tester_ids"`
}

type buildModel struct {
	BuildID types.String `tfsdk:"build_id"`
}

type exportComplianceModel struct {
	BuildID                 types.String `tfsdk:"build_id"`
	UsesNonExemptEncryption types.Bool   `tfsdk:"uses_non_exempt_encryption"`
}

type inviteModel struct {
	AppID        types.String `tfsdk:"app_id"`
	BetaTesterID types.String `tfsdk:"beta_tester_id"`
}

type appTestersModel struct {
	AppID         types.String `tfsdk:"app_id"`
	BetaTesterIDs types.Set    `tfsdk:"beta_tester_ids"`
}

var buildIDAttribute = actionString("App Store Connect ID of the build, e.g. `data.apple_build.latest.id`.")

func buildGroupsLinkages(ctx context.Context, set types.Set) gen.BuildBetaGroupsLinkagesRequest {
	var body gen.BuildBetaGroupsLinkagesRequest
	for _, id := range ids(ctx, set) {
		e := asc.Push(&body.Data)
		e.Id, e.Type = id, gen.BuildBetaGroupsLinkagesRequestDataTypeBetaGroups
	}
	return body
}

func buildTestersLinkages(ctx context.Context, set types.Set) gen.BuildIndividualTestersLinkagesRequest {
	var body gen.BuildIndividualTestersLinkagesRequest
	for _, id := range ids(ctx, set) {
		e := asc.Push(&body.Data)
		e.Id, e.Type = id, gen.BuildIndividualTestersLinkagesRequestDataTypeBetaTesters
	}
	return body
}

var newAddBuildToBetaGroupsAction = newAction("add_build_to_beta_groups",
	"Adds a build to beta groups, so their testers can test it. The first build added to an external group goes to "+
		"TestFlight App Review; submit it with `apple_submit_beta_review`.",
	map[string]schema.Attribute{"build_id": buildIDAttribute, "beta_group_ids": actionSet("App Store Connect IDs of the beta groups.")},
	func(ctx context.Context, c *asc.Client, m *buildGroupsModel) (string, error) {
		out, err := c.BuildsBetaGroupsCreateToManyRelationshipWithResponse(ctx, m.BuildID.ValueString(), buildGroupsLinkages(ctx, m.BetaGroupIDs))
		return fmt.Sprintf("Added build %s to %d beta groups", m.BuildID.ValueString(), len(m.BetaGroupIDs.Elements())), asc.Err(out, err)
	})

var newRemoveBuildFromBetaGroupsAction = newAction("remove_build_from_beta_groups",
	"Removes a build from beta groups, so their testers can no longer install it.",
	map[string]schema.Attribute{"build_id": buildIDAttribute, "beta_group_ids": actionSet("App Store Connect IDs of the beta groups.")},
	func(ctx context.Context, c *asc.Client, m *buildGroupsModel) (string, error) {
		out, err := c.BuildsBetaGroupsDeleteToManyRelationshipWithResponse(ctx, m.BuildID.ValueString(), buildGroupsLinkages(ctx, m.BetaGroupIDs))
		return fmt.Sprintf("Removed build %s from %d beta groups", m.BuildID.ValueString(), len(m.BetaGroupIDs.Elements())), asc.Err(out, err)
	})

var newAddBuildTestersAction = newAction("add_build_testers",
	"Gives individual beta testers access to a build, outside of any group.",
	map[string]schema.Attribute{"build_id": buildIDAttribute, "beta_tester_ids": actionSet("App Store Connect IDs of the beta testers.")},
	func(ctx context.Context, c *asc.Client, m *buildTestersModel) (string, error) {
		out, err := c.BuildsIndividualTestersCreateToManyRelationshipWithResponse(ctx, m.BuildID.ValueString(), buildTestersLinkages(ctx, m.BetaTesterIDs))
		return fmt.Sprintf("Gave %d testers build %s", len(m.BetaTesterIDs.Elements()), m.BuildID.ValueString()), asc.Err(out, err)
	})

var newRemoveBuildTestersAction = newAction("remove_build_testers",
	"Removes individual beta testers' access to a build. Access through groups is unaffected.",
	map[string]schema.Attribute{"build_id": buildIDAttribute, "beta_tester_ids": actionSet("App Store Connect IDs of the beta testers.")},
	func(ctx context.Context, c *asc.Client, m *buildTestersModel) (string, error) {
		out, err := c.BuildsIndividualTestersDeleteToManyRelationshipWithResponse(ctx, m.BuildID.ValueString(), buildTestersLinkages(ctx, m.BetaTesterIDs))
		return fmt.Sprintf("Removed %d testers from build %s", len(m.BetaTesterIDs.Elements()), m.BuildID.ValueString()), asc.Err(out, err)
	})

var newSubmitBetaReviewAction = newAction("submit_beta_review",
	"Submits a build to TestFlight App Review, which external testing requires. The build needs an answer to the "+
		"export compliance question (`apple_set_build_export_compliance`), and only one build of each version can be in "+
		"review at a time.",
	map[string]schema.Attribute{"build_id": buildIDAttribute},
	func(ctx context.Context, c *asc.Client, m *buildModel) (string, error) {
		var body gen.BetaAppReviewSubmissionCreateRequest
		body.Data.Type = gen.BetaAppReviewSubmissionCreateRequestDataTypeBetaAppReviewSubmissions
		body.Data.Relationships.Build.Data.Id = m.BuildID.ValueString()
		body.Data.Relationships.Build.Data.Type = gen.BetaAppReviewSubmissionCreateRequestDataRelationshipsBuildDataTypeBuilds
		out, err := c.BetaAppReviewSubmissionsCreateInstanceWithResponse(ctx, body)
		return "Submitted build " + m.BuildID.ValueString() + " for beta app review", asc.Err(out, err)
	})

var newNotifyBetaTestersAction = newAction("notify_beta_testers",
	"Notifies a build's testers that it is available, for builds whose automatic notification is off "+
		"(`apple_build_beta_detail`).",
	map[string]schema.Attribute{"build_id": buildIDAttribute},
	func(ctx context.Context, c *asc.Client, m *buildModel) (string, error) {
		var body gen.BuildBetaNotificationCreateRequest
		body.Data.Type = gen.BuildBetaNotificationCreateRequestDataTypeBuildBetaNotifications
		body.Data.Relationships.Build.Data.Id = m.BuildID.ValueString()
		body.Data.Relationships.Build.Data.Type = gen.BuildBetaNotificationCreateRequestDataRelationshipsBuildDataTypeBuilds
		out, err := c.BuildBetaNotificationsCreateInstanceWithResponse(ctx, body)
		return "Notified the testers of build " + m.BuildID.ValueString(), asc.Err(out, err)
	})

var newInviteBetaTesterAction = newAction("invite_beta_tester",
	"Sends, or sends again, a TestFlight invitation to a tester who belongs to one of the app's groups or builds.",
	map[string]schema.Attribute{
		"app_id":         actionString("App Store Connect ID of the app."),
		"beta_tester_id": actionString("App Store Connect ID of the beta tester."),
	},
	func(ctx context.Context, c *asc.Client, m *inviteModel) (string, error) {
		var body gen.BetaTesterInvitationCreateRequest
		body.Data.Type = gen.BetaTesterInvitationCreateRequestDataTypeBetaTesterInvitations
		body.Data.Relationships.App.Data.Id = m.AppID.ValueString()
		body.Data.Relationships.App.Data.Type = gen.BetaTesterInvitationCreateRequestDataRelationshipsAppDataTypeApps
		d := asc.Alloc(&asc.Alloc(&body.Data.Relationships.BetaTester).Data)
		d.Id, d.Type = m.BetaTesterID.ValueString(), gen.BetaTesterInvitationCreateRequestDataRelationshipsBetaTesterDataTypeBetaTesters
		out, err := c.BetaTesterInvitationsCreateInstanceWithResponse(ctx, body)
		return "Invited tester " + m.BetaTesterID.ValueString(), asc.Err(out, err)
	})

func updateBuild(ctx context.Context, c *asc.Client, id string, set func(*gen.BuildUpdateRequest)) error {
	var body gen.BuildUpdateRequest
	body.Data.Type, body.Data.Id = gen.BuildUpdateRequestDataTypeBuilds, id
	set(&body)
	out, err := c.BuildsUpdateInstanceWithResponse(ctx, id, body)
	return asc.Err(out, err)
}

var newSetBuildExportComplianceAction = newAction("set_build_export_compliance",
	"Answers a build's export compliance question: whether it uses encryption that is not exempt from US export "+
		"regulations. Builds cannot be tested until it is answered, unless the app's Info.plist answers it.",
	map[string]schema.Attribute{
		"build_id": buildIDAttribute,
		"uses_non_exempt_encryption": schema.BoolAttribute{
			MarkdownDescription: "Whether the build uses non-exempt encryption.",
			Required:            true,
		},
	},
	func(ctx context.Context, c *asc.Client, m *exportComplianceModel) (string, error) {
		err := updateBuild(ctx, c, m.BuildID.ValueString(), func(b *gen.BuildUpdateRequest) {
			asc.Alloc(&b.Data.Attributes).UsesNonExemptEncryption = new(m.UsesNonExemptEncryption.ValueBool())
		})
		return "Set the export compliance of build " + m.BuildID.ValueString(), err
	})

var newExpireBuildAction = newAction("expire_build",
	"Expires a build, so testers can no longer install it. Expiring cannot be undone.",
	map[string]schema.Attribute{"build_id": buildIDAttribute},
	func(ctx context.Context, c *asc.Client, m *buildModel) (string, error) {
		err := updateBuild(ctx, c, m.BuildID.ValueString(), func(b *gen.BuildUpdateRequest) {
			asc.Alloc(&b.Data.Attributes).Expired = new(true)
		})
		return "Expired build " + m.BuildID.ValueString(), err
	})

var newRemoveBetaTestersFromAppAction = newAction("remove_beta_testers_from_app",
	"Removes testers from every beta group and build of an app, without deleting them from other apps.",
	map[string]schema.Attribute{
		"app_id":          actionString("App Store Connect ID of the app."),
		"beta_tester_ids": actionSet("App Store Connect IDs of the beta testers."),
	},
	func(ctx context.Context, c *asc.Client, m *appTestersModel) (string, error) {
		var body gen.AppBetaTestersLinkagesRequest
		for _, id := range ids(ctx, m.BetaTesterIDs) {
			e := asc.Push(&body.Data)
			e.Id, e.Type = id, gen.AppBetaTestersLinkagesRequestDataTypeBetaTesters
		}
		out, err := c.AppsBetaTestersDeleteToManyRelationshipWithResponse(ctx, m.AppID.ValueString(), body)
		return fmt.Sprintf("Removed %d testers from app %s", len(body.Data), m.AppID.ValueString()), asc.Err(out, err)
	})
