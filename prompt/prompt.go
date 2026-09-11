// Package prompt defines application-supplied operating instructions for agents.
package prompt

import "encoding/json"

type Prompt struct {
	Role         string   `json:"role"`
	Instructions []string `json:"instructions"`
}

func (p Prompt) Render() (string, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	return string(data), err
}

func (p Prompt) Clone() Prompt {
	p.Instructions = append([]string(nil), p.Instructions...)
	return p
}
