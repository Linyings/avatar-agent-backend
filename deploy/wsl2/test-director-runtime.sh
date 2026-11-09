#!/usr/bin/env bash
set -euo pipefail
cd /app/avatar-runtime
cp /workspace/director-motion.mjs /workspace/content-motion.mjs /workspace/workflow-render.mjs /workspace/workflow.py ./
cp -a /workspace/src/. ./src/
cp /workspace/tests/director-motion.test.mjs /workspace/tests/verify-director-runtime.mjs ./tests/
npm run check
node --test tests/*.test.mjs
/opt/avatar-venv/bin/python -m unittest test_workflow.py
node tests/verify-director-runtime.mjs /verification/fixture
