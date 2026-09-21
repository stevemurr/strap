package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
)

type streaming struct{ release chan struct{} }

func (p streaming) Submit(ctx context.Context, _ provider.Request, o provider.Observer) (provider.Response, error) {
	if err := o.OnDelta(provider.Delta{Channel: provider.ChannelReasoning, Text: "working 🌍"}); err != nil {
		return provider.Response{}, err
	}
	if err := o.OnDelta(provider.Delta{Text: "partial 🌍"}); err != nil {
		return provider.Response{}, err
	}
	select {
	case <-p.release:
		return provider.Response{Content: "partial 🌍 done", Reasoning: "working 🌍"}, nil
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	}
}
func TestHTTPReconnectRecoversActiveOutputAndMatchesSDK(t *testing.T) {
	release := make(chan struct{})
	s := servedSession(t, streaming{release: release}, httpapi.BearerToken("test-token"))
	session, service := s.Session, s.http
	server := httptest.NewServer(service)
	defer server.Close()
	base := "/sessions/" + session.ID()
	_, err := session.Send(session.Root(), "go")
	if err != nil {
		t.Fatal(err)
	}
	response := openStream(t, server, base+"/events/stream")
	decoder := json.NewDecoder(response.Body)
	resumed := projection.New(identity.SessionID(session.ID()))
	var cursor uint64
	for {
		var r httpapi.StreamRecord
		if err = decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.Event == nil {
			t.Fatal(r)
		}
		if err = resumed.Apply(*r.Event); err != nil {
			t.Fatal(err)
		}
		cursor = r.Event.Sequence
		if r.Event.Kind == "output_delta" && string(r.Event.Payload) != "" {
			var delta agent.OutputDelta
			if err := json.Unmarshal(r.Event.Payload, &delta); err != nil {
				t.Fatal(err)
			}
			if delta.Channel != provider.ChannelContent {
				continue
			}
			break
		}
	}
	response.Body.Close()
	id := identity.OutputID{Agent: session.Root(), Call: 1}
	direct, err := session.InspectOutput(context.Background(), id)
	if err != nil || direct.Output.Status != agent.OutputActive {
		t.Fatal(direct, err)
	}
	endpoint := fmt.Sprintf("%s/outputs/%s/1", base, session.Root())
	w := request(t, service, "GET", endpoint, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var remote harness.OutputInspection
	if err = json.Unmarshal(w.Body.Bytes(), &remote); err != nil {
		t.Fatal(err)
	}
	if remote.Output.TextBytes != direct.Output.TextBytes || remote.Output.Status != agent.OutputActive {
		t.Fatal(remote, direct)
	}
	textURL := fmt.Sprintf("%s/text?through=%d&max_bytes=128", endpoint, remote.Output.Through.Sequence)
	w = request(t, service, "GET", textURL, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var text harness.TextPage
	_ = json.Unmarshal(w.Body.Bytes(), &text)
	if text.Text != "partial 🌍" || !text.End {
		t.Fatal(text)
	}
	reasonURL := fmt.Sprintf("%s/text?channel=reasoning&through=%d&max_bytes=128", endpoint, remote.Output.Through.Sequence)
	w = request(t, service, "GET", reasonURL, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var reasoning harness.TextPage
	if err := json.Unmarshal(w.Body.Bytes(), &reasoning); err != nil {
		t.Fatal(err)
	}
	if reasoning.Text != "working 🌍" || reasoning.Channel != provider.ChannelReasoning || !reasoning.End {
		t.Fatal(reasoning)
	}
	// Explicit invalid channels must fail rather than fall back to answer content.
	w = request(t, service, "GET", fmt.Sprintf("%s/text?channel=invalid&through=%d", endpoint, remote.Output.Through.Sequence), nil)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		v, err := session.InspectOutput(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Output.Status == agent.OutputComplete {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err = session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	response = openStream(t, server, fmt.Sprintf("%s/events/stream?after=%d", base, cursor))
	decoder = json.NewDecoder(response.Body)
	for {
		var r httpapi.StreamRecord
		if err = decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.Type == "end" {
			break
		}
		if r.Event == nil {
			t.Fatal(r)
		}
		if err = resumed.Apply(*r.Event); err != nil {
			t.Fatal(err)
		}
	}
	response.Body.Close()
	fresh := projection.New(identity.SessionID(session.ID()))
	sub, err := session.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			break
		}
		if err = fresh.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := resumed.Output(id)
	b, _ := fresh.Output(id)
	if a.Status != agent.OutputComplete || a.TextBytes != b.TextBytes || a.Through != b.Through || a.TextBytes != uint64(len("partial 🌍 done")) {
		t.Fatal(a, b)
	}
}
