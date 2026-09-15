// Package formulas evaluates the integer arithmetic formulas stored by the
// pricing rules engine.
package formulas

import (
	"errors"
	"fmt"
)

// Evaluate parses expr by recursive descent: one function per grammar level,
// each consuming from a shared cursor, so precedence and left associativity
// fall out of the call structure and the whole string is read once.
func Evaluate(expr string) (int, error) {
	p := &parser{src: expr}
	if _, ok := p.peek(); !ok {
		return 0, errors.New("formulas: empty formula")
	}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	if c, ok := p.peek(); ok {
		return 0, fmt.Errorf("formulas: unexpected %q at offset %d", c, p.pos)
	}
	return v, nil
}

type parser struct {
	src string
	pos int
}

// peek skips spaces and reports the next byte without consuming it.
func (p *parser) peek() (byte, bool) {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
	if p.pos < len(p.src) {
		return p.src[p.pos], true
	}
	return 0, false
}

func (p *parser) expr() (int, error) {
	v, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		c, ok := p.peek()
		if !ok || (c != '+' && c != '-') {
			return v, nil
		}
		p.pos++
		r, err := p.term()
		if err != nil {
			return 0, err
		}
		if c == '+' {
			v += r
		} else {
			v -= r
		}
	}
}

func (p *parser) term() (int, error) {
	v, err := p.factor()
	if err != nil {
		return 0, err
	}
	for {
		c, ok := p.peek()
		if !ok || (c != '*' && c != '/') {
			return v, nil
		}
		p.pos++
		r, err := p.factor()
		if err != nil {
			return 0, err
		}
		if c == '*' {
			v *= r
		} else {
			if r == 0 {
				return 0, errors.New("formulas: division by zero")
			}
			v /= r
		}
	}
}

func (p *parser) factor() (int, error) {
	c, ok := p.peek()
	if !ok {
		return 0, errors.New("formulas: missing operand at end of formula")
	}
	switch {
	case c == '-':
		p.pos++
		v, err := p.factor()
		return -v, err
	case c == '(':
		p.pos++
		v, err := p.expr()
		if err != nil {
			return 0, err
		}
		if c, ok := p.peek(); !ok || c != ')' {
			return 0, fmt.Errorf("formulas: missing ')' at offset %d", p.pos)
		}
		p.pos++
		return v, nil
	case c >= '0' && c <= '9':
		v := 0
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			v = v*10 + int(p.src[p.pos]-'0')
			p.pos++
		}
		return v, nil
	}
	return 0, fmt.Errorf("formulas: unexpected %q at offset %d", c, p.pos)
}
