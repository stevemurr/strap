package chatwire_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/internal/chatwire"
)

// A rejected call records the response shape around it: the finish reason the
// server reported and how many calls the response carried, so a trace can
// tell a truncated call from one that was followed by another.
func TestRejectedArgumentsRecordFinishReasonAndCallPosition(t *testing.T) {
	body := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[
		{"id":"first","type":"function","function":{"name":"write_file","arguments":"{\"path\": \"a\", \"content\": \"open"}},
		{"id":"second","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"b\",\"content\":\"\"}"}}]}}]}`
	c, err := chatwire.New("", "https://model.test", &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Submit(context.Background(), struct{}{}, nil)
	var detail *provider.ToolArgumentsError
	if !errors.As(err, &detail) || detail.CallID != "first" || detail.FinishReason != "tool_calls" || detail.Index != 0 || detail.Calls != 2 {
		t.Fatalf("lost response shape: %+v (%v)", detail, err)
	}
	if !strings.Contains(err.Error(), "incomplete JSON") || !strings.Contains(err.Error(), "call 1 of 2") {
		t.Fatalf("message does not classify the defect: %v", err)
	}
}
