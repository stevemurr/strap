package tool

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Parameters is an immutable, compiled input contract. Its zero value is invalid.
// Schema and Decode use the same contract; callers cannot replace either half.
// Supported inputs are structs, pointers, slices, strings, booleans and integers.
// JSON fields without omitempty are required. Optional fields may be omitted;
// explicit null values are rejected, matching their non-null schema types.
// Custom JSON/text codecs, maps, interfaces, recursive types and ambiguous fields
// are rejected at construction instead of silently weakening the schema.
type Parameters[A any] struct {
	root *parameterNode
	// Keep A in the representation so contracts for different argument types
	// cannot be explicitly converted into one another.
	_ func(A)
}

type parameterNode struct {
	kind                          string
	description                   string
	fields                        map[string]*parameterNode
	required                      []string
	item                          *parameterNode
	minimum, maximum              *big.Int
	minLength, minItems, maxItems *int
	unique                        bool
	enum                          []string
	anyRequired                   []string
	rejects                       map[string]string // fields models tend to send, with guidance
}

// Constraint adds a restriction to a JSON field path (for example steps[].title).
// Constructors copy their inputs. NewParameters verifies paths and applicability.
type Constraint struct {
	path  string
	apply func(*parameterNode) error
}

// Description annotates a field without changing its validation contract.
func Description(path, text string) Constraint {
	return Constraint{path, func(p *parameterNode) error { p.description = text; return nil }}
}

func MinLength(path string, n int) Constraint { return countConstraint(path, "string", "minLength", n) }
func MinItems(path string, n int) Constraint  { return countConstraint(path, "array", "minItems", n) }
func MaxItems(path string, n int) Constraint  { return countConstraint(path, "array", "maxItems", n) }
func countConstraint(path, kind, keyword string, n int) Constraint {
	return Constraint{path, func(p *parameterNode) error {
		if p.kind != kind || n < 0 {
			return fmt.Errorf("invalid %s constraint", keyword)
		}
		value := n
		switch keyword {
		case "minLength":
			p.minLength = &value
		case "minItems":
			p.minItems = &value
		case "maxItems":
			p.maxItems = &value
		}
		return nil
	}}
}
func Minimum(path string, n int64) Constraint { return integerConstraint(path, n, true) }
func Maximum(path string, n int64) Constraint { return integerConstraint(path, n, false) }
func integerConstraint(path string, n int64, minimum bool) Constraint {
	return Constraint{path, func(p *parameterNode) error {
		if p.kind != "integer" {
			return fmt.Errorf("numeric bound requires an integer")
		}
		v := big.NewInt(n)
		if minimum {
			if v.Cmp(p.minimum) < 0 {
				return fmt.Errorf("minimum exceeds Go type range")
			}
			p.minimum = v
		} else {
			if v.Cmp(p.maximum) > 0 {
				return fmt.Errorf("maximum exceeds Go type range")
			}
			p.maximum = v
		}
		return nil
	}}
}
func Enum(path string, values ...string) Constraint {
	values = slices.Clone(values)
	return Constraint{path, func(p *parameterNode) error {
		if p.kind != "string" || len(values) == 0 {
			return fmt.Errorf("enum requires a string and values")
		}
		for _, value := range values {
			if !utf8.ValidString(value) {
				return fmt.Errorf("enum values must be UTF-8")
			}
		}
		p.enum = slices.Clone(values)
		slices.Sort(p.enum)
		p.enum = slices.Compact(p.enum)
		return nil
	}}
}
func UniqueItems(path string) Constraint {
	return Constraint{path, func(p *parameterNode) error {
		if p.kind != "array" {
			return fmt.Errorf("uniqueItems requires an array")
		}
		p.unique = true
		return nil
	}}
}

