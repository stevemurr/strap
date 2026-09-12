package jsonstream

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWireEquivalenceWithLargeUTF8AndBinary(t *testing.T) {
	input := struct {
		Text   string          `json:"text"`
		Bytes  []byte          `json:"bytes"`
		At     time.Time       `json:"at"`
		Raw    json.RawMessage `json:"raw"`
		Nested map[string]any  `json:"nested"`
		Empty  []int           `json:"empty,omitempty"`
	}{Text: strings.Repeat("<&✓\n", 10000), Bytes: bytes.Repeat([]byte{0, 1, 2, 255}, 10000), At: time.Now().UTC(), Raw: json.RawMessage(`{"ok":true}`), Nested: map[string]any{"nil": nil, "float": 1.5, "list": []string{"a", "b"}}}
	var out bytes.Buffer
	if err := Encode(&out, input); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if json.Unmarshal(out.Bytes(), &a) != nil || json.Unmarshal(want, &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("wire mismatch")
	}
}
