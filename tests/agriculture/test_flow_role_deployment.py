"""Deployment wiring checks; not a claim that Docker or real DB startup passed."""
from pathlib import Path
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[2]


class FlowRoleDeploymentTests(unittest.TestCase):
    def test_legacy_runtime_never_loads_operator_env_file(self):
        services = yaml.safe_load((ROOT / 'docker-compose.flow.yml').read_text())['services']
        for name, service in services.items():
            with self.subTest(name=name):
                self.assertNotIn('.env.flow', service.get('env_file', []))
                if name not in {'postgres', 'platform-migrate', 'loom-migrate'}:
                    env = service.get('environment', {})
                    self.assertFalse(any('MIGRATION' in key for key in env))
                    self.assertFalse(any('_migrate:' in str(value) for value in env.values()))

    def test_runtime_and_explicit_migration_identities(self):
        for filename in ('docker-compose.flow.yml', 'docker-compose.agriculture.yml'):
            services = yaml.safe_load((ROOT / filename).read_text())['services']
            for runtime, migration, domain in [('platform', 'platform-migrate', 'platform'),
                                                ('loom-engine', 'loom-migrate', 'intel')]:
                with self.subTest(filename=filename, runtime=runtime):
                    self.assertIn(f'flow_{domain}_app:', services[runtime]['environment']['DATABASE_URL'])
                    self.assertEqual(services[runtime]['depends_on'][migration]['condition'],
                                     'service_completed_successfully')
                    owner_env = services[migration]['environment']
                    self.assertTrue(any(f'flow_{domain}_migrate:' in str(v) for v in owner_env.values()))
                    self.assertNotIn('env_file', services[migration])

    def test_background_roles_not_passed_to_unrelated_workers(self):
        services = yaml.safe_load((ROOT / 'docker-compose.flow.yml').read_text())['services']
        for name, service in services.items():
            env = service.get('environment', {})
            if 'FLOW_PLATFORM_SYSTEM_DSN' in env:
                self.assertEqual(name, 'platform')
            if 'LOOM_SYSTEM_DATABASE_URL' in env:
                self.assertIn(name, {'loom-engine', 'loom-scheduler', 'loom-enrich', 'loom-research'})
        for name in ('loom-engine', 'loom-scheduler', 'loom-enrich', 'loom-research'):
            self.assertIn('flow_intel_system:', services[name]['environment']['LOOM_SYSTEM_DATABASE_URL'])
            self.assertEqual(services[name]['environment']['LOOM_AUTO_MIGRATE'], 'false')

    def test_metrics_scrapers_require_distinct_private_files(self):
        config = yaml.safe_load((ROOT / 'deploy/flow/monitoring/prometheus.yml').read_text())
        jobs = {job['job_name']: job for job in config['scrape_configs']}
        self.assertEqual(jobs['loom-engine']['authorization']['credentials_file'], '/etc/prometheus/loom.token')
        self.assertEqual(jobs['flow-platform']['authorization']['credentials_file'], '/etc/prometheus/platform.token')
        ignored = (ROOT / 'deploy/flow/monitoring/.gitignore').read_text().splitlines()
        self.assertIn('loom.token', ignored)

    def test_bootstrap_fixed_roles_and_quoted_passwords(self):
        source = (ROOT / 'deploy/flow/postgres/init-databases.sh').read_text()
        self.assertNotIn("PASSWORD '${", source)
        self.assertIn("PASSWORD :'owner_password'", source)
        self.assertIn('WITH INHERIT FALSE, SET TRUE', source)
        self.assertIn('NOLOGIN NOINHERIT NOSUPERUSER', source)
        self.assertNotIn('GRANT ALL', source)


if __name__ == '__main__':
    unittest.main()
