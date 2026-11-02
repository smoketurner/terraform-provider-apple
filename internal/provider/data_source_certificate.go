package provider

import (
	"context"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

type certificateData struct {
	ID                 types.String `tfsdk:"id"`
	SerialNumber       types.String `tfsdk:"serial_number"`
	CertificateType    types.String `tfsdk:"certificate_type"`
	Name               types.String `tfsdk:"name"`
	DisplayName        types.String `tfsdk:"display_name"`
	Platform           types.String `tfsdk:"platform"`
	ExpirationDate     types.String `tfsdk:"expiration_date"`
	Activated          types.Bool   `tfsdk:"activated"`
	CertificateContent types.String `tfsdk:"certificate_content"`
}

func (m *certificateData) apply(c *gen.Certificate) {
	var r certificateModel
	r.apply(c, time.Now())
	*m = certificateData{
		ID: r.ID, SerialNumber: r.SerialNumber, CertificateType: r.CertificateType, Name: r.Name, DisplayName: r.DisplayName,
		Platform: r.Platform, ExpirationDate: r.ExpirationDate, Activated: r.Activated, CertificateContent: r.CertificateContent,
	}
}

var certificateDataAttributes = map[string]schema.Attribute{
	"id":                  dsComputed("App Store Connect ID of the certificate."),
	"serial_number":       dsComputed("Serial number, in hexadecimal."),
	"certificate_type":    dsComputed("Certificate type."),
	"name":                dsComputed("Name of the certificate."),
	"display_name":        dsComputed("Display name of the certificate."),
	"platform":            dsComputed("Platform of the certificate, if it has one."),
	"expiration_date":     dsComputed("Expiration date, in RFC 3339 format."),
	"activated":           schema.BoolAttribute{MarkdownDescription: "Whether the certificate is activated.", Computed: true},
	"certificate_content": dsSensitive("The certificate, base64-encoded DER."),
}

var newCertificateDataSource = lookupDataSource("certificate",
	"Looks up a certificate by its App Store Connect ID or serial number.", "serial_number",
	withAttributes(certificateDataAttributes, map[string]schema.Attribute{
		"id":            dsLookup("App Store Connect ID of the certificate. Set this or `serial_number`."),
		"serial_number": dsLookup("Serial number, in hexadecimal, in either case. Set this or `id`."),
	}),
	func(ctx context.Context, c *asc.Client, m *certificateData, diags *diag.Diagnostics) {
		var found *gen.Certificate
		var err error
		if id := m.ID.ValueString(); id != "" {
			out, e := c.CertificatesGetInstanceWithResponse(ctx, id, nil)
			if err = asc.Err(out, e); err == nil {
				found = &out.JSON200.Data
			}
		} else {
			found, err = certificateBySerial(ctx, c, m.SerialNumber.ValueString())
		}
		if err != nil {
			lookupError(diags, "certificate", err)
			return
		}
		m.apply(found)
	})

type certificatesData struct {
	CertificateTypes types.Set         `tfsdk:"certificate_types"`
	DisplayNames     types.Set         `tfsdk:"display_names"`
	SerialNumbers    types.Set         `tfsdk:"serial_numbers"`
	MerchantID       types.String      `tfsdk:"merchant_id"`
	PassTypeID       types.String      `tfsdk:"pass_type_id"`
	Certificates     []certificateData `tfsdk:"certificates"`
}

var newCertificatesDataSource = listDataSource("certificates", "Lists the certificates that match every given filter.",
	map[string]schema.Attribute{
		"certificate_types": dsFilter("Certificate types to match.", certificateTypes...),
		"display_names":     dsFilter("Display names to match."),
		"serial_numbers":    dsFilter("Serial numbers to match, in hexadecimal, in either case."),
		"merchant_id":       schema.StringAttribute{MarkdownDescription: "Only certificates of this merchant ID.", Optional: true},
		"pass_type_id":      schema.StringAttribute{MarkdownDescription: "Only certificates of this pass type ID.", Optional: true},
		"certificates":      listOf("The matching certificates, ordered by ID.", certificateDataAttributes),
	},
	readCertificates,
	datasourcevalidator.Conflicting(path.MatchRoot("merchant_id"), path.MatchRoot("pass_type_id")))

func readCertificates(ctx context.Context, c *asc.Client, m *certificatesData, diags *diag.Diagnostics) {
	var all []gen.Certificate
	var err error
	switch {
	case !m.MerchantID.IsNull():
		var out *gen.MerchantIdsCertificatesGetToManyRelatedResponse
		out, err = c.MerchantIdsCertificatesGetToManyRelatedWithResponse(ctx, m.MerchantID.ValueString(),
			&gen.MerchantIdsCertificatesGetToManyRelatedParams{Limit: new(listAll)})
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.CertificatesResponse) []gen.Certificate { return p.Data },
				func(p *gen.CertificatesResponse) *string { return p.Links.Next })
		}
	case !m.PassTypeID.IsNull():
		var out *gen.PassTypeIdsCertificatesGetToManyRelatedResponse
		out, err = c.PassTypeIdsCertificatesGetToManyRelatedWithResponse(ctx, m.PassTypeID.ValueString(),
			&gen.PassTypeIdsCertificatesGetToManyRelatedParams{Limit: new(listAll)})
		if err = asc.Err(out, err); err == nil {
			all, err = asc.All(ctx, c, out.JSON200,
				func(p *gen.CertificatesResponse) []gen.Certificate { return p.Data },
				func(p *gen.CertificatesResponse) *string { return p.Links.Next })
		}
	default:
		all, err = findCertificates(ctx, c, gen.CertificatesGetCollectionParams{
			FilterCertificateType: filterParam[gen.CertificatesGetCollectionParamsFilterCertificateType](ctx, m.CertificateTypes, diags),
			FilterDisplayName:     filterParam[string](ctx, m.DisplayNames, diags),
		})
	}
	if err != nil {
		apiError(diags, "list certificates", err)
		return
	}
	serials := setStrings(ctx, m.SerialNumbers, diags)
	m.Certificates = []certificateData{}
	for i := range all {
		a := asc.Value(all[i].Attributes)
		if !passes(ctx, m.CertificateTypes, string(asc.Value(a.CertificateType)), diags) ||
			!passes(ctx, m.DisplayNames, asc.Value(a.DisplayName), diags) || !matchesSerial(serials, asc.Value(a.SerialNumber)) {
			continue
		}
		var item certificateData
		item.apply(&all[i])
		m.Certificates = append(m.Certificates, item)
	}
}

func matchesSerial(serials []string, serial string) bool {
	if len(serials) == 0 {
		return true
	}
	for _, s := range serials {
		if strings.EqualFold(s, serial) {
			return true
		}
	}
	return false
}
