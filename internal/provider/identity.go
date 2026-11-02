package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

// errNoMatch is returned by natural key lookups that find nothing.
var errNoMatch = errors.New("no match")

// resourceIdentity gives a resource an identity schema mirroring its import
// ID, which is "/"-separated when it has several parts. Resources embed it
// and defer setIdentity at the top of Create, Read and Update, so every path
// records the identity of whatever ends up in state.
type resourceIdentity struct {
	attrs []identityAttribute
	// resolve maps the first part of a string import ID, which may be a
	// natural key such as a bundle identifier, to the resource's ID. Nil
	// means the import ID is the ID.
	resolve func(ctx context.Context, c *asc.Client, key string) (string, error)
	// keyName describes the natural key in errors, e.g. "bundle identifier".
	keyName string
}

type identityAttribute struct {
	name        string
	description string
	// state is the state attribute that holds the value.
	state string
}

func idIdentity(description string) []identityAttribute {
	return []identityAttribute{{name: "id", description: description, state: "id"}}
}

func (ri resourceIdentity) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	attrs := make(map[string]identityschema.Attribute, len(ri.attrs))
	for _, a := range ri.attrs {
		attrs[a.name] = identityschema.StringAttribute{Description: a.description, RequiredForImport: true}
	}
	resp.IdentitySchema = identityschema.Schema{Attributes: attrs}
}

// importState sets the state attributes from the import ID or identity,
// resolving a natural key in a string ID with c.
func (ri resourceIdentity) importState(ctx context.Context, c *asc.Client, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := ri.importParts(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.ID != "" && ri.resolve != nil {
		id, err := ri.resolve(ctx, c, parts[0])
		switch {
		case errors.Is(err, errNoMatch):
			resp.Diagnostics.AddError("Cannot import", fmt.Sprintf("No resource has the ID or %s %q.", ri.keyName, parts[0]))
			return
		case err != nil:
			apiError(&resp.Diagnostics, "look up "+parts[0], err)
			return
		}
		parts[0] = id
	}
	for i, a := range ri.attrs {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(a.state), parts[i])...)
	}
}

func (ri resourceIdentity) importParts(ctx context.Context, req resource.ImportStateRequest, diags *diag.Diagnostics) []string {
	names := make([]string, len(ri.attrs))
	for i, a := range ri.attrs {
		names[i] = a.name
	}
	if req.ID != "" {
		parts := strings.Split(req.ID, "/")
		if len(names) == 1 {
			parts = []string{req.ID}
		}
		if len(parts) != len(names) || slices.Contains(parts, "") {
			diags.AddError("Invalid import ID", fmt.Sprintf("Expected an import ID in the form %q, got %q.", strings.Join(names, "/"), req.ID))
			return nil
		}
		return parts
	}
	parts := make([]string, len(names))
	for i, name := range names {
		var v types.String
		diags.Append(req.Identity.GetAttribute(ctx, path.Root(name), &v)...)
		if diags.HasError() {
			return nil
		}
		if v.ValueString() == "" {
			diags.AddAttributeError(path.Root(name), "Invalid import identity", fmt.Sprintf("Identity attribute %q must not be empty.", name))
			return nil
		}
		parts[i] = v.ValueString()
	}
	return parts
}

// setIdentity copies the identity attributes from the first of states that
// holds the resource, and does nothing when none does, such as after a failed
// create. Read passes its prior state too, because the framework requires an
// identity even when Read removes the resource.
func (ri resourceIdentity) setIdentity(ctx context.Context, identity *tfsdk.ResourceIdentity, diags *diag.Diagnostics, states ...*tfsdk.State) {
	if identity == nil {
		return
	}
	for _, state := range states {
		if state.Raw.IsNull() {
			continue
		}
		for _, a := range ri.attrs {
			var v types.String
			diags.Append(state.GetAttribute(ctx, path.Root(a.state), &v)...)
			diags.Append(identity.SetAttribute(ctx, path.Root(a.name), v)...)
		}
		return
	}
}

// resolveByID returns key when it is the ID of an existing resource, which
// get reports, and otherwise the ID found by lookup.
func resolveByID(ctx context.Context, key string, get func(ctx context.Context, id string) error, lookup func(ctx context.Context, key string) (string, error)) (string, error) {
	err := get(ctx, key)
	if err == nil {
		return key, nil
	}
	if !asc.IsNotFound(err) {
		return "", err
	}
	return lookup(ctx, key)
}
