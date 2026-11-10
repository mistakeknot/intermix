# intermix

MCP server for cross-repo matrix evaluation: run an agent (Skaffen) against
unfamiliar codebases, classify the outcome of each cell, and track failure
patterns over time.

## Tools

- `init_matrix` — define a matrix of repositories and tasks
- `run_cell` — execute one cell in an isolated clone
- `classify_result` — assign a failure category (fixed taxonomy plus analysis)
- `report_matrix` — summarise results and regressions
- `run_batch` — launch several cells in parallel tmux sessions
- `poll_batch` — wait for the active batch sessions and collect results

The core tools are stateless: matrix state is reconstructed from JSONL files on
each call, which supports recovery after a crash. Progress that has not yet been
written out can still be lost if a run is interrupted. `run_cell` executes one
cell at a time, with limits on total cells, consecutive failures and per-cell
time.

## Build and test

```bash
go build -o bin/intermix-mcp ./cmd/intermix-mcp/
go test ./...
```

See `AGENTS.md` for the development guide.
