import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location('manage', Path(__file__).with_name('manage.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class InstallerSafety(unittest.TestCase):
    def archive(self, extra=None):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, 'w') as z:
            for name in ['portal', 'VERSION', 'Runtime.Dockerfile', 'compose.yaml', 'web/dist/index.html', 'scripts/manage.py']:
                z.writestr(name, '0.1.0' if name == 'VERSION' else 'fixture')
            for name, body in extra or []:
                entry=zipfile.ZipInfo()
                entry.filename=name
                z.writestr(entry, body)
        return stream.getvalue()

    def test_extract_rejects_escape_and_backslash(self):
        with tempfile.TemporaryDirectory() as tmp:
            for name in ['../outside', '/outside', 'C:/outside', '..\\outside']:
                with self.assertRaises(ValueError): m.extract(self.archive([(name, 'x')]), Path(tmp) / 'target')

    def test_complete_package_and_marker(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'portal'
            m.extract(self.archive(), root)
            with self.assertRaises(ValueError): m.validate_root(root)
            m.write_private(root / 'installation.json', {'marker': m.MARKER})
            self.assertEqual(m.validate_root(root), root.resolve())
            with self.assertRaises(ValueError): m.validate_root(Path(root.anchor), False)

    def test_uninstall_requires_named_installation_and_explicit_purge(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'portal'
            root.mkdir()
            with self.assertRaises(ValueError): m.validate_root(root)
            m.write_private(root / 'installation.json', {'marker': m.MARKER})
            with self.assertRaises(ValueError): m.uninstall(root, True, '')
            self.assertTrue(root.exists())

    def test_compose_scope_is_fixed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            with patch.object(m, 'run', return_value=b'') as run:
                m.compose(root, 'down', '--remove-orphans')
                args=run.call_args.args[0]
                self.assertIn('starhub-portal', args)
                self.assertNotIn('--volumes', args)
                self.assertNotIn('prune', args)
                self.assertEqual(run.call_args.kwargs['env']['DOCKER_CONFIG'], str(root/'docker-config'))
                self.assertEqual(run.call_args.kwargs['env']['BUILDX_CONFIG'], str(root/'docker-config/buildx'))

    def test_origin_and_environment_validation(self):
        self.assertEqual(m.origin_valid('http://45.129.8.122:18088/'), 'http://45.129.8.122:18088')
        for bad in ['file:///tmp/x', 'https://user:pass@example.test', 'http://host/path']:
            with self.assertRaises(ValueError): m.origin_valid(bad)
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(ValueError): m.env_write(Path(tmp)/'file', {'KEY':'one\nINJECT=value'})

    def test_release_requires_official_asset_digest(self):
        payload={'tag_name':'v0.1.0','assets':[{'name':'starhub-portal-linux-amd64.zip','browser_download_url':'https://github.com/'+m.REPO+'/releases/download/v0.1.0/starhub-portal-linux-amd64.zip','digest':'sha256:'+'0'*64}]}
        with patch.object(m,'architecture',return_value='amd64'),patch.object(m,'download',side_effect=[json.dumps(payload).encode(),b'corrupted']):
            with self.assertRaisesRegex(ValueError,'SHA-256'):m.release()


if __name__ == '__main__': unittest.main()
