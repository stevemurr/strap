package chatwire

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/provider"
	"io"
	"strings"
	"unicode/utf8"
)

// readStream assembles protocol fragments before validating the final response.
// No tool call escapes this adapter until all arguments and termination validate.
func readStream(r io.Reader, observer provider.Observer) (provider.Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var text strings.Builder
	calls := map[int]*functionCall{}
	var usage json.RawMessage
	role, finish := "", ""
	var frame strings.Builder
	failure := func(err error) (provider.Response, error) { return provider.Response{Usage: decodeUsage(usage)}, err }
	consume := func(data string) (bool, error) {
		if data == "[DONE]" {
			return true, nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Usage   json.RawMessage `json:"usage"`
			Choices []struct {
				Index  int     `json:"index"`
				Finish *string `json:"finish_reason"`
				Delta  struct {
					Role    string `json:"role"`
					Content string `json:"content"`
					Calls   []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if !utf8.ValidString(data) {
			return false, errors.New("invalid UTF-8 stream")
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, fmt.Errorf("decode stream: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, fmt.Errorf("provider stream error: %.4096s", chunk.Error)
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			return false, nil
		}
		if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 {
			return false, errors.New("expected one choice at index zero")
		}
		c := chunk.Choices[0]
		if finish != "" {
			return false, errors.New("choice after finish")
		}
		if c.Delta.Role != "" {
			if role != "" && role != c.Delta.Role {
				return false, errors.New("conflicting role")
			}
			role = c.Delta.Role
		}
		if c.Delta.Content != "" {
			if observer != nil {
				if err := observer.OnDelta(provider.Delta{Text: c.Delta.Content}); err != nil {
					return false, err
				}
			}
			text.WriteString(c.Delta.Content)
		}
		for _, d := range c.Delta.Calls {
			if d.Index < 0 || d.Index >= 128 {
				return false, errors.New("invalid tool index")
			}
			call := calls[d.Index]
			if call == nil {
				call = &functionCall{}
				calls[d.Index] = call
			}
			if d.ID != "" {
				if call.ID != "" && call.ID != d.ID {
					return false, errors.New("conflicting tool ID")
				}
				call.ID = d.ID
			}
			if d.Type != "" {
				if call.Type != "" && call.Type != d.Type {
					return false, errors.New("conflicting tool type")
				}
				call.Type = d.Type
			}
			if len(call.Function.Arguments)+len(d.Function.Arguments) > 16<<20 {
				return false, errors.New("tool arguments exceed limit")
			}
			call.Function.Name += d.Function.Name
			call.Function.Arguments += d.Function.Arguments
		}
		if c.Finish != nil {
			finish = *c.Finish
		}
		return false, nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line != "" {
			if strings.HasPrefix(line, "data:") {
				if frame.Len() > 0 {
					frame.WriteByte('\n')
				}
				frame.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
				if frame.Len() > 1<<20 {
					return failure(errors.New("stream frame exceeds limit"))
				}
			}
			continue
		}
		if frame.Len() == 0 {
			continue
		}
		done, err := consume(frame.String())
		frame.Reset()
		if err != nil {
			return failure(err)
		}
		if !done {
			continue
		}
		assembled := completion{Usage: usage}
		assembled.Choices = append(assembled.Choices, struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{Message: chatMessage{Role: role}, FinishReason: finish})
		value := text.String()
		assembled.Choices[0].Message.Content = &value
		for i := 0; i < len(calls); i++ {
			call := calls[i]
			if call == nil {
				return failure(errors.New("noncontiguous tool indices"))
			}
			assembled.Choices[0].Message.ToolCalls = append(assembled.Choices[0].Message.ToolCalls, *call)
		}
		return decode(assembled)
	}
	if err := scanner.Err(); err != nil {
		return failure(fmt.Errorf("read stream: %w", err))
	}
	return failure(io.ErrUnexpectedEOF)
}
