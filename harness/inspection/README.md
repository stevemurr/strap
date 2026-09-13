# Trace inspection

`inspection` reads saved JSONL artifacts or borrowed live session sources without
constructing a harness or executing recorded actions.

```go
reader, err := inspection.OpenJSONL(ctx, path)
if err != nil { return err }
defer reader.Close(context.Background())
view, err := reader.At(ctx, eventlog.Cursor{})
if err != nil { return err }
output, err := view.InspectOutput(ctx, identity.OutputID{Agent: "agent-4", Call: 6})
```

`At` pins one readable prefix. Zero selects the current head; a session-qualified
cursor selects exactly that prefix, including sequence zero. Later appends cannot
change the view. Historical views replay from the beginning; projection caching
and checkpoints are not implemented. Metadata memory grows with the prefix's
entities/indexes; large text remains in the source and is read with bounded pages.

`New(ctx, source)` borrows a read-only source. Closing the reader cancels/joins its
reads and invalidates dependent views, but never closes a borrowed source.
`OpenJSONL` owns and closes its archive handles. Disposing a live source makes its
borrowed views unavailable. A close deadline limits waiting without abandoning
cleanup; another `Close` may join it.

An interrupted archive has failed source health and a finite inspectable prefix.
It does not imply that an output lacking a terminal record is still executing.
Unsupported or corrupt records fail explicitly; inspection does not repair an
archive or resume execution. `Head` reports source health separately from the
recorded lifecycle returned by queries.

`ReadOutputText` accepts content/reasoning, byte offset, and a maximum page size
(up to 1 MiB). `ReadContent`, `ReadRecord`, and explicit `ResolveRecord` preserve
content integrity and cannot access facts committed after the view boundary.

The production length-failure acceptance test is opt-in; user archives are not
committed:

```sh
STRAP_LENGTH_TRACE=/absolute/path/to/trace-20260912-175308.jsonl go test ./harness/inspection -run TestLengthFailureArchive
```
