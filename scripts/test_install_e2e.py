"""Real Docker install/uninstall test; only in an isolated Linux CI runner."""
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import sys
import re
from types import SimpleNamespace
from unittest.mock import patch
import urllib.request
import http.cookiejar
import zipfile

if os.environ.get('GITHUB_ACTIONS') != 'true' or os.name == 'nt':
    raise SystemExit('Run only on the disposable Linux GitHub Actions runner')
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('manage',ROOT/'scripts/manage.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
version=(ROOT/'VERSION').read_text().strip()
bundle=(ROOT/'dist'/('starhub-portal-linux-'+m.architecture()+'.zip')).read_bytes()
# Match the real helper's UMask=0077, including archive extraction permissions.
os.umask(0o077)
# Load a real verified upstream plugin through an isolated Docker config. The
# runner's system plugin and existing Docker engine are never replaced.
with tempfile.TemporaryDirectory(prefix='starhub-compose-ci-') as tmp:
    config=Path(tmp)
    m.install_compose_plugin(config/'cli-plugins')
    m.install_buildx_plugin(config/'cli-plugins')
    actual=m.run(['docker','--config',str(config),'compose','version'],timeout=30)
    assert b'Docker Compose version' in actual,actual
    actual=m.run(['docker','--config',str(config),'buildx','version'],timeout=30)
    assert m.buildx_version(actual)>=(0,17,0),actual
    verified_plugins={p.name:p.read_bytes() for p in (config/'cli-plugins').iterdir() if p.is_file()}
    print('Verified official Compose plugin loads with isolated DOCKER_CONFIG.')
with tempfile.TemporaryDirectory(prefix='starhub-portal-ci-',dir='/opt') as tmp:
    root=Path(tmp)/'installation'
    args=SimpleNamespace(root=root,port=18088,origin='http://127.0.0.1:18088',bind='127.0.0.1',admin_email='ci@example.test',ask_password=False,version=version)
    try:
        original_compose=m.compose
        def fail_first_build(directory,*command,**kwargs):
            if command==('up','-d','--build'):
                raise RuntimeError('Synthetic build failure before initial administrator creation')
            return original_compose(directory,*command,**kwargs)
        with patch.object(m,'release',return_value=(version,bundle)),patch.object(m,'helper_install'),patch.object(m,'compose',side_effect=fail_first_build):
            try:m.install(args)
            except RuntimeError as e:assert 'Synthetic build failure' in str(e)
            else:raise AssertionError('Initial failure did not occur')
        initial_keys=(root/'portal.env').read_bytes(),(root/'compose.env').read_bytes()
        # All real project builds below use the exact newly downloaded plugins,
        # including through the hardened helper's private Docker client state.
        plugins=root/'docker-config/cli-plugins';plugins.mkdir(parents=True)
        for name,data in verified_plugins.items():
            (plugins/name).write_bytes(data);(plugins/name).chmod(0o755)
        assert not (root/'credentials.json').exists()
        args.resume=True
        with patch.object(m,'helper_install'):
            m.install(args)
        assert initial_keys==((root/'portal.env').read_bytes(),(root/'compose.env').read_bytes())
        assert m.read_json(root/'installation.json')['install_complete'] is True
        args.resume=False
        credentials=m.read_json(root/'credentials.json')
        opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        def call(path,body=None,csrf=''):
            req=urllib.request.Request(args.origin+'/portal/v1/'+path,data=None if body is None else json.dumps(body).encode(),headers={'Content-Type':'application/json','Origin':args.origin,'X-CSRF-Token':csrf})
            with opener.open(req,timeout=10) as res:return json.load(res)
        login=call('auth/login',credentials)
        def check_frontend():
            with opener.open(args.origin+'/',timeout=10) as res:
                html=res.read().decode()
                assert res.status==200 and 'id="app"' in html
            assets=re.findall(r'(?:src|href)="(/assets/[^\"]+\.(?:js|css))"',html)
            assert any(x.endswith('.js') for x in assets) and any(x.endswith('.css') for x in assets)
            for asset in assets:
                with opener.open(args.origin+asset,timeout=10) as res:
                    assert res.status==200 and len(res.read())>100
        check_frontend()
        assert login['user']['role']=='admin' and login['user']['is_owner'] is True
        # Legacy crash after admin-create but before saving credentials must not
        # reset that owner or create another one when explicitly resumed.
        saved=root/'saved-ci-credentials.json'
        (root/'credentials.json').rename(saved)
        marker=m.read_json(root/'installation.json');marker.pop('install_complete')
        m.write_private(root/'installation.json',marker)
        args.resume=True
        with patch.object(m,'helper_install'):
            m.install(args)
        args.resume=False
        assert not (root/'credentials.json').exists()
        saved.rename(root/'credentials.json')
        assert call('me')['csrf']==login['csrf']
        assert call('me')['csrf']==login['csrf']
        settings=call('admin/settings')
        assert not settings['secrets']['control_configured']
        assert call('admin/plans')==[]
        # Detached lifecycle operations must preserve the same database/login.
        with patch.object(m,'helper_stop'),patch.object(m,'helper_install'):
            m.service_control(root,'stop')
            m.service_control(root,'start')
            assert call('me')['csrf']==login['csrf']
            m.service_control(root,'restart')
            assert call('me')['csrf']==login['csrf']
        # Reproduce the real helper's systemd restrictions; CLI-only updates do
        # not catch Buildx trying to write to the protected /root/.docker path.
        code="import sys;sys.path.insert(0,sys.argv[1]+'/scripts');import manage;from pathlib import Path;manage.compose(Path(sys.argv[1]),'build','app')"
        m.run(['systemd-run','--quiet','--collect','--wait','--pipe',
               '-p','ProtectHome=true','-p','ProtectSystem=full','-p','PrivateTmp=true',
               '-p','NoNewPrivileges=true','-p','ReadWritePaths='+str(root),
               sys.executable,'-c',code,str(root)])
        m.local(root,'heartbeat',{'status':{'mode':'ci-e2e'}})
        assert m.local(root,'claim',{})['job'] is None
        call('admin/announcements',{'title':'rollback fixture','body':'Preserve business data across rollback','enabled':True},login['csrf'])
        old_health=m.health
        future='99.0.0'
        out=io.BytesIO()
        with zipfile.ZipFile(io.BytesIO(bundle)) as source,zipfile.ZipFile(out,'w',zipfile.ZIP_DEFLATED) as target:
            for item in source.infolist():target.writestr(item,future if item.filename=='VERSION' else source.read(item))
        def fail_new_health(directory,expected=None,timeout=90):
            if expected==future:raise RuntimeError('Synthetic health failure after real replacement')
            return old_health(directory,expected,timeout)
        with patch.object(m,'release',return_value=(future,out.getvalue())),patch.object(m,'health',side_effect=fail_new_health):
            assert m.update(root,future)=='rolled_back'
        assert old_health(root,version)['version']==version
        check_frontend()
        assert call('admin/announcements')[0]['body']=='Preserve business data across rollback'
        # Real IPv6-only host publication must keep local health and helper
        # requests on ::1 rather than assuming an IPv4 published port.
        original_env=m.environment(root/'compose.env')
        m.env_write(root/'compose.env',{**original_env,'BIND_IP':'[::1]'})
        m.compose(root,'up','-d','app')
        assert m.health(root,version)['version']==version
        m.local(root,'heartbeat',{'status':{'mode':'ci-ipv6-e2e'}})
        assert m.local(root,'claim',{})['job'] is None
        m.env_write(root/'compose.env',original_env)
        m.compose(root,'up','-d','app');m.health(root,version)
        with patch.object(m,'run',wraps=m.run) as run,patch.object(m.shutil,'which',return_value=None):
            m.uninstall(root)
        assert (root/'data/postgres').exists()
        assert (root/'portal.env').exists()
        m.compose(root,'up','-d')
        m.health(root,version)
        with patch.object(m,'helper_install'):
            m.install(args)  # existing installation is never overwritten
        assert m.read_json(root/'credentials.json')==credentials
        print('Fresh install, interrupted-install resume, existing-admin preservation, login, stable CSRF, local helper, preserved-data uninstall and restore passed.')
    finally:
        if (root/'compose.yaml').exists():m.compose(root,'down','--remove-orphans')
