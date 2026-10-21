# Shared helpers for the example test scripts.
#
#   . "$(dirname "$0")/example-lib.sh"
#
# Sourced, never executed: it defines functions and no behaviour of its own, so
# a script can decide when to start looking for ports and commands. Everything
# it needs is bash plus curl; no nc, lsof, timeout or jq.
#
# shellcheck shell=bash

# Log files and rendered configs land in one directory per run, removed on exit
# unless KEEP=1. PIDS is what cleanup kills.
PIDS=()
WORK=""

say() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

need_cmd() {
  local name
  for name in "$@"; do
    command -v "$name" >/dev/null 2>&1 || die "required command not found: $name"
  done
}

# init_work_dir creates the scratch directory for logs and temp configs.
#
# It sets WORK in the caller's shell on purpose: a function that returned the
# path via `$(...)` would run in a subshell, so every caller would get its own
# directory and cleanup would delete a different one than the logs went to.
init_work_dir() {
  if [ -z "$WORK" ]; then
    WORK=$(mktemp -d "${TMPDIR:-/tmp}/mcpx-example.XXXXXX")
  fi
}

# port_in_use uses bash's /dev/tcp: a successful connect means something is
# already listening, which is exactly what makes the following start() fail with
# a confusing "address already in use" from a child process.
port_in_use() {
  local port=$1
  (exec 3<>"/dev/tcp/127.0.0.1/${port}") >/dev/null 2>&1
}

# require_port_free name port envvar
require_port_free() {
  local name=$1 port=$2 envvar=$3
  if port_in_use "$port"; then
    die "${name} port ${port} is already in use; stop it or set ${envvar}=<free port>"
  fi
}

# wait_for_http url timeout_seconds label
wait_for_http() {
  local url=$1 timeout=${2:-20} label=${3:-$1}
  local waited=0
  while [ "$waited" -lt "$((timeout * 5))" ]; do
    if curl -fsS --max-time 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
    waited=$((waited + 1))
  done
  return 1
}

# start_process tag command... -> prints the pid
#
# stdout and stderr are kept apart per process: the gateway logs to stderr and
# the example service logs to stderr too, so a failing run can show both without
# mixing them into the assertions.
start_process() {
  local tag=$1
  shift
  init_work_dir
  "$@" >"$WORK/${tag}.out" 2>"$WORK/${tag}.err" &
  local pid=$!
  PIDS+=("$pid")
  printf '%s\n' "$pid"
}

stop_process() {
  local pid=$1
  [ -n "$pid" ] || return 0
  kill "$pid" 2>/dev/null || return 0
  wait "$pid" 2>/dev/null || true
}

# show_logs prints the tails of every captured stream; called on failure only.
show_logs() {
  [ -n "$WORK" ] || return 0
  local file
  for file in "$WORK"/*.err "$WORK"/*.out; do
    [ -f "$file" ] || continue
    printf '\n--- %s (last 15 lines) ---\n' "$(basename "$file")" >&2
    tail -15 "$file" >&2 || true
  done
}

# cleanup is the EXIT trap of every script that sources this file.
cleanup() {
  local rc=$?
  if [ "${KEEP:-0}" = "1" ]; then
    printf 'note: KEEP=1, leaving pids %s alive; logs in %s\n' "${PIDS[*]:-none}" "${WORK:-<none>}" >&2
    return 0
  fi
  local pid
  for pid in ${PIDS[@]+"${PIDS[@]}"}; do
    stop_process "$pid"
  done
  if [ "$rc" -ne 0 ]; then
    show_logs
  fi
  [ -n "$WORK" ] && rm -rf "$WORK"
  return 0
}

# render_example_config src dst upstream_port gateway_port
#
# example/mcpx.example.yaml hardcodes the two ports on purpose (a config file
# that expands environment variables is a different feature), so an overridden
# port is applied by rewriting a copy.
render_example_config() {
  local src=$1 dst=$2 upstream_port=$3 gateway_port=$4
  sed -e "s|127\.0\.0\.1:18081|127.0.0.1:${upstream_port}|g" \
    -e "s|127\.0\.0\.1:18848|127.0.0.1:${gateway_port}|g" \
    "$src" >"$dst"
}

# require_gateway_binary repo_root -> prints the path
require_gateway_binary() {
  local root=$1
  if [ ! -x "$root/bin/mcpx" ]; then
    say "building bin/mcpx"
    make -C "$root" build >/dev/null || die "make build failed"
  fi
  printf '%s\n' "$root/bin/mcpx"
}
