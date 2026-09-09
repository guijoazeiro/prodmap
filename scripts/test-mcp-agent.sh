#!/usr/bin/env bash
# Opt-in model-driven MCP validation. It deliberately leaves no artifacts behind.
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
FIXTURE_DIR="$REPO_DIR/testdata/mcp-agent"

MCP_AGENT_MODEL=${MCP_AGENT_MODEL:-gpt-5.6-terra}
MCP_AGENT_REASONING_EFFORT=${MCP_AGENT_REASONING_EFFORT:-medium}
MCP_AGENT_RUNS=${MCP_AGENT_RUNS:-1}
MCP_AGENT_CODEX_BIN=${MCP_AGENT_CODEX_BIN:-codex}
MCP_AGENT_PRODMAP_BIN=${MCP_AGENT_PRODMAP_BIN:-"$REPO_DIR/bin/prodmap"}
ACTIVE_RUN_DIR=

fail() {
  printf '%s\n' "mcp-agent test failed: $1" >&2
  return 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable"
}

validate_configuration() {
  [[ "$MCP_AGENT_MODEL" =~ ^[A-Za-z0-9._-]+$ ]] || { fail "model is invalid"; return 1; }
  [[ "$MCP_AGENT_REASONING_EFFORT" =~ ^(minimal|low|medium|high|xhigh|max|ultra)$ ]] || { fail "reasoning effort is invalid"; return 1; }
  [[ "$MCP_AGENT_RUNS" =~ ^[1-5]$ ]] || { fail "runs must be an integer between 1 and 5"; return 1; }
}

validate_fixtures() {
  local expected
  while read -r expected path; do
    [[ $(sha256sum "$FIXTURE_DIR/$path" | awk '{print $1}') == "$expected" ]] || fail "fixture integrity check failed"
    jq -e . "$FIXTURE_DIR/$path" >/dev/null || fail "fixture JSON is invalid"
  done <<'EOF'
22f3774b4deb8194bd811764e7f2c724c091b5db8c9e6421f0b57d87ef990fb2 before.traces.otlp.jsonl
2886d8d7d400ff9a7e8c477f86982619a16b1d19900ce4088933bcaed9b6ac3e after.traces.otlp.jsonl
a613c1fbabf3110ab7592c559882e7da62729ad856b5ca346c7a894f9c04890b deployments.jsonl
EOF
}

validate_output_schema() {
  local schema=$1
  jq -e '
    type == "object" and .type == "object" and .additionalProperties == false and
    (.required | type == "array" and length > 0) and
    (.properties | type == "object") and
    ([.properties[] | select(has("const")) | select((.type? | type) != "string" or (.type | length) == 0)] | length == 0)
  ' "$schema" >/dev/null || { fail "output schema has a const without a type"; return 1; }
}

assert_redacted_file() {
  local file=$1
  [[ -s "$file" ]] || return 0
  # Only report the category: diagnostics must not echo a rejected value.
  if LC_ALL=C grep -Eqi 'external_id|git[_ -]?head|vcs[_ -]?revision|image[_ -]?(reference|id)|artifact[_ -]?identity|authorization:|bearer[[:space:]]+[A-Za-z0-9._-]+|password|token[=:]|[A-Za-z]:\\|/(home|tmp|etc|var)/|https?://|postgres(ql)?://' "$file"; then
    fail "redaction check failed"
  fi
}

