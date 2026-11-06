#!/usr/bin/env python3
"""Starhub Portal's narrow, local deployment helper. Python 3.10+, stdlib only.

Never accepts arbitrary commands from the HTTP control plane. Only validated
official release versions and validated optional gateway domains are applied.
"""
import argparse
import contextlib
import getpass
import hashlib
import io
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

REPO = 'czy-code-923/starhub-portal'
MINIMUM_CONTROL = (0,25,4)
VERSION = re.compile(r'^v?[0-9]{1,5}\.[0-9]{1,5}\.[0-9]{1,5}$')
MARKER = 'starhub-portal-v1'
DEFAULT_ROOT = Path(os.environ.get('ProgramData', r'C:\ProgramData')) / 'StarhubPortal' if os.name == 'nt' else Path('/opt/starhub-portal')


def write_private(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(path.name + '.new')
    with open(temp, 'w', encoding='utf-8', newline='\n') as f:
        if os.name != 'nt':
            os.fchmod(f.fileno(), 0o600)
        f.write(data if isinstance(data, str) else json.dumps(data, ensure_ascii=False, indent=2))
        f.flush()
        os.fsync(f.fileno())
    temp.replace(path)


def read_json(path):
    return json.loads(Path(path).read_text(encoding='utf-8'))


class CommandFailure(RuntimeError):
    def __init__(self, label, returncode, detail):
        super().__init__(f'{label} failed (exit {returncode}); inspect local service logs')
        self.detail = detail


def run(args, cwd=None, input=None, timeout=600, output=None, env=None):
    result = subprocess.run([str(x) for x in args], cwd=cwd, input=input, stdout=output or subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, check=False, env=env)
    if result.returncode:
        # Persist diagnostics locally; redact values from this installation's secret files.
        detail = result.stderr.decode('utf-8', errors='replace')[-24000:]
        if cwd:
            for name in ('compose.env', 'portal.env'):
                file = Path(cwd) / name
                if file.is_file():
                    for value in environment(file).values():
                        if len(value) >= 8:
                            detail = detail.replace(value, '[redacted]')
            write_private(Path(cwd) / 'last-command-error.log', detail)
        if os.environ.get('GITHUB_ACTIONS') == 'true':
            print(detail, file=sys.stderr)
        label = Path(str(args[0])).name
        if label in ('docker', 'systemctl') and len(args) > 1:
            label += ' ' + str(args[1])
        if label == 'docker compose' and len(args) > 2 and str(args[2]) in ('version', 'build', 'up', 'logs'):
            label += ' ' + str(args[2])
        failure = CommandFailure(label, result.returncode, detail)
        if cwd:
            failure.args = (str(failure) + '; diagnostic: ' + str(Path(cwd) / 'last-command-error.log'),)
        raise failure
    return result.stdout or b''


def download(url, limit=200 << 20):
    if not url.startswith('https://'):
        raise ValueError('HTTPS required')
    request = urllib.request.Request(url, headers={'User-Agent': 'Starhub-Portal-Installer/0.1'})
    with urllib.request.urlopen(request, timeout=60) as response:
        data = response.read(limit + 1)
    if len(data) > limit:
        raise ValueError('Download exceeds size limit')
    return data


def architecture():
    machine = platform.machine().lower()
    if machine in ('amd64', 'x86_64'):
        return 'amd64'
    if machine in ('arm64', 'aarch64'):
        return 'arm64'
    raise RuntimeError('Supported hosts: x86_64 / aarch64, or Windows Docker Desktop Linux containers')


def release(version='latest'):
    if version != 'latest' and not VERSION.fullmatch(version):
        raise ValueError('Invalid release version')
    endpoint = 'latest' if version == 'latest' else 'tags/v' + version.lstrip('v')
    info = json.loads(download('https://api.github.com/repos/' + REPO + '/releases/' + endpoint, 1 << 20))
    tag = info.get('tag_name', '')
    if not VERSION.fullmatch(tag) or info.get('draft') or info.get('prerelease'):
        raise ValueError('Only official stable releases are accepted')
    name = 'starhub-portal-linux-' + architecture() + '.zip'
    asset = next((x for x in info.get('assets', []) if x['name'] == name), None)
    if not asset:
        raise RuntimeError('No compatible official release package')
    url = 'https://github.com/' + REPO + '/releases/download/' + tag + '/' + name
    if asset.get('browser_download_url') != url:
        raise ValueError('Unexpected release asset URL')
    data = download(url)
    digest = asset.get('digest', '')
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', digest):
        raise RuntimeError('Official GitHub asset SHA-256 is unavailable; retry when publishing finishes')
    if hashlib.sha256(data).hexdigest() != digest[7:]:
        raise ValueError('Release SHA-256 mismatch')
    return tag.lstrip('v'), data


def extract(data, target):
    target = Path(target).resolve()
    target.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        entries = z.infolist()
        if len(entries) > 10000 or sum(x.file_size for x in entries) > 300 << 20:
            raise ValueError('Archive size exceeds limit')
        for item in entries:
            if '\\' in item.filename or ':' in item.filename or (item.external_attr >> 16) & 0o170000 == 0o120000:
                raise ValueError('Unsafe archive entry')
            path = (target / item.filename).resolve()
            if not path.is_relative_to(target):
                raise ValueError('Archive path escapes installation directory')
        z.extractall(target)
    for required in ['portal', 'VERSION', 'Runtime.Dockerfile', 'compose.yaml', 'web/dist/index.html', 'scripts/manage.py']:
        if not (target / required).is_file():
            raise ValueError('Incomplete release package: ' + required)
    if os.name != 'nt':
        (target / 'portal').chmod(0o555)


def validate_root(root, require=True):
    root = Path(root).resolve()
    if root == Path(root.anchor) or len(root.parts) < 3 or root.is_symlink():
        raise ValueError('Unsafe installation root')
    if require and (not (root / 'installation.json').is_file() or read_json(root / 'installation.json').get('marker') != MARKER):
        raise ValueError('Not a Starhub Portal installation; refusing to modify')
    return root


def compose(root, *args, input=None, output=None, timeout=600):
    # ProtectHome keeps host credentials private. Buildx still needs writable
    # client state, so keep it inside this installation instead of /root/.docker.
    config = root / 'docker-config'
    if config.is_symlink():
        raise ValueError('Docker client state must not be a symlink')
    config.mkdir(mode=0o700, exist_ok=True)
    env = {**os.environ, 'DOCKER_CONFIG': str(config), 'BUILDX_CONFIG': str(config / 'buildx')}
    return run(['docker', 'compose', '--project-name', 'starhub-portal', '--env-file', str(root / 'compose.env'),
                '-f', str(root / 'compose.yaml'), *args], cwd=root, input=input, output=output, timeout=timeout, env=env)


def environment(path):
    result = {}
    for line in Path(path).read_text(encoding='utf-8').splitlines():
        if line and not line.startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            result[key] = value
    return result


def env_write(path, data):
    if any('\n' in str(v) or '\r' in str(v) for v in data.values()):
        raise ValueError('Multiline environment value rejected')
    write_private(path, ''.join(f'{k}={v}\n' for k, v in data.items()))


def local_origin(root):
    env = environment(root / 'compose.env')
    bind=env.get('BIND_IP','').strip('[]')
    host='[::1]' if ':' in bind else '127.0.0.1'
    return 'http://'+host+':'+str(int(env['PORTAL_PORT']))


def health(root, expected=None, timeout=90):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(local_origin(root)+'/portal/v1/health', timeout=3) as res:
                data = json.load(res)
            if data['status'] == 'ok' and (expected is None or data['version'] == expected):
                return data
        except (OSError, ValueError, KeyError):
            pass
        time.sleep(2)
    raise RuntimeError('Portal health check timed out')


def check_docker(install=False):
    if not shutil.which('docker'):
        if not install or os.name == 'nt':
            raise RuntimeError('Install Docker Desktop with Linux containers on Windows, or Docker Engine + Compose v2 on Linux')
        # Docker's official convenience installer supports Debian/Ubuntu/RHEL-family Linux.
        script = download('https://get.docker.com', 2 << 20)
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(script)
            name = f.name
        try:
            run(['sh', name], timeout=1200)
        finally:
            Path(name).unlink(missing_ok=True)
    if install:
        enable_docker_boot()
    if run(['docker', 'info', '--format', '{{.OSType}}'], timeout=20).strip() != b'linux':
        raise RuntimeError('Docker must be running in Linux containers mode')
    try:
        run(['docker', 'compose', 'version'], timeout=20)
    except CommandFailure as exc:
        missing = any(x in exc.detail.lower() for x in ('is not a docker command', 'unknown command'))
        if not install or os.name == 'nt' or not missing:
            raise RuntimeError('Docker Compose plugin is unavailable or broken. Run docker compose version for details; Linux installation can automatically add a missing official plugin.') from None
        print('Docker Engine is ready; Compose plugin is missing. Installing the verified official Docker Compose plugin.', flush=True)
        install_compose_plugin()
        run(['docker', 'compose', 'version'], timeout=20)
    if install:
        check_buildx(True)


def buildx_version(data):
    match = re.search(r'\bv?(\d+)\.(\d+)\.(\d+)', data.decode('utf-8', errors='replace'))
    return tuple(map(int, match.groups())) if match else None


def check_buildx(install=False):
    try:
        current = buildx_version(run(['docker', 'buildx', 'version'], timeout=20))
    except CommandFailure:
        current = None
    if current is not None and current >= (0, 17, 0):
        return
    if not install or os.name == 'nt':
        raise RuntimeError('Compose builds require Docker Buildx 0.17.0+. Update the Buildx plugin (Docker Desktop on Windows), then retry.')
    print('Buildx is missing or older than 0.17.0; installing a verified official plugin. Existing Docker Engine is preserved.', flush=True)
    install_buildx_plugin()
    current = buildx_version(run(['docker', 'buildx', 'version'], timeout=20))
    if current is None or current < (0, 17, 0):
        raise RuntimeError('Buildx 0.17.0+ is still not active. Check whether a user-specific Docker plugin overrides the system plugin.')


def install_compose_plugin(directory=None):
    return install_docker_plugin('compose', directory)


def install_buildx_plugin(directory=None):
    return install_docker_plugin('buildx', directory, replace_old=True)


def install_docker_plugin(kind, directory=None, replace_old=False):
    # Docker's documented system-wide CLI plugin directory works even when the
    # updater uses a private DOCKER_CONFIG. No Docker daemon restart/reinstall.
    if kind not in ('compose', 'buildx'):
        raise ValueError('Unsupported Docker plugin')
    label = 'Docker Compose' if kind == 'compose' else 'Docker Buildx'
    info = json.loads(download('https://api.github.com/repos/docker/' + kind + '/releases/latest', 2 << 20))
    tag = info.get('tag_name', '')
    if not VERSION.fullmatch(tag) or info.get('draft') or info.get('prerelease'):
        raise ValueError('Only official stable ' + label + ' releases are accepted')
    arch = architecture()
    name = ('docker-compose-linux-' + {'amd64': 'x86_64', 'arm64': 'aarch64'}[arch]) if kind == 'compose' else ('buildx-' + tag + '.linux-' + arch)
    base = 'https://github.com/docker/' + kind + '/releases/download/' + tag + '/'
    url = base + name
    asset = next((x for x in info.get('assets', []) if x.get('name') == name), None)
    if not asset or asset.get('browser_download_url') != url:
        raise ValueError('Official ' + label + ' asset unavailable for this architecture')
    data = download(url)
    digest = asset.get('digest', '')
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', digest):
        checksum_name = name + '.sha256' if kind == 'compose' else 'checksums.txt'
        checksum_url = base + checksum_name
        checksum = next((x for x in info.get('assets', []) if x.get('name') == checksum_name), None)
        if not checksum or checksum.get('browser_download_url') != checksum_url:
            raise ValueError('Official ' + label + ' SHA-256 unavailable; refusing unverified install')
        text = download(checksum_url, 65536).decode('ascii')
        if kind == 'compose':
            digest = 'sha256:' + text.split()[0]
        else:
            matches = [line.split()[0] for line in text.splitlines() if len(line.split()) == 2 and line.split()[1].lstrip('*') == name]
            if len(matches) != 1:
                raise ValueError('Official Buildx checksum entry is missing or ambiguous')
            digest = 'sha256:' + matches[0]
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', digest) or hashlib.sha256(data).hexdigest() != digest[7:]:
        raise ValueError(label + ' SHA-256 mismatch; plugin not installed')
    if not data.startswith(b'\x7fELF'):
        raise ValueError('Official ' + label + ' asset is not a Linux executable')
    directory = Path(directory) if directory is not None else Path('/usr/local/lib/docker/cli-plugins')
    if directory.is_symlink():
        raise ValueError('Compose plugin directory must not be a symlink')
    directory.mkdir(mode=0o755, parents=True, exist_ok=True)
    if os.name != 'nt':
        stat = directory.stat()
        if stat.st_uid != os.geteuid() or stat.st_mode & 0o022:
            raise ValueError('Compose plugin directory must be owned by the installer and not writable by others')
    target = directory / ('docker-' + kind)
    if target.is_symlink():
        raise ValueError('Existing plugin symlink preserved; repair it explicitly')
    original = target.stat() if target.exists() else None
    if original and not replace_old:
        raise ValueError('An existing system Compose plugin was preserved. Run docker compose version and repair it explicitly.')
    if original and (not target.is_file() or (os.name != 'nt' and (original.st_uid != os.geteuid() or original.st_mode & 0o022))):
        raise ValueError('Existing Buildx plugin permissions are unsafe; refusing replacement')
    with tempfile.NamedTemporaryFile(dir=directory, prefix='.starhub-compose-', delete=False) as f:
        temp = Path(f.name)
    try:
        with temp.open('wb') as f:
            f.write(data); f.flush(); os.fsync(f.fileno())
            if os.name != 'nt': os.fchmod(f.fileno(), 0o755)
    except Exception:
        temp.unlink(missing_ok=True)
        raise
    try:
        if original:
            if target.stat() != original:
                raise ValueError('Existing Buildx plugin changed during installation; retry after the other operation finishes')
            backups = directory / '.starhub-backups'
            if backups.is_symlink():
                raise ValueError('Plugin backup directory must not be a symlink')
            backups.mkdir(mode=0o700, exist_ok=True)
            if os.name != 'nt':
                stat = backups.stat()
                if stat.st_uid != os.geteuid() or stat.st_mode & 0o022:
                    raise ValueError('Plugin backup directory permissions are unsafe')
            backup = backups / (target.name + '-' + secrets.token_hex(8))
            os.link(target, backup)
            os.replace(temp, target)
        else:
            # Hard-link creation is atomic and cannot overwrite another install.
            os.link(temp, target)
    finally:
        temp.unlink(missing_ok=True)
    print('Verified ' + label + ' ' + tag + ' installed at ' + str(target), flush=True)


def enable_docker_boot():
    # Start/enable the existing engine; never restart or stop a shared daemon.
    if os.name != 'nt' and shutil.which('systemctl') and Path('/run/systemd/system').is_dir():
        run(['systemctl', 'enable', '--now', 'docker.service'])


def helper_stop(disable=False):
    if os.name == 'nt':
        with contextlib.suppress(RuntimeError):
            run(['schtasks', '/End', '/TN', 'StarhubPortalHelper'])
        if disable:
            run(['schtasks', '/Change', '/TN', 'StarhubPortalHelper', '/DISABLE'])
    elif shutil.which('systemctl') and Path('/run/systemd/system').is_dir():
        if run(['systemctl', 'show', '--property=LoadState', '--value', 'starhub-portal-helper.service']).strip() == b'not-found':
            return
        run(['systemctl', 'disable' if disable else 'stop', *(['--now'] if disable else []), 'starhub-portal-helper.service'])
    else:
        raise RuntimeError('No running systemd scheduler. Manage the local helper with your OS service manager; no background process was started.')


def service_control(root, action):
    root = validate_root(root)
    if os.name != 'nt':
        if os.geteuid() != 0:
            raise RuntimeError('Log in as root (or use sudo if installed) to manage services')
        if not shutil.which('systemctl') or not Path('/run/systemd/system').is_dir():
            raise RuntimeError('Automatic boot management requires a running systemd Linux host. Configure your OS service manager explicitly.')
    if action == 'stop':
        helper_stop(disable=True)
    else:
        enable_docker_boot()
        check_docker()
        if action == 'restart':
            helper_stop()
    compose(root, 'stop' if action == 'stop' else 'up', *([] if action == 'stop' else ['-d']))
    if action == 'restart':
        compose(root, 'restart')
    gateway_file = root / 'gateway/compose.yaml'
    if gateway_file.exists():
        run(['docker', 'compose', '-f', gateway_file, 'stop' if action == 'stop' else 'up', *([] if action == 'stop' else ['-d'])], cwd=gateway_file.parent)
        if action == 'restart':
            run(['docker', 'compose', '-f', gateway_file, 'restart'], cwd=gateway_file.parent)
    if action == 'stop':
        print('Portal services stopped; data retained. Run start to restore services and boot startup. Shared Docker and other projects were not stopped.')
    else:
        data = health(root)
        helper_install(root)
        print('Portal healthy: ' + json.dumps(data, ensure_ascii=False))
        print('Linux: Docker and the helper are enabled at boot; project containers restart unless explicitly stopped.' if os.name != 'nt' else 'Windows: containers restart with Docker Desktop; the helper runs after administrator sign-in. Enable Docker Desktop startup in its settings.')


def detect_ip():
    candidates=[]
    for family,target in ((socket.AF_INET,('1.1.1.1',80)),(socket.AF_INET6,('2606:4700:4700::1111',80))):
        try:
            with socket.socket(family,socket.SOCK_DGRAM) as sock:
                sock.connect(target);value=sock.getsockname()[0]
                if ipaddress.ip_address(value).is_global:return value
                candidates.append(value)
        except OSError:pass
    return candidates[0] if candidates else '127.0.0.1'


def address_origin(address,port):
    address=str(ipaddress.ip_address(address.strip().strip('[]')))
    if not 1024<=port<=65535:raise ValueError('Port must be 1024-65535')
    host='['+address+']' if ':' in address else address
    return 'http://'+host+':'+str(port)


def install_origins(args):
    address=getattr(args,'ip','') or detect_ip()
    direct=address_origin(address,args.port)
    preferred=origin_valid(args.origin) if args.origin else direct
    bind=getattr(args,'bind','') or ('::' if ':' in address.strip('[]') else '0.0.0.0')
    return preferred,direct,bind


def origin_valid(origin):
    u = urllib.parse.urlsplit(origin)
    if u.scheme not in ('http', 'https') or not u.hostname or u.username or u.password or u.path not in ('', '/') or u.query or u.fragment:
        raise ValueError('Origin must be http(s)://host[:port]')
    return origin.rstrip('/')


def helper_install(root):
    python = Path(sys.executable).resolve()
    script = root / 'scripts/manage.py'
    if os.name == 'nt':
        # Task executes as the installing administrator, hidden, only when signed in.
        wrapper = root / 'worker.ps1'
        write_private(wrapper, f"& '{str(python).replace(chr(39), chr(39)*2)}' '{str(script).replace(chr(39), chr(39)*2)}' worker --root '{str(root).replace(chr(39), chr(39)*2)}'\n")
        task = f'PowerShell.exe -NoProfile -NonInteractive -WindowStyle Hidden -File "{wrapper}"'
        run(['schtasks', '/Create', '/F', '/TN', 'StarhubPortalHelper', '/SC', 'ONLOGON', '/RL', 'HIGHEST', '/TR', task])
        # Start hidden; never display an additional terminal window.
        run(['schtasks', '/Run', '/TN', 'StarhubPortalHelper'])
    elif shutil.which('systemctl'):
        if any(x in str(root) for x in (' ', '\n', '"', '%')):
            raise ValueError('systemd installation root must not contain spaces or specifiers')
        write_private('/etc/systemd/system/starhub-portal-helper.service', f'''[Unit]
Description=Starhub Portal local release and gateway helper
After=docker.service network-online.target
Requires=docker.service
[Service]
Type=simple
ExecStart={python} {script} worker --root {root}
Restart=always
RestartSec=10
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths={root} /etc/systemd/system
[Install]
WantedBy=multi-user.target
''')
        run(['systemctl', 'daemon-reload'])
        run(['systemctl', 'enable', '--now', 'starhub-portal-helper.service'])
    else:
        print('No service scheduler detected. Run: python3 ' + str(script) + ' worker --root ' + str(root))


def install(args):
    if os.name != 'nt' and os.geteuid() != 0:
        raise RuntimeError('Run this installer as root / sudo')
    print('[1/4] Checking Docker Engine and Compose plugin', flush=True)
    check_docker(True)
    root = validate_root(args.root, False)
    if (root / 'installation.json').exists():
        validate_root(root)
        marker = read_json(root / 'installation.json')
        if getattr(args, 'resume', False):
            if marker.get('install_complete') is True or (root / 'credentials.json').exists():
                raise RuntimeError('This installation is already initialized. Use start or update; existing credentials are preserved.')
            if not all((root / name).is_file() for name in ('compose.env', 'portal.env', 'compose.yaml', 'current/VERSION', 'scripts/manage.py')):
                raise RuntimeError('Interrupted installation is incomplete; preserve the directory and inspect it before recovery')
            if (root / 'current/VERSION').read_text().strip() != environment(root / 'compose.env')['PORTAL_VERSION']:
                raise ValueError('Interrupted installation version mismatch; refusing unsafe resume')
            print('Resuming the interrupted initial installation. Existing keys, data, IP and port are preserved: ' + marker['origin'], flush=True)
            finish_install(root, args, marker)
            return
        print('Existing installation preserved. Use update to upgrade or start to run it. For an interrupted INITIAL install, rerun install with --resume; credentials and data are never replaced.')
        return
    if getattr(args, 'resume', False):
        raise RuntimeError('No marked interrupted installation found. Omit --resume for a new install.')
    if root.exists() and any(root.iterdir()):
        raise RuntimeError('Install directory is not empty; refusing to overwrite')
    port = args.port
    if not 1024 <= port <= 65535:
        raise ValueError('Port must be between 1024 and 65535')
    origin,ip_origin,bind_address=install_origins(args)
    with socket.socket(socket.AF_INET6 if ':' in bind_address else socket.AF_INET) as sock:
        sock.bind((bind_address,port))
    print('[2/4] Downloading and verifying the official portal release', flush=True)
    version, data = release(args.version)
    root.mkdir(parents=True, exist_ok=True)
    if os.name != 'nt':
        root.chmod(0o700)
    package = root / 'releases' / version
    extract(data, package)
    if (package / 'VERSION').read_text(encoding='utf-8').strip() != version:
        raise ValueError('Package version mismatch')
    shutil.copytree(package, root / 'current')
    shutil.copy2(package / 'compose.yaml', root / 'compose.yaml')
    (root / 'scripts').mkdir()
    shutil.copy2(package / 'scripts/manage.py', root / 'scripts/manage.py')
    if (package/'scripts/domain_bridge.py').exists():shutil.copy2(package/'scripts/domain_bridge.py',root/'scripts/domain_bridge.py')
    settings = {'PORTAL_VERSION': version, 'DB_PASSWORD': secrets.token_hex(32), 'PORTAL_PORT': str(port), 'BIND_IP': '['+bind_address+']' if ':' in bind_address else bind_address}
    env_write(root / 'compose.env', settings)
    env_write(root / 'portal.env', {'MASTER_KEY': secrets.token_hex(32), 'WORKER_TOKEN': secrets.token_hex(32), 'PUBLIC_ORIGIN': origin,'IP_ORIGIN':ip_origin})
    marker = {'marker': MARKER, 'root': str(root), 'version': version, 'origin': origin, 'ip_origin':ip_origin, 'port': port, 'install_complete': False}
    write_private(root / 'installation.json', marker)
    finish_install(root, args, marker)


def finish_install(root, args, marker):
    version = environment(root / 'compose.env')['PORTAL_VERSION']
    print('[3/4] Building and starting the project containers', flush=True)
    compose(root, 'up', '-d', '--build', timeout=1200)
    health(root, version)
    print('[4/4] Registering the administrator and background helper', flush=True)
    admins = int(compose(root, 'exec', '-T', 'db', 'psql', '-U', 'portal', '-d', 'portal', '-At', '-c', "SELECT count(*) FROM customers WHERE role='admin'").strip())
    if admins == 0:
        password = getpass.getpass('Initial administrator password (12-256 bytes, empty = random): ') if args.ask_password else secrets.token_urlsafe(24)
        if not password:
            password = secrets.token_urlsafe(24)
        if not 12 <= len(password.encode()) <= 256:
            raise ValueError('Administrator password must have 12-256 bytes. Retry install --resume to continue.')
        compose(root, 'exec', '-T', 'app', '/app/portal', 'admin-create', args.admin_email, input=(password+'\n').encode())
        write_private(root / 'credentials.json', {'email': args.admin_email, 'password': password})
    else:
        print('An administrator already exists and was preserved. If the initial credential file is unavailable, use the explicit admin-reset command to recover access.', flush=True)
    if os.name == 'nt':
        # Remove inherited read access to the installation and all stored secrets.
        run(['icacls', str(root), '/inheritance:r', '/grant:r', '*S-1-5-32-544:(OI)(CI)F', '*S-1-5-18:(OI)(CI)F', '/T'])
    helper_install(root)
    shutil.copy2(root / 'current/scripts/link-control.py', root / 'scripts/link-control.py')
    write_private(root / 'installation.json', {**marker, 'install_complete': True})
    print('Starhub Portal installed: ' + marker['origin'])
    if (root / 'credentials.json').exists():
        print('Credentials are stored only in ' + str(root / 'credentials.json') + ' (administrator access required).')
    print('Check address: python3 ' + str(root / 'scripts/manage.py') + ' status')
    print('If a provider/host firewall is active, allow only the configured TCP entry port: ' + str(marker['port']))


def local(root, action, body):
    token = environment(root / 'portal.env')['WORKER_TOKEN']
    req = urllib.request.Request(local_origin(root)+'/portal/v1/local/'+action, data=json.dumps(body).encode(),
                                 headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(req, timeout=20) as res:
        return json.load(res)


def rollback(root, state):
    old = Path(state['old']).resolve()
    backups = (root / 'backups').resolve()
    backup = Path(state['backup']).resolve()
    if not old.is_relative_to(backups) or not backup.is_relative_to(backups):
        raise ValueError('Unsafe recovery journal')
    compose(root, 'stop', 'app')
    if old.exists():
        if (root / 'current').exists():
            (root / 'current').rename(backups / ('failed-' + secrets.token_hex(8)))
        old.rename(root / 'current')
    env_write(root / 'compose.env', state['env'])
    shutil.copy2(root / 'current' / 'compose.yaml', root / 'compose.yaml')
    shutil.copy2(root / 'current/compose.yaml', root / 'compose.yaml')
    with open(backup, 'rb') as data:
        compose(root, 'exec', '-T', 'db', 'pg_restore', '-U', 'portal', '-d', 'portal', '--clean', '--if-exists', '--no-owner', '--no-acl', input=data.read())
    compose(root, 'up', '-d', '--build', timeout=1200)
    health(root, state['env']['PORTAL_VERSION'])


def update(root, version):
    version, data = release(version)
    current = environment(root / 'compose.env')
    if version == current['PORTAL_VERSION']:
        return 'Already at requested version'
    def semver(v): return tuple(map(int, v.split('.')))
    if semver(version) <= semver(current['PORTAL_VERSION']):
        raise ValueError('Downgrades require an explicit offline restore; not supported by automatic update')
    package = root / 'releases' / version
    if package.exists():
        package = root / 'releases' / (version + '-' + secrets.token_hex(4))
    extract(data, package)
    if (package / 'VERSION').read_text().strip() != version:
        raise ValueError('Package version mismatch')
    backups = root / 'backups'
    backups.mkdir(exist_ok=True)
    stamp = time.strftime('%Y%m%d-%H%M%S') + '-' + secrets.token_hex(3)
    backup = backups / (stamp + '.dump')
    old = backups / (stamp + '-previous')
    compose(root, 'stop', 'app')
    try:
        with open(backup, 'wb') as out:
            compose(root, 'exec', '-T', 'db', 'pg_dump', '-U', 'portal', '-d', 'portal', '-Fc', output=out)
        state = {'old': str(old), 'backup': str(backup), 'env': current}
        for name in ('compose.env', 'portal.env', 'installation.json'):
            shutil.copy2(root / name, backups / (stamp + '-' + name))
        write_private(root / 'recovery.json', state)
    except Exception:
        compose(root, 'start', 'app')
        raise
    try:
        (root / 'current').rename(old)
        shutil.copytree(package, root / 'current')
        shutil.copy2(package / 'compose.yaml', root / 'compose.yaml')
        env_write(root / 'compose.env', {**current, 'PORTAL_VERSION': version})
        compose(root, 'up', '-d', '--build', timeout=1200)
        health(root, version)
        marker = read_json(root / 'installation.json')
        marker['version'] = version
        write_private(root / 'installation.json', marker)
        shutil.copy2(package / 'scripts/manage.py', root / 'scripts/manage.py')
        shutil.copy2(package / 'scripts/link-control.py', root / 'scripts/link-control.py')
        if (package/'scripts/domain_bridge.py').exists():shutil.copy2(package/'scripts/domain_bridge.py',root/'scripts/domain_bridge.py')
    except Exception:
        rollback(root, state)
        (root / 'recovery.json').unlink(missing_ok=True)
        return 'rolled_back'
    (root / 'recovery.json').unlink()
    return 'Updated and verified v' + version


def gateway(root, payload):
    domain = payload.get('domain', '')
    if not domain:
        owned = root / 'gateway/compose.yaml'
        if owned.exists(): run(['docker', 'compose', '-f', owned, 'down'], cwd=owned.parent)
        if (root/'scripts/domain_bridge.py').exists():
            import domain_bridge
            domain_bridge.disable(root)
        marker=read_json(root/'installation.json');marker['public_origin']=marker.get('ip_origin',marker['origin']);write_private(root/'installation.json',marker)
        return 'No domain requested; IP entry remains available'
    if not re.fullmatch(r'[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?', domain) or '.' not in domain or '..' in domain:
        raise ValueError('Invalid gateway domain')
    port = int(environment(root / 'compose.env')['PORTAL_PORT'])
    directory = root / 'gateway'
    directory.mkdir(exist_ok=True)
    scheme = 'https' if payload.get('https') else 'http'
    snippet = f'''# Add to the existing Nginx/Baota site for {domain}; use its certificate settings.
location / {{
    proxy_pass http://127.0.0.1:{port};
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 60s;
}}
'''
    write_private(directory / 'nginx-location.conf', snippet)
    config = f'''{scheme}://{domain} {{
    reverse_proxy app:18088 {{
        header_up X-Real-IP {{remote_host}}
    }}
}}
'''
    linked=False
    if payload.get('https') and (root/'scripts/domain_bridge.py').exists():
        import domain_bridge
        linked=domain_bridge.configure(root,domain,port,loopback='::1' if '[' in local_origin(root) else '127.0.0.1')
    free = True
    for number in (80, 443):
        try:
            with socket.socket() as s:
                s.bind(('0.0.0.0', number))
        except OSError:
            free = False
    network = json.loads(run(['docker', 'network', 'inspect', 'starhub-portal_outbound']))[0]
    env = environment(root / 'portal.env')
    env['TRUSTED_PROXIES'] = ','.join(x['Gateway'] + '/32' for x in network['IPAM']['Config'] if ':' not in x.get('Gateway', ''))
    owned = directory / 'compose.yaml'
    if free and not linked:
        write_private(directory / 'Caddyfile', config)
        write_private(owned, '''name: starhub-portal-gateway
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes: ["./Caddyfile:/etc/caddy/Caddyfile:ro", "./data:/data", "./config:/config"]
    networks: [outbound]
    mem_limit: 128m
    logging: {driver: json-file, options: {max-size: 5m, max-file: "2"}}
networks:
  outbound: {external: true, name: starhub-portal_outbound}
''')
        env['TRUSTED_PROXIES'] += ',' + ','.join(x['Subnet'] for x in network['IPAM']['Config'] if ':' not in x.get('Subnet', ''))
        run(['docker', 'compose', '-f', owned, 'up', '-d'], cwd=directory)
    elif owned.exists() and not linked:
        env['TRUSTED_PROXIES'] += ',' + ','.join(x['Subnet'] for x in network['IPAM']['Config'] if ':' not in x.get('Subnet', ''))
        write_private(directory / 'Caddyfile', config)
        run(['docker', 'compose', '-f', owned, 'exec', '-T', 'caddy', 'caddy', 'reload', '--config', '/etc/caddy/Caddyfile'], cwd=directory)
    env_write(root / 'portal.env', env)
    compose(root, 'up', '-d', 'app')
    expected = health(root)['installation_id']
    deadline=time.monotonic()+75
    while time.monotonic()<deadline:
        try:
            req=urllib.request.Request(scheme+'://'+domain+'/portal/v1/health',headers={'User-Agent':'Starhub-Portal-Gateway-Check'})
            with urllib.request.urlopen(req,timeout=5) as response:result=json.loads(response.read(16<<10))
            if result.get('installation_id')==expected:
                marker=read_json(root/'installation.json');marker['public_origin']=scheme+'://'+domain;write_private(root/'installation.json',marker)
                return 'Domain verified with '+('existing Starhub certificate / Nginx' if linked else 'gateway')+'; IP entry remains available'
        except (OSError,ValueError):pass
        if not free and not owned.exists() and not linked:
            raise RuntimeError('Existing Nginx/Baota detected: apply gateway/nginx-location.conf in that site and configure its certificate, then use Retry domain configuration.')
        time.sleep(3)
    raise RuntimeError('Domain gateway queued but end-to-end verification failed. Check DNS, upstream port, existing site mapping and HTTPS origin mode; retry the domain job.')


def control_compatibility(root,api=None):
    # Same-machine root installations already use this private loopback client
    # for certificate reuse. Only request a verified official panel release;
    # the existing control-plane updater performs its own integrity checks.
    if api is None:
        if not (root/'scripts/domain_bridge.py').exists():return 'external-control'
        import domain_bridge
        api=domain_bridge.control_client()
    if api is None:return 'external-control'
    current=api('session').get('version','')
    try:version=tuple(int(x) for x in current.lstrip('v').split('.'))
    except ValueError:raise ValueError('Unrecognized installed control version') from None
    if version>=MINIMUM_CONTROL:return 'ready: '+current
    release=api('panel-release')
    latest=release.get('latest','')
    try:available=tuple(int(x) for x in latest.lstrip('v').split('.'))
    except ValueError:return 'waiting for official compatible release'
    if available<MINIMUM_CONTROL:return 'waiting for Starhub 0.25.4+'
    if not release.get('automatic_update'):return 'control updater offline; update Starhub first'
    try:api('panel-updates',{'current':current,'version':latest},'POST')
    except RuntimeError as e:
        if 'HTTP 409' not in str(e):raise
    return 'compatible control update queued: '+latest


def resource_status(root):
    """Bounded, once-per-minute observations; no containers outside this project."""
    status={}
    try:
        rows=run(['docker','stats','--no-stream','--format','{{json .}}','starhub-portal-app-1','starhub-portal-db-1'],timeout=8).decode().splitlines()
        for line in rows[:2]:
            row=json.loads(line);name=row.get('Name','')
            if name in ('starhub-portal-app-1','starhub-portal-db-1'):
                status['app_resources' if '-app-' in name else 'db_resources']='RAM '+str(row.get('MemUsage',''))[:60]+' · CPU '+str(row.get('CPUPerc',''))[:20]
    except Exception:pass
    try:
        total,used,free=shutil.disk_usage(root)
        status['disk_available']=f'{free/(1024**3):.2f} / {total/(1024**3):.2f} GiB'
    except OSError:pass
    return status


def worker(root):
    # Single process lock avoids two helpers claiming/updating the same installation.
    lock = open(root / 'helper.lock', 'a+')
    if os.name == 'nt':
        import msvcrt
        lock.seek(0); lock.write('0'); lock.flush(); lock.seek(0)
        msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
    else:
        import fcntl
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    if (root / 'recovery.json').exists():
        rollback(root, read_json(root / 'recovery.json'))
        (root / 'recovery.json').unlink()
    last_control_check=0
    control_state='not checked'
    resources={}
    while True:
        try:
            if (root / 'pending-report.json').exists():
                local(root, 'report', read_json(root / 'pending-report.json'))
                (root / 'pending-report.json').unlink()
            if time.monotonic()-last_control_check>60:
                try:control_state=control_compatibility(root)
                except Exception:control_state='control compatibility check pending; inspect local gateway'
                last_control_check=time.monotonic()
                resources=resource_status(root)
            local(root, 'heartbeat', {'status': {'platform': platform.system(), 'mode': 'official-releases','control_compatibility':control_state,**resources}})
            job = local(root, 'claim', {}).get('job')
            if job:
                report = {'id': job['id'], 'state': 'succeeded', 'detail': ''}
                try:
                    result = update(root, job['payload']['version']) if job['kind'] == 'update' else gateway(root, job['payload'])
                    report['state'] = 'rolled_back' if result == 'rolled_back' else 'succeeded'
                    report['detail'] = result[:450]
                except Exception as e:
                    report['state'] = 'failed'
                    report['detail'] = str(e)[:450]
                write_private(root / 'pending-report.json', report)
                if job['kind'] == 'update':
                    local(root, 'report', report)
                    (root / 'pending-report.json').unlink()
                    os.execv(sys.executable, [sys.executable, str(root / 'scripts/manage.py'), 'worker', '--root', str(root)])
        except Exception:
            # Do not log bearer tokens, decrypted job payloads, payment or mail settings.
            print('Local helper waiting for portal / pending recovery', flush=True)
        time.sleep(15)


def uninstall(root, purge=False, confirm=''):
    # Only this exact named Compose project and its own helper are touched.
    if purge and confirm != 'DELETE-PORTAL-DATA':
        raise ValueError('Data purge requires --confirm DELETE-PORTAL-DATA')
    if os.name == 'nt':
        for args in (['schtasks', '/End', '/TN', 'StarhubPortalHelper'], ['schtasks', '/Delete', '/F', '/TN', 'StarhubPortalHelper']):
            with contextlib.suppress(Exception): run(args)
    elif shutil.which('systemctl'):
        with contextlib.suppress(Exception): run(['systemctl', 'disable', '--now', 'starhub-portal-helper.service'])
        Path('/etc/systemd/system/starhub-portal-helper.service').unlink(missing_ok=True)
        run(['systemctl', 'daemon-reload'])
    if (root / 'gateway/compose.yaml').exists():
        run(['docker', 'compose', '-f', root / 'gateway/compose.yaml', 'down'], cwd=root / 'gateway')
    if (root/'scripts/domain_bridge.py').exists():
        import domain_bridge
        domain_bridge.disable(root)
    compose(root, 'down', '--remove-orphans')
    if purge:
        verified = validate_root(root)
        if verified != root.resolve() or verified.is_symlink():
            raise ValueError('Purge target changed')
        shutil.rmtree(verified)
        print('Portal data removed. Docker, Baota, Starhub control plane and other services were preserved.')
    else:
        print('Portal stopped and uninstalled. Data and secrets retained at ' + str(root))
        print('Restore services and boot startup: python3 ' + str(root / 'scripts/manage.py') + ' start --root ' + str(root))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['install', 'update', 'worker', 'status', 'credentials', 'admin-reset', 'uninstall', 'repair-helper', 'domain-enable', 'domain-disable', 'start', 'stop', 'restart', 'logs'])
    parser.add_argument('--root', type=Path, default=DEFAULT_ROOT)
    parser.add_argument('--port', type=int, default=18088)
    parser.add_argument('--bind', default='', choices=['','0.0.0.0','127.0.0.1','::','::1'])
    parser.add_argument('--ip',default='',help='Direct IPv4/IPv6 entry; auto-detected when omitted. Independent of --origin domain.')
    parser.add_argument('--origin', default='')
    parser.add_argument('--domain', default='')
    parser.add_argument('--http', action='store_true', help='Use HTTP domain instead of HTTPS')
    parser.add_argument('--allow-http', action='store_true', help='Explicitly permit HTTP access on your trusted network')
    parser.add_argument('--admin-email', default='admin@starhub.local')
    parser.add_argument('--ask-password', action='store_true')
    parser.add_argument('--resume', action='store_true', help='Continue an interrupted initial install without regenerating keys or changing existing administrators')
    parser.add_argument('--version', default='latest')
    parser.add_argument('--purge-data', action='store_true')
    parser.add_argument('--confirm', default='')
    if any(arg.startswith('--ask-') and arg != '--ask-password' for arg in sys.argv[1:]):
        parser.error('Use --ask-password exactly; enter the password only at the subsequent private prompt, not in the option name.')
    args = parser.parse_args()
    if args.action == 'install':
        install(args)
        return
    root = validate_root(args.root)
    if args.action in ('start', 'stop', 'restart'):
        service_control(root, args.action)
        return
    if args.action == 'logs':
        print(compose(root, 'logs', '--tail', '100', '--no-color', 'app', 'db').decode('utf-8', errors='replace'))
        return
    if args.action in ('domain-enable','domain-disable'):
        marker=read_json(root/'installation.json')
        if args.action=='domain-enable' and not args.domain:raise ValueError('Use --domain your.example.com')
        if args.action=='domain-disable':
            bind=environment(root/'compose.env').get('BIND_IP','0.0.0.0').strip('[]')
            if bind in ('127.0.0.1','::1'):raise ValueError('This installation is loopback-only. Keep the SSH tunnel; configure an explicit public bind before switching the public entry.')
        result=local(root,'domain',{'domain':args.domain if args.action=='domain-enable' else '', 'https':not args.http,'ip_origin':marker.get('ip_origin',marker['origin']),'allow_http':args.allow_http})
        print(json.dumps(result,ensure_ascii=False));return
    if args.action == 'worker': worker(root)
    elif args.action == 'update': print(update(root, args.version))
    elif args.action == 'uninstall': uninstall(root, args.purge_data, args.confirm)
    elif args.action == 'status':
        marker = read_json(root / 'installation.json')
        print('IP entry: ' + marker.get('ip_origin',marker['origin']))
        print('Preferred entry: '+marker.get('public_origin',marker['origin']))
        print('Health/version: ' + json.dumps(health(root), ensure_ascii=False))
    elif args.action == 'credentials':
        print(json.dumps(read_json(root / 'credentials.json'), ensure_ascii=False, indent=2))
    elif args.action == 'admin-reset':
        password = getpass.getpass('New panel administrator password: ')
        compose(root, 'exec', '-T', 'app', '/app/portal', 'admin-reset', args.admin_email, input=(password+'\n').encode())
        write_private(root / 'credentials.json', {'email': args.admin_email, 'password': password})
        print('Panel administrator password reset and sessions revoked.')
    elif args.action == 'repair-helper': helper_install(root)


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        print('Starhub Portal: ' + str(exc), file=sys.stderr)
        sys.exit(1)
