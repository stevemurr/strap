# Research implementation checkpoints

Implements RESEARCH_STATUS_DESIGN.md in independently tested commits. A stage is
complete only when its code, contracts, and relevant deterministic checks pass.
Live evaluation comes after the complete runtime; no fixture-specific prompting
is promoted into production instructions.

| Area | State |
| --- | --- |
| Rejected tool argument recording | Complete — 835c6ca |
| Progress ledger and passive inspection foundation | Complete — 2e4b611 |
| 1. Researcher registration and role configuration | Complete — d041026 |
| 2. Research assignment and terminal lifecycle | Complete in this checkpoint |
| 3. Immutable research briefs and delivery | Pending |
| 4. Session and HTTP progress APIs | Pending |
| 5. Worker progress API migration | Pending |
| 6. Bounded progress and brief model readers | Pending |
| 7. Current authorization for live read continuations | Pending |
| 8. Selective progress notifications | Pending |
| 9. Notification coverage and exchange admission | Pending |
| 10. Explicit inbox yielding | Pending |
| 11. Assignment-bound researcher diagnostics | Pending |
| 12. Host-issued execution evidence and validation | Pending |
| 13. Cancellation and error evidence preservation | Pending |
| 14. Role prompts and progress presentation | Pending |
| 15. End-to-end acceptance and bounded live evaluation | Pending |

The initial researcher has file/PDF/web reads and messaging. Diagnostics are
added only with their explicit execution contract. Creation alone starts no work.
The existing `submit_work` name is retained for implementation/repair delivery;
research uses its own submission. `wait_for_input` is the selected wait spelling.
