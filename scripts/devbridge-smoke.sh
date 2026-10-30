#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

DEFAULT_DATA_ROOT="/private/tmp/devbridge-product-smoke/data"
DATA_ROOT="${DEVBRIDGE_DATA_ROOT:-${DEFAULT_DATA_ROOT}}"
LOG_DIR="${DATA_ROOT}/logs"
RUNTIME_ROOT="${DATA_ROOT}/runtime"
LAUNCH_ROOT="/private/tmp/devbridge-product-smoke/launcher"

SERVER_ADDR="${DEVBRIDGE_SERVER_ADDR:-127.0.0.1:8081}"
MCP_ADDR="${DEVBRIDGE_MCP_ADDR:-127.0.0.1:8080}"

SERVER_URL="http://${SERVER_ADDR}"
MCP_URL="http://${MCP_ADDR}"

SERVER_PID_FILE="${RUNTIME_ROOT}/devbridge.pid"
MCP_PID_FILE="${RUNTIME_ROOT}/devbridge-mcp.pid"

SERVER_BIN="${LAUNCH_ROOT}/devbridge"
MCP_BIN="${LAUNCH_ROOT}/devbridge-mcp"

SERVER_BOOT_LOG="${LAUNCH_ROOT}/devbridge-start.log"
SERVER_LOG="${RUNTIME_ROOT}/devbridge.log"
MCP_LOG="${RUNTIME_ROOT}/devbridge-mcp.log"

CORE_STRUCTURED_LOG="${LOG_DIR}/devbridge.jsonl"
MCP_STRUCTURED_LOG="${LOG_DIR}/devbridge-mcp.jsonl"

SOURCE_REPOSITORY="${DEVBRIDGE_SOURCE_REPOSITORY:-${REPO_ROOT}}"
PRODUCT_VERSION="0.1.0-dev"

SERVER_PORT="${SERVER_ADDR##*:}"
MCP_PORT="${MCP_ADDR##*:}"

listener_pids() {
  local port="$1"

  if ! command -v lsof >/dev/null 2>&1; then
    echo "lsof is required to inspect smoke listener ownership." >&2
    return 1
  fi

  lsof -nP -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null | sort -n -u || true
}

format_pids() {
  local pids="$1"

  if [[ -z "${pids}" ]]; then
    printf '%s' "none"
  else
    printf '%s\n' "${pids}" | paste -sd, -
  fi
}

verify_port_free() {
  local port="$1"
  local pids
  pids="$(listener_pids "${port}")"

  if [[ -n "${pids}" ]]; then
    echo "Smoke port ${port} is still occupied by listener PID(s): $(format_pids "${pids}")" >&2
    return 1
  fi
}

force_clear_port() {
  local port="$1"
  local pids pid

  pids="$(listener_pids "${port}")"
  if [[ -n "${pids}" ]]; then
    echo "Force-clearing smoke port ${port} (listener PID(s): $(format_pids "${pids}"))..."
    while IFS= read -r pid; do
      [[ -n "${pid}" ]] || continue
      if [[ "${pid}" == "$$" ]]; then
        echo "Refusing to kill launcher PID ${pid} discovered on smoke port ${port}." >&2
        continue
      fi
      kill -9 "${pid}" 2>/dev/null || true
    done <<<"${pids}"
  fi

  for _ in $(seq 1 50); do
    if verify_port_free "${port}" 2>/dev/null; then
      return
    fi
    sleep 0.1
  done

  verify_port_free "${port}"
}

