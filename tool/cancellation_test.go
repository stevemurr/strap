package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Cancel at successive cooperative checkpoints instead of racing a timer against
// fast filesystem operations. The assertions concern atomicity, not check counts.
type checkpointContext struct {
	context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	remaining int
}

func (c *checkpointContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remaining--
	if c.remaining <= 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestFileOperationsRemainAtomicAcrossCancellation(t *testing.T) {
	for _, operation := range []string{"read_file", "write_file", "edit_file"} {
		for checkpoint := 1; checkpoint <= 12; checkpoint++ {
			t.Run(fmt.Sprintf("%s/%d", operation, checkpoint), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "file")
				if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				_, kit := fileTools(t, FilesConfig{Dir: dir})
				args := map[string]any{"path": "file"}
				switch operation {
				case "read_file":
					args["offset"] = nil
					args["limit"] = nil
				case "write_file":
					args["content"] = "replacement"
				case "edit_file":
					args["old"] = "original"
					args["new"] = "replacement"
				}
				raw, _ := MarshalInput(args)
				base, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &checkpointContext{Context: base, cancel: cancel, remaining: checkpoint}
				result, err := kit[operation].Call(ctx, Call{Arguments: raw})
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				want := "replacement"
				if operation == "read_file" || err != nil {
					want = "original"
				}
				if string(data) != want {
					t.Fatalf("partial or canceled write: %q, err=%v", data, err)
				}
				if operation == "read_file" && err == nil {
					var got ReadFileResult
					if err := json.Unmarshal([]byte(result.Content.Text()), &got); err != nil || got.Content != "1\toriginal\n" {
						t.Fatal(got, err)
					}
				}
				leftovers, _ := filepath.Glob(filepath.Join(dir, ".strap-write-*"))
				if len(leftovers) != 0 {
					t.Fatal("temporary files leaked", leftovers)
				}
			})
		}
	}
}
