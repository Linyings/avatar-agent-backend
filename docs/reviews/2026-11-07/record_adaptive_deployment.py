"""Record the actual WSL services and deployed sources without reading secrets."""
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[3]
deployed = Path('/opt/aicat/avatar-mvp/src')
files = [
    'backend/internal/agent/session.go',
    'backend/internal/agent/execution_followthrough.go',
    'backend/internal/agent/activity.go',
    'backend/internal/avatar/workflow_plan.go',
    'backend/internal/server/avatar_workflow_execute.go',
    'backend/internal/server/agent_packages.go',
    'frontend/src/features/agent/StudioWorkspace.tsx',
    'frontend/src/features/agent/ExecutionStatus.tsx',
    'frontend/src/features/agent/workflowProgress.ts',
]
hashes = []
for name in files:
    source = hashlib.sha256((root / name).read_bytes()).hexdigest()
    live = hashlib.sha256((deployed / name).read_bytes()).hexdigest()
    hashes.append({'file': name, 'source': source, 'deployed': live, 'match': source == live})
containers = []
for name in ['aicat-avatar-api-1', 'aicat-avatar-web-1', 'aicat-avatar-postgres-1']:
    line = subprocess.check_output(['docker', 'inspect', '--format', '{{.State.Status}}|{{.State.Health.Status}}|{{.State.StartedAt}}|{{.Image}}', name], text=True).strip()
    status, health, started_at, image = line.split('|')
    containers.append({'name': name, 'status': status, 'health': health, 'started_at': started_at, 'image': image})
ip = subprocess.check_output(['hostname', '-I'], text=True).split()[0]
report = {'time': datetime.now(timezone.utc).isoformat(), 'ip_url': f'http://{ip}:8080/', 'localhost_url': 'http://localhost:8080/', 'source_hashes': hashes, 'containers': containers}
Path(__file__).with_name('adaptive-deployment.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
assert all(row['match'] for row in hashes), 'Deployed sources differ'
assert all(row['status'] == 'running' and row['health'] == 'healthy' for row in containers), 'Service health check failed'
print(json.dumps({'ip_url': report['ip_url'], 'matching_sources': len(hashes), 'healthy_services': len(containers)}))
