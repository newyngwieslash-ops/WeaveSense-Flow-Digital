"""Configuration contracts, not evidence that a browser or database ran."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]


def test_chain_requires_non_skip_gate_and_preserves_existing_identity_suites():
    shell = (ROOT / 'scripts/agriculture/identity-postgres-ci.sh').read_text()
    assert 'AGRI_IDENTITY_CHAIN_REQUIRED:-0' in shell
    assert "-run '^TestIdentityBrowserGISWriteChain$'" in shell
    assert 'identity-chain-result.py --input' in shell
    assert '$chain_code == 0 && $chain_gate == 0' in shell
    for key in ('AGRI_TEST_PG_MIGRATION_DSN', 'AGRI_TEST_PG_RUNTIME_DSN',
                'AGRI_TEST_GEO_MIGRATION_DSN', 'AGRI_TEST_GEO_RUNTIME_DSN'):
        assert f'export {key}=' in shell
    for suite in ('TestIdentityRealHTTPGoPython', 'test_agriculture_identity_postgres.py',
                  'test_agriculture_legacy_write_integration.py'):
        assert suite in shell
    assert '2> "$private/chain-stderr.log"' in shell
    assert '> "$private/chain.jsonl"' in shell
    assert '-p 127.0.0.1::6379 -e REDIS_PASSWORD redis:7.4.2-alpine' in shell
    assert 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping' in shell
    assert 'Non-loopback Redis binding refused' in shell
    assert 'export REDIS_URL="redis://:$REDIS_PASSWORD@127.0.0.1:' in shell
    assert 'docker rm -f "$redis_container"' in shell
    assert 'docker container inspect "$redis_container"' in shell
    assert "'redis_image_id':os.environ['IDENTITY_REDIS_IMAGE_ID']" in shell


def test_chain_ci_is_read_only_builds_actual_profiles_and_uploads_no_raw_diagnostics():
    workflow = (ROOT / '.github/workflows/agriculture-identity.yml').read_text()
    assert 'contents: read' in workflow
    assert 'persist-credentials: false' in workflow
    assert "AGRI_IDENTITY_CHAIN_REQUIRED: '1'" in workflow
    assert 'echo "AGRI_E2E_SOURCE_COMMIT=$(python3' in workflow
    assert 'echo "CANVAS_BUILD_COMMIT=$(python3' in workflow
    assert 'bun install --frozen-lockfile --ignore-scripts' in workflow
    assert 'npm ci --ignore-scripts --no-audit --no-fund' in workflow
    assert 'apt-get download nginx nginx-common' in workflow
    assert 'dpkg-deb -x' in workflow
    assert 'identity-chain-result.test.py' in workflow
    for unsafe in ('contents: write', 'git push', 'docker push', '--no-sandbox',
                   'systemctl start', 'service nginx start', 'chain.jsonl', 'chain-stderr.log'):
        assert unsafe not in workflow
    assert 'product/.local/evidence/identity-*.json' in workflow


class IdentityChainConfigurationTests(unittest.TestCase):
    def test_shell_contract(self):
        test_chain_requires_non_skip_gate_and_preserves_existing_identity_suites()

    def test_workflow_contract(self):
        test_chain_ci_is_read_only_builds_actual_profiles_and_uploads_no_raw_diagnostics()


if __name__ == '__main__':
    unittest.main()
