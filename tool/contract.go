package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// InputContractVersion identifies the single object-input tool protocol.
const InputContractVersion = "object-input-v1"

// Contract is an immutable, opaque input contract. Custom tools expose one built
// by NewParameters; arbitrary raw JSON schemas cannot bypass registration checks.
type Contract struct{ root *parameterNode }

func (p Parameters[A]) Contract() Contract      { return Contract{p.root} }
func (f Func[A]) InputContract() Contract       { return f.Spec.Parameters.Contract() }
func (t *composedTool) InputContract() Contract { return Contract{t.root} }
func (c Contract) Schema() json.RawMessage {
	if c.root == nil {
		return nil
	}
	raw, _ := json.Marshal(c.root.schema())
	return raw
}

// ValidateTool enforces a single contract for custom tools as well as built-ins.
func ValidateTool(t Tool) error {
	if t == nil || (reflect.ValueOf(t).Kind() == reflect.Pointer && reflect.ValueOf(t).IsNil()) {
		return fmt.Errorf("nil tool")
	}
	if checked, ok := t.(interface{ Validate() error }); ok {
		if err := checked.Validate(); err != nil {
			return err
		}
	}
	declared, ok := t.(interface{ InputContract() Contract })
	if !ok || declared.InputContract().root == nil {
		return fmt.Errorf("tool must expose a typed InputContract")
	}
	def := t.Definition()
	if def.Name == "" {
		return fmt.Errorf("tool has no name")
	}
	if !bytes.Equal(def.Parameters, declared.InputContract().Schema()) {
		return fmt.Errorf("%s: advertised parameters differ from its input contract", def.Name)
	}
	return nil
}

// ValidateArguments is also applied at dispatch, so a custom Call cannot bypass
// the structural contract even when it does not use Func's typed decoder.
func ValidateArguments(t Tool, raw json.RawMessage) error {
	declared, ok := t.(interface{ InputContract() Contract })
	if !ok {
		return fmt.Errorf("tool has no input contract")
	}
	_, err := decodeParameterValue(declared.InputContract().root, raw)
	return err
}