is_running() {
  local pid_file="$1"

  [[ -f "${pid_file}" ]] || return 1

  local pid
  pid="$(cat "${pid_file}")"

  [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
  kill -0 "${pid}" 2>/dev/null
}

stop_process() {
  local name="$1"
  local pid_file="$2"

  if ! is_running "${pid_file}"; then
    rm -f "${pid_file}"
    return
  fi

  local pid
  pid="$(cat "${pid_file}")"

  if [[ ! "${pid}" =~ ^[0-9]+$ ]]; then
    rm -f "${pid_file}"
    return
  fi
  if [[ "${pid}" == "$$" ]]; then
    echo "Refusing to stop launcher PID ${pid} from ${pid_file}." >&2
    rm -f "${pid_file}"
    return
  fi

  echo "Stopping ${name} (${pid})..."
  kill "${pid}" 2>/dev/null || true

  for _ in $(seq 1 50); do
    if ! kill -0 "${pid}" 2>/dev/null; then
      rm -f "${pid_file}"
      return
    fi
    sleep 0.1
  done

  echo "${name} did not stop gracefully; killing..."
  kill -9 "${pid}" 2>/dev/null || true
  rm -f "${pid_file}"
}

wait_http() {
  local url="$1"
  local name="$2"

  for _ in $(seq 1 100); do
    if curl -fsS "${url}" >/dev/null 2>&1; then
      return
    fi
    sleep 0.1
  done

  echo "${name} failed readiness check: ${url}" >&2
  return 1
}

print_log_tail() {
  local name="$1"
  local log_file="$2"

  echo "--- ${name} log (last 40 lines) ---" >&2
  if [[ -f "${log_file}" ]]; then
    tail -n 40 "${log_file}" >&2
  else
    echo "Log file does not exist: ${log_file}" >&2
  fi
}

wait_listener_identity() {
  local name="$1"
  local port="$2"
  local expected_pid="$3"
  local log_file="$4"
  local actual_pids

  for _ in $(seq 1 100); do
    if ! kill -0 "${expected_pid}" 2>/dev/null; then
      echo "${name} exited before listening on port ${port}; expected PID ${expected_pid}." >&2
      print_log_tail "${name}" "${log_file}"
      return 1
    fi

    actual_pids="$(listener_pids "${port}")"
    if [[ -z "${actual_pids}" ]]; then
      sleep 0.1
      continue
    fi

    if [[ "${actual_pids}" == "${expected_pid}" ]]; then
      return
    fi

    echo "${name} listener ownership mismatch on port ${port}." >&2
    echo "  expected PID: ${expected_pid}" >&2
    echo "  actual listener PID(s): $(format_pids "${actual_pids}")" >&2
    print_log_tail "${name}" "${log_file}"
    return 1
  done

  echo "${name} did not listen on port ${port}; expected PID ${expected_pid}." >&2
  echo "  actual listener PID(s): $(format_pids "$(listener_pids "${port}")")" >&2
  print_log_tail "${name}" "${log_file}"
  return 1
}

cleanup_failed_start() {
  local server_pid="${1:-}"
  local mcp_pid="${2:-}"

  echo "Cleaning up failed smoke start..." >&2
  for pid in "${server_pid}" "${mcp_pid}"; do
    [[ "${pid}" =~ ^[0-9]+$ ]] || continue
    if [[ "${pid}" != "$$" ]]; then
      kill -9 "${pid}" 2>/dev/null || true
    fi
  done
  force_clear_port "${SERVER_PORT}" || true
  force_clear_port "${MCP_PORT}" || true
  rm -f "${SERVER_PID_FILE}" "${MCP_PID_FILE}"
}

start() {
  local server_pid=""
  local mcp_pid=""
  local source_head
  local build_identity

  source_head="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
  build_identity="${PRODUCT_VERSION}+g${source_head:0:7}"

  mkdir -p "${LAUNCH_ROOT}"

  if is_running "${SERVER_PID_FILE}" || is_running "${MCP_PID_FILE}"; then
    echo "Dev Bridge smoke processes are already running."
    echo "Use: $0 restart"
    exit 1
  fi

  echo "Starting Dev Bridge from:"
  echo "  ${REPO_ROOT}"
  echo
  echo "HEAD:"
  git -C "${REPO_ROOT}" log -1 --oneline
  echo

  echo "Building smoke binaries..."
  (
    cd "${REPO_ROOT}"
    go build -ldflags "-X github.com/lazyun/devbridge/internal/buildinfo.ProductVersion=${PRODUCT_VERSION} -X github.com/lazyun/devbridge/internal/buildinfo.GitCommit=${source_head}" -o "${SERVER_BIN}" ./cmd/devbridge
    go build -ldflags "-X github.com/lazyun/devbridge/internal/buildinfo.ProductVersion=${PRODUCT_VERSION} -X github.com/lazyun/devbridge/internal/buildinfo.GitCommit=${source_head}" -o "${MCP_BIN}" ./cmd/devbridge-mcp
  )
  echo "Build: ${build_identity}"

  nohup "${SERVER_BIN}" \
    -mode server \
    -data-root "${DATA_ROOT}" \
    -listen "${SERVER_ADDR}" \
    -admin-test \
    >"${SERVER_BOOT_LOG}" 2>&1 &
  server_pid=$!

  if ! wait_listener_identity "devbridge" "${SERVER_PORT}" "${server_pid}" "${SERVER_BOOT_LOG}"; then
    cleanup_failed_start "${server_pid}" "${mcp_pid}"
    return 1
  fi
  mv "${SERVER_BOOT_LOG}" "${SERVER_LOG}"
  printf '%s\n' "${server_pid}" >"${SERVER_PID_FILE}"

  nohup "${MCP_BIN}" \
    -data-root "${DATA_ROOT}" \
    -core-url "${SERVER_URL}" \
    -listen "${MCP_ADDR}" \
    >"${MCP_LOG}" 2>&1 &
  mcp_pid=$!
  printf '%s\n' "${mcp_pid}" >"${MCP_PID_FILE}"

  if ! wait_listener_identity "devbridge-mcp" "${MCP_PORT}" "${mcp_pid}" "${MCP_LOG}"; then
    cleanup_failed_start "${server_pid}" "${mcp_pid}"
    return 1
  fi

  echo "Waiting for Dev Bridge HTTP readiness..."
  if ! wait_http "${SERVER_URL}/ui/" "devbridge" ||
    ! wait_http "${MCP_URL}/healthz" "devbridge-mcp"; then
    print_log_tail "devbridge" "${SERVER_LOG}"
    print_log_tail "devbridge-mcp" "${MCP_LOG}"
    cleanup_failed_start "${server_pid}" "${mcp_pid}"
    return 1
  fi

  echo
  echo "Dev Bridge is running."
  echo "Console: ${SERVER_URL}/ui/"
  echo "MCP:     ${MCP_URL}/mcp"
  echo
  echo "Logs:"
  echo "  ${SERVER_LOG}"
  echo "  ${MCP_LOG}"
  echo "Structured logs:"
  echo "  ${CORE_STRUCTURED_LOG}"
  echo "  ${MCP_STRUCTURED_LOG}"
}

register_project() {
  local expected_head
  expected_head="$(git -C "${SOURCE_REPOSITORY}" rev-parse HEAD)"

  echo
  echo "Registering smoke repository:"
  echo "  ${SOURCE_REPOSITORY}"
  echo "  HEAD: ${expected_head}"

  local result
  result="$(
    curl -fsS \
      -X POST \
      "${SERVER_URL}/admin/test/v1/fixture" \
      -H 'Content-Type: application/json' \
      -d "$(python3 - "${SOURCE_REPOSITORY}" <<'PY'
import json
import sys

print(json.dumps({
    "source_repository": sys.argv[1],
}))
PY
)"
  )"

  echo
  echo "Fixture:"
  echo "${result}"

  local project_id
  project_id="$(
    printf '%s' "${result}" |
      python3 -c 'import json,sys; print(json.load(sys.stdin)["project_id"])'
  )"

  echo
  echo "Repository state:"
  curl -fsS \
    "${SERVER_URL}/v1/repositories?project_id=${project_id}"
  echo
}

