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


def run(args, cwd=None, input=None, timeout=600, output=None):
    result = subprocess.run([str(x) for x in args], cwd=cwd, input=input, stdout=output or subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, check=False)
    if result.returncode:
        # Command output may contain credentials. Keep failure messages bounded and generic.
        raise RuntimeError(f'{Path(str(args[0])).name} failed (exit {result.returncode}); inspect local service logs')
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
    return run(['docker', 'compose', '--project-name', 'starhub-portal', '--env-file', str(root / 'compose.env'),
                '-f', str(root / 'compose.yaml'), *args], cwd=root, input=input, output=output, timeout=timeout)


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


def health(root, expected=None, timeout=90):
    env = environment(root / 'compose.env')
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            with urllib.request.urlopen(f'http://127.0.0.1:{int(env["PORTAL_PORT"])}/portal/v1/health', timeout=3) as res:
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
    if run(['docker', 'info', '--format', '{{.OSType}}'], timeout=20).strip() != b'linux':
        raise RuntimeError('Docker must be running in Linux containers mode')
    run(['docker', 'compose', 'version'], timeout=20)


def detect_ip():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        try:
            s.connect(('1.1.1.1', 80))
            return s.getsockname()[0]
        except OSError:
            return '127.0.0.1'


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
    check_docker(True)
    root = validate_root(args.root, False)
    if (root / 'installation.json').exists():
        validate_root(root)
        print('Existing installation preserved. Use update to upgrade; credentials and data are never replaced.')
        return
    if root.exists() and any(root.iterdir()):
        raise RuntimeError('Install directory is not empty; refusing to overwrite')
    port = args.port
    if not 1024 <= port <= 65535:
        raise ValueError('Port must be between 1024 and 65535')
    with socket.socket() as sock:
        sock.bind(('0.0.0.0', port))
    origin = origin_valid(args.origin or f'http://{detect_ip()}:{port}')
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
    settings = {'PORTAL_VERSION': version, 'DB_PASSWORD': secrets.token_hex(32), 'PORTAL_PORT': str(port), 'BIND_IP': args.bind}
    env_write(root / 'compose.env', settings)
    env_write(root / 'portal.env', {'MASTER_KEY': secrets.token_hex(32), 'WORKER_TOKEN': secrets.token_hex(32), 'PUBLIC_ORIGIN': origin})
    marker = {'marker': MARKER, 'root': str(root), 'version': version, 'origin': origin, 'port': port}
    write_private(root / 'installation.json', marker)
    compose(root, 'up', '-d', '--build', timeout=1200)
    health(root, version)
    password = getpass.getpass('Initial administrator password (12+ chars, empty = random): ') if args.ask_password else secrets.token_urlsafe(24)
    if not password:
        password = secrets.token_urlsafe(24)
    if len(password.encode()) < 12:
        raise ValueError('Administrator password must have at least 12 bytes. Use admin-reset after correcting it.')
    compose(root, 'exec', '-T', 'app', '/app/portal', 'admin-create', args.admin_email, input=(password+'\n').encode())
    write_private(root / 'credentials.json', {'email': args.admin_email, 'password': password})
    if os.name == 'nt':
        # Remove inherited read access to the installation and all stored secrets.
        run(['icacls', str(root), '/inheritance:r', '/grant:r', '*S-1-5-32-544:(OI)(CI)F', '*S-1-5-18:(OI)(CI)F', '/T'])
    helper_install(root)
    print('Starhub Portal installed: ' + origin)
    print('Credentials are stored only in ' + str(root / 'credentials.json') + ' (administrator access required).')
    print('Check address: python3 ' + str(root / 'scripts/manage.py') + ' status')
    print('If a provider/host firewall is active, allow only the configured TCP entry port: ' + str(port))


def local(root, action, body):
    token = environment(root / 'portal.env')['WORKER_TOKEN']
    port = int(environment(root / 'compose.env')['PORTAL_PORT'])
    req = urllib.request.Request(f'http://127.0.0.1:{port}/portal/v1/local/{action}', data=json.dumps(body).encode(),
                                 headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
    with urllib.request.urlopen(req, timeout=20) as res:
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
    except Exception:
        compose(root, 'start', 'app')
        raise
    state = {'old': str(old), 'backup': str(backup), 'env': current}
    write_private(root / 'recovery.json', state)
    for name in ('compose.env', 'portal.env', 'installation.json'):
        shutil.copy2(root / name, backups / (stamp + '-' + name))
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
    if free:
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
    elif owned.exists():
        write_private(directory / 'Caddyfile', config)
        run(['docker', 'compose', '-f', owned, 'exec', '-T', 'caddy', 'caddy', 'reload', '--config', '/etc/caddy/Caddyfile'], cwd=directory)
    env_write(root / 'portal.env', env)
    compose(root, 'up', '-d', 'app')
    expected = health(root)['installation_id']
    try:
        result = json.loads(download(scheme + '://' + domain + '/portal/v1/health', 16 << 10)) if scheme == 'https' else None
        if result and result.get('installation_id') == expected:
            return 'Domain verified; IP entry remains available'
    except (OSError, ValueError):
        pass
    if not free and not owned.exists():
        raise RuntimeError('Existing proxy detected. Apply gateway/nginx-location.conf in Baota, configure certificate, then retry settings.')
    raise RuntimeError('Gateway configured; DNS/certificate reachability not yet verified. Check local gateway logs.')


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
    while True:
        try:
            if (root / 'pending-report.json').exists():
                local(root, 'report', read_json(root / 'pending-report.json'))
                (root / 'pending-report.json').unlink()
            local(root, 'heartbeat', {'status': {'platform': platform.system(), 'mode': 'official-releases'}})
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
    compose(root, 'down', '--remove-orphans')
    if purge:
        verified = validate_root(root)
        if verified != root.resolve() or verified.is_symlink():
            raise ValueError('Purge target changed')
        shutil.rmtree(verified)
        print('Portal data removed. Docker, Baota, Starhub control plane and other services were preserved.')
    else:
        print('Portal stopped and uninstalled. Data and secrets retained at ' + str(root))
        print('Restore: docker compose --env-file ' + str(root / 'compose.env') + ' -f ' + str(root / 'compose.yaml') + ' up -d; then reinstall the helper with repair-helper.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['install', 'update', 'worker', 'status', 'credentials', 'admin-reset', 'uninstall', 'repair-helper'])
    parser.add_argument('--root', type=Path, default=DEFAULT_ROOT)
    parser.add_argument('--port', type=int, default=18088)
    parser.add_argument('--bind', default='0.0.0.0', choices=['0.0.0.0', '127.0.0.1'])
    parser.add_argument('--origin', default='')
    parser.add_argument('--admin-email', default='admin@starhub.local')
    parser.add_argument('--ask-password', action='store_true')
    parser.add_argument('--version', default='latest')
    parser.add_argument('--purge-data', action='store_true')
    parser.add_argument('--confirm', default='')
    args = parser.parse_args()
    if args.action == 'install':
        install(args)
        return
    root = validate_root(args.root)
    if args.action == 'worker': worker(root)
    elif args.action == 'update': print(update(root, args.version))
    elif args.action == 'uninstall': uninstall(root, args.purge_data, args.confirm)
    elif args.action == 'status':
        marker = read_json(root / 'installation.json')
        print('IP entry: ' + marker['origin'])
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
