#!/usr/bin/env bash
# Spawn a named Claude Code subagent in a new Herdr pane and send it a prompt.
#
# usage: scripts/agent.sh <name> <model> <effort> "<prompt>" [--cwd DIR] [--direction right|down] [--wait] [--timeout MS] [--advisor none|fable|opus|sonnet]
#   name      unique live agent name, [a-z][a-z0-9_-]{0,31}
#   model     fable | opus | sonnet | full model id
#   effort    low | medium | high | xhigh | max
#   prompt    the full task prompt (point it at AGENTS.md)
#   advisor   defaults by model: sonnet -> opus, opus -> fable, fable -> none (AGENTS.md table)
#
# Policy (AGENTS.md): fable above low or opus above xhigh needs Mike's approval first.
set -euo pipefail

if [ "${HERDR_ENV:-}" != "1" ]; then
  echo "not running inside Herdr" >&2; exit 1
fi

name=${1:?name}; model=${2:?model}; effort=${3:?effort}; prompt=${4:?prompt}; shift 4
prompt="$prompt

Rule from Mike: deleting is a decision, not a side effect. Never run rm -rf, rm -r, git clean, git reset --hard, git checkout -- ., a force push, or anything that removes a directory on your own. If something must go, move it into coordination/trash/ (or a trash/ folder in your scratchpad) and list it in your report so Mike can decide. Scratch files and temp databases go in your scratchpad directory only."
cwd=$PWD; direction=""; wait_flag=""; timeout=600000; advisor=""
while [ $# -gt 0 ]; do
  case $1 in
    --cwd) cwd=$2; shift 2;;
    --direction) direction=$2; shift 2;;
    --wait) wait_flag=1; shift;;
    --timeout) timeout=$2; shift 2;;
    --advisor) advisor=$2; shift 2;;
    *) echo "unknown arg $1" >&2; exit 2;;
  esac
done

if [ -z "$advisor" ]; then
  case "$model" in
    sonnet*|claude-sonnet*) advisor=opus;;
    opus*|claude-opus*) advisor=fable;;
    *) advisor=none;;
  esac
fi
advisor_args=()
[ "$advisor" != "none" ] && advisor_args=(--advisor "$advisor")

case "$model:$effort" in
  fable:low|opus:low|opus:medium|opus:high|opus:xhigh|sonnet:*) ;;
  *) echo "warning: $model/$effort is above the default policy; confirm Mike approved it" >&2;;
esac

if [ -z "$direction" ]; then
  layout=$(herdr pane layout --pane "$HERDR_PANE_ID")
  w=$(printf '%s' "$layout" | python3 -c 'import sys,json;d=json.load(sys.stdin)["result"]["layout"];p=[x for x in d["panes"] if x["pane_id"]==sys.argv[1]][0]["rect"];print(p["width"])' "$HERDR_PANE_ID")
  h=$(printf '%s' "$layout" | python3 -c 'import sys,json;d=json.load(sys.stdin)["result"]["layout"];p=[x for x in d["panes"] if x["pane_id"]==sys.argv[1]][0]["rect"];print(p["height"])' "$HERDR_PANE_ID")
  if [ "$w" -gt $((h * 3)) ]; then direction=right; else direction=down; fi
fi

split=$(herdr pane split --current --direction "$direction" --cwd "$cwd" --no-focus)
pane=$(printf '%s' "$split" | python3 -c 'import sys,json;print(json.load(sys.stdin)["result"]["pane"]["pane_id"])')

herdr agent start "$name" --kind claude --pane "$pane" --timeout 60000 -- --model "$model" --effort "$effort" ${advisor_args[@]+"${advisor_args[@]}"} >/dev/null
echo "started $name ($model/$effort, advisor $advisor) in pane $pane"

if [ -n "$wait_flag" ]; then
  herdr agent prompt "$name" "$prompt" --wait --timeout "$timeout"
else
  herdr agent prompt "$name" "$prompt"
fi
