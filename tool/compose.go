package tool

import (
	"bytes"
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

// Some tool-call servers read top-level properties without traversing oneOf.
// Include nested property/item hints too, so steps and scopes retain their shape.
// Hints must accept every branch; the original oneOf remains authoritative.
func compositionSchema(branches []json.RawMessage) (json.RawMessage, error) {
	hints, err := compositionHints(branches)
	if err != nil {
		return nil, err
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(hints, &schema); err != nil {
		return nil, err
	}
	schema["oneOf"], err = json.Marshal(branches)
	if err != nil {
		return nil, err
	}
	return json.Marshal(schema)
}

func compositionHints(alternatives []json.RawMessage) (json.RawMessage, error) {
	if len(alternatives) == 0 {
		return json.RawMessage(`{}`), nil
	}
	identical := true
	for _, raw := range alternatives[1:] {
		identical = identical && bytes.Equal(raw, alternatives[0])
	}
	if identical {
		return alternatives[0], nil
	}
	kind := ""
	properties := map[string][]json.RawMessage{}
	items := []json.RawMessage{}
	for i, raw := range alternatives {
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Items      json.RawMessage            `json:"items"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, err
		}
		if i == 0 {
			kind = schema.Type
		}
		// An unconstrained hint must stay unconstrained in nested compositions.
		if kind == "" || schema.Type != kind {
			return json.RawMessage(`{}`), nil
		}
		for name, property := range schema.Properties {
			properties[name] = append(properties[name], property)
		}
		item := schema.Items
		if len(item) == 0 {
			item = json.RawMessage(`{}`)
		}
		items = append(items, item)
	}
	hint := map[string]any{"type": kind}
	if kind == "object" {
		fields := map[string]json.RawMessage{}
		for name, choices := range properties {
			field, err := compositionHints(choices)
			if err != nil {
				return nil, err
			}
			fields[name] = field
		}
		// Do not merge required fields or additionalProperties across branches.
		hint["properties"] = fields
	}
	if kind == "array" {
		item, err := compositionHints(items)
		if err != nil {
			return nil, err
		}
		hint["items"] = item
	}
	return json.Marshal(hint)
}
