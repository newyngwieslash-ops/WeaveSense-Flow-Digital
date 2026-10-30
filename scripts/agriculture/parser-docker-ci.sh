#!/usr/bin/env bash
# Only local disposable Docker test objects; no deployment/push/remote secrets.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
PYTHON=${AGRI_IDENTITY_PYTHON:?absolute locked GIS Python required}
command -v docker >/dev/null
test "$(id -u)" -ne 0
docker info >/dev/null 2>&1
OWN=$(mktemp -d "$ROOT/.local/parser-docker.XXXXXXXX")
IMAGE=""
cleanup() {
  if test -n "$IMAGE"; then docker image rm "$IMAGE" >/dev/null 2>&1 || true; fi
  rm -rf -- "$OWN"
}
trap cleanup EXIT
mkdir "$OWN/context"
python3 "$ROOT/scripts/agriculture/export-python-lock.py" --group main --output "$OWN/context/requirements.txt"
"$PYTHON" - "$ROOT" "$OWN/context" <<'PY'
import hashlib, json, pathlib, shutil, sys
root, context = map(pathlib.Path, sys.argv[1:])
backend = root / 'services/agri-gis/backend'
files = ['app/__init__.py', 'app/core/__init__.py', 'app/core/dataset_files.py',
         'app/core/exceptions.py', 'app/services/__init__.py',
         'app/services/field_workbook_parser.py', 'app/schemas/__init__.py',
         'app/schemas/field_verification.py', 'app/agriculture/__init__.py',
         'app/agriculture/ingestion/__init__.py', 'app/agriculture/ingestion/worker.py',
         'app/agriculture/ingestion/limits.py', 'app/agriculture/ingestion/archive.py']
for name in files:
    source, target = backend / name, context / name
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
shutil.copyfile(root / 'deploy/agriculture/Dockerfile.parser', context / 'Dockerfile')
inputs = ['services/agri-gis/backend/' + p for p in files]
inputs += ['deploy/agriculture/Dockerfile.parser', 'services/agri-gis/backend/pyproject.toml',
           'services/agri-gis/backend/poetry.lock', 'scripts/agriculture/export-python-lock.py']
(context.parent/'build-provenance.json').write_text(json.dumps({
    'source_sha256':{p:hashlib.sha256((root/p).read_bytes()).hexdigest() for p in inputs},
    'requirements_sha256':hashlib.sha256((context/'requirements.txt').read_bytes()).hexdigest(),
}))
PY
# Versioned official base is resolved once to a digest; the build uses that
# digest, and execution uses the newly built local immutable image ID.
docker pull python:3.12.14-slim >/dev/null
BASE=$(docker image inspect python:3.12.14-slim --format '{{index .RepoDigests 0}}')
case "$BASE" in python@sha256:*) ;; *) echo 'invalid official base digest' >&2; exit 1;; esac
docker build --build-arg "BASE_IMAGE=$BASE" --build-arg "BUILD_INSTANCE=$(basename "$OWN")" --iidfile "$OWN/image-id" "$OWN/context" >/dev/null
IMAGE=$(cat "$OWN/image-id")
case "$IMAGE" in sha256:*) ;; *) exit 1;; esac
mkdir -p "$ROOT/.local/evidence"
export AGRI_PARSER_DOCKER_TEST_IMAGE="$IMAGE"
cd "$ROOT/services/agri-gis/backend"
status=0
"$PYTHON" -m pytest -q tests/test_agriculture_ingestion_docker.py --junitxml="$OWN/result.xml" >"$OWN/test.log" 2>&1 || status=$?
"$PYTHON" - "$ROOT" "$OWN/result.xml" "$IMAGE" "$BASE" "$status" "$OWN/build-provenance.json" <<'PY'
import datetime, hashlib, json, os, pathlib, re, subprocess, sys, xml.etree.ElementTree as ET
root, result, image, base, status, build_manifest = sys.argv[1:]
assembly_path = pathlib.Path(root)/'.local/evidence/session-ci-provenance.json'
assembly = json.loads(assembly_path.read_text()) if assembly_path.exists() else None
source_commit = os.environ.get('AGRI_E2E_SOURCE_COMMIT')
if source_commit:
    assert re.fullmatch('[0-9a-f]{40}', source_commit), 'invalid source commit'
    if assembly is not None:
        assert assembly.get('source_commit') == source_commit, 'assembly commit mismatch'
elif assembly is not None:
    source_commit = assembly.get('source_commit')
    assert isinstance(source_commit,str) and re.fullmatch('[0-9a-f]{40}',source_commit)
else:
    source_commit = subprocess.check_output(['git','-C',root,'rev-parse','HEAD'],text=True).strip()
    assert re.fullmatch('[0-9a-f]{40}',source_commit)
cases = list(ET.parse(result).iter('testcase')) if pathlib.Path(result).exists() else []
required = {'test_docker_actual_raster_and_malformed_inputs',
            'test_docker_no_host_files_no_network_readonly',
            'test_docker_timeout_removes_container_and_temp'}
passed = int(status)==0 and {c.attrib['name'] for c in cases} == required and not any(list(c) for c in cases)
paths = ['deploy/agriculture/Dockerfile.parser', 'scripts/agriculture/parser-docker-ci.sh']
paths += [str(p.relative_to(root)) for p in (pathlib.Path(root)/'services/agri-gis/backend/app/agriculture/ingestion').glob('*.py')]
paths += ['services/agri-gis/backend/tests/test_agriculture_ingestion_docker.py',
          'services/agri-gis/backend/tests/test_agriculture_ingestion_parser.py']
build = json.loads(pathlib.Path(build_manifest).read_text())
source_hashes = build['source_sha256'] | {p:hashlib.sha256((pathlib.Path(root)/p).read_bytes()).hexdigest() for p in paths}
report = {'kind':'actual-docker-parser', 'passed':passed, 'exit_code':int(status), 'source_commit':source_commit,
          'time_utc':datetime.datetime.now(datetime.UTC).isoformat(), 'base_digest':base,
          'image_id':image, 'tests':[dict(c.attrib,status='pass' if not list(c) else 'not_pass') for c in cases],
          'source_sha256':source_hashes, 'requirements_sha256':build['requirements_sha256'],
          'assembly_provenance':assembly,
          'assembly_provenance_sha256':hashlib.sha256(assembly_path.read_bytes()).hexdigest() if assembly else None}
(pathlib.Path(root)/'.local/evidence/parser-docker.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps({'passed':passed,'observed_tests':len(cases)}))
sys.exit(0 if passed else 1)
PY
