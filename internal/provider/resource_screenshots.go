package provider

import (
	"context"
	"strings"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// The values of gen.ScreenshotDisplayType and gen.PreviewType; a test checks
// them against the generated types.
var (
	screenshotDisplayTypes = []string{
		"APP_IPHONE_67", "APP_IPHONE_61", "APP_IPHONE_65", "APP_IPHONE_58", "APP_IPHONE_55", "APP_IPHONE_47", "APP_IPHONE_40",
		"APP_IPHONE_35", "APP_IPAD_PRO_3GEN_129", "APP_IPAD_PRO_3GEN_11", "APP_IPAD_PRO_129", "APP_IPAD_105", "APP_IPAD_97",
		"APP_DESKTOP", "APP_WATCH_ULTRA", "APP_WATCH_SERIES_10", "APP_WATCH_SERIES_7", "APP_WATCH_SERIES_4", "APP_WATCH_SERIES_3",
		"APP_APPLE_TV", "APP_APPLE_VISION_PRO", "IMESSAGE_APP_IPHONE_67", "IMESSAGE_APP_IPHONE_61", "IMESSAGE_APP_IPHONE_65",
		"IMESSAGE_APP_IPHONE_58", "IMESSAGE_APP_IPHONE_55", "IMESSAGE_APP_IPHONE_47", "IMESSAGE_APP_IPHONE_40",
		"IMESSAGE_APP_IPAD_PRO_3GEN_129", "IMESSAGE_APP_IPAD_PRO_3GEN_11", "IMESSAGE_APP_IPAD_PRO_129", "IMESSAGE_APP_IPAD_105",
		"IMESSAGE_APP_IPAD_97",
	}
	previewTypes = []string{
		"IPHONE_67", "IPHONE_61", "IPHONE_65", "IPHONE_58", "IPHONE_55", "IPHONE_47", "IPHONE_40", "IPHONE_35",
		"IPAD_PRO_3GEN_129", "IPAD_PRO_3GEN_11", "IPAD_PRO_129", "IPAD_105", "IPAD_97", "DESKTOP", "APPLE_TV", "APPLE_VISION_PRO",
	}
)

// mediaFailure describes why App Store Connect rejected a file.
func mediaFailure(state string, errs *[]gen.AppMediaStateError) string {
	if state != "FAILED" {
		return ""
	}
	var parts []string
	for _, e := range asc.Value(errs) {
		parts = append(parts, asc.Value(e.Code)+": "+asc.Value(e.Description))
	}
	if len(parts) == 0 {
		return "processing failed"
	}
	return strings.Join(parts, "; ")
}

var screenshotAPI = mediaAPI{
	typeName: "app_screenshot_set",
	noun:     "screenshot",
	description: "Sets an App Store version localization's screenshots for one display type, in order. App Store " +
		"Connect needs screenshots for each device family the app supports; see \"Screenshot specifications\" for sizes.",
	types:    screenshotDisplayTypes,
	maxFiles: 10,
	findSet: func(ctx context.Context, c *asc.Client, localizationID, typ string) (string, error) {
		out, err := c.AppStoreVersionLocalizationsAppScreenshotSetsGetToManyRelatedWithResponse(ctx, localizationID,
			&gen.AppStoreVersionLocalizationsAppScreenshotSetsGetToManyRelatedParams{
				FilterScreenshotDisplayType: &[]gen.AppStoreVersionLocalizationsAppScreenshotSetsGetToManyRelatedParamsFilterScreenshotDisplayType{
					gen.AppStoreVersionLocalizationsAppScreenshotSetsGetToManyRelatedParamsFilterScreenshotDisplayType(typ),
				},
			})
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		for _, s := range out.JSON200.Data {
			if string(asc.Value(asc.Value(s.Attributes).ScreenshotDisplayType)) == typ {
				return s.Id, nil
			}
		}
		return "", errNoMatch
	},
	getSet: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppScreenshotSetsGetInstanceWithResponse(ctx, id, nil)
		return asc.Err(out, err)
	},
	createSet: func(ctx context.Context, c *asc.Client, localizationID, typ string) (string, error) {
		var body gen.AppScreenshotSetCreateRequest
		body.Data.Type = gen.AppScreenshotSetCreateRequestDataTypeAppScreenshotSets
		body.Data.Attributes.ScreenshotDisplayType = gen.ScreenshotDisplayType(typ)
		d := asc.Alloc(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).AppStoreVersionLocalization).Data)
		d.Id, d.Type = localizationID, gen.AppScreenshotSetCreateRequestDataRelationshipsAppStoreVersionLocalizationDataTypeAppStoreVersionLocalizations
		out, err := c.AppScreenshotSetsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
	deleteSet: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppScreenshotSetsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
	list: func(ctx context.Context, c *asc.Client, setID string) ([]mediaFile, error) {
		out, err := c.AppScreenshotSetsAppScreenshotsGetToManyRelatedWithResponse(ctx, setID, &gen.AppScreenshotSetsAppScreenshotsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		var files []mediaFile
		for _, s := range out.JSON200.Data {
			a := asc.Value(s.Attributes)
			state := string(asc.Value(asc.Value(a.AssetDeliveryState).State))
			files = append(files, mediaFile{id: s.Id, checksum: asc.Value(a.SourceFileChecksum), failed: state == "FAILED" || state == "AWAITING_UPLOAD"})
		}
		return files, nil
	},
	reserve: func(ctx context.Context, c *asc.Client, setID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.AppScreenshotCreateRequest
		body.Data.Type = gen.AppScreenshotCreateRequestDataTypeAppScreenshots
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		d := &body.Data.Relationships.AppScreenshotSet.Data
		d.Id, d.Type = setID, gen.AppScreenshotCreateRequestDataRelationshipsAppScreenshotSetDataTypeAppScreenshotSets
		out, err := c.AppScreenshotsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.AppScreenshotUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppScreenshotUpdateRequestDataTypeAppScreenshots, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.AppScreenshotsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		state := asc.Value(asc.Value(out.JSON200.Data.Attributes).AssetDeliveryState)
		return mediaFailure(string(asc.Value(state.State)), state.Errors), nil
	},
	deleteFile: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppScreenshotsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
	reorder: func(ctx context.Context, c *asc.Client, setID string, ids []string) error {
		var body gen.AppScreenshotSetAppScreenshotsLinkagesRequest
		for _, id := range ids {
			e := asc.Push(&body.Data)
			e.Id, e.Type = id, gen.AppScreenshotSetAppScreenshotsLinkagesRequestDataTypeAppScreenshots
		}
		out, err := c.AppScreenshotSetsAppScreenshotsReplaceToManyRelationshipWithResponse(ctx, setID, body)
		return asc.Err(out, err)
	},
}

