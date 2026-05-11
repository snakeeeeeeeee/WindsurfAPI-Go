#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:3456}"
API_KEY="${API_KEY:-sk-windsurf-default}"
MODEL="${MODEL:-claude-sonnet-4.6}"
MATRIX="${MATRIX:-quick}"
TIMEOUT="${TIMEOUT:-90}"

curl_json() {
  local path="$1"
  local payload="$2"
  curl -fsS --max-time "${TIMEOUT}" \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    "${BASE_URL}${path}" \
    -d "${payload}"
}

curl_sse() {
  local path="$1"
  local payload="$2"
  curl -fsS --no-buffer --max-time "${TIMEOUT}" \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    "${BASE_URL}${path}" \
    -d "${payload}"
}

chat_tool='{"type":"function","function":{"name":"echo_text","description":"Echo text.","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false},"strict":true}}'
messages_tool='{"name":"echo_text","description":"Echo text.","input_schema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}}'
responses_tool='{"type":"function","name":"echo_text","description":"Echo text.","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}}'

run_quick() {
  echo "[1/5] /healthz"
  curl -fsS --max-time "${TIMEOUT}" "${BASE_URL}/healthz" >/dev/null

  echo "[2/5] /v1/chat/completions text"
  curl_json "/v1/chat/completions" '{"model":"'"${MODEL}"'","messages":[{"role":"user","content":"Reply with exactly: hi"}]}' | grep -q '"choices"'

  echo "[3/5] /v1/messages text"
  curl_json "/v1/messages" '{"model":"'"${MODEL}"'","messages":[{"role":"user","content":"Reply with exactly: hi"}]}' | grep -q '"content"'

  echo "[4/5] /v1/responses text"
  curl_json "/v1/responses" '{"model":"'"${MODEL}"'","input":"Reply with exactly: hi"}' | grep -q '"object":"response"'

  echo "[5/5] /debug/direct"
  curl -fsS --max-time "${TIMEOUT}" -H "Authorization: Bearer ${API_KEY}" "${BASE_URL}/debug/direct" | grep -q '"protocol"'
}

run_streams() {
  echo "[stream 1/3] chat/completions"
  local out
  out="$(curl_sse "/v1/chat/completions" '{"model":"'"${MODEL}"'","stream":true,"messages":[{"role":"user","content":"Reply with exactly: hi"}]}')"
  grep -q 'data: \[DONE\]' <<<"${out}"

  echo "[stream 2/3] messages"
  out="$(curl_sse "/v1/messages" '{"model":"'"${MODEL}"'","stream":true,"messages":[{"role":"user","content":"Reply with exactly: hi"}]}')"
  grep -q 'event: message_stop' <<<"${out}"

  echo "[stream 3/3] responses"
  out="$(curl_sse "/v1/responses" '{"model":"'"${MODEL}"'","stream":true,"input":"Reply with exactly: hi"}')"
  grep -q 'event: response.completed' <<<"${out}"
}

run_tools() {
  echo "[tools 1/6] chat first-leg"
  curl_json "/v1/chat/completions" '{"model":"'"${MODEL}"'","messages":[{"role":"user","content":"Use echo_text exactly once with text TOOL_OK."}],"tools":['"${chat_tool}"'],"tool_choice":{"type":"function","function":{"name":"echo_text"}}}' | grep -q '"tool_calls"'

  echo "[tools 2/6] chat tool-result"
  curl_json "/v1/chat/completions" '{"model":"'"${MODEL}"'","messages":[{"role":"user","content":"Use echo_text once, then answer with only the returned text."},{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_reg_1","type":"function","function":{"name":"echo_text","arguments":"{\"text\":\"TOOL_RESULT_OK\"}"}}]},{"role":"tool","tool_call_id":"toolu_reg_1","content":"TOOL_RESULT_OK"}],"tools":['"${chat_tool}"']}' | grep -q '"choices"'

  echo "[tools 3/6] messages first-leg"
  curl_json "/v1/messages" '{"model":"'"${MODEL}"'","messages":[{"role":"user","content":"Use echo_text exactly once with text TOOL_OK."}],"tools":['"${messages_tool}"'],"tool_choice":{"type":"tool","name":"echo_text"}}' | grep -q '"tool_use"'

  echo "[tools 4/6] messages tool-result"
  curl_json "/v1/messages" '{"model":"'"${MODEL}"'","messages":[{"role":"user","content":"Use echo_text once, then answer with only the returned text."},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_reg_1","name":"echo_text","input":{"text":"TOOL_RESULT_OK"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_reg_1","content":"TOOL_RESULT_OK"}]}],"tools":['"${messages_tool}"']}' | grep -q '"content"'

  echo "[tools 5/6] responses first-leg"
  curl_json "/v1/responses" '{"model":"'"${MODEL}"'","input":[{"type":"message","role":"user","content":"Use echo_text exactly once with text TOOL_OK."}],"tools":['"${responses_tool}"'],"tool_choice":{"type":"function","name":"echo_text"}}' | grep -q '"function_call"'

  echo "[tools 6/6] responses tool-result"
  curl_json "/v1/responses" '{"model":"'"${MODEL}"'","input":[{"type":"message","role":"user","content":"Use echo_text once, then answer with only the returned text."},{"type":"function_call","call_id":"toolu_reg_1","name":"echo_text","arguments":"{\"text\":\"TOOL_RESULT_OK\"}"},{"type":"function_call_output","call_id":"toolu_reg_1","output":"TOOL_RESULT_OK"}],"tools":['"${responses_tool}"']}' | grep -q '"object":"response"'
}

case "${MATRIX}" in
  quick)
    run_quick
    ;;
  streams)
    run_quick
    run_streams
    ;;
  tools)
    run_quick
    run_tools
    ;;
  full)
    run_quick
    run_streams
    run_tools
    ;;
  *)
    echo "unknown MATRIX=${MATRIX}; expected quick, streams, tools, or full" >&2
    exit 2
    ;;
esac

echo "ok matrix=${MATRIX} model=${MODEL}"