// Reject documents a field the contract does not accept and the guidance to
// return when a model sends it. The field must not exist on the object; the
// hint is appended to the rejection so the retry can be right the first time.
func Reject(path, field, hint string) Constraint {
	return Constraint{path, func(p *parameterNode) error {
		if p.kind != "object" || field == "" || hint == "" || p.fields[field] != nil {
			return fmt.Errorf("reject hint requires an object, an unknown field and a hint")
		}
		if p.rejects == nil {
			p.rejects = map[string]string{}
		}
		p.rejects[field] = hint
		return nil
	}}
}

// AtLeastOne requires at least one named property of an object. It is used for
// structural patches that identify an existing step or supply a new step title.
func AtLeastOne(path string, fields ...string) Constraint {
	fields = slices.Clone(fields)
	return Constraint{path, func(p *parameterNode) error {
		if p.kind != "object" || len(fields) == 0 {
			return fmt.Errorf("at least one property is required")
		}
		for _, field := range fields {
			if _, ok := p.fields[field]; !ok {
				return fmt.Errorf("unknown property %q", field)
			}
		}
		p.anyRequired = slices.Clone(fields)
		return nil
	}}
}

func NewParameters[A any](constraints ...Constraint) (Parameters[A], error) {
	var zero Parameters[A]
	t := reflect.TypeFor[A]()
	if t.Kind() != reflect.Struct {
		return zero, fmt.Errorf("parameters must be a struct, got %v", t)
	}
	root, err := compileParameter(t, map[reflect.Type]bool{})
	if err != nil {
		return zero, err
	}
	for _, rule := range constraints {
		if rule.apply == nil {
			return zero, fmt.Errorf("empty parameter constraint")
		}
		node, err := root.lookup(rule.path)
		if err != nil {
			return zero, err
		}
		if err := rule.apply(node); err != nil {
			return zero, fmt.Errorf("%s: %w", rule.path, err)
		}
	}
	if err := root.check(); err != nil {
		return zero, err
	}
	return Parameters[A]{root: root}, nil
}

// parameters is for statically declared built-in contracts. Configuration-driven
// contracts use NewParameters and return construction errors to their caller.
func parameters[A any](constraints ...Constraint) Parameters[A] {
	p, err := NewParameters[A](constraints...)
	if err != nil {
		panic(err)
	}
	return p
}

