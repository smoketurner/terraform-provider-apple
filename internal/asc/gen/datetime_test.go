package gen

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDateTimeParse(t *testing.T) {
	want := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, s := range []string{
		"2027-01-02T03:04:05.000+0000",
		"2027-01-02T03:04:05+0000",
		"2027-01-02T03:04:05Z",
		"2027-01-01T22:04:05.000-0500",
	} {
		var d DateTime
		if err := json.Unmarshal([]byte(`"`+s+`"`), &d); err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		if !d.Equal(want) {
			t.Errorf("%s parsed as %v, want %v", s, d.Time, want)
		}
	}
}

func TestDateTimeParseErrors(t *testing.T) {
	for _, doc := range []string{`"2027-01-02"`, `"soon"`, `1700000000`, `""`} {
		var d DateTime
		if err := json.Unmarshal([]byte(doc), &d); err == nil {
			t.Errorf("%s parsed as %v", doc, d.Time)
		}
	}
	d := DateTime{time.Unix(1, 0)}
	if err := d.UnmarshalJSON([]byte("null")); err != nil || d.Unix() != 1 {
		t.Errorf("null changed the value to %v (%v)", d.Time, err)
	}
	if _, err := ParseDateTime(""); err == nil {
		t.Error("empty string parsed")
	}
}

func TestDateTimeMarshal(t *testing.T) {
	d := DateTime{time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)}
	b, err := json.Marshal(struct {
		At DateTime `json:"at"`
	}{d})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"at":"2027-01-02T03:04:05Z"}` {
		t.Errorf("marshaled %s", b)
	}
	var back DateTime
	if err := back.UnmarshalText([]byte(d.Format(time.RFC3339))); err != nil || !back.Equal(d.Time) {
		t.Errorf("round trip = %v, %v", back.Time, err)
	}
}
