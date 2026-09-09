#!/usr/bin/env bash
set -euo pipefail

TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/prodmap-mcp-agent-test.XXXXXX")
cleanup() { rm -rf -- "$TEST_DIR"; }
trap cleanup EXIT

MCP_AGENT_LIBRARY=1 source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/test-mcp-agent.sh"

test_fail() { printf 'offline runner test failed: %s\n' "$1" >&2; exit 1; }
expect_failure() {
  if "$@" >/dev/null 2>&1; then
    test_fail "expected controlled failure"
  fi
}

result="$TEST_DIR/result.json"
known="$TEST_DIR/known-investigation.json"
events="$TEST_DIR/events.jsonl"
prompt="$TEST_DIR/prompt.md"
cat >"$prompt" <<'EOF'
Use only the bounded Prodmap MCP tools. Do not use a deployment UUID.
EOF
cat >"$result" <<'EOF'
{"deployment_id":"0190d510-0000-7000-8000-000000000001","environment":"reference","service":"payment-api","metric":"latency_p95","comparison_status":"AVAILABLE","classification":"CANDIDATE","direction":"INCREASE","confidence":"LOW","causality_claimed":false,"investigation_key":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","comparison_key":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","classification_key":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","before_value":250000000,"after_value":300000000,"absolute_delta":50000000,"relative_delta":0.2,"before_sample_count":4,"after_sample_count":4,"facts":["p95 increased"],"limitations":["candidate is not causal"]}
EOF
cat >"$known" <<'EOF'
{"data":{"investigation_key":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","causality_claimed":false,"deployment":{"id":"0190d510-0000-7000-8000-000000000001","environment":"reference","service":"payment-api"},"regression":{"comparison_key":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","metric":"latency_p95","status":"AVAILABLE","before":{"value":250000000,"sample_count":4},"after":{"value":300000000,"sample_count":4},"absolute_delta":50000000,"relative_delta":0.2,"classification":{"classification_key":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","result":"CANDIDATE","direction":"INCREASE","confidence":{"level":"LOW"}}}}}
EOF
write_direct_flow() {
  local type=$1 output=$2
  cat >"$output" <<EOF
{"type":"$type","id":"call-list","server":"prodmap_eval","tool":"list_deployments","status":"completed"}
{"type":"$type","id":"call-investigate","server":"prodmap_eval","tool":"investigate_deployment","status":"completed"}
{"type":"turn.completed","status":"completed"}
EOF
}

write_direct_flow mcp_tool_call "$events"

validate_result "$result" "$known"
validate_events "$events" "$prompt"
fixture_prompt="$FIXTURE_DIR/prompt.md"
grep -Fq '"deployment": "<deployment_id returned by list_deployments>"' "$fixture_prompt" || test_fail "prompt does not require the deployment argument"
grep -Fq 'Do not use `deployment_id` as an' "$fixture_prompt" || test_fail "prompt does not prohibit deployment_id as an argument"
grep -Fq 'Do not respond, summarize, or end the task between the two' "$fixture_prompt" || test_fail "prompt allows an intervening response"
grep -Fq 'do not produce a final response' "$fixture_prompt" || test_fail "prompt allows a fabricated result"
validate_output_schema "$FIXTURE_DIR/result.schema.json"
jq '.properties.environment.const = "reference" | del(.properties.environment.type)' "$FIXTURE_DIR/result.schema.json" >"$TEST_DIR/schema-without-const-type.json"
expect_failure validate_output_schema "$TEST_DIR/schema-without-const-type.json"
jq -e '[.properties[] | select(has("const"))] | length == 0' "$FIXTURE_DIR/result.schema.json" >/dev/null || test_fail "schema leaks semantic result constants"
jq -e '(.properties.comparison_status.enum | length > 1) and (.properties.classification.enum | length > 1) and (.properties.direction.enum | length > 1) and (.properties.confidence.enum | length > 1)' "$FIXTURE_DIR/result.schema.json" >/dev/null || test_fail "semantic enums are not open to domain outcomes"

# All known MCP type spellings are normalized from top-level event envelopes.
for type in mcp_tool_call mcpToolCall McpToolCall; do
  write_direct_flow "$type" "$TEST_DIR/$type.jsonl"
  validate_events "$TEST_DIR/$type.jsonl" "$prompt"
done

# The preferred completed envelope deduplicates a preceding started envelope.
cat >"$TEST_DIR/item-envelopes.jsonl" <<'EOF'
{"type":"item.started","item":{"type":"mcpToolCall","id":"call-list","server":"prodmap_eval","tool":"list_deployments","status":"started"}}
{"type":"item.completed","item":{"type":"mcpToolCall","id":"call-list","server":"prodmap_eval","tool":"list_deployments","status":"completed","result":{"status":"failed","type":"tool","server":"untrusted"}}}
{"type":"item.started","item":{"type":"McpToolCall","id":"call-investigate","server":"prodmap_eval","tool":"investigate_deployment","status":"started"}}
{"type":"item.completed","item":{"type":"McpToolCall","id":"call-investigate","server":"prodmap_eval","tool":"investigate_deployment","status":"completed","structured_content":{"type":"shell_command","tool":"other","server":"untrusted"}}}
EOF
validate_events "$TEST_DIR/item-envelopes.jsonl" "$prompt"

# Configuration and local-auth failures occur before any model request.
MCP_AGENT_MODEL=''; expect_failure validate_configuration; MCP_AGENT_MODEL=gpt-5.6-terra
MCP_AGENT_MODEL='not a model'; expect_failure validate_configuration; MCP_AGENT_MODEL=gpt-5.6-terra
MCP_AGENT_RUNS=0; expect_failure validate_configuration
MCP_AGENT_RUNS=6; expect_failure validate_configuration
MCP_AGENT_RUNS=1
fake_codex="$TEST_DIR/codex-no-login"
printf '#!/usr/bin/env bash\nexit 1\n' >"$fake_codex"; chmod +x "$fake_codex"
MCP_AGENT_CODEX_BIN="$fake_codex" MCP_AGENT_PRODMAP_BIN=/bin/true expect_failure main

# Structured fake event regressions operate only on protocol fields.
for mutation in list_failure no_investigate inverted shell web third wrong_server forbidden turn_failed; do
  case "$mutation" in
    list_failure) { cat "$events"; printf '%s\n' '{"type":"mcp_tool_call","server":"prodmap_eval","tool":"list_deployments","status":"failed"}'; } >"$TEST_DIR/mutation.jsonl" ;;
    no_investigate) jq 'select(.tool != "investigate_deployment")' "$events" >"$TEST_DIR/mutation.jsonl" ;;
    inverted) tac "$events" >"$TEST_DIR/mutation.jsonl" ;;
    shell) { cat "$events"; printf '%s\n' '{"type":"shell_command"}'; } >"$TEST_DIR/mutation.jsonl" ;;
    web) { cat "$events"; printf '%s\n' '{"type":"web_search"}'; } >"$TEST_DIR/mutation.jsonl" ;;
    third) { cat "$events"; printf '%s\n' '{"type":"mcp_tool_call","server":"other","tool":"list_deployments"}'; } >"$TEST_DIR/mutation.jsonl" ;;
    wrong_server) jq 'if .tool == "investigate_deployment" then .server = "wrong" else . end' "$events" >"$TEST_DIR/mutation.jsonl" ;;
    forbidden) jq 'if .tool == "investigate_deployment" then .tool = "third_tool" else . end' "$events" >"$TEST_DIR/mutation.jsonl" ;;
    turn_failed) { cat "$events"; printf '%s\n' '{"type":"turn.failed"}'; } >"$TEST_DIR/mutation.jsonl" ;;
  esac
  expect_failure validate_events "$TEST_DIR/mutation.jsonl" "$prompt"
