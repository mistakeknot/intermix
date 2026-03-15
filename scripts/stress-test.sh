#!/usr/bin/env bash
# stress-test.sh — Run a Skaffen cross-repo stress test campaign
#
# Usage:
#   ./stress-test.sh chi zod click                    # 3 repos × 3 default tasks
#   ./stress-test.sh --tasks add-test chi zod          # 2 repos × 1 task
#   ./stress-test.sh --timeout 900s cobra commander    # custom timeout
#   ./stress-test.sh --all                             # all 12 repos × 3 tasks
#   ./stress-test.sh --list                            # list available repos
#
# Options:
#   --tasks TASK1,TASK2,...   Comma-separated task IDs (default: add-test,refactor-extract,add-feature)
#   --timeout DURATION       Per-cell timeout (default: 600s)
#   --poll-timeout DURATION  Max time to wait for all cells (default: 30m)
#   --bead ID                Parent bead ID for failure tracking
#   --name NAME              Campaign name (default: stress-YYYYMMDD-HHMMSS)
#   --dir PATH               Campaign working directory (default: /tmp/intermix-campaign-YYYYMMDD)
#   --manifest PATH          Path to manifest YAML (default: auto-detected)
#   --all                    Run all repos in the manifest
#   --list                   List available repos and tasks, then exit
#   --dry-run                Show what would run without launching
#   -h, --help               Show this help
#
# Prerequisites:
#   - skaffen on PATH (ln -s os/Skaffen/skaffen ~/.local/bin/skaffen)
#   - Claude Max logged in (claude /login)
#   - tmux, python3 installed
#   - intermix binary built (interverse/intermix/bin/intermix-mcp)

set -euo pipefail

# --- Defaults ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INTERMIX_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$INTERMIX_ROOT/bin/intermix-mcp"
MANIFEST="$INTERMIX_ROOT/examples/skaffen-stress.yaml"
TASKS="add-test,refactor-extract,add-feature"
TIMEOUT="600s"
POLL_TIMEOUT="30m"
BEAD_ID=""
CAMPAIGN_NAME="stress-$(date +%Y%m%d-%H%M%S)"
CAMPAIGN_DIR="/tmp/intermix-campaign-$(date +%Y%m%d)"
DRY_RUN=false
LIST_ONLY=false
ALL_REPOS=false
REPOS=()

# --- Parse args ---
while [[ $# -gt 0 ]]; do
    case "$1" in
        --tasks)      TASKS="$2"; shift 2 ;;
        --timeout)    TIMEOUT="$2"; shift 2 ;;
        --poll-timeout) POLL_TIMEOUT="$2"; shift 2 ;;
        --bead)       BEAD_ID="$2"; shift 2 ;;
        --name)       CAMPAIGN_NAME="$2"; shift 2 ;;
        --dir)        CAMPAIGN_DIR="$2"; shift 2 ;;
        --manifest)   MANIFEST="$2"; shift 2 ;;
        --all)        ALL_REPOS=true; shift ;;
        --list)       LIST_ONLY=true; shift ;;
        --dry-run)    DRY_RUN=true; shift ;;
        -h|--help)
            sed -n '2,/^$/{ s/^# //; s/^#//; p }' "$0"
            exit 0
            ;;
        -*)
            echo "error: unknown option $1" >&2
            exit 1
            ;;
        *)
            REPOS+=("$1"); shift ;;
    esac
done

# --- Validate ---
if [[ ! -f "$BINARY" ]]; then
    echo "error: intermix binary not found at $BINARY" >&2
    echo "  run: cd $INTERMIX_ROOT && go build -o bin/intermix-mcp ./cmd/intermix-mcp/" >&2
    exit 1
fi

if ! command -v skaffen &>/dev/null; then
    echo "error: skaffen not on PATH" >&2
    echo "  run: ln -sf $(dirname "$INTERMIX_ROOT")/os/Skaffen/skaffen ~/.local/bin/skaffen" >&2
    exit 1
fi

if ! command -v tmux &>/dev/null; then
    echo "error: tmux not installed" >&2
    exit 1
fi

if [[ ! -f "$MANIFEST" ]]; then
    echo "error: manifest not found at $MANIFEST" >&2
    exit 1
fi

# --- List mode ---
if $LIST_ONLY; then
    echo "Available repos:"
    python3 -c "
import yaml
with open('$MANIFEST') as f:
    m = yaml.safe_load(f)
