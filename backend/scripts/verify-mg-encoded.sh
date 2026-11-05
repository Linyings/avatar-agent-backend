#!/usr/bin/env bash
set -euo pipefail
mkdir -p /tmp/mg-check/tests /tmp/mg-check/catalog
tar -C /input -cf - mg-native.mjs mg-shell.js native-styles.json mg-upstream workflow-render.mjs tests/smoke-mg-encoded.mjs catalog/mg-3d-render-reference.mp4 catalog/mg-liquid-reference.mp4 | tar -C /tmp/mg-check -xf -
ln -s /app/avatar-runtime/node_modules /tmp/mg-check/node_modules
cd /app/avatar-runtime
node /tmp/mg-check/tests/smoke-mg-encoded.mjs /evidence/encoded
