package conversation_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/tool"
)

func TestPDFImagesReachModelThroughAgent(t *testing.T) {
	for _, program := range []string{"pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skip("Poppler required")
		}
	}
	pdf, err := tool.NewPDF(tool.PDFConfig{Dir: "../tool/testdata", MaxDimension: 400})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
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
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"pdf-1","type":"function","function":{"name":"read_pdf","arguments":"{\"path\":\"pages.pdf\",\"pages\":[3,1]}"}}]}}]}`)
			return
		}
		if len(request.Messages) != 5 || request.Messages[3].Role != "tool" || request.Messages[3].ToolCallID != "pdf-1" || request.Messages[4].Role != "user" {
			t.Errorf("bad PDF message sequence: %+v", request.Messages)
			return
		}
		var parts []struct {
			Text     string `json:"text"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(request.Messages[4].Content, &parts); err != nil {
			t.Error(err)
			return
		}
		images := 0
		for _, part := range parts {
			if part.ImageURL == nil {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(part.ImageURL.URL, "data:image/png;base64,"))
			if err != nil {
				t.Error(err)
				return
			}
			img, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Error(err)
				return
			}
			if img.Bounds().Dx() != 400 || img.Bounds().Dy() != 300 {
				t.Error("wrong rendered image size")
			}
			images++
		}
		if images != 2 || !strings.Contains(string(request.Messages[4].Content), "page 3 of 3") || !strings.Contains(string(request.Messages[4].Content), "page 1 of 3") {
			t.Error("missing PDF pages or labels")
		}
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"PDF read"}}]}`)
	}))
	defer server.Close()
	p, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "vision", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c := emptyConversation(t)
	if _, err := c.CreateAgent(message.User, agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "Read the PDF"}, Tools: []tool.Tool{pdf}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(c.Root(), "read pages 3 and 1"); err != nil {
		t.Fatal(err)
	}
	userReply(t, c, "PDF read")
	if calls.Load() != 2 {
		t.Fatal("unexpected model loop")
	}
}

func TestImageResultHistoryIsIndependent(t *testing.T) {
	original := content.Content{{Text: "page"}, {Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2, 3}}}}
	page := tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Parameters: testParameters[struct{}](t), Name: "page"}, Invoke: func(context.Context, tool.Call, struct{}) (tool.Result, error) {
		return tool.Result{Content: original}, nil
	}}
	c, m := setup(t, page)
	if _, err := c.Send(c.Root(), "read"); err != nil {
		t.Fatal(err)
	}
	m.next(t).tool("page", `{}`)
	next := m.next(t)
	original[1].Image.Data[0] = 9
	next.request.Messages[3].Content[1].Image.Data[0] = 8
	next.text("done")
	userReply(t, c, "done")
	if _, err := c.Send(c.Root(), "follow-up"); err != nil {
		t.Fatal(err)
	}
	next = m.next(t)
	if next.request.Messages[3].Content[1].Image.Data[0] != 1 {
		t.Fatal("image history mutated across boundary")
	}
}