func compileParameter(t reflect.Type, visiting map[reflect.Type]bool) (*parameterNode, error) {
	if visiting[t] {
		return nil, fmt.Errorf("recursive parameter type %v", t)
	}
	visiting[t] = true
	defer delete(visiting, t)
	for _, codec := range []reflect.Type{reflect.TypeFor[json.Unmarshaler](), reflect.TypeFor[json.Marshaler](), reflect.TypeFor[encoding.TextUnmarshaler](), reflect.TypeFor[encoding.TextMarshaler]()} {
		if t.Implements(codec) || reflect.PointerTo(t).Implements(codec) {
			return nil, fmt.Errorf("custom codec on parameter type %v is unsupported", t)
		}
	}
	if t.Kind() == reflect.Pointer {
		return compileParameter(t.Elem(), visiting)
	}
	p := &parameterNode{}
	switch t.Kind() {
	case reflect.Struct:
		p.kind = "object"
		p.fields = map[string]*parameterNode{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			if field.PkgPath != "" {
				return nil, fmt.Errorf("unexported parameter field %s.%s", t, field.Name)
			}
			optional := false
			for _, opt := range tag[1:] {
				if opt != "omitempty" {
					return nil, fmt.Errorf("unsupported JSON option %q on %s", opt, field.Name)
				}
				optional = true
			}
			child, err := compileParameter(field.Type, visiting)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", field.Name, err)
			}
			if field.Anonymous && tag[0] == "" {
				if field.Type.Kind() != reflect.Struct || optional {
					return nil, fmt.Errorf("embedded parameters must be required value structs")
				}
				for name, n := range child.fields {
					if !validParameterName(name) {
						return nil, fmt.Errorf("unsupported JSON field name %q", name)
					}
					if _, exists := p.fields[name]; exists {
						return nil, fmt.Errorf("ambiguous parameter field %s", name)
					}
					p.fields[name] = n
				}
				p.required = append(p.required, child.required...)
				continue
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			if !validParameterName(name) {
				return nil, fmt.Errorf("unsupported JSON field name %q", name)
			}
			if _, exists := p.fields[name]; exists {
				return nil, fmt.Errorf("ambiguous parameter field %s", name)
			}
			p.fields[name] = child
			if !optional {
				p.required = append(p.required, name)
			}
		}
		slices.Sort(p.required)
	case reflect.Slice:
		p.kind = "array"
		// encoding/json encodes byte slices as strings, unlike ordinary slices.
		if t.Elem().Kind() == reflect.Uint8 {
			return nil, fmt.Errorf("byte slices are unsupported; use a string or integer slice")
		}
		var err error
		p.item, err = compileParameter(t.Elem(), visiting)
		if err != nil {
			return nil, err
		}
	case reflect.String:
		p.kind = "string"
	case reflect.Bool:
		p.kind = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		p.kind = "integer"
		bound := new(big.Int).Lsh(big.NewInt(1), uint(t.Bits()-1))
		p.minimum = new(big.Int).Neg(bound)
		p.maximum = new(big.Int).Sub(bound, big.NewInt(1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		p.kind = "integer"
		p.minimum = big.NewInt(0)
		p.maximum = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(t.Bits())), big.NewInt(1))
	default:
		return nil, fmt.Errorf("unsupported parameter type %v", t)
	}
	return p, nil
}
func (p *parameterNode) lookup(path string) (*parameterNode, error) {
	if path == "" {
		return p, nil
	}
	node := p
	for part := range strings.SplitSeq(path, ".") {
		name := strings.TrimSuffix(part, "[]")
		if node.kind != "object" || node.fields[name] == nil {
			return nil, fmt.Errorf("unknown parameter path %q", path)
		}
		node = node.fields[name]
		if strings.HasSuffix(part, "[]") {
			if node.kind != "array" {
				return nil, fmt.Errorf("%s is not an array", name)
			}
			node = node.item
		}
	}
	return node, nil
}
func (p *parameterNode) check() error {
	if p.minimum != nil && p.minimum.Cmp(p.maximum) > 0 {
		return fmt.Errorf("contradictory integer bounds")
	}
	if p.minItems != nil && p.maxItems != nil && *p.minItems > *p.maxItems {
		return fmt.Errorf("contradictory array bounds")
	}
	for _, v := range p.enum {
		if p.minLength != nil && utf8.RuneCountInString(v) < *p.minLength {
			return fmt.Errorf("enum value violates minimum length")
		}
	}
	for _, child := range p.fields {
		if err := child.check(); err != nil {
			return err
		}
	}
	if p.item != nil {
		return p.item.check()
	}
	return nil
}
func (p *parameterNode) schema() map[string]any {
	s := map[string]any{"type": p.kind}
	if p.description != "" {
		s["description"] = p.description
	}
	if p.kind == "object" {
		fields := map[string]any{}
		for name, child := range p.fields {
			fields[name] = child.schema()
		}
		s["properties"] = fields
		s["additionalProperties"] = false
		// Emit [] even for empty objects, never null.
		s["required"] = append([]string{}, p.required...)
		if len(p.anyRequired) > 0 {
			alternatives := []any{}
			for _, field := range p.anyRequired {
				alternatives = append(alternatives, map[string]any{"required": []string{field}})
			}
			s["anyOf"] = alternatives
		}
	}
	if p.item != nil {
		s["items"] = p.item.schema()
	}
	if p.minimum != nil {
		s["minimum"] = json.Number(p.minimum.String())
		s["maximum"] = json.Number(p.maximum.String())
	}
	if p.minLength != nil {
		s["minLength"] = *p.minLength
	}
	if p.minItems != nil {
		s["minItems"] = *p.minItems
	}
	if p.maxItems != nil {
		s["maxItems"] = *p.maxItems
	}
	if p.unique {
		s["uniqueItems"] = true
	}
	if p.enum != nil {
		s["enum"] = slices.Clone(p.enum)
	}
	return s
}

