#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

if [ "${1:-}" = "review" ] && [ -z "${ZAI_API_KEY:-}" ]; then
  echo 'ZAI_API_KEY is empty. Set it before running a Bastion review.' >&2
  exit 1
fi

PI_CODING_AGENT_DIR="$root/.bastion/pi-agent" exec bastion "$@"