sanitize_diagnostic() {
  local value=$1
  if printf '%s' "$value" | LC_ALL=C grep -Eqi 'external_id|git[_ -]?head|vcs[_ -]?revision|image[_ -]?(reference|id)|artifact[_ -]?identity|authorization:|bearer[[:space:]]+[A-Za-z0-9._-]+|password|token[=:]|[A-Za-z]:\\|/(home|tmp|etc|var)/|https?://|postgres(ql)?://'; then
    printf '%s' 'redacted'
    return
  fi
  value=${value//$'\r'/ }
  value=${value//$'\n'/ }
  printf '%s' "${value:0:200}"
}

codex_failure_diagnostic() {
  local events=$1 raw type code message
  raw=$(jq -r -s '
    [ .[]
      | . as $event
      | ($event.item? // {}) as $item
      | select((($event.type? // "") | tostring | test("(error|failed|invalid)"; "i")) or ($event.error? != null) or ($item.error? != null))
      | [($event.type? // $item.type? // "unknown"), ($event.code? // $event.error?.code? // $item.error?.code? // "unknown"), ($event.message? // $event.error?.message? // $item.error?.message? // "no message")]
    ] | first // ["unknown", "unknown", "no structured error event"] | @tsv
  ' "$events" 2>/dev/null || true)
  if [[ -z "$raw" ]]; then
    raw=$'unknown\tunknown\tno structured error event'
  fi
  IFS=$'\t' read -r type code message <<<"$raw"
  printf 'type=%s code=%s message=%s' "$(sanitize_diagnostic "$type")" "$(sanitize_diagnostic "$code")" "$(sanitize_diagnostic "$message")"
}

validate_result() {
  local result=$1 known=$2
  jq -e --slurpfile known "$known" '
    . as $final |
    $known[0].data as $known |
    type == "object" and
    (.deployment_id | type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")) and
    (.investigation_key | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    (.comparison_key | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    (.classification_key | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    (.comparison_status | IN("AVAILABLE", "UNKNOWN")) and
    (.classification | IN("CANDIDATE", "NO_SIGNAL", "UNKNOWN")) and
    (.direction | IN("INCREASE", "DECREASE", "NONE", "UNKNOWN")) and
    (.confidence | IN("LOW", "UNKNOWN")) and
    (.causality_claimed | type == "boolean") and
    (.facts | type == "array" and length > 0) and (.limitations | type == "array" and length > 0) and
    $final.deployment_id == $known.deployment.id and
    $final.environment == $known.deployment.environment and
    $final.service == $known.deployment.service and
    $final.metric == $known.regression.metric and
    $final.comparison_status == $known.regression.status and
    $final.classification == $known.regression.classification.result and
    $final.direction == $known.regression.classification.direction and
    $final.confidence == $known.regression.classification.confidence.level and
    $final.causality_claimed == $known.causality_claimed and
    $final.investigation_key == $known.investigation_key and
    $final.comparison_key == $known.regression.comparison_key and
    $final.classification_key == $known.regression.classification.classification_key and
    $final.before_value == $known.regression.before.value and
    $final.after_value == $known.regression.after.value and
    $final.absolute_delta == $known.regression.absolute_delta and
    $final.relative_delta == $known.regression.relative_delta and
    $final.before_sample_count == $known.regression.before.sample_count and
    $final.after_sample_count == $known.regression.after.sample_count
  ' "$result" >/dev/null || { fail "final response schema or semantics are invalid"; return 1; }
  assert_redacted_file "$result"
}

event_protocol_records() {
  local events=$1
  # Only each JSONL event and its direct item are protocol input. Tool arguments,
  # results, and structured content are deliberately never traversed.
  jq -c -s '
    def text: if . == null then "" else tostring end;
    def lower: ascii_downcase;
    def mcp_type: . == "mcp_tool_call" or . == "mcpToolCall" or . == "McpToolCall";
    def terminal_status($event_type; $event; $item):
      ($event_type | lower) as $type |
      if ($type == "item.completed" or ($type | endswith(".completed"))) then "completed"
      elif ($type == "item.started" or ($type | endswith(".started"))) then "started"
      elif ($type == "turn.failed" or ($type | endswith(".failed"))) then "failed"
      else ($item.status? // $event.status? // "" | text) end;
    [ to_entries[]
      | (.key + 1) as $line
      | .value as $event
      | ($event.item? // null) as $item
      | ($event.type? | text) as $event_type
      | ($item.type? | text) as $item_type
      | (($event_type | mcp_type) or ($item_type | mcp_type)) as $is_mcp
      | (terminal_status($event_type; $event; $item)) as $status
      | {
          line: $line,
          event_type: $event_type,
          item_type: $item_type,
          item_id: ($item.id? // $event.item_id? // $event.id? // "" | text),
          server: ($item.server? // $item.server_name? // $event.server? // $event.server_name? // "" | text),
          tool: ($item.tool? // $item.tool_name? // $item.name? // $event.tool? // $event.tool_name? // $event.name? // "" | text),
          status: $status,
          is_mcp: $is_mcp,
          top_error: (($event_type | lower) == "error" or ($event_type | lower) == "turn.failed" or ($event.error? != null) or ($event.is_error? == true)),
          mcp_failure: ($is_mcp and (($status | lower) == "failed" or ($item.error? != null) or ($item.is_error? == true))),
          prohibited: (if $is_mcp then false else (($event_type + " " + $item_type | lower) | test("(shell|command|web|apply[_-]?patch|file[_-]?(edit|write|modify)|tool_call)")) end)
        }
    ]
  ' "$events"
}

event_contract_diagnostic() {
  local records=$1 selected
  selected=$(jq -c '
    ([.[] | select(.top_error or .mcp_failure or .prohibited)] | first) //
    ([.[] | select(.is_mcp)] | first) //
    {line:0,event_type:"unknown",item_type:"",item_id:"",server:"",tool:"",status:""}
  ' <<<"$records")
  local line event_type item_type item_id server tool status
  line=$(jq -r '.line' <<<"$selected")
  event_type=$(sanitize_diagnostic "$(jq -r '.event_type' <<<"$selected")")
  item_type=$(sanitize_diagnostic "$(jq -r '.item_type' <<<"$selected")")
  item_id=$(sanitize_diagnostic "$(jq -r '.item_id' <<<"$selected")")
  server=$(sanitize_diagnostic "$(jq -r '.server' <<<"$selected")")
  tool=$(sanitize_diagnostic "$(jq -r '.tool' <<<"$selected")")
  status=$(sanitize_diagnostic "$(jq -r '.status' <<<"$selected")")
  printf 'line=%s event_type=%s item_type=%s item_id=%s server=%s tool=%s status=%s' \
    "$line" "$event_type" "$item_type" "$item_id" "$server" "$tool" "$status"
}

validate_events() {
  local events=$1 prompt=$2 records diagnostic
  # The UUID must be discovered through MCP rather than handed to the model.
  if grep -Eq '[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}' "$prompt"; then
    fail "prompt contains a deployment UUID"
  fi
  records=$(event_protocol_records "$events") || { fail "MCP events are not valid JSONL"; return 1; }
  if ! jq -e '
    ([.[] | select(.top_error or .mcp_failure or .prohibited)] | length == 0) as $no_violations |
    ([.[] | select(.is_mcp)]
      | reduce .[] as $call ({};
          ($call.item_id | if . == "" then "line:" + ($call.line | tostring) else . end) as $id |
          if .[$id] == null or ($call.status == "completed" and .[$id].status != "completed") then .[$id] = $call else . end)
      | [.[]] | sort_by(.line)) as $calls |
    $no_violations and
    ($calls | length == 2) and
    ($calls | all(.status == "completed" and .server == "prodmap_eval")) and
    ($calls | map(.tool) == ["list_deployments", "investigate_deployment"])
  ' <<<"$records" >/dev/null; then
    diagnostic=$(event_contract_diagnostic "$records")
    fail "MCP event contract is invalid: $diagnostic"
    return 1
  fi
  assert_redacted_file "$events"
}

assert_database_unchanged() {
  local before_hash=$1 after_hash=$2 state_before=$3 state_after=$4
  [[ "$before_hash" == "$after_hash" && "$state_before" == "$state_after" ]] || fail "MCP changed the fixture database"
}

cleanup_directory() {
  local directory=$1
  rm -rf -- "$directory"
}

cleanup_active_run() {
  if [[ -n "$ACTIVE_RUN_DIR" ]]; then
    cleanup_directory "$ACTIVE_RUN_DIR"
    ACTIVE_RUN_DIR=
  fi
}

fixture_investigation() {
  local project=$1 deployment_id=$2 result=$3
  "$MCP_AGENT_PRODMAP_BIN" investigate --project-dir "$project" --deployment "$deployment_id" \
    --metric latency_p95 --before 5m --after 5m --min-samples 4 --json >"$result"
  jq -e '
    .data.regression.status == "AVAILABLE" and .data.regression.classification.result == "CANDIDATE" and
    .data.regression.classification.direction == "INCREASE" and .data.regression.classification.confidence.level == "LOW" and
    .data.causality_claimed == false
  ' "$result" >/dev/null || fail "fixture does not produce the required investigation"
}

run_once() {
  local run_number=$1 run_dir project db before_hash after_hash state_before state_after deployment_id known events final stderr start elapsed
  run_dir=$(mktemp -d "${TMPDIR:-/tmp}/prodmap-mcp-agent.XXXXXX")
  ACTIVE_RUN_DIR=$run_dir
  project="$run_dir/project"
  mkdir -p "$project"

  "$MCP_AGENT_PRODMAP_BIN" init --project-dir "$project" --json >/dev/null
  "$MCP_AGENT_PRODMAP_BIN" deployments ingest --project-dir "$project" --file "$FIXTURE_DIR/deployments.jsonl" --json >/dev/null
  "$MCP_AGENT_PRODMAP_BIN" telemetry ingest --project-dir "$project" --environment reference --window-start 2026-09-01T18:58:44Z --window-end 2026-09-01T19:03:44Z --file "$FIXTURE_DIR/before.traces.otlp.jsonl" --json >/dev/null
  "$MCP_AGENT_PRODMAP_BIN" telemetry ingest --project-dir "$project" --environment reference --window-start 2026-09-01T19:03:44Z --window-end 2026-09-01T19:08:44Z --file "$FIXTURE_DIR/after.traces.otlp.jsonl" --json >/dev/null

  known="$run_dir/deployments.json"
  "$MCP_AGENT_PRODMAP_BIN" deploys --project-dir "$project" --environment reference --service payment-api --since 2026-09-01T19:03:00Z --until 2026-09-01T19:05:00Z --json >"$known"
  deployment_id=$(jq -er '.data.items | if length == 1 then .[0].id else error("expected one deployment") end' "$known") || fail "fixture deployment lookup failed"
  fixture_investigation "$project" "$deployment_id" "$run_dir/known-investigation.json"

  db="$project/.prodmap/prodmap.db"
  before_hash=$(sha256sum "$db" | awk '{print $1}')
  state_before=$("$MCP_AGENT_PRODMAP_BIN" status --project-dir "$project" --json | jq -c '.data | {services, runtime_instances}')
  events="$run_dir/events.jsonl"
  final="$run_dir/final.json"
  stderr="$run_dir/stderr.txt"
  start=$(date +%s)
  if ! env -u CODEX_API_KEY "$MCP_AGENT_CODEX_BIN" exec --ephemeral --ignore-user-config --sandbox read-only --skip-git-repo-check --json \
    --model "$MCP_AGENT_MODEL" --output-schema "$FIXTURE_DIR/result.schema.json" --output-last-message "$final" \
    -C "$project" \
    -c "model_reasoning_effort=\"$MCP_AGENT_REASONING_EFFORT\"" \
    -c "mcp_servers.prodmap_eval.command=\"$MCP_AGENT_PRODMAP_BIN\"" \
    -c "mcp_servers.prodmap_eval.args=[\"mcp\",\"serve\",\"--project-dir\",\"$project\"]" \
    -c 'mcp_servers.prodmap_eval.required=true' \
    -c 'mcp_servers.prodmap_eval.enabled=true' \
    -c 'mcp_servers.prodmap_eval.enabled_tools=["list_deployments","investigate_deployment"]' \
    -c 'mcp_servers.prodmap_eval.startup_timeout_sec=30' \
    -c 'mcp_servers.prodmap_eval.tool_timeout_sec=35' \
    <"$FIXTURE_DIR/prompt.md" >"$events" 2>"$stderr"; then
    local diagnostic
    diagnostic=$(codex_failure_diagnostic "$events")
    assert_redacted_file "$stderr"
    fail "codex exec failed: $diagnostic"
    return 1
  fi
  elapsed=$(( $(date +%s) - start ))

  validate_events "$events" "$FIXTURE_DIR/prompt.md"
  validate_result "$final" "$run_dir/known-investigation.json"
  assert_redacted_file "$stderr"
  after_hash=$(sha256sum "$db" | awk '{print $1}')
  state_after=$("$MCP_AGENT_PRODMAP_BIN" status --project-dir "$project" --json | jq -c '.data | {services, runtime_instances}')
  assert_database_unchanged "$before_hash" "$after_hash" "$state_before" "$state_after"
  printf 'run %s/%s passed: tools=list_deployments,investigate_deployment classification=CANDIDATE duration=%ss\n' "$run_number" "$MCP_AGENT_RUNS" "$elapsed"
  cleanup_active_run
}

main() {
  validate_configuration
  require_command "$MCP_AGENT_CODEX_BIN"
  require_command jq
  require_command sha256sum
  [[ -x "$MCP_AGENT_PRODMAP_BIN" ]] || { fail "Prodmap binary is unavailable"; return 1; }
  "$MCP_AGENT_CODEX_BIN" login status >/dev/null 2>&1 || { fail "local Codex authentication is unavailable"; return 1; }
  validate_fixtures
  validate_output_schema "$FIXTURE_DIR/result.schema.json"
  local run
  for ((run = 1; run <= MCP_AGENT_RUNS; run++)); do
    run_once "$run"
  done
  printf 'mcp-agent test passed: model=%s effort=%s runs=%s/%s tools=list_deployments,investigate_deployment classification=CANDIDATE\n' \
    "$MCP_AGENT_MODEL" "$MCP_AGENT_REASONING_EFFORT" "$MCP_AGENT_RUNS" "$MCP_AGENT_RUNS"
}

if [[ ${MCP_AGENT_LIBRARY:-0} != 1 ]]; then
  trap cleanup_active_run EXIT
  main "$@"
fi
