package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/stevemurr/strap/provider"
)

type preparedTool interface {
	Tool
	Validate() error
	snapshot() preparedTool
	prepare(context.Context, Call) (func() (Result, error), error)
}

type composedTool struct {
	spec     provider.ToolDefinition
	branches []preparedTool
}

// Compose exposes complete typed tools under one name. Definitions are snapshotted
// at construction. Exactly one branch must validate; no handler runs while matching.
// Parameters on definition must be empty: the alternatives supply the schema.
// Arbitrary Tool implementations cannot participate without a typed contract.
func Compose(definition provider.ToolDefinition, branches ...Tool) (Tool, error) {
	if definition.Name == "" || len(definition.Parameters) > 0 || len(branches) == 0 {
		return nil, fmt.Errorf("composition requires a name, branches and no supplied parameters")
	}
	result := &composedTool{spec: definition}
	schemas := []json.RawMessage{}
	names := map[string]bool{}
	for _, branch := range branches {
		typed, ok := branch.(preparedTool)
		if !ok || (reflect.ValueOf(typed).Kind() == reflect.Pointer && reflect.ValueOf(typed).IsNil()) {
			return nil, fmt.Errorf("composition requires non-nil typed tools")
		}
		typed = typed.snapshot()
		if err := typed.Validate(); err != nil {
			return nil, err
		}
		def := typed.Definition()
		if names[def.Name] {
			return nil, fmt.Errorf("duplicate branch %s", def.Name)
		}
		names[def.Name] = true
		result.branches = append(result.branches, typed)
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(def.Parameters, &schema); err != nil {
			return nil, err
		}
		schema["title"], _ = json.Marshal(def.Name)
		if def.Description != "" {
			schema["description"], _ = json.Marshal(def.Description)
		}
		raw, err := json.Marshal(schema)
		if err != nil {
			return nil, err
		}
		schemas = append(schemas, raw)
	}
	var err error
	if len(schemas) == 1 {
		result.spec.Parameters = append(json.RawMessage(nil), schemas[0]...)
	} else {
		result.spec.Parameters, err = compositionSchema(schemas)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (t *composedTool) Definition() provider.ToolDefinition {
	d := t.spec
	d.Parameters = append(json.RawMessage(nil), d.Parameters...)
	return d
}
func (t *composedTool) Validate() error {
	for _, branch := range t.branches {
		if err := branch.Validate(); err != nil {
			return err
		}
	}
	return nil
}
func (t *composedTool) prepare(ctx context.Context, c Call) (func() (Result, error), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var selected func() (Result, error)
	failures := []string{}
	for _, branch := range t.branches {
		invoke, err := branch.prepare(ctx, c)
		if err != nil {
			failures = append(failures, branch.Definition().Name+": "+err.Error())
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("%s arguments match multiple operations", t.spec.Name)
		}
		selected = invoke
	}
	if selected == nil {
		return nil, fmt.Errorf("%s arguments match no operation: %s", t.spec.Name, strings.Join(failures, "; "))
	}
	return selected, nil
}
func (t *composedTool) Call(ctx context.Context, c Call) (Result, error) {
	invoke, err := t.prepare(ctx, c)
	if err != nil {
		return Result{}, err
	}
	return invoke()
}
func compose(def provider.ToolDefinition, branches ...Tool) Tool {
	result, err := Compose(def, branches...)
	if err != nil {
		panic(err)
	}
	return result
}

func (t *composedTool) snapshot() preparedTool {
	copy := &composedTool{spec: t.Definition()}
	for _, branch := range t.branches {
		copy.branches = append(copy.branches, branch.snapshot())
	}
	return copy
}

// Some tool-call servers use only top-level properties when converting model
// output into JSON argument values. Repeat common field types there so arrays
// and numbers survive that conversion. These are redundant type hints, derived
// from the exact branches: oneOf still enforces every required/forbidden field
// and constraint. Differing types receive no hint. Never merge branch rules.
func compositionSchema(branches []json.RawMessage) (json.RawMessage, error) {
	types := map[string]map[string]bool{}
	for _, raw := range branches {
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, err
		}
		for name, property := range schema.Properties {
			if types[name] == nil {
				types[name] = map[string]bool{}
			}
			types[name][property.Type] = true
		}
	}
	hints := map[string]any{}
	for name, choices := range types {
		// Retain mixed-type field names with an unconstrained hint so nested
		// compositions cannot mistake an omitted hint for an absent field.
		hints[name] = map[string]any{}
		if len(choices) != 1 {
			continue
		}
		for kind := range choices {
			if kind != "" {
				hints[name] = map[string]any{"type": kind}
			}
		}
	}
	return json.Marshal(map[string]any{"type": "object", "properties": hints, "oneOf": branches})
}
