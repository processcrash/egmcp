#!/usr/bin/env bash
# entrypoint.sh — small wrapper that ensures the data directory exists
# and the config file is seeded before delegating to the egmcp binary.
#
# This script exists so that Docker users can mount /data as a volume
# without losing their config and so that the platform can be developed
# in containers without first running egmcp directly on the host.

set -euo pipefail

EGMCP_BIN="${EGMCP_BIN:-/usr/local/bin/egmcp}"

# 1. Ensure /data subdirectories exist (image also creates them, but
#    operators may mount custom paths).
mkdir -p "${EGMCP_DATA_DIR:-/data}" \
         "${EGMCP_INSTANCES_DIR:-/data/instances}" \
         "${EGMCP_PLUGINS_DIR:-/data/plugins}"

# 2. If a config file was specified but doesn't exist, let egmcp
#    bootstrap it on first run. The output will include the random
#    admin password.
CONFIG="${EGMCP_CONFIG:-/data/configs/admin.yaml}"
mkdir -p "$(dirname "$CONFIG")"

# 3. Pass the original args through. In most setups CMD is "egmcp".
if [ "$#" -eq 0 ]; then
  set -- egmcp
fi

# 4. If the first arg is "egmcp" or empty, replace with the binary path.
first="${1:-}"
case "$first" in
  egmcp|"" )
    shift || true
    exec "$EGMCP_BIN" "$@"
    ;;
  * )
    exec "$@"
    ;;
esac