var previewAPI = mediaAPI{
	typeName: "app_preview_set",
	noun:     "app preview",
	description: "Sets an App Store version localization's app preview videos for one device type, in order; up to " +
		"three. App Store Connect processes videos after upload, which can take a while; see \"App preview specifications\".",
	types:    previewTypes,
	maxFiles: 3,
	findSet: func(ctx context.Context, c *asc.Client, localizationID, typ string) (string, error) {
		out, err := c.AppStoreVersionLocalizationsAppPreviewSetsGetToManyRelatedWithResponse(ctx, localizationID,
			&gen.AppStoreVersionLocalizationsAppPreviewSetsGetToManyRelatedParams{
				FilterPreviewType: &[]gen.AppStoreVersionLocalizationsAppPreviewSetsGetToManyRelatedParamsFilterPreviewType{
					gen.AppStoreVersionLocalizationsAppPreviewSetsGetToManyRelatedParamsFilterPreviewType(typ),
				},
			})
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		for _, s := range out.JSON200.Data {
			if string(asc.Value(asc.Value(s.Attributes).PreviewType)) == typ {
				return s.Id, nil
			}
		}
		return "", errNoMatch
	},
	getSet: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppPreviewSetsGetInstanceWithResponse(ctx, id, nil)
		return asc.Err(out, err)
	},
	createSet: func(ctx context.Context, c *asc.Client, localizationID, typ string) (string, error) {
		var body gen.AppPreviewSetCreateRequest
		body.Data.Type = gen.AppPreviewSetCreateRequestDataTypeAppPreviewSets
		body.Data.Attributes.PreviewType = gen.PreviewType(typ)
		d := asc.Alloc(&asc.Alloc(&asc.Alloc(&body.Data.Relationships).AppStoreVersionLocalization).Data)
		d.Id, d.Type = localizationID, gen.AppPreviewSetCreateRequestDataRelationshipsAppStoreVersionLocalizationDataTypeAppStoreVersionLocalizations
		out, err := c.AppPreviewSetsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		return out.JSON201.Data.Id, nil
	},
	deleteSet: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppPreviewSetsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
	list: func(ctx context.Context, c *asc.Client, setID string) ([]mediaFile, error) {
		out, err := c.AppPreviewSetsAppPreviewsGetToManyRelatedWithResponse(ctx, setID, &gen.AppPreviewSetsAppPreviewsGetToManyRelatedParams{Limit: new(listAll)})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		var files []mediaFile
		for _, p := range out.JSON200.Data {
			a := asc.Value(p.Attributes)
			state := string(asc.Value(asc.Value(a.VideoDeliveryState).State))
			files = append(files, mediaFile{id: p.Id, checksum: asc.Value(a.SourceFileChecksum), failed: state == "FAILED" || state == "AWAITING_UPLOAD"})
		}
		return files, nil
	},
	reserve: func(ctx context.Context, c *asc.Client, setID, name string, size int) (string, []gen.UploadOperation, error) {
		var body gen.AppPreviewCreateRequest
		body.Data.Type = gen.AppPreviewCreateRequestDataTypeAppPreviews
		body.Data.Attributes.FileName, body.Data.Attributes.FileSize = name, size
		d := &body.Data.Relationships.AppPreviewSet.Data
		d.Id, d.Type = setID, gen.AppPreviewCreateRequestDataRelationshipsAppPreviewSetDataTypeAppPreviewSets
		out, err := c.AppPreviewsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return "", nil, err
		}
		return out.JSON201.Data.Id, asc.Value(asc.Value(out.JSON201.Data.Attributes).UploadOperations), nil
	},
	commit: func(ctx context.Context, c *asc.Client, id, checksum string) (string, error) {
		var body gen.AppPreviewUpdateRequest
		body.Data.Type, body.Data.Id = gen.AppPreviewUpdateRequestDataTypeAppPreviews, id
		a := asc.Alloc(&body.Data.Attributes)
		a.Uploaded, a.SourceFileChecksum = new(true), new(checksum)
		out, err := c.AppPreviewsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return "", err
		}
		state := asc.Value(asc.Value(out.JSON200.Data.Attributes).VideoDeliveryState)
		return mediaFailure(string(asc.Value(state.State)), state.Errors), nil
	},
	deleteFile: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.AppPreviewsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
	reorder: func(ctx context.Context, c *asc.Client, setID string, ids []string) error {
		var body gen.AppPreviewSetAppPreviewsLinkagesRequest
		for _, id := range ids {
			e := asc.Push(&body.Data)
			e.Id, e.Type = id, gen.AppPreviewSetAppPreviewsLinkagesRequestDataTypeAppPreviews
		}
		out, err := c.AppPreviewSetsAppPreviewsReplaceToManyRelationshipWithResponse(ctx, setID, body)
		return asc.Err(out, err)
	},
}

var (
	newScreenshotSetResource = newMediaSetResource(screenshotAPI)
	newPreviewSetResource    = newMediaSetResource(previewAPI)
)
