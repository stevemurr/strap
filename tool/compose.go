package tool

import (
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
	contract() *parameterNode
	snapshot() preparedTool
	prepare(context.Context, Call) (func() (Result, error), error)
}

type composedTool struct {
	root     *parameterNode
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

// ComposeBy is Compose for alternatives selected by a required field, such as
// submit_audit's verdict. Every branch must require field and constrain it to
// exactly one non-null enum value. Complete branch contracts are authoritative.
// Dispatch runs the selected branch's strict decode and reports its error.
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
	nodes := []*parameterNode{}
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
		node := *typed.contract().fields["input"]
		if def.Description != "" {
			node.description = def.Description
		}
		nodes = append(nodes, &node)
	}
	if discriminator != "" {
		var err error
		result.values, result.byValue, err = discriminatorValues(nodes, discriminator)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", definition.Name, err)
		}
	}
	if len(nodes) == 1 {
		result.root = nodes[0]
	} else {
		result.root = &parameterNode{alternatives: nodes, exclusive: true}
	}
	result.root = inputObjectNode(result.root)
	if _, err := result.root.schemaNodes(4096); err != nil {
		return nil, err
	}
	return result, nil
}

// Inspect contracts, never serialized schemas or parser hints.
func discriminatorValue(p *parameterNode, field string) (string, error) {
	if len(p.alternatives) > 0 {
		value := ""
		for _, branch := range p.alternatives {
			v, err := discriminatorValue(branch, field)
			if err != nil {
				return "", err
			}
			if value != "" && value != v {
				return "", fmt.Errorf("nested forms must share one discriminator value")
			}
			value = v
		}
		return value, nil
	}
	f := p.fields[field]
	if f == nil || f.nullable || len(f.enum) != 1 || f.enum[0] == "" {
		return "", fmt.Errorf("must constrain %s to exactly one enum value, non-null", field)
	}
	return f.enum[0], nil
}
func discriminatorValues(nodes []*parameterNode, field string) ([]string, map[string]int, error) {
	values := make([]string, 0, len(nodes))
	byValue := map[string]int{}
	for i, node := range nodes {
		value, err := discriminatorValue(node, field)
		if err != nil {
			return nil, nil, fmt.Errorf("branch %d %w", i+1, err)
		}
		if _, exists := byValue[value]; exists {
			return nil, nil, fmt.Errorf("branches share %s value %q", field, value)
		}
		byValue[value] = i
		values = append(values, value)
	}
	return values, byValue, nil
}
func (t *composedTool) contract() *parameterNode { return t.root }

func (t *composedTool) Definition() provider.ToolDefinition {
	d := t.spec
	d.Parameters, _ = json.Marshal(t.root.schema())
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
	if err := validateValues(c.Arguments); err != nil {
		return nil, err
	}
	var envelope struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(c.Arguments, &envelope); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Input, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s arguments must be a JSON object with input.%s set to one of %s", t.spec.Name, t.discriminator, allowed)
	}
	var value string
	if raw, ok := object[t.discriminator]; !ok || json.Unmarshal(raw, &value) != nil || value == "" {
		return nil, fmt.Errorf("%s requires input.%s: one of %s", t.spec.Name, t.discriminator, allowed)
	}
	i, ok := t.byValue[value]
	if !ok {
		return nil, fmt.Errorf("%s input.%s must be one of %s, not %q", t.spec.Name, t.discriminator, allowed, value)
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
	copy := &composedTool{root: t.root, spec: t.spec, discriminator: t.discriminator, values: slices.Clone(t.values), byValue: maps.Clone(t.byValue)}
	for _, branch := range t.branches {
		copy.branches = append(copy.branches, branch.snapshot())
	}
	return copy
}
