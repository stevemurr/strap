package jsonstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Embedded is an exported embedded DTO field, which the encoder requires to
// carry an explicit JSON name.
type Embedded struct {
	N int `json:"n"`
}

// shortWriter fails once a fixed number of bytes have been accepted, standing in
// for a connection that drops mid-record.
type shortWriter struct {
	budget int
	err    error
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.budget {
		n := w.budget
		w.budget = 0
		return n, w.err
	}
	w.budget -= len(p)
	return len(p), nil
}

// Encoding matches encoding/json for the DTO shapes the wire format uses.
func TestEncodeMatchesStandardLibraryAcrossKinds(t *testing.T) {
	type inner struct {
		N int `json:"n"`
	}
	value := struct {
		Skipped    string `json:"-"`
		unexported int    //nolint:unused // present to prove it is skipped
		Untagged   string
		I8         int8              `json:"i8"`
		I16        int16             `json:"i16"`
		I32        int32             `json:"i32"`
		I64        int64             `json:"i64"`
		U          uint              `json:"u"`
		U16        uint16            `json:"u16"`
		U32        uint32            `json:"u32"`
		U64        uint64            `json:"u64"`
		F32        float32           `json:"f32"`
		F64        float64           `json:"f64"`
		Yes        bool              `json:"yes"`
		Array      [3]int            `json:"array"`
		Structs    []inner           `json:"structs"`
		Pointer    *inner            `json:"pointer"`
		NilPointer *inner            `json:"nil_pointer"`
		NilSlice   []int             `json:"nil_slice"`
		NilMap     map[string]string `json:"nil_map"`
		NilRaw     json.RawMessage   `json:"nil_raw"`
		Any        any               `json:"any"`
		NilAny     any               `json:"nil_any"`
		OmitStr    string            `json:"omit_str,omitempty"`
		OmitNum    int               `json:"omit_num,omitempty"`
		OmitMap    map[string]int    `json:"omit_map,omitempty"`
		KeptNum    int               `json:"kept_num,omitempty"`
	}{
		Skipped: "never", unexported: 1, Untagged: "kept",
		I8: -8, I16: -16, I32: -32, I64: -64, U: 1, U16: 16, U32: 32, U64: 64,
		F32: 1.5, F64: -2.25, Yes: true, Array: [3]int{1, 2, 3},
		Structs: []inner{{1}, {2}}, Pointer: &inner{7}, Any: "boxed", KeptNum: 9,
	}
	var out bytes.Buffer
	if err := Encode(&out, value); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got, expected any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("wire mismatch\n got: %s\nwant: %s", out.String(), want)
	}
	if strings.Contains(out.String(), "never") {
		t.Fatal(`a "-" tagged field reached the wire`)
	}
}

// Values the wire format cannot represent are refused rather than written as
// something approximate.
func TestEncodeRejectsUnrepresentableValues(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"invalid UTF-8 string", struct {
			S string `json:"s"`
		}{string([]byte{0xff, 0xfe})}},
		{"invalid UTF-8 map key", map[string]int{string([]byte{0xff}): 1}},
		{"invalid raw JSON", struct {
			R json.RawMessage `json:"r"`
		}{json.RawMessage(`{`)}},
		{"non-string map key", map[int]string{1: "a"}},
		{"channel", struct {
			C chan int `json:"c"`
		}{make(chan int)}},
		{"function", struct {
			F func() `json:"f"`
		}{func() {}}},
		{"complex number", struct {
			C complex128 `json:"c"`
		}{complex(1, 2)}},
	}
	for _, tc := range cases {
		if err := Encode(&bytes.Buffer{}, tc.value); err == nil {
			t.Fatal("encoded an unrepresentable value:", tc.name)
		}
	}
	// An embedded field has no name of its own on the wire, so it must be given
	// one explicitly rather than inheriting its type name.
	if err := Encode(&bytes.Buffer{}, struct{ Embedded }{Embedded{1}}); err == nil {
		t.Fatal("encoded an embedded field with no explicit JSON name")
	}
}

// Deeply nested values are bounded so a cyclic or pathological DTO cannot
// exhaust the stack.
func TestEncodeBoundsNestingDepth(t *testing.T) {
	var deep any = 1
	for i := 0; i < 200; i++ {
		deep = []any{deep}
	}
	if err := Encode(&bytes.Buffer{}, deep); err == nil {
		t.Fatal("encoded a value past the nesting limit")
	}
	var shallow any = 1
	for i := 0; i < 20; i++ {
		shallow = []any{shallow}
	}
	if err := Encode(&bytes.Buffer{}, shallow); err != nil {
		t.Fatal(err)
	}
}

// A writer that fails partway through surfaces its error from whichever stage
// of the encoding was in flight, and never reports success.
func TestEncodeReportsWriterFailureAtEveryStage(t *testing.T) {
	value := struct {
		Text   string            `json:"text"`
		Bytes  []byte            `json:"bytes"`
		Raw    json.RawMessage   `json:"raw"`
		List   []int             `json:"list"`
		Map    map[string]string `json:"map"`
		Nested struct {
			N int `json:"n"`
		} `json:"nested"`
		Float float64 `json:"float"`
	}{
		Text:  strings.Repeat("wide ✓ text ", 300),
		Bytes: bytes.Repeat([]byte{1, 2, 3}, 100),
		Raw:   json.RawMessage(`{"ok":true}`),
		List:  []int{1, 2, 3},
		Map:   map[string]string{"k": "v"},
		Float: 1.25,
	}
	var full bytes.Buffer
	if err := Encode(&full, value); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("connection lost")
	for budget := 0; budget < full.Len(); budget++ {
		w := &shortWriter{budget: budget, err: sentinel}
		if err := Encode(w, value); err == nil {
			t.Fatal("encoding reported success after the writer failed at byte", budget)
		}
	}
	// The full budget still succeeds.
	if err := Encode(&shortWriter{budget: full.Len(), err: sentinel}, value); err != nil {
		t.Fatal(err)
	}
}
