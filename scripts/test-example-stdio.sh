#!/usr/bin/env bash
#
# End-to-end test of the stdio example.
#
#   make example-test-stdio
#   EXAMPLE_UPSTREAM_PORT=28081 ./scripts/test-example-stdio.sh
#   KEEP=1 ./scripts/test-example-stdio.sh
#
# What it proves: the same config works over stdio, where the client owns the
# gateway process. example/python-stdio/client.py starts `mcpx stdio`, speaks
# newline-delimited JSON-RPC and does the per-call assertions; this script runs
# it against a live notes service and then checks the one rule stdio cannot
# break — nothing but protocol and results on the client's stdout.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(dirname "$here")
# shellcheck source=scripts/example-lib.sh
. "$here/example-lib.sh"

trap cleanup EXIT

UPSTREAM_PORT=${EXAMPLE_UPSTREAM_PORT:-18081}
CONFIG_SRC="$root/example/mcpx.example.yaml"
CLIENT="$root/example/python-stdio/client.py"
SERVER="$root/example/python-http/server.py"

need_cmd python3 curl
[ -f "$CONFIG_SRC" ] || die "missing $CONFIG_SRC"
[ -f "$CLIENT" ] || die "missing $CLIENT"
[ -f "$SERVER" ] || die "missing $SERVER"

mcpx=$(require_gateway_binary "$root")
init_work_dir
config="$WORK/mcpx.example.yaml"
# The gateway has no listen address in this mode, so only the upstream port is
# rewritten; the gateway port keeps its default and stays unused.
render_example_config "$CONFIG_SRC" "$config" "$UPSTREAM_PORT" 18848

say "validating the example config"
check_output=$("$mcpx" check -c "$config" 2>/dev/null)
printf '%s\n' "$check_output"
printf '%s\n' "$check_output" | grep -q "tools: 5" || die "mcpx check does not report five tools"

require_port_free "example service" "$UPSTREAM_PORT" EXAMPLE_UPSTREAM_PORT

say "starting the notes service on 127.0.0.1:${UPSTREAM_PORT}"
start_process upstream python3 "$SERVER" --host 127.0.0.1 --port "$UPSTREAM_PORT" >/dev/null
wait_for_http "http://127.0.0.1:${UPSTREAM_PORT}/healthz" 20 "notes service" ||
  die "the notes service did not become ready on port ${UPSTREAM_PORT}"

say "running the stdio client (it starts and stops the gateway itself)"
set +e
python3 "$CLIENT" --mcpx "$mcpx" --config "$config" 2>"$WORK/client.err" | tee "$WORK/client.out"
client_rc=${PIPESTATUS[0]}
set -e
if [ "$client_rc" -ne 0 ]; then
  cat "$WORK/client.err" >&2
  die "the stdio client failed with exit ${client_rc}"
fi
grep -qx 'PASS' "$WORK/client.out" || die "the stdio client did not print PASS"

# The gateway writes JSON logs to stderr. If one ever appeared on stdout it
# would corrupt every frame after it, so this is the assertion worth keeping.
if grep -q '"level"' "$WORK/client.out"; then
  grep '"level"' "$WORK/client.out" >&2
  die "gateway logs leaked onto the client's stdout"
fi

say "PASS: stdio example (five tools, tools/list, tools/call, clean stdout)"
