"""Run the real POSIX bootstrap against isolated fake system commands."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


@unittest.skipIf(os.name == 'nt' or not shutil.which('sh'), 'POSIX shell test runs on Linux CI')
class BootstrapPrivileges(unittest.TestCase):
    def execute(self, uid, sudo=False):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);bin_dir=root/'bin';bin_dir.mkdir()
            log=root/'commands';payload=root/'payload'
            commands={
                'id': 'echo "$FAKE_UID"',
                'python3': 'printf "python3" >> "$COMMAND_LOG"; for arg do printf " <%s>" "$arg" >> "$COMMAND_LOG"; done; printf "\\n" >> "$COMMAND_LOG"',
                'curl': ':',
                'mktemp': 'echo "$TEMP_PAYLOAD"',
                'rm': ':',
            }
            if sudo:
                commands['sudo']='printf "sudo" >> "$COMMAND_LOG"; for arg do printf " <%s>" "$arg" >> "$COMMAND_LOG"; done; printf "\\n" >> "$COMMAND_LOG"'
            for name,body in commands.items():
                path=bin_dir/name;path.write_text('#!/bin/sh\n'+body+'\n');path.chmod(0o755)
            # PATH deliberately excludes a real sudo, Python and package manager.
            env={**os.environ,'PATH':str(bin_dir),'FAKE_UID':str(uid),'COMMAND_LOG':str(log),'TEMP_PAYLOAD':str(payload)}
            script=Path(__file__).with_name('install.sh').resolve()
            result=subprocess.run(['/bin/sh',str(script),'--admin-email','a b@example.test','--port','19088'],env=env,capture_output=True,text=True)
            return result,log.read_text() if log.exists() else ''

    def test_root_install_does_not_require_sudo(self):
        result,log=self.execute(0)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertIn('<install> <--admin-email> <a b@example.test> <--port> <19088>',log)
        self.assertNotIn('sudo',log)

    def test_nonroot_elevation_preserves_arguments(self):
        result,log=self.execute(1000,True)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertTrue(log.startswith('sudo <sh>'))
        self.assertIn('<a b@example.test>',log)
        self.assertNotIn('python3',log)

    def test_nonroot_without_sudo_stops_before_download(self):
        result,log=self.execute(1000)
        self.assertNotEqual(result.returncode,0)
        self.assertIn('Log in as root',result.stderr)
        self.assertEqual(log,'')


if __name__ == '__main__':unittest.main()
