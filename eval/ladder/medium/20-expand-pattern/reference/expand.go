// Package fixtures expands compact repeat patterns into fixture payloads.
package fixtures

import (
	"bytes"
	"errors"
	"fmt"
)

// limit is the longest expansion Expand will produce.
const limit = 10_000_000

// Expand parses the pattern by recursive descent. Every group's body is
// expanded once and repeated with bytes.Repeat; the byte budget is checked
// before each repeat and append so an oversized pattern fails before any
// large allocation.
func Expand(pattern string) (string, error) {
	p := parser{src: pattern}
	out, err := p.sequence(0)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

type parser struct {
	src string
	pos int
}

// sequence parses items until the end of input (depth 0) or a closing
// bracket (depth > 0), which it leaves for the caller to consume.
func (p *parser) sequence(depth int) ([]byte, error) {
	var out []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c >= 'a' && c <= 'z':
			if len(out)+1 > limit {
				return nil, errTooLong
			}
			out = append(out, c)
			p.pos++
		case c >= '0' && c <= '9':
			chunk, err := p.group(depth)
			if err != nil {
				return nil, err
			}
			if len(out)+len(chunk) > limit {
				return nil, errTooLong
			}
			out = append(out, chunk...)
		case c == ']':
			if depth == 0 {
				return nil, fmt.Errorf("fixtures: unmatched ']' at %d", p.pos)
			}
			return out, nil
		case c == '[':
			return nil, fmt.Errorf("fixtures: '[' without a count at %d", p.pos)
		default:
			return nil, fmt.Errorf("fixtures: invalid byte %q at %d", c, p.pos)
		}
	}
	if depth > 0 {
		return nil, errors.New("fixtures: missing ']'")
	}
	return out, nil
}

var errTooLong = fmt.Errorf("fixtures: expansion longer than %d bytes", limit)

// group parses count '[' pattern ']' starting at a digit.
func (p *parser) group(depth int) ([]byte, error) {
	start := p.pos
	if p.src[p.pos] == '0' {
		return nil, fmt.Errorf("fixtures: count with a leading zero at %d", start)
	}
	count := 0
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		if count <= limit {
			count = count*10 + int(p.src[p.pos]-'0')
		}
		p.pos++
	}
	if p.pos >= len(p.src) || p.src[p.pos] != '[' {
		return nil, fmt.Errorf("fixtures: count at %d is not followed by '['", start)
	}
	p.pos++
	body, err := p.sequence(depth + 1)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("fixtures: empty group at %d", start)
	}
	p.pos++ // the ']' that ended the body
	if count > limit || len(body)*count > limit {
		return nil, errTooLong
	}
	return bytes.Repeat(body, count), nil
}