// Schema returns a fresh wire representation. Mutating it cannot affect Decode.
func (p Parameters[A]) Schema() json.RawMessage {
	if p.root == nil {
		return nil
	}
	raw, _ := json.Marshal(p.root.schema())
	return raw
}
func (p Parameters[A]) Decode(raw json.RawMessage) (A, error) {
	var args A
	if p.root == nil {
		return args, fmt.Errorf("uninitialized parameters")
	}
	if !utf8.Valid(raw) {
		return args, fmt.Errorf("arguments must be UTF-8")
	}
	if err := validateValues(raw); err != nil {
		return args, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return args, err
	}
	if value == nil {
		return args, fmt.Errorf("arguments must be a JSON object, not null")
	}
	normalized, err := p.root.validate(value, "arguments")
	if err != nil {
		return args, err
	}
	// Normalize integral JSON numbers (1.0 / 1e0) to Go integer syntax without
	// float64 roundoff. JSON Schema defines these as integers too.
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return args, err
	}
	decoder = json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, fmt.Errorf("decode validated arguments: %w", err)
	}
	return args, nil
}
func (p *parameterNode) validate(value any, path string) (any, error) {
	bad := func() (any, error) {
		// A string where an array or object belongs is the signature of a
		// server tool parser that could not parse the emitted value and passed
		// its raw text through. Observed generations close the array correctly
		// and then keep writing, so naming the trailing text is the repair the
		// model needs; "must be array" alone reads as a type error it already
		// believes it satisfied.
		if _, text := value.(string); text && (p.kind == "array" || p.kind == "object") {
			closer := "]"
			if p.kind == "object" {
				closer = "}"
			}
			return nil, fmt.Errorf("%s must be %s, not a string; send the %s itself and stop at its closing %s, with no characters after it", path, p.kind, p.kind, closer)
		}
		return nil, fmt.Errorf("%s must be %s", path, p.kind)
	}
	switch p.kind {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return bad()
		}
		names := slices.Sorted(maps.Keys(obj))
		// Report every disallowed field at once. Models repair exactly what the
		// message names, so naming one field at a time produces a retry per field.
		var unknown, hints []string
		for _, name := range names {
			if p.fields[name] == nil {
				unknown = append(unknown, name)
				if hint := p.rejects[name]; hint != "" {
					hints = append(hints, name+": "+hint)
				}
			}
		}
		if len(unknown) > 0 {
			msg := fmt.Sprintf("%s.%s is not an allowed field", path, unknown[0])
			if len(unknown) > 1 {
				msg += fmt.Sprintf(" (also not allowed: %s)", strings.Join(unknown[1:], ", "))
			}
			if len(hints) > 0 {
				msg += "; " + strings.Join(hints, "; ")
			}
			return nil, errors.New(msg)
		}
		var missing []string
		for _, name := range p.required {
			if _, ok := obj[name]; !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			msg := fmt.Sprintf("%s.%s is required", path, missing[0])
			if len(missing) > 1 {
				msg += fmt.Sprintf(" (also required: %s)", strings.Join(missing[1:], ", "))
			}
			return nil, errors.New(msg)
		}
		if len(p.anyRequired) > 0 {
			found := false
			for _, name := range p.anyRequired {
				if _, ok := obj[name]; ok {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("%s requires at least one of %s", path, strings.Join(p.anyRequired, ", "))
			}
		}
		for _, name := range names {
			v, err := p.fields[name].validate(obj[name], path+"."+name)
			if err != nil {
				return nil, err
			}
			obj[name] = v
		}
		return obj, nil
	case "array":
		items, ok := value.([]any)
		if !ok {
			return bad()
		}
		if p.minItems != nil && len(items) < *p.minItems {
			return nil, fmt.Errorf("%s requires at least %d items", path, *p.minItems)
		}
		if p.maxItems != nil && len(items) > *p.maxItems {
			return nil, fmt.Errorf("%s permits at most %d items", path, *p.maxItems)
		}
		// Report every failing element at once, for the same reason the object
		// case reports every disallowed field at once: a model repairs exactly
		// what the message names, so naming one element produces a retry per
		// element. A plan whose second and third steps both omit a title takes
		// two rejections to fix when only the second is named.
		seen := map[string]bool{}
		var failures []string
		for i, item := range items {
			v, err := p.item.validate(item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				failures = append(failures, err.Error())
				continue
			}
			items[i] = v
			if p.unique {
				raw, _ := json.Marshal(v)
				key := string(raw)
				if seen[key] {
					return nil, fmt.Errorf("%s contains duplicate items", path)
				}
				seen[key] = true
			}
		}
		if len(failures) > 0 {
			msg := failures[0]
			if len(failures) > 1 {
				msg += " (also: " + strings.Join(failures[1:], "; ") + ")"
			}
			return nil, errors.New(msg)
		}
		return items, nil
	case "string":
		s, ok := value.(string)
		if !ok {
			return bad()
		}
		if p.minLength != nil && utf8.RuneCountInString(s) < *p.minLength {
			return nil, fmt.Errorf("%s must contain at least %d characters", path, *p.minLength)
		}
		if p.enum != nil && !slices.Contains(p.enum, s) {
			return nil, fmt.Errorf("%s must be one of %s", path, strings.Join(p.enum, ", "))
		}
		return s, nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return bad()
		}
		return value, nil
	case "integer":
		n, ok := value.(json.Number)
		if !ok {
			return bad()
		}
		integer, ok := jsonInteger(string(n))
		if !ok {
			return bad()
		}
		if integer.Cmp(p.minimum) < 0 || integer.Cmp(p.maximum) > 0 {
			return nil, fmt.Errorf("%s must be between %s and %s", path, p.minimum, p.maximum)
		}
		return json.Number(integer.String()), nil
	}
	return nil, fmt.Errorf("unsupported parameter contract")
}

