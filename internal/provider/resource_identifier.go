package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// identifier is a merchant ID or pass type ID: a unique identifier and a
// name that can be changed.
type identifier struct {
	ID, Identifier, Name string
}

// identifierKind holds the operations of one identifier type.
type identifierKind struct {
	typeName    string // e.g. "merchant_id"
	noun        string // e.g. "merchant ID"
	description string
	example     string
	create      func(ctx context.Context, c *asc.Client, identifier, name string) (*identifier, error)
	get         func(ctx context.Context, c *asc.Client, id string) (*identifier, error)
	rename      func(ctx context.Context, c *asc.Client, id, name string) (*identifier, error)
	remove      func(ctx context.Context, c *asc.Client, id string) error
	// list returns the identifiers matching the filters; nil filters match
	// everything.
	list func(ctx context.Context, c *asc.Client, identifiers, names *[]string) ([]identifier, error)
}

// byIdentifier finds an identifier by its exact identifier string.
func (k identifierKind) byIdentifier(ctx context.Context, c *asc.Client, value string) (*identifier, error) {
	all, err := k.list(ctx, c, &[]string{value}, nil)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Identifier == value {
			return &all[i], nil
		}
	}
	return nil, errNoMatch
}

func (k identifierKind) resolve(ctx context.Context, c *asc.Client, key string) (string, error) {
	return resolveByID(ctx, key,
		func(ctx context.Context, id string) error {
			_, err := k.get(ctx, c, id)
			return err
		},
		func(ctx context.Context, key string) (string, error) {
			found, err := k.byIdentifier(ctx, c, key)
			if err != nil {
				return "", err
			}
			return found.ID, nil
		})
}

func merchantIdentifier(m *gen.MerchantId) *identifier {
	return &identifier{ID: m.Id, Identifier: asc.Value(asc.Value(m.Attributes).Identifier), Name: asc.Value(asc.Value(m.Attributes).Name)}
}

func passTypeIdentifier(p *gen.PassTypeId) *identifier {
	return &identifier{ID: p.Id, Identifier: asc.Value(asc.Value(p.Attributes).Identifier), Name: asc.Value(asc.Value(p.Attributes).Name)}
}

