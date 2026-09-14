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
| 2. Research assignment and terminal lifecycle | Complete — 977d471 |
| 3. Immutable research briefs and delivery | Complete — eb3910d |
| 4. Session and HTTP progress APIs | Complete — 93bbfd9; collection pagination follows in area 6 |
| 5. Worker progress API migration | Complete — 80b11b1 |
| 6. Bounded progress and brief model readers | Complete — c897f2d |
| 7. Current authorization for live read continuations | Complete — 79185f3; readers exposed to all roles and HTTP |
| 8. Selective progress notifications | Complete — 414366a; bounded references, activity silence, timed findings, immediate attention/delivery |
| 9. Notification coverage and exchange admission | Complete — 101034f; accepted-prefix admission, secondary-work coverage, preserved tool continuations |
| 10. Explicit inbox yielding | Complete — 7f63746; sole-call capability, matched errors, correlated passive yield records |
| 11. Assignment-bound researcher diagnostics | Complete — 407ae05; separate configured shell, atomic binding capture, no inferred assignment |
| 12. Host-issued execution evidence and validation | Complete in this checkpoint; accepted finish references, bounded captures, same-work validation and archive resolution |
| 13. Cancellation and error evidence preservation | Pending |
| 14. Role prompts and progress presentation | Pending |
| 15. End-to-end acceptance and bounded live evaluation | Pending |

The initial researcher has file/PDF/web reads and messaging. Diagnostics are
added only with their explicit execution contract. Creation alone starts no work.
The existing `submit_work` name is retained for implementation/repair delivery;
research uses its own submission. `wait_for_input` is the selected wait spelling.
