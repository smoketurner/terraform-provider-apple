package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

// Versioned metadata. Since App Store Connect API 4.4.1, the localizations
// and images of in-app purchases, subscriptions and subscription groups
// belong to versions: a version is edited in Prepare for Submission, goes
// through App Review, and is read-only after
// (https://developer.apple.com/documentation/appstoreconnectapi/working-with-in-app-purchase-versions).
// The provider hides versions, as it does app info: resources are keyed by
// the product (and locale), change the version being prepared, and start
// the next version when the current one has been reviewed.

// versionInfo is a version of a product's metadata.
type versionInfo struct {
	id     string
	number int
	state  string
}

// metadataVersions lists and starts the versions of one kind of product.
type metadataVersions struct {
	noun   string // e.g. "in-app purchase"
	list   func(ctx context.Context, c *asc.Client, parentID string) ([]versionInfo, error)
	create func(ctx context.Context, c *asc.Client, parentID string) (string, error)
}

var versionStatesInReview = []string{"READY_FOR_REVIEW", "WAITING_FOR_REVIEW", "IN_REVIEW"}

// find returns the version being prepared, if any; the one to read: that
// version, or else the latest one that isn't replaced; and the latest.
func (k metadataVersions) find(ctx context.Context, c *asc.Client, parentID string) (draft, current, latest *versionInfo, err error) {
	all, err := k.list(ctx, c, parentID)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range all {
		v := &all[i]
		if latest == nil || v.number > latest.number {
			latest = v
		}
		switch {
		case v.state == "PREPARE_FOR_SUBMISSION":
			draft = v
		case v.state != "REPLACED_WITH_NEW_VERSION" && (current == nil || v.number > current.number):
			current = v
		}
	}
	if draft != nil {
		current = draft
	}
	return draft, current, latest, nil
}

// versionLocks serializes starting a product's next version: a product's
// localization and image resources apply in parallel, and only one of them
// may create the version.
var versionLocks = struct {
	sync.Mutex
	byProduct map[string]*sync.Mutex
}{byProduct: map[string]*sync.Mutex{}}

func lockVersions(key string) func() {
	versionLocks.Lock()
	lock, ok := versionLocks.byProduct[key]
	if !ok {
		lock = &sync.Mutex{}
		versionLocks.byProduct[key] = lock
	}
	versionLocks.Unlock()
	lock.Lock()
	return lock.Unlock
}

// editable returns the product's version being prepared, starting one when
// the latest has been reviewed.
func (k metadataVersions) editable(ctx context.Context, c *asc.Client, parentID string) (string, error) {
	defer lockVersions(k.noun + "/" + parentID)()
	draft, _, latest, err := k.find(ctx, c, parentID)
	if err != nil {
		return "", err
	}
	if draft != nil {
		return draft.id, nil
	}
	if latest != nil && slices.Contains(versionStatesInReview, latest.state) {
		return "", fmt.Errorf("the %s is in review; its metadata can change once review finishes", k.noun)
	}
	id, err := k.create(ctx, c, parentID)
	if asc.HasCode(err, "STATE_ERROR") {
		// Another client started the version first; use it.
		if draft, _, _, lerr := k.find(ctx, c, parentID); lerr == nil && draft != nil {
			return draft.id, nil
		}
	}
	return id, err
}

// clearable returns v, or "" for an unset optional value, which clears it
// on update.
func clearable(v *string) *string {
	if v == nil {
		return new("")
	}
	return v
}

// ---- versioned localizations ----

// localizationField is an attribute of a localization besides its locale.
type localizationField struct {
	attr     string // e.g. "custom_app_name"
	json     string // e.g. "customAppName"
	desc     string
	required bool
	min, max int
}

// localization is a localization read from the API.
type localization struct {
	id     string
	locale string
	values map[string]*string // by JSON name
}

// localizationAPI is the part of the API a kind of versioned localization
// needs. Values are keyed by JSON name; a nil value clears it on update.
type localizationAPI struct {
	typeName    string
	description string
	parent      string // attribute name of the product's ID
	parentDesc  string
	versions    metadataVersions
	fields      []localizationField
	list        func(ctx context.Context, c *asc.Client, versionID string) ([]localization, error)
	create      func(ctx context.Context, c *asc.Client, versionID, locale string, values map[string]*string) (*localization, error)
	update      func(ctx context.Context, c *asc.Client, id string, values map[string]*string) (*localization, error)
	remove      func(ctx context.Context, c *asc.Client, id string) error
}

var (
	_ resource.ResourceWithConfigure   = &versionedLocalizationResource{}
	_ resource.ResourceWithImportState = &versionedLocalizationResource{}
	_ resource.ResourceWithIdentity    = &versionedLocalizationResource{}
)

type versionedLocalizationResource struct {
	resourceIdentity
	api    localizationAPI
	client *asc.Client
}

func newVersionedLocalizationResource(api localizationAPI) func() resource.Resource {
	return func() resource.Resource {
		return &versionedLocalizationResource{api: api, resourceIdentity: resourceIdentity{attrs: []identityAttribute{
			{name: api.parent, description: api.parentDesc, state: api.parent},
			{name: "locale", description: "Locale, e.g. `en-US`.", state: "locale"},
		}}}
	}
}

