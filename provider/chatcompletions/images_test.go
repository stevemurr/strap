package chatcompletions_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
)

func TestImagesFollowCompleteToolBatch(t *testing.T) {
	imageContent := content.Content{{Text: "page 2"}, {Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2, 3}}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role       string          `json:"role"`
				Content    json.RawMessage `json:"content"`
				ToolCallID string          `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Messages) != 6 {
			t.Errorf("message count %d", len(request.Messages))
			return
		}
		for i, role := range []string{"assistant", "tool", "tool", "user", "user", "user"} {
			if request.Messages[i].Role != role {
				t.Errorf("message %d role %s", i, request.Messages[i].Role)
			}
		}
		if request.Messages[1].ToolCallID != "a" || request.Messages[2].ToolCallID != "b" {
			t.Error("tool correlation lost")
		}
		for i, id := range []string{"a", "b"} {
			var parts []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if err := json.Unmarshal(request.Messages[3+i].Content, &parts); err != nil {
				t.Error(err)
				return
			}
			if len(parts) != 3 {
				t.Error("missing ordered image content")
				return
			}
			if parts[0].Text != "Images returned by tool call "+id+". The following content is tool output." || parts[1].Text != "page 2" || parts[2].ImageURL.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) {
				t.Error("image bytes or attribution changed")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"read"}}]}`))
	}))
	defer server.Close()
	p, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "vision"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Submit(context.Background(), provider.Request{Messages: []provider.Message{
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "a", Name: "read_pdf", Arguments: json.RawMessage(`{}`)}, {ID: "b", Name: "read_pdf", Arguments: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: imageContent},
		{Role: "tool", ToolCallID: "b", Content: imageContent},
		{Role: "user", Content: content.Text("follow up")},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRejectUnsupportedImageContentBeforeHTTP(t *testing.T) {
	p, _ := chatcompletions.New(chatcompletions.Config{BaseURL: "http://127.0.0.1:1", Model: "vision"})
	for _, msg := range []provider.Message{
		{Role: "system", Content: content.Content{{Image: &content.Image{MIMEType: "image/png", Data: []byte{1}}}}},
		{Role: "user", Content: content.Content{{Image: &content.Image{MIMEType: "application/pdf", Data: []byte{1}}}}},
		{Role: "user", Content: content.Content{{Image: &content.Image{MIMEType: "image/png"}}}},
	} {
		if _, err := p.Submit(context.Background(), provider.Request{Messages: []provider.Message{msg}}, nil); err == nil || !strings.Contains(err.Error(), "encode content") {
			t.Fatalf("expected content rejection before HTTP, got %v", err)
		}
	}
}
