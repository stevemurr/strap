package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
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
	// A discriminated composition dispatches on one field instead of trying
	// every branch; values holds each branch's value in branch order.
	discriminator string
	values        []string
	byValue       map[string]int
}

// Compose exposes complete typed tools under one name. Definitions are snapshotted
// at construction. Exactly one branch must validate; no handler runs while matching.
// Parameters on definition must be empty: the alternatives supply the schema.
// Arbitrary Tool implementations cannot participate without a typed contract.
func Compose(definition provider.ToolDefinition, branches ...Tool) (Tool, error) {
	return composeTool(definition, "", branches...)
}

// ComposeBy is Compose for alternatives that one field tells apart, such as
// assign_work's kind or submit_audit's verdict. Every branch must constrain
// field to exactly one enum value. Instead of a oneOf, the advertised schema
// is one flat object: the union of the branches' properties, field as a single
// enum of every branch value, and a top-level required naming field plus
// every property all branches require. Models that flatten or ignore oneOf
// otherwise never learn that the field is mandatory or what it accepts.
// Dispatch reads field and runs that branch's strict decode, so a rejection
// states one form's rule instead of one contradictory verdict per form.
func ComposeBy(field string, definition provider.ToolDefinition, branches ...Tool) (Tool, error) {
	if field == "" {
		return nil, fmt.Errorf("composition discriminator must be named")
	}
	return composeTool(definition, field, branches...)
}

