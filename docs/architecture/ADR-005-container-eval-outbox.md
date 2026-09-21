# ADR-005: Container evaluations submit through an outbox

## Context

Coding evals used randomized host temporary directories, then moved each workspace
under its results directory. Models sometimes corrupted the long absolute paths
or confused the live and archived locations. Running several sessions in one
process also prevented every problem from sharing a fixed workspace path.

## Decision

Run one coding problem per agent container. The harness, shell, file tools and
language servers execute locally against `/workspace`. Public task metadata and
starter files come from `/problems`. `/results` holds the trace and execution
result. The CLI has fixed paths and requires fresh writable mounts; host
orchestration owns retries and parallel containers.

After the session and its processes close, publish a versioned manifest and a
workspace snapshot under `/outbox/submission`. Build it under `.pending` and
rename inside the same mount so readers see either no submission or a complete
one. A snapshot failure or interrupted run publishes nothing. Normal session
budget exhaustion and idle completion can submit partial work with their flags.

A separate `strap eval grade` process runs in a fresh container. It receives the
outbox read-only, the matching results mount, and the private ladder read-only
at `/grading`. It copies the submission into its own empty `/workspace`, adds
hidden tests, and executes them with the container's Go toolchain. It updates
the grade and reports without changing the outbox or original agent workspace.
An ungraded execution has outcome `submitted`; it is not counted as a failure.

## Consequences

The agent runtime image includes only public problem fixtures. Hidden tests and
reference solutions are mounted only for grading, without adding a path remapping
layer to any tool. The same image supplies the toolchain to both phases.

Remove the coding runner's scratch paths, workspace moves, batch worker pool and
resume logic. The `-problem` command replaces task/tier batch selection, arbitrary
output paths and in-process parallelism. `list` and `selfcheck` still support
selection because they do not launch agents. The separate coordination interaction
suite keeps its own fixture workflow.

The host must wait for the agent container to exit successfully before consuming
the ready directory, mount it read-only in the grader, and run only one grader
per attempt at a time. Atomic publication is a completeness contract, not a
sandbox or a distributed queue. The implementation rejects symlinks and special
files in snapshots. Fresh grader containers can regrade a submission without
another model call. Public Go APIs accept explicit mount directories for isolated
tests, with the same lifecycle and no alternate host-run implementation.