// jsonInteger recognizes integral decimal/exponent forms exactly, without
// allocating powers proportional to an untrusted exponent. Supported Go integer
// types need at most 20 significant decimal digits.
func jsonInteger(literal string) (*big.Int, bool) {
	negative := strings.HasPrefix(literal, "-")
	literal = strings.TrimPrefix(literal, "-")
	exponent := new(big.Int)
	if at := strings.IndexAny(literal, "eE"); at >= 0 {
		if _, ok := exponent.SetString(literal[at+1:], 10); !ok {
			return nil, false
		}
		literal = literal[:at]
	}
	if at := strings.IndexByte(literal, '.'); at >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(literal)-at-1)))
		literal = literal[:at] + literal[at+1:]
	}
	literal = strings.TrimLeft(literal, "0")
	if literal == "" {
		return new(big.Int), true
	}
	digits := strings.TrimRight(literal, "0")
	exponent.Add(exponent, big.NewInt(int64(len(literal)-len(digits))))
	if exponent.Sign() < 0 || len(digits) > 20 || exponent.Cmp(big.NewInt(int64(20-len(digits)))) > 0 {
		return nil, false
	}
	digits += strings.Repeat("0", int(exponent.Int64()))
	if negative {
		digits = "-" + digits
	}
	return new(big.Int).SetString(digits, 10)
}

// Match encoding/json's accepted tag names so it cannot silently fall back to
// the Go field name after the contract compiled a different JSON property.
func validParameterName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", r) || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}