stop() {
  stop_process "devbridge-mcp" "${MCP_PID_FILE}"
  stop_process "devbridge" "${SERVER_PID_FILE}"

  force_clear_port "${SERVER_PORT}"
  force_clear_port "${MCP_PORT}"
  verify_port_free "${SERVER_PORT}"
  verify_port_free "${MCP_PORT}"
  rm -f "${SERVER_PID_FILE}" "${MCP_PID_FILE}"

  echo "Dev Bridge smoke processes stopped."
}

reset_state() {
  force_clear_port "${SERVER_PORT}"
  force_clear_port "${MCP_PORT}"
  verify_port_free "${SERVER_PORT}"
  verify_port_free "${MCP_PORT}"

  if [[ -n "${DEVBRIDGE_DATA_ROOT:-}" || "${DATA_ROOT}" != "${DEFAULT_DATA_ROOT}" ]]; then
    echo "Refusing to reset a user-selected DataRoot: ${DATA_ROOT}" >&2
    echo "Smoke reset may remove only its built-in isolated DataRoot: ${DEFAULT_DATA_ROOT}" >&2
    return 1
  fi

  echo "Removing smoke DataRoot:"
  echo "  ${DATA_ROOT}"

  rm -rf "${DATA_ROOT}"

  mkdir -p "${LAUNCH_ROOT}"
}

