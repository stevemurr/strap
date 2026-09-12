package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/tool"
)

func TestCanceledStartupClosesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A canceled controller rejects root creation after local tools are initialized.
	if err := run(ctx, []string{"-C", t.TempDir()}, io.Discard); err == nil {
		t.Fatal("canceled controller started")
	}
}
func TestMainHelp(t *testing.T) {
	old := os.Args
	os.Args = []string{"strap", "-help"}
	defer func() { os.Args = old }()
	main()
}
func TestUnknownFlagAndMissingAgentErrors(t *testing.T) {
	if err := run(context.Background(), []string{"-unknown"}, io.Discard); err == nil {
		t.Fatal("unknown flag accepted")
	}
	c := conversation.New(context.Background())
	defer c.Close(context.Background())
	for _, op := range managementTools(c) {
		if op.Definition().Name == "list_agents" {
			continue
		}
		if _, err := op.Call(context.Background(), tool.Call{Arguments: json.RawMessage(`{"agent_id":"missing"}`)}); err == nil {
			t.Fatal("unknown agent accepted")
		}
	}
}

func TestStartupReachesTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// No input is sent, so startup never contacts the configured model endpoint.
	err := run(ctx, []string{"-C", t.TempDir(), "-base-url", "http://127.0.0.1:1"}, io.Discard)
	if err != nil && !strings.Contains(err.Error(), "tty") && !strings.Contains(err.Error(), "terminal") {
		t.Fatal(err)
	}
}

func TestInspectionPagingOptionsReachController(t *testing.T) {
	c := conversation.New(context.Background())
	defer c.Close(context.Background())
	if _, err := inspectTool(c).Call(context.Background(), tool.Call{Arguments: json.RawMessage(`{"agent_id":"missing","limit":3,"before":2}`)}); err == nil {
		t.Fatal("unknown agent accepted")
	}
}
