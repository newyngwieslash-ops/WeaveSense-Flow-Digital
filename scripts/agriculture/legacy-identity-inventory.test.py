"""Fresh reconstruction tests; no historical result is reused."""
import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('legacy-identity-inventory.py')
spec = importlib.util.spec_from_file_location('inventory', SCRIPT)
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.app = self.root / 'services/agri-gis/backend/app'
        (self.app / 'schemas').mkdir(parents=True)
        (self.app / 'schemas/request.py').write_text('''
raise RuntimeError('scanner must not execute source')
class BaseRequest:
    investigator_code: str
class CreateRequest(BaseRequest):
    assignee_code: str
class RecordResponse:
    applicant_code: str
    approved_by_code: str
''')
        self.endpoint = self.app / 'endpoint.py'
        self.endpoint.write_text('''
from app.schemas.request import CreateRequest
router = APIRouter(prefix='/api/v1/examples')
@router.post('')
async def create(body: CreateRequest):
    return Service.create(body)
@router.get('/download')
async def download():
    return Service.download()
app.include_router(router)
''')

    def test_schema_actor_target_history_and_identityless_route(self):
        report = inventory.scan(self.root)
        self.assertEqual(report['summary']['declared_routes'], 2)
        fields = report['routes'][0]['schema_identity_fields']
        self.assertEqual({f['field'] for f in fields}, {'investigator_code', 'assignee_code'})
        self.assertTrue(report['routes'][1]['no_caller_identity_field'])
        kinds = {s['field']: s['classification'] for s in report['identity_sites']}
        self.assertEqual(kinds['assignee_code'], 'assignment_target_not_actor')
        self.assertEqual(kinds['applicant_code'], 'response_or_internal_identity_reference_not_authentication')
        self.assertIn('approved_by_code', kinds)
        self.assertEqual(len(report['route_registrations']), 1)

    def test_determinism_and_new_actor_and_credentials_are_not_ignored(self):
        before = inventory.scan(self.root)
        self.assertEqual(before, inventory.scan(self.root))
        with self.endpoint.open('a') as stream:
            stream.write("\n@router.post('/new')\nasync def new(actor_code: str, credential_secret: str): pass\n")
        after = inventory.scan(self.root)
        self.assertNotEqual(before, after)
        self.assertTrue(any(s['field'] == 'actor_code' for s in after['identity_sites']))
        self.assertTrue(any(s['classification'] == 'service_credential_not_human_actor' for s in after['identity_sites']))

    def test_snapshot_cli_detects_new_identityless_route(self):
        args = [sys.executable, str(SCRIPT), '--root', str(self.root)]
        self.assertEqual(subprocess.run([*args, '--write'], capture_output=True).returncode, 0)
        self.assertEqual(subprocess.run([*args, '--check'], capture_output=True).returncode, 0)
        with self.endpoint.open('a') as stream:
            stream.write("\n@router.get('/list')\nasync def listing(): pass\n")
        self.assertNotEqual(subprocess.run([*args, '--check'], capture_output=True).returncode, 0)

    def test_invalid_python_fails_instead_of_skipping_surface(self):
        self.endpoint.write_text('def broken(:')
        with self.assertRaises(SyntaxError):
            inventory.scan(self.root)


if __name__ == '__main__':
    unittest.main()