service_status() {
  local name="$1"
  local pid_file="$2"
  local port="$3"
  local pid="missing"
  local listeners state
  local pid_alive=false

  if [[ -f "${pid_file}" ]]; then
    pid="$(cat "${pid_file}")"
    if [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null; then
      pid_alive=true
    fi
  fi

  listeners="$(listener_pids "${port}")"

  if [[ -n "${listeners}" ]]; then
    if [[ "${pid_alive}" == true && "${listeners}" == "${pid}" ]]; then
      state="RUNNING"
    else
      state="LISTENER_MISMATCH"
    fi
  elif [[ -f "${pid_file}" ]]; then
    if [[ "${pid_alive}" == true ]]; then
      state="LISTENER_MISMATCH"
    else
      state="STALE_PID"
    fi
  else
    state="STOPPED"
  fi

  echo "${name}:"
  echo "  pid_file: ${pid}"
  echo "  listener: $(format_pids "${listeners}")"
  echo "  status: ${state}"
}

running_build() {
  local response

  response="$(
    curl -fsS \
      -X POST \
      "${MCP_URL}/mcp" \
      -H 'Content-Type: application/json' \
      -H 'Accept: application/json, text/event-stream' \
      -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bridge_diagnostics","arguments":{}}}' \
      2>/dev/null
  )" || return 1

  printf '%s' "${response}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["structuredContent"]["serverVersion"])'
}

status() {
  local build="unavailable"
  local observed_build
  if observed_build="$(running_build)"; then
    build="${observed_build}"
  fi

  echo "Repository:"
  echo "  ${REPO_ROOT}"
  echo "  HEAD: $(git -C "${REPO_ROOT}" rev-parse HEAD)"
  echo "  Build: ${build}"
  echo

  service_status "devbridge" "${SERVER_PID_FILE}" "${SERVER_PORT}"
  echo
  service_status "devbridge-mcp" "${MCP_PID_FILE}" "${MCP_PORT}"

  echo
  echo "Console: ${SERVER_URL}/ui/"
  echo "MCP:     ${MCP_URL}/mcp"
  echo "DataRoot: ${DATA_ROOT}"
  echo "Log dir:  ${LOG_DIR}"
}

print_structured_logs() {
  local selector="${1:-all}"

  case "${selector}" in
    core)
      print_log_tail "devbridge structured" "${CORE_STRUCTURED_LOG}"
      ;;
    mcp)
      print_log_tail "devbridge-mcp structured" "${MCP_STRUCTURED_LOG}"
      ;;
    all)
      print_log_tail "devbridge structured" "${CORE_STRUCTURED_LOG}"
      print_log_tail "devbridge-mcp structured" "${MCP_STRUCTURED_LOG}"
      ;;
    *)
      echo "Usage: $0 logs {core|mcp|all}" >&2
      return 2
      ;;
  esac
}

case "${1:-start}" in
  start)
    start
    ;;

  stop)
    stop
    ;;

  restart)
    stop
    start
    ;;

  reset)
    reset_state
    start
    register_project
    ;;

  status)
    status
    ;;

  logs)
    if [[ $# -gt 2 ]]; then
      echo "Usage: $0 logs {core|mcp|all}" >&2
      exit 2
    fi
    print_structured_logs "${2:-all}"
    ;;

  *)
    echo "Usage: $0 {start|stop|restart|reset|status|logs [core|mcp|all]}" >&2
    exit 2
    ;;
esac
