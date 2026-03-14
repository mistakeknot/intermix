# intermix

> See `AGENTS.md` for full development guide.

## Overview

MCP server providing 4 stateless matrix evaluation tools (init_matrix, run_cell, classify_result, report_matrix) with JSONL persistence, subprocess Skaffen execution, hybrid failure taxonomy, and bead-integrated regression tracking.

## Quick Commands

```bash
# Build binary
go build -o bin/intermix-mcp ./cmd/intermix-mcp/

# Run Go tests
go test ./...

# Validate structure
python3 -c "import json; json.load(open('.claude-plugin/plugin.json'))"
```

## Design Decisions (Do Not Re-Ask)

- Go binary for MCP server (mark3labs/mcp-go), mirrors interlab's architecture
- Stateless tools — state reconstructed from JSONL on each call (crash recovery for free)
- Sequential cell execution — no concurrency in v1 (eval data correctness > speed)
- Hybrid taxonomy: fixed categories for aggregation + LLM analysis for nuance
- Subprocess Skaffen spawn: `skaffen --mode print` in isolated clone directories
- Circuit breakers: max cells (100), max consecutive failures (5), per-cell timeout (300s)
