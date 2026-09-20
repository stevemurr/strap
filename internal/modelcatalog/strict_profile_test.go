package modelcatalog

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBundledProfilesHaveNoLegacyStrictness(t *testing.T) {
	var c catalog
	d := json.NewDecoder(bytes.NewReader(bundled))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		t.Fatal(err)
	}
}
