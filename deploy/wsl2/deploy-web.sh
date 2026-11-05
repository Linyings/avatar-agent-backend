#!/usr/bin/env bash
# Compatibility entry point; API process and in-flight orders are preserved.
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$script_dir/deploy.sh" frontend "$@"
