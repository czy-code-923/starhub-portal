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
MINIMUM_CONTROL = (0,25,3)
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
    if run(['docker', 'info', '--format', '{{.OSType}}'], timeout=20).strip() != b'linux':
        raise RuntimeError('Docker must be running in Linux containers mode')
    run(['docker', 'compose', 'version'], timeout=20)


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
    origin,ip_origin,bind_address=install_origins(args)
    with socket.socket(socket.AF_INET6 if ':' in bind_address else socket.AF_INET) as sock:
        sock.bind((bind_address,port))
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
    marker = {'marker': MARKER, 'root': str(root), 'version': version, 'origin': origin, 'ip_origin':ip_origin, 'port': port}
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
    shutil.copy2(package / 'scripts/link-control.py', root / 'scripts/link-control.py')
    print('Starhub Portal installed: ' + origin)
    print('Credentials are stored only in ' + str(root / 'credentials.json') + ' (administrator access required).')
    print('Check address: python3 ' + str(root / 'scripts/manage.py') + ' status')
    print('If a provider/host firewall is active, allow only the configured TCP entry port: ' + str(port))


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
    if available<MINIMUM_CONTROL:return 'waiting for Starhub 0.25.3+'
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
        print('Restore: docker compose --env-file ' + str(root / 'compose.env') + ' -f ' + str(root / 'compose.yaml') + ' up -d; then reinstall the helper with repair-helper.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['install', 'update', 'worker', 'status', 'credentials', 'admin-reset', 'uninstall', 'repair-helper'])
    parser.add_argument('--root', type=Path, default=DEFAULT_ROOT)
    parser.add_argument('--port', type=int, default=18088)
    parser.add_argument('--bind', default='', choices=['','0.0.0.0','127.0.0.1','::','::1'])
    parser.add_argument('--ip',default='',help='Direct IPv4/IPv6 entry; auto-detected when omitted. Independent of --origin domain.')
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
