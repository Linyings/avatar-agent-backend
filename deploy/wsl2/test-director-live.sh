#!/usr/bin/env bash
set -euo pipefail
cd /app/avatar-runtime
cp /workspace/director-motion.mjs /workspace/content-motion.mjs /workspace/workflow-render.mjs /workspace/workflow.py ./
cp -a /workspace/src/. ./src/
cp /workspace/tests/verify-director-live.mjs ./tests/
cp /workspace/tests/director-motion.test.mjs ./tests/
npm run check
node --test tests/director-motion.test.mjs
node tests/verify-director-live.mjs /verification/live
