# intermix

MCP server for cross-repo matrix evaluation: run an agent (Skaffen) against
unfamiliar codebases, classify the outcome of each cell, and track failure
patterns over time.

## Tools

- `init_matrix` — define a matrix of repositories and tasks
- `run_cell` — execute one cell in an isolated clone
- `classify_result` — assign a failure category (fixed taxonomy plus analysis)
- `report_matrix` — summarise results and regressions

State is kept in JSONL files and rebuilt on each call, so a crash loses
nothing. Cells run sequentially, with limits on total cells, consecutive
failures and per-cell time.

## Build and test

```bash
go build -o bin/intermix-mcp ./cmd/intermix-mcp/
go test ./...
```

See `AGENTS.md` for the development guide.