func (r *versionedLocalizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.api.typeName
}

func (r *versionedLocalizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		r.api.parent: replacedString(r.api.parentDesc),
		"locale":     replacedString("Locale, e.g. `en-US`.", localeValidator),
		"id":         schema.StringAttribute{MarkdownDescription: "App Store Connect ID of the localization, which changes with each version.", Computed: true},
	}
	for _, f := range r.api.fields {
		v := []validator.String{stringvalidator.LengthBetween(f.min, f.max)}
		if f.required {
			attrs[f.attr] = schema.StringAttribute{MarkdownDescription: f.desc, Required: true, Validators: v}
		} else {
			attrs[f.attr] = optionalString(f.desc, v...)
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: r.api.description + " Changes go to the version being prepared; when the current version " +
			"has been reviewed, applying starts the next one. Changes fail while a version is in review. Destroying " +
			"the resource deletes the localization from the version being prepared, and otherwise leaves it. Import " +
			"it with `<" + r.api.parent + ">/<locale>`.",
		Attributes: attrs,
	}
}

func (r *versionedLocalizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *versionedLocalizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *versionedLocalizationResource) find(ctx context.Context, versionID, locale string) (*localization, error) {
	all, err := r.api.list(ctx, r.client, versionID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].locale == locale {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

// write sets the locale's localization in the version being prepared: a
// new version may start with copies of the previous one's localizations, so
// it updates the copy when there is one.
func (r *versionedLocalizationResource) write(ctx context.Context, parentID, locale string, values map[string]*string) (*localization, error) {
	versionID, err := r.api.versions.editable(ctx, r.client, parentID)
	if err != nil {
		return nil, err
	}
	existing, err := r.find(ctx, versionID, locale)
	switch {
	case err == nil:
		return r.api.update(ctx, r.client, existing.id, values)
	case errors.Is(err, errNoMatch):
		return r.api.create(ctx, r.client, versionID, locale, values)
	}
	return nil, err
}

// planned reads the product, locale and field values of a plan.
func (r *versionedLocalizationResource) planned(ctx context.Context, plan tfsdk.Plan, diags *diag.Diagnostics) (parent, locale string, values map[string]*string) {
	var p, l types.String
	diags.Append(plan.GetAttribute(ctx, path.Root(r.api.parent), &p)...)
	diags.Append(plan.GetAttribute(ctx, path.Root("locale"), &l)...)
	values = map[string]*string{}
	for _, f := range r.api.fields {
		var v types.String
		diags.Append(plan.GetAttribute(ctx, path.Root(f.attr), &v)...)
		values[f.json] = knownString(v)
	}
	return p.ValueString(), l.ValueString(), values
}

// record writes l's ID and values to state.
func (r *versionedLocalizationResource) record(ctx context.Context, state *tfsdk.State, l *localization, diags *diag.Diagnostics) {
	diags.Append(state.SetAttribute(ctx, path.Root("id"), l.id)...)
	for _, f := range r.api.fields {
		diags.Append(state.SetAttribute(ctx, path.Root(f.attr), stringValue(l.values[f.json]))...)
	}
}

func (r *versionedLocalizationResource) upsert(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics) {
	parent, locale, values := r.planned(ctx, plan, diags)
	if diags.HasError() {
		return
	}
	l, err := r.write(ctx, parent, locale, values)
	if err != nil {
		apiError(diags, "set "+strings.ReplaceAll(r.api.typeName, "_", " "), err)
		return
	}
	state.Raw = plan.Raw
	r.record(ctx, state, l, diags)
}

func (r *versionedLocalizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *versionedLocalizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	r.upsert(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

func (r *versionedLocalizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var parent, locale types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(r.api.parent), &parent)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("locale"), &locale)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, current, _, err := r.api.versions.find(ctx, r.client, parent.ValueString())
	var l *localization
	if err == nil && current == nil {
		err = errNoMatch
	}
	if err == nil {
		l, err = r.find(ctx, current.id, locale.ValueString())
	}
	if asc.IsNotFound(err) || errors.Is(err, errNoMatch) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+strings.ReplaceAll(r.api.typeName, "_", " "), err)
		return
	}
	r.record(ctx, &resp.State, l, &resp.Diagnostics)
}

// Delete removes the locale from the version being prepared; reviewed
// versions keep it.
func (r *versionedLocalizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var parent, locale types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(r.api.parent), &parent)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("locale"), &locale)...)
	if resp.Diagnostics.HasError() {
		return
	}
	draft, _, _, err := r.api.versions.find(ctx, r.client, parent.ValueString())
	if asc.IsNotFound(err) || err == nil && draft == nil {
		return
	}
	var l *localization
	if err == nil {
		l, err = r.find(ctx, draft.id, locale.ValueString())
	}
	if errors.Is(err, errNoMatch) {
		return
	}
	if err == nil {
		if err = r.api.remove(ctx, r.client, l.id); asc.IsNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "delete "+strings.ReplaceAll(r.api.typeName, "_", " "), err)
	}
}
