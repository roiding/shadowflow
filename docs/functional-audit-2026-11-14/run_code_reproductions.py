"""Run the audit's diagnostic tests through Go's overlay, without repo edits."""

import json
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
BACKEND = HERE.parents[1] / "backend"
PACKAGES = {
    "internal/repository/sqlite": "audit_sqlite_test.go.txt",
    "internal/limitup": "audit_limitup_test.go.txt",
    "internal/datasource/thsquote": "audit_thsquote_test.go.txt",
}

with tempfile.TemporaryDirectory(prefix="shadowflow-functional-audit-") as temp:
    overlay = Path(temp) / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {
        str(BACKEND / package / "functional_audit_overlay_test.go"): str(HERE / source)
        for package, source in PACKAGES.items()
    }}))
    result = subprocess.run(
        ["go", "test", "-overlay=" + str(overlay), "-run", "^TestFunctionalAudit", "-count=1", "-v",
         *("./" + package for package in PACKAGES)],
        cwd=BACKEND, text=True, capture_output=True, timeout=60,
    )
    text = result.stdout + result.stderr
    (HERE / "code-reproductions.log").write_text(text)
    print(text)
    raise SystemExit(result.returncode)
