# Key Shop Tasks

Plan: `docs/superpowers/plans/2026-10-17-key-shop.md`
Spec: `docs/superpowers/specs/2026-10-17-key-shop-design.md`
Branch: `feat/key-shop`
Workspace: `.worktrees/key-shop`

Status values: `pending`, `in_progress`, `complete`, `blocked`.

| ID | Task | Status | Verification |
| --- | --- | --- | --- |
| T1 | Secret identity and billing formula | complete | `go test ./keyshop -count=1` |
| T2 | SQLite quota store and idempotent finalize | complete | `go test ./keyshop -count=1` |
| T3 | Plugin ABI admission hooks | complete | `go test ./keyshop -run 'TestRegister|TestAuthenticate|TestIntercept' -count=1` |
| T4 | Admin API, public session API, embedded pages | complete | `go test ./keyshop -run TestManagementCreateAndPublicSession -count=1` |
| T5 | Stamp lifecycle request id onto usage records | complete | `go test ./internal/runtime/executor/helps -run TestUsageReporterUsesExecutionRequestID -count=1` |
| T6 | Host alias `/key-dashboard` and AGENTS.md flow | complete | `go test ./internal/api -run TestKeyShop -count=1` |
| T7 | Full plugin tests and server compile | complete | `go test ./keyshop -race -count=1`, `go build -buildmode=c-shared`, `go build ./cmd/server` |

## Rules

- Do not start T2 before T1 tests pass.
- Do not start T3 before T2 tests pass.
- T4 depends on T3.
- T5 may start after T4. T6 may start after T4. T7 waits for T1 through T6.
- T7 is the stop gate. Do not stop before T7 is complete.
- Update this file and the plan checkboxes in the same commit as the task they track.
- Do not deploy.

## Advisor gate

Implementation starts only after the advisor records `SPEC_OK` against the spec and plan. A blocking finding returns the files to revision before T1.