for r in m['repos']:
    print(f\"  {r['id']:15s} {r['language']:12s} {r.get('complexity','?')}\")
print()
print('Available tasks:')
for t in m['tasks']:
    repos = t.get('repos', [])
    scope = ', '.join(repos) if repos else 'all'
    print(f\"  {t['id']:25s} {t['difficulty']:8s} ({scope})\")
"
    exit 0
fi

# --- Resolve repos ---
if $ALL_REPOS; then
    REPOS=($(python3 -c "
import yaml
with open('$MANIFEST') as f:
    m = yaml.safe_load(f)
for r in m['repos']:
    print(r['id'])
"))
fi

if [[ ${#REPOS[@]} -eq 0 ]]; then
    echo "error: no repos specified. Use repo names as arguments or --all" >&2
    echo "  run: $0 --list to see available repos" >&2
    exit 1
fi

# Convert to JSON arrays
REPOS_JSON=$(printf '%s\n' "${REPOS[@]}" | python3 -c "import sys,json; print(json.dumps([l.strip() for l in sys.stdin]))")
IFS=',' read -ra TASK_ARR <<< "$TASKS"
TASKS_JSON=$(printf '%s\n' "${TASK_ARR[@]}" | python3 -c "import sys,json; print(json.dumps([l.strip() for l in sys.stdin]))")

CELL_COUNT=$(( ${#REPOS[@]} * ${#TASK_ARR[@]} ))

# --- Summary ---
echo "=== Stress Test Campaign ==="
echo "  Name:     $CAMPAIGN_NAME"
echo "  Repos:    ${REPOS[*]}"
echo "  Tasks:    $TASKS"
echo "  Cells:    $CELL_COUNT"
echo "  Timeout:  $TIMEOUT per cell, $POLL_TIMEOUT poll"
echo "  Dir:      $CAMPAIGN_DIR"
if [[ -n "$BEAD_ID" ]]; then
    echo "  Bead:     $BEAD_ID"
fi
echo ""

if $DRY_RUN; then
    echo "(dry run — exiting)"
    exit 0
fi

# --- MCP helper ---
MCP_CALL="$CAMPAIGN_DIR/mcp-call.py"
mkdir -p "$CAMPAIGN_DIR"

cat > "$MCP_CALL" << 'PYEOF'
#!/usr/bin/env python3
import json, subprocess, sys, os

BINARY = os.environ.get("INTERMIX_BINARY", "intermix-mcp")

def call_tool(tool_name, args, timeout=900):
    init_msg = json.dumps({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                   "clientInfo": {"name": "campaign", "version": "0.1.0"}}
    })
    call_msg = json.dumps({
        "jsonrpc": "2.0", "id": 2, "method": "tools/call",
        "params": {"name": tool_name, "arguments": args}
    })
    proc = subprocess.run(
        [BINARY], input=init_msg + "\n" + call_msg + "\n",
        capture_output=True, text=True, timeout=timeout,
        cwd=args.get("working_directory", os.getcwd())
    )
    lines = [l.strip() for l in proc.stdout.strip().split("\n") if l.strip()]
    if len(lines) < 2:
        return {"error": f"Expected 2 responses, got {len(lines)}", "stderr": proc.stderr}
    return json.loads(lines[-1])

if __name__ == "__main__":
    tool_name = sys.argv[1]
    args = json.loads(sys.argv[2]) if len(sys.argv) > 2 else {}
    timeout = int(sys.argv[3]) if len(sys.argv) > 3 else 900
    result = call_tool(tool_name, args, timeout)
    content = result.get("result", {}).get("content", [])
    if content and content[0].get("type") == "text":
        print(content[0]["text"])
    else:
        print(json.dumps(result, indent=2))
PYEOF
chmod +x "$MCP_CALL"

export INTERMIX_BINARY="$BINARY"

# --- Clean old state ---
echo "Cleaning old campaign state..."
for sess in $(tmux ls -F '#{session_name}' 2>/dev/null | grep '^intermix-' || true); do
    tmux kill-session -t "$sess" 2>/dev/null || true
done
rm -rf "$CAMPAIGN_DIR/intermix.jsonl" "$CAMPAIGN_DIR/.intermix-manifest.json" \
       "$CAMPAIGN_DIR/.intermix-batch.json" "$CAMPAIGN_DIR/cells/" /tmp/intermix/

# --- Init ---
echo "Initializing matrix..."
python3 "$MCP_CALL" init_matrix "$(json_args=$(printf '{"manifest_path":"%s","name":"%s","bead_id":"%s","working_directory":"%s"}' \
    "$MANIFEST" "$CAMPAIGN_NAME" "$BEAD_ID" "$CAMPAIGN_DIR") && echo "$json_args")" 2>&1 | head -3

# --- Launch ---
echo ""
echo "Launching $CELL_COUNT cells..."
LAUNCH_ARGS=$(python3 -c "
import json
print(json.dumps({
    'repos': $REPOS_JSON,
    'tasks': $TASKS_JSON,
    'working_directory': '$CAMPAIGN_DIR',
    'bead_id': '$BEAD_ID'
}))
")
python3 "$MCP_CALL" run_batch "$LAUNCH_ARGS" 300

# --- Poll ---
echo ""
echo "Waiting for results (timeout: $POLL_TIMEOUT)..."

# Convert poll timeout to seconds for python
POLL_SECS=$(python3 -c "
import re
s = '$POLL_TIMEOUT'
m = re.match(r'(\d+)(s|m|h)?', s)
if m:
    v, u = int(m.group(1)), m.group(2) or 's'
    print({'s':1,'m':60,'h':3600}[u] * v)
else:
    print(1800)
")

POLL_ARGS=$(python3 -c "
import json
print(json.dumps({
    'working_directory': '$CAMPAIGN_DIR',
    'bead_id': '$BEAD_ID',
    'timeout': '$TIMEOUT'
}))
")
python3 "$MCP_CALL" poll_batch "$POLL_ARGS" "$POLL_SECS"

echo ""
echo "Campaign complete. Results in: $CAMPAIGN_DIR/cells/"
echo "Run details: ls $CAMPAIGN_DIR/cells/*.run.json"
