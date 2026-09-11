//go:build darwin || linux

package conversation_test

import (
	"encoding/json"
	"testing"

	"github.com/stevemurr/strap/tool"
)

// Exercise actual tools through the production loop and a model-created child.
// This catches adapters that work directly but lose results or configured tools.
func TestLocalToolsThroughRootAndChild(t *testing.T) {
	dir := t.TempDir()
	files, err := tool.NewFiles(tool.FilesConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := tool.NewShell(tool.ShellConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	c, m := setup(t, append([]tool.Tool{shell}, files.Tools()...)...)
	if _, err := c.Send(c.Root(), "write, edit, and delegate a check"); err != nil {
		t.Fatal(err)
	}
	m.next(t).tool("write_file", `{"path":"result.txt","content":"before\n"}`)
	written := m.next(t)
	var writeResult tool.WriteFileResult
	last := written.request.Messages[len(written.request.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "call-1" {
		t.Fatalf("uncorrelated tool result: %+v", last)
	}
	if err := json.Unmarshal([]byte(last.Content.Text()), &writeResult); err != nil || writeResult.BytesWritten != 7 {
		t.Fatalf("write result: %+v, %v", writeResult, err)
	}
	written.tool("edit_file", `{"path":"result.txt","old":"before","new":"after"}`)
	m.next(t).tool("shell", `{"command":"cat result.txt; exit 3"}`)
	checked := m.next(t)
	last = checked.request.Messages[len(checked.request.Messages)-1]
	var shellResult tool.ShellResult
	if err := json.Unmarshal([]byte(last.Content.Text()), &shellResult); err != nil || shellResult.Output != "after\n" || shellResult.ExitCode == nil || *shellResult.ExitCode != 3 {
		t.Fatalf("shell result: %+v, %v", shellResult, err)
	}
	checked.tool("create_agent", `{"task":"Read and check result.txt"}`)
	var root, child call
	for range 2 {
		got := m.next(t)
		if got.request.Agent == c.Root() {
			root = got
		} else {
			child = got
		}
	}
	if root.answer == nil || child.answer == nil {
		t.Fatal("missing root or child call")
	}
	want := map[string]bool{"shell": true, "read_file": true, "write_file": true, "edit_file": true}
	for _, definition := range child.request.Tools {
		delete(want, definition.Name)
	}
	if len(want) != 0 {
		t.Fatalf("child did not inherit %v", want)
	}
	root.text("delegated")
	userReply(t, c, "delegated")
	child.tool("read_file", `{"path":"result.txt"}`)
	read := m.next(t)
	var readResult tool.ReadFileResult
	last = read.request.Messages[len(read.request.Messages)-1]
	if err := json.Unmarshal([]byte(last.Content.Text()), &readResult); err != nil || readResult.Content != "1\tafter\n" {
		t.Fatalf("child read result: %+v, %v", readResult, err)
	}
	read.text("file checked")
	m.next(t).text("done")
	userReply(t, c, "done")
}