done
records=$(event_protocol_records "$TEST_DIR/mutation.jsonl")
diagnostic=$(event_contract_diagnostic "$records")
[[ "$diagnostic" == *'line='*'event_type=turn.failed'*'item_type='*'item_id='*'server='*'tool='*'status=failed'* ]] || test_fail "event diagnostic is incomplete"

# Final-output validation is schema- and semantic-based rather than textual.
for filter in \
  '.deployment_id="not-a-uuid"' \
  '.classification="NO_SIGNAL"' \
  '.classification_key="sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"' \
  '.before_value=999' \
  '.after_sample_count=999' \
  '.causality_claimed=true' \
  'del(.facts)' \
  '.facts=[]' \
  '.external_id="forbidden"'; do
  jq "$filter" "$result" >"$TEST_DIR/mutation.json"
  expect_failure validate_result "$TEST_DIR/mutation.json" "$known"
done
printf 'not-json\n' >"$TEST_DIR/invalid.json"
expect_failure validate_result "$TEST_DIR/invalid.json" "$known"
printf 'Authorization: Bearer redacted-value\n' >"$TEST_DIR/diagnostic.txt"
expect_failure assert_redacted_file "$TEST_DIR/diagnostic.txt"
printf '%s\n' '{"type":"error","code":"invalid_json_schema","message":"schema must have a type key"}' >"$TEST_DIR/schema-error.jsonl"
diagnostic=$(codex_failure_diagnostic "$TEST_DIR/schema-error.jsonl")
[[ "$diagnostic" == 'type=error code=invalid_json_schema message=schema must have a type key' ]] || test_fail "structured Codex diagnostic was not preserved"

# Read-only and cleanup checks are independent of Codex output formatting.
assert_database_unchanged hash hash '{"services":1}' '{"services":1}'
expect_failure assert_database_unchanged before after '{"services":1}' '{"services":1}'
cleanup_target="$TEST_DIR/cleanup-target"; mkdir "$cleanup_target"; cleanup_directory "$cleanup_target"; [[ ! -e "$cleanup_target" ]] || test_fail "cleanup did not remove temporary data"
cleanup_target="$TEST_DIR/cleanup-failure"; mkdir "$cleanup_target"; (false) || true; cleanup_directory "$cleanup_target"; [[ ! -e "$cleanup_target" ]] || test_fail "failure cleanup did not remove temporary data"
cleanup_target="$TEST_DIR/cleanup-exit-trap"; mkdir "$cleanup_target"
runner_script="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/test-mcp-agent.sh"
bash -c 'MCP_AGENT_LIBRARY=1 source "$1"; ACTIVE_RUN_DIR=$2; trap cleanup_active_run EXIT; false' _ "$runner_script" "$cleanup_target" >/dev/null 2>&1 || true
[[ ! -e "$cleanup_target" ]] || test_fail "exit trap did not remove temporary data"

printf 'offline mcp-agent runner tests passed\n'