var merchantIDKind = identifierKind{
	typeName:    "merchant_id",
	noun:        "merchant ID",
	description: "Registers a merchant ID for Apple Pay.",
	example:     "merchant.com.example",
	create: func(ctx context.Context, c *asc.Client, value, name string) (*identifier, error) {
		var body gen.MerchantIdCreateRequest
		body.Data.Type = gen.MerchantIdCreateRequestDataTypeMerchantIds
		body.Data.Attributes.Identifier, body.Data.Attributes.Name = value, name
		out, err := c.MerchantIdsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return merchantIdentifier(&out.JSON201.Data), nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (*identifier, error) {
		out, err := c.MerchantIdsGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return merchantIdentifier(&out.JSON200.Data), nil
	},
	rename: func(ctx context.Context, c *asc.Client, id, name string) (*identifier, error) {
		var body gen.MerchantIdUpdateRequest
		body.Data.Type, body.Data.Id = gen.MerchantIdUpdateRequestDataTypeMerchantIds, id
		asc.Alloc(&body.Data.Attributes).Name = new(name)
		out, err := c.MerchantIdsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return merchantIdentifier(&out.JSON200.Data), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.MerchantIdsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
	list: func(ctx context.Context, c *asc.Client, identifiers, names *[]string) ([]identifier, error) {
		out, err := c.MerchantIdsGetCollectionWithResponse(ctx, &gen.MerchantIdsGetCollectionParams{
			FilterIdentifier: identifiers, FilterName: names, Limit: new(listAll),
		})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.MerchantIdsResponse) []gen.MerchantId { return p.Data },
			func(p *gen.MerchantIdsResponse) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		ids := make([]identifier, len(all))
		for i := range all {
			ids[i] = *merchantIdentifier(&all[i])
		}
		return ids, nil
	},
}

var passTypeIDKind = identifierKind{
	typeName:    "pass_type_id",
	noun:        "pass type ID",
	description: "Registers a pass type ID for signing Wallet passes.",
	example:     "pass.com.example",
	create: func(ctx context.Context, c *asc.Client, value, name string) (*identifier, error) {
		var body gen.PassTypeIdCreateRequest
		body.Data.Type = gen.PassTypeIdCreateRequestDataTypePassTypeIds
		body.Data.Attributes.Identifier, body.Data.Attributes.Name = value, name
		out, err := c.PassTypeIdsCreateInstanceWithResponse(ctx, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return passTypeIdentifier(&out.JSON201.Data), nil
	},
	get: func(ctx context.Context, c *asc.Client, id string) (*identifier, error) {
		out, err := c.PassTypeIdsGetInstanceWithResponse(ctx, id, nil)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return passTypeIdentifier(&out.JSON200.Data), nil
	},
	rename: func(ctx context.Context, c *asc.Client, id, name string) (*identifier, error) {
		var body gen.PassTypeIdUpdateRequest
		body.Data.Type, body.Data.Id = gen.PassTypeIdUpdateRequestDataTypePassTypeIds, id
		asc.Alloc(&body.Data.Attributes).Name = new(name)
		out, err := c.PassTypeIdsUpdateInstanceWithResponse(ctx, id, body)
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		return passTypeIdentifier(&out.JSON200.Data), nil
	},
	remove: func(ctx context.Context, c *asc.Client, id string) error {
		out, err := c.PassTypeIdsDeleteInstanceWithResponse(ctx, id)
		return asc.Err(out, err)
	},
	list: func(ctx context.Context, c *asc.Client, identifiers, names *[]string) ([]identifier, error) {
		out, err := c.PassTypeIdsGetCollectionWithResponse(ctx, &gen.PassTypeIdsGetCollectionParams{
			FilterIdentifier: identifiers, FilterName: names, Limit: new(listAll),
		})
		if err := asc.Err(out, err); err != nil {
			return nil, err
		}
		all, err := asc.All(ctx, c, out.JSON200,
			func(p *gen.PassTypeIdsResponse) []gen.PassTypeId { return p.Data },
			func(p *gen.PassTypeIdsResponse) *string { return p.Links.Next })
		if err != nil {
			return nil, err
		}
		ids := make([]identifier, len(all))
		for i := range all {
			ids[i] = *passTypeIdentifier(&all[i])
		}
		return ids, nil
	},
}

var (
	_ resource.ResourceWithConfigure   = &identifierResource{}
	_ resource.ResourceWithImportState = &identifierResource{}
	_ resource.ResourceWithIdentity    = &identifierResource{}
)

type identifierResource struct {
	resourceIdentity
	kind   identifierKind
	client *asc.Client
}

type identifierModel struct {
	ID         types.String `tfsdk:"id"`
	Identifier types.String `tfsdk:"identifier"`
	Name       types.String `tfsdk:"name"`
}

func (m *identifierModel) apply(i *identifier) {
	m.ID, m.Identifier, m.Name = types.StringValue(i.ID), types.StringValue(i.Identifier), types.StringValue(i.Name)
}

func newIdentifierResource(kind identifierKind) func() resource.Resource {
	return func() resource.Resource {
		return &identifierResource{kind: kind, resourceIdentity: resourceIdentity{
			attrs:   idIdentity("App Store Connect ID of the " + kind.noun + "."),
			resolve: kind.resolve,
			keyName: "identifier",
		}}
	}
}

func (r *identifierResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind.typeName
}

func (r *identifierResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: r.kind.description + " Import it by its App Store Connect ID or its identifier.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("App Store Connect ID of the " + r.kind.noun + "."),
			"identifier": replacedString("Identifier, e.g. `"+r.kind.example+"`. Changing it creates a new "+r.kind.noun+".",
				stringvalidator.LengthAtLeast(1)),
			"name": schema.StringAttribute{
				MarkdownDescription: "Description of the " + r.kind.noun + ". Changes are made in place.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
		},
	}
}

func (r *identifierResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromResource(req, resp)
}

func (r *identifierResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importState(ctx, r.client, req, resp)
}

func (r *identifierResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan identifierModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.kind.create(ctx, r.client, plan.Identifier.ValueString(), plan.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "create "+r.kind.noun, err)
		return
	}
	plan.apply(created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *identifierResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State, &req.State)
	var state identifierModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.kind.get(ctx, r.client, state.ID.ValueString())
	if asc.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read "+r.kind.noun, err)
		return
	}
	state.apply(found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *identifierResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.setIdentity(ctx, resp.Identity, &resp.Diagnostics, &resp.State)
	var plan identifierModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.kind.rename(ctx, r.client, plan.ID.ValueString(), plan.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "update "+r.kind.noun, err)
		return
	}
	plan.apply(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *identifierResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state identifierModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.kind.remove(ctx, r.client, state.ID.ValueString()); err != nil && !asc.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete "+r.kind.noun, err)
	}
}
