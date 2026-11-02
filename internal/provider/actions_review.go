package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// Submitting a version to App Review and releasing it are steps of a
// release, not state, so they are actions. A review submission collects the
// items to review for one platform of an app
// (https://developer.apple.com/documentation/appstoreconnectapi/review-submissions).

type versionActionModel struct {
	VersionID types.String `tfsdk:"version_id"`
}

var versionActionAttribute = actionString("App Store Connect ID of the App Store version, e.g. `apple_app_store_version.example.id`.")

// versionTarget returns the app and platform of a version.
func versionTarget(ctx context.Context, c *asc.Client, id string) (appID string, platform gen.Platform, err error) {
	out, err := c.AppStoreVersionsGetInstanceWithResponse(ctx, id, &gen.AppStoreVersionsGetInstanceParams{
		Include: &[]gen.AppStoreVersionsGetInstanceParamsInclude{gen.AppStoreVersionsGetInstanceParamsIncludeApp},
	})
	if err := asc.Err(out, err); err != nil {
		return "", "", err
	}
	app := versionAppID(&out.JSON200.Data)
	if app.IsNull() {
		return "", "", errors.New("the API did not return the version's app")
	}
	return app.ValueString(), asc.Value(asc.Value(out.JSON200.Data.Attributes).Platform), nil
}

// reviewSubmissions lists an app's review submissions for platform in
// states.
func reviewSubmissions(ctx context.Context, c *asc.Client, appID string, platform gen.Platform, states ...gen.ReviewSubmissionsGetCollectionParamsFilterState) ([]gen.ReviewSubmission, error) {
	out, err := c.ReviewSubmissionsGetCollectionWithResponse(ctx, &gen.ReviewSubmissionsGetCollectionParams{
		FilterApp:      []string{appID},
		FilterPlatform: &[]gen.ReviewSubmissionsGetCollectionParamsFilterPlatform{gen.ReviewSubmissionsGetCollectionParamsFilterPlatform(platform)},
		FilterState:    &states,
		Limit:          new(listAll),
	})
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return out.JSON200.Data, nil
}

// updateSubmission submits a review submission, or cancels it.
func updateSubmission(ctx context.Context, c *asc.Client, id string, submit bool) error {
	var body gen.ReviewSubmissionUpdateRequest
	body.Data.Type, body.Data.Id = gen.ReviewSubmissionUpdateRequestDataTypeReviewSubmissions, id
	a := asc.Alloc(&body.Data.Attributes)
	if submit {
		a.Submitted = new(true)
	} else {
		a.Canceled = new(true)
	}
	out, err := c.ReviewSubmissionsUpdateInstanceWithResponse(ctx, id, body)
	return asc.Err(out, err)
}

var newSubmitForReviewAction = newAction("submit_for_review",
	"Submits an App Store version to App Review. The version needs a build and the metadata App Review requires. "+
		"The version is added to the platform's review submission that is being prepared, or a new one, which is "+
		"then submitted.",
	map[string]schema.Attribute{"version_id": versionActionAttribute},
	func(ctx context.Context, c *asc.Client, m *versionActionModel) (string, error) {
		appID, platform, err := versionTarget(ctx, c, m.VersionID.ValueString())
		if err != nil {
			return "", err
		}
		open, err := reviewSubmissions(ctx, c, appID, platform, gen.ReviewSubmissionsGetCollectionParamsFilterStateREADYFORREVIEW)
		if err != nil {
			return "", err
		}
		var submissionID string
		if len(open) > 0 {
			submissionID = open[0].Id
		} else {
			var body gen.ReviewSubmissionCreateRequest
			body.Data.Type = gen.ReviewSubmissionCreateRequestDataTypeReviewSubmissions
			asc.Alloc(&body.Data.Attributes).Platform = new(platform)
			body.Data.Relationships.App.Data.Id = appID
			body.Data.Relationships.App.Data.Type = gen.ReviewSubmissionCreateRequestDataRelationshipsAppDataTypeApps
			out, err := c.ReviewSubmissionsCreateInstanceWithResponse(ctx, body)
			if err := asc.Err(out, err); err != nil {
				return "", err
			}
			submissionID = out.JSON201.Data.Id
		}
		var item gen.ReviewSubmissionItemCreateRequest
		item.Data.Type = gen.ReviewSubmissionItemCreateRequestDataTypeReviewSubmissionItems
		item.Data.Relationships.ReviewSubmission.Data.Id = submissionID
		item.Data.Relationships.ReviewSubmission.Data.Type = gen.ReviewSubmissionItemCreateRequestDataRelationshipsReviewSubmissionDataTypeReviewSubmissions
		v := asc.Alloc(&asc.Alloc(&item.Data.Relationships.AppStoreVersion).Data)
		v.Id, v.Type = m.VersionID.ValueString(), gen.ReviewSubmissionItemCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		out, err := c.ReviewSubmissionItemsCreateInstanceWithResponse(ctx, item)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		err = updateSubmission(ctx, c, submissionID, true)
		return "Submitted App Store version " + m.VersionID.ValueString() + " for review", err
	})

