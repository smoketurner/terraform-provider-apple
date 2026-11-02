package asc

import (
	"testing"

	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func TestAllocAndAppend(t *testing.T) {
	var p gen.Profile
	a := Alloc(&p.Attributes)
	a.Name = new("dev")
	if Alloc(&p.Attributes) != a {
		t.Error("Alloc replaced an allocated value")
	}
	rel := Alloc(&p.Relationships)
	c := Alloc(&rel.Certificates)
	Append(&c.Data).Id = "C1"
	Append(&c.Data).Id = "C2"
	if got := *p.Relationships.Certificates.Data; len(got) != 2 || got[1].Id != "C2" {
		t.Errorf("certificates = %+v", got)
	}
	if Value(p.Attributes.Name) != "dev" || Value(p.Attributes.Uuid) != "" {
		t.Error("Value did not dereference")
	}
}
