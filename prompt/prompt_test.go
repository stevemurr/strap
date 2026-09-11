package prompt_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/prompt"
)

func TestRenderRoundTripAndCloneIsolation(t *testing.T) {
	original := prompt.Prompt{Role: `A "specialist"`, Instructions: []string{"Line one\nLine two", "Preserve café 界"}}
	copy := original.Clone()
	copy.Instructions[0] = "modified"
	if original.Instructions[0] != "Line one\nLine two" {
		t.Fatal("clone shares instructions")
	}
	rendered, err := original.Render()
	if err != nil {
		t.Fatal(err)
	}
	var decoded prompt.Prompt
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("lost fields: %+v", decoded)
	}
}
