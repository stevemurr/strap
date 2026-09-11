# Developer notes

A readable conversation with **strong emphasis**, *italics*, ~~old text~~, and `inline code`.

## Changes

- Agent attribution on tool calls
- Compact groups with repeat counts
  - Nested items stay indented
- [x] Markdown rendering
- [ ] Next iteration

### Execution details

1. Read the configuration
2. Apply the update
3. Report the result

> The agent stays available while delegated work runs.
>
> **Tip:** press F2 before selecting text to copy.

#### Code example

```go
func greet(name string) string {
    return "Hello, " + name
}
```

##### Results

| Agent | Operation | Calls |
| :--- | :--- | ---: |
| agent-1 | Read file | 3 |
| agent-2 | Write file | 1 |

###### References

[Documentation](https://example.com/docs) and an image: ![Architecture](https://example.com/diagram.png)

---

Alternative heading
-------------------

Term
: A short definition.