var newCancelReviewSubmissionAction = newAction("cancel_review_submission",
	"Removes an App Store version's platform from App Review by canceling its open review submission. The version "+
		"becomes Developer Rejected, can be edited again, and can be submitted again.",
	map[string]schema.Attribute{"version_id": versionActionAttribute},
	func(ctx context.Context, c *asc.Client, m *versionActionModel) (string, error) {
		appID, platform, err := versionTarget(ctx, c, m.VersionID.ValueString())
		if err != nil {
			return "", err
		}
		open, err := reviewSubmissions(ctx, c, appID, platform, gen.ReviewSubmissionsGetCollectionParamsFilterStateWAITINGFORREVIEW,
			gen.ReviewSubmissionsGetCollectionParamsFilterStateINREVIEW, gen.ReviewSubmissionsGetCollectionParamsFilterStateUNRESOLVEDISSUES)
		if err != nil {
			return "", err
		}
		if len(open) == 0 {
			return "", fmt.Errorf("app %s has no %s review submission in review", appID, platform)
		}
		for _, sub := range open {
			err := updateSubmission(ctx, c, sub.Id, false)
			if err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("Canceled %d review submissions", len(open)), nil
	})

var newReleaseVersionAction = newAction("release_version",
	"Releases an approved App Store version that is waiting for release (Pending Developer Release) because its "+
		"`release_type` is `MANUAL`.",
	map[string]schema.Attribute{"version_id": versionActionAttribute},
	func(ctx context.Context, c *asc.Client, m *versionActionModel) (string, error) {
		var body gen.AppStoreVersionReleaseRequestCreateRequest
		body.Data.Type = gen.AppStoreVersionReleaseRequestCreateRequestDataTypeAppStoreVersionReleaseRequests
		rel := &body.Data.Relationships.AppStoreVersion.Data
		rel.Id, rel.Type = m.VersionID.ValueString(), gen.AppStoreVersionReleaseRequestCreateRequestDataRelationshipsAppStoreVersionDataTypeAppStoreVersions
		out, err := c.AppStoreVersionReleaseRequestsCreateInstanceWithResponse(ctx, body)
		return "Released App Store version " + m.VersionID.ValueString(), asc.Err(out, err)
	})

type assignDeclarationModel struct {
	BuildID       types.String `tfsdk:"build_id"`
	DeclarationID types.String `tfsdk:"declaration_id"`
}

var newAssignEncryptionDeclarationAction = newAction("assign_encryption_declaration",
	"Assigns an encryption declaration to a build of the same app, which answers the build's export compliance question.",
	map[string]schema.Attribute{
		"build_id":       buildIDAttribute,
		"declaration_id": actionString("App Store Connect ID of the encryption declaration, e.g. `apple_app_encryption_declaration.example.id`."),
	},
	func(ctx context.Context, c *asc.Client, m *assignDeclarationModel) (string, error) {
		var body gen.BuildAppEncryptionDeclarationLinkageRequest
		body.Data.Id, body.Data.Type = m.DeclarationID.ValueString(), gen.BuildAppEncryptionDeclarationLinkageRequestDataTypeAppEncryptionDeclarations
		out, err := c.BuildsAppEncryptionDeclarationUpdateToOneRelationshipWithResponse(ctx, m.BuildID.ValueString(), body)
		return "Assigned encryption declaration " + m.DeclarationID.ValueString() + " to build " + m.BuildID.ValueString(), asc.Err(out, err)
	})