func composeTool(definition provider.ToolDefinition, discriminator string, branches ...Tool) (Tool, error) {
	if definition.Name == "" || len(definition.Parameters) > 0 || len(branches) == 0 {
		return nil, fmt.Errorf("composition requires a name, branches and no supplied parameters")
	}
	result := &composedTool{spec: definition, discriminator: discriminator}
	schemas := []json.RawMessage{}
	names := map[string]bool{}
	for _, branch := range branches {
		if _, ok := branch.(ControlTool); ok {
			return nil, fmt.Errorf("control tools must be registered independently")
		}
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
		// Branch names are internal diagnostics, not callable public tools.
		// Advertising them as schema titles encourages models to invoke them.
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
	switch {
	case discriminator != "":
		result.values, result.byValue, err = discriminatorValues(schemas, discriminator)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", definition.Name, err)
		}
		result.spec.Parameters, err = discriminatedSchema(schemas, discriminator, result.values)
	case len(schemas) == 1:
		result.spec.Parameters = append(json.RawMessage(nil), schemas[0]...)
	default:
		result.spec.Parameters, err = compositionSchema(schemas)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// discriminatorValues reads each branch's single allowed value for field.
func discriminatorValues(schemas []json.RawMessage, field string) ([]string, map[string]int, error) {
	values := make([]string, 0, len(schemas))
	byValue := map[string]int{}
	for i, raw := range schemas {
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, nil, err
		}
		property, ok := schema.Properties[field]
		if !ok || len(property.Enum) != 1 || property.Enum[0] == "" {
			return nil, nil, fmt.Errorf("branch %d must constrain %s to exactly one enum value", i+1, field)
		}
		if _, dup := byValue[property.Enum[0]]; dup {
			return nil, nil, fmt.Errorf("branches share %s value %q", field, property.Enum[0])
		}
		byValue[property.Enum[0]] = i
		values = append(values, property.Enum[0])
	}
	return values, byValue, nil
}

// discriminatedSchema flattens the branches into one object schema keyed by
// the discriminator; see ComposeBy.
func discriminatedSchema(schemas []json.RawMessage, field string, values []string) (json.RawMessage, error) {
	hints, err := compositionHints(schemas, values, "Only when "+field+" is ")
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(hints, &schema); err != nil {
		return nil, err
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties[field] = map[string]any{"type": "string", "enum": values, "description": "Selects the operation; which other fields apply depends on it."}
	schema["type"] = "object"
	schema["properties"] = properties
	required := []string{field}
	for _, name := range requiredByAll(schemas) {
		if name != field {
			required = append(required, name)
		}
	}
	schema["required"] = required
	return json.Marshal(schema)
}

// requiredByAll returns the properties every branch requires, in the first
// branch's order. They are safe to advertise at the top level of any
// composition: whichever form the model means, it must supply them.
func requiredByAll(schemas []json.RawMessage) []string {
	var common []string
	for i, raw := range schemas {
		var schema struct {
			Required []string `json:"required"`
		}
		if json.Unmarshal(raw, &schema) != nil {
			return nil
		}
		if i == 0 {
			common = schema.Required
			continue
		}
		common = slices.DeleteFunc(common, func(name string) bool { return !slices.Contains(schema.Required, name) })
	}
	return common
}
func (t *composedTool) Definition() provider.ToolDefinition {
	d := t.spec
	d.Parameters = append(json.RawMessage(nil), d.Parameters...)
	return d
}

// BookkeepingParameters is the union of the branches' declarations.
func (t *composedTool) BookkeepingParameters() []string {
	seen := map[string]bool{}
	var names []string
	for _, branch := range t.branches {
		if b, ok := branch.(interface{ BookkeepingParameters() []string }); ok {
			for _, name := range b.BookkeepingParameters() {
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
		}
	}
	slices.Sort(names)
	return names
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
	if t.discriminator != "" {
		return t.prepareBy(ctx, c)
	}
	var selected func() (Result, error)
	failures := []string{}
	for i, branch := range t.branches {
		invoke, err := branch.prepare(ctx, c)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", formLabel(i, branch.Definition().Description), err))
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

// prepareBy selects the branch named by the discriminator and reports that
// one branch's rejection, so the model learns which field of which form was
// wrong instead of receiving every form's complaint.
func (t *composedTool) prepareBy(ctx context.Context, c Call) (func() (Result, error), error) {
	allowed := strings.Join(t.values, ", ")
	var object map[string]json.RawMessage
	if err := json.Unmarshal(c.Arguments, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s arguments must be a JSON object with %s set to one of %s", t.spec.Name, t.discriminator, allowed)
	}
	var value string
	if raw, ok := object[t.discriminator]; !ok || json.Unmarshal(raw, &value) != nil || value == "" {
		return nil, fmt.Errorf("%s requires %s: one of %s", t.spec.Name, t.discriminator, allowed)
	}
	i, ok := t.byValue[value]
	if !ok {
		return nil, fmt.Errorf("%s %s must be one of %s, not %q", t.spec.Name, t.discriminator, allowed, value)
	}
	invoke, err := t.branches[i].prepare(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("%s with %s %s: %w", t.spec.Name, t.discriminator, value, err)
	}
	return invoke, nil
}

func (t *composedTool) Call(ctx context.Context, c Call) (Result, error) {
	invoke, err := t.prepare(ctx, c)
	if err != nil {
		return Result{}, err
	}
	return invoke()
}

// formLabel names an alternative by its description so a rejection says which
// operation the arguments were closest to. Branch names stay internal.
func formLabel(i int, description string) string {
	if first, _, ok := strings.Cut(description, "."); ok && strings.TrimSpace(first) != "" {
		return fmt.Sprintf("form %d, %s", i+1, strings.TrimSpace(first))
	}
	return fmt.Sprintf("form %d", i+1)
}

func compose(def provider.ToolDefinition, branches ...Tool) Tool {
	result, err := Compose(def, branches...)
	if err != nil {
		panic(err)
	}
	return result
}

func composeBy(field string, def provider.ToolDefinition, branches ...Tool) Tool {
	result, err := ComposeBy(field, def, branches...)
	if err != nil {
		panic(err)
	}
	return result
}

func (t *composedTool) snapshot() preparedTool {
	copy := &composedTool{spec: t.Definition(), discriminator: t.discriminator, values: slices.Clone(t.values), byValue: maps.Clone(t.byValue)}
	for _, branch := range t.branches {
		copy.branches = append(copy.branches, branch.snapshot())
	}
	return copy
}

// Some tool-call servers read top-level properties without traversing oneOf.
// Include nested property/item hints too, so steps and scopes retain their shape.
// Hints must accept every branch; the original oneOf remains authoritative.
func compositionSchema(branches []json.RawMessage) (json.RawMessage, error) {
	labels := make([]string, len(branches))
	for i, raw := range branches {
		var branch struct {
			Description string `json:"description"`
		}
		_ = json.Unmarshal(raw, &branch)
		labels[i] = hintLabel(i, branch.Description)
	}
	hints, err := compositionHints(branches, labels, "Only in ")
	if err != nil {
		return nil, err
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(hints, &schema); err != nil {
		return nil, err
	}
	// Properties every form requires are required whichever form is meant;
	// a model that only reads the top level then still supplies them.
	if common := requiredByAll(branches); len(common) > 0 {
		schema["required"], _ = json.Marshal(common)
	}
	schema["oneOf"], err = json.Marshal(branches)
	if err != nil {
		return nil, err
	}
	return json.Marshal(schema)
}

// hintLabel names an alternative for property annotations: the first word of
// its description ("the create form") or its position ("form 2").
func hintLabel(i int, description string) string {
	if word, _, _ := strings.Cut(strings.TrimSpace(description), " "); word != "" {
		return "the " + strings.ToLower(strings.TrimRight(word, ".,:;")) + " form"
	}
	return fmt.Sprintf("form %d", i+1)
}

// Servers that flatten oneOf show every property as if it applied everywhere.
// A property present in only some alternatives is annotated with the forms that
// accept it, so the model reads the restriction where it reads the field.
func compositionHints(alternatives []json.RawMessage, labels []string, prefix string) (json.RawMessage, error) {
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
	owners := map[string][]int{}
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
			owners[name] = append(owners[name], i)
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
			field, err := compositionHints(choices, pick(labels, owners[name]), prefix)
			if err != nil {
				return nil, err
			}
			if len(owners[name]) < len(alternatives) && len(labels) == len(alternatives) {
				field, err = describe(field, prefix+strings.Join(pick(labels, owners[name]), " or "))
				if err != nil {
					return nil, err
				}
			}
			fields[name] = field
		}
		// Do not merge required fields or additionalProperties across branches.
		hint["properties"] = fields
	}
	if kind == "array" {
		item, err := compositionHints(items, labels, prefix)
		if err != nil {
			return nil, err
		}
		hint["items"] = item
	}
	return json.Marshal(hint)
}

func pick(labels []string, indices []int) []string {
	out := make([]string, 0, len(indices))
	for _, i := range indices {
		if i < len(labels) {
			out = append(out, labels[i])
		}
	}
	return out
}

// describe prefixes text onto a hint's description without changing its contract.
func describe(hint json.RawMessage, text string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(hint, &m); err != nil {
		return nil, err
	}
	if prior, ok := m["description"]; ok {
		var s string
		if json.Unmarshal(prior, &s) == nil && s != "" {
			text += ". " + s
		}
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	m["description"] = encoded
	return json.Marshal(m)
}
