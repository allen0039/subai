#!/usr/bin/env python3
"""Audit the fixed upstream import; no network, Git mutations or credentials."""
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BASELINE = ROOT / 'docs/subai/UPSTREAM_FILES.json'


def audit():
    baseline = json.loads(BASELINE.read_text())
    changed, missing = [], []
    for name, digest in baseline.items():
        path = ROOT / name
        if not path.is_file():
            missing.append(name)
        elif hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            changed.append(name)
    result = {
        'upstream': json.loads((ROOT / 'docs/subai/UPSTREAM.json').read_text()),
        'tracked_upstream_files': len(baseline),
        'unchanged': len(baseline) - len(changed) - len(missing),
        'changed': changed,
        'missing': missing,
        'inventory': {
            'frontend_views': sorted(n for n in baseline if n.startswith('frontend/src/views/') and n.endswith('.vue') and '/__tests__/' not in n),
            'backend_route_files': sorted(n for n in baseline if n.startswith('backend/internal/server/routes/') and n.endswith('.go') and not n.endswith('_test.go')),
            'backend_services': sorted(n for n in baseline if n.startswith('backend/internal/service/') and n.endswith('.go') and not n.endswith('_test.go')),
            'database_migrations': sorted(n for n in baseline if n.startswith('backend/migrations/') and n.endswith('.sql')),
            'upstream_workflows': sorted(n for n in baseline if n.startswith('.github/workflows/')),
        },
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return int(bool(missing))


if __name__ == '__main__':
    raise SystemExit(audit())
