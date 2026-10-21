#!/usr/bin/env bash
#
# End-to-end test of the Streamable HTTP example.
#
#   make example-test-http
#   EXAMPLE_UPSTREAM_PORT=28081 EXAMPLE_GATEWAY_PORT=28848 ./scripts/test-example-http.sh
#   KEEP=1 ./scripts/test-example-http.sh     # leave both processes running
#
# What it proves: example/mcpx.example.yaml compiles into five tools, the notes
# service answers mcpx's calls, and example/python-http/client.py can drive a
# whole MCP session over HTTP (handshake, session header, 202 notification, both
# tools/list and tools/call, DELETE). The client does the per-call assertions;
# this script is the wiring plus the "nothing was left running" guarantee.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(dirname "$here")
# shellcheck source=scripts/example-lib.sh
. "$here/example-lib.sh"

trap cleanup EXIT

UPSTREAM_PORT=${EXAMPLE_UPSTREAM_PORT:-18081}
GATEWAY_PORT=${EXAMPLE_GATEWAY_PORT:-18848}
CONFIG_SRC="$root/example/mcpx.example.yaml"
CLIENT="$root/example/python-http/client.py"
SERVER="$root/example/python-http/server.py"

need_cmd python3 curl
[ -f "$CONFIG_SRC" ] || die "missing $CONFIG_SRC"
[ -f "$CLIENT" ] || die "missing $CLIENT"
[ -f "$SERVER" ] || die "missing $SERVER"

mcpx=$(require_gateway_binary "$root")
init_work_dir
config="$WORK/mcpx.example.yaml"
render_example_config "$CONFIG_SRC" "$config" "$UPSTREAM_PORT" "$GATEWAY_PORT"

say "validating the example config"
check_output=$("$mcpx" check -c "$config" 2>/dev/null)
printf '%s\n' "$check_output"
for tool in notes_list notes_get notes_create notes_delete notes_request; do
  printf '%s\n' "$check_output" | grep -q "\b${tool}\b" ||
    die "mcpx check does not list the tool ${tool}"
done

require_port_free "example service" "$UPSTREAM_PORT" EXAMPLE_UPSTREAM_PORT
require_port_free "gateway" "$GATEWAY_PORT" EXAMPLE_GATEWAY_PORT

say "starting the notes service on 127.0.0.1:${UPSTREAM_PORT}"
start_process upstream python3 "$SERVER" --host 127.0.0.1 --port "$UPSTREAM_PORT" >/dev/null
wait_for_http "http://127.0.0.1:${UPSTREAM_PORT}/healthz" 20 "notes service" ||
  die "the notes service did not become ready on port ${UPSTREAM_PORT}"

say "starting the gateway on 127.0.0.1:${GATEWAY_PORT}"
start_process gateway "$mcpx" serve -c "$config" >/dev/null
wait_for_http "http://127.0.0.1:${GATEWAY_PORT}/healthz" 20 "gateway" ||
  die "the gateway did not become ready on port ${GATEWAY_PORT}"

health=$(curl -fsS "http://127.0.0.1:${GATEWAY_PORT}/healthz")
printf '%s\n' "$health" | grep -q '"tools":5' ||
  die "gateway /healthz does not report five tools: ${health}"
note "$health"

say "running the HTTP client"
set +e
python3 "$CLIENT" --url "http://127.0.0.1:${GATEWAY_PORT}/mcp" 2>"$WORK/client.err" | tee "$WORK/client.out"
client_rc=${PIPESTATUS[0]}
set -e
if [ "$client_rc" -ne 0 ]; then
  cat "$WORK/client.err" >&2
  die "the HTTP client failed with exit ${client_rc}"
fi
grep -qx 'PASS' "$WORK/client.out" || die "the HTTP client did not print PASS"

say "PASS: HTTP example (five tools, tools/list, tools/call, 202 notification, DELETE)"
