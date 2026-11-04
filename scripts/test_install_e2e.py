"""Real Docker install/uninstall test; only in an isolated Linux CI runner."""
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
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
with tempfile.TemporaryDirectory(prefix='starhub-portal-ci-') as tmp:
    root=Path(tmp)/'installation'
    args=SimpleNamespace(root=root,port=18088,origin='http://127.0.0.1:18088',bind='127.0.0.1',admin_email='ci@example.test',ask_password=False,version=version)
    try:
        with patch.object(m,'release',return_value=(version,bundle)),patch.object(m,'helper_install'):
            m.install(args)
        credentials=m.read_json(root/'credentials.json')
        opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        def call(path,body=None,csrf=''):
            req=urllib.request.Request(args.origin+'/portal/v1/'+path,data=None if body is None else json.dumps(body).encode(),headers={'Content-Type':'application/json','Origin':args.origin,'X-CSRF-Token':csrf})
            with opener.open(req,timeout=10) as res:return json.load(res)
        login=call('auth/login',credentials)
        assert login['user']['role']=='admin'
        assert call('me')['csrf']==login['csrf']
        settings=call('admin/settings')
        assert not settings['secrets']['control_configured']
        assert call('admin/plans')==[]
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
        assert call('admin/announcements')[0]['body']=='Preserve business data across rollback'
        with patch.object(m,'run',wraps=m.run) as run,patch.object(m.shutil,'which',return_value=None):
            m.uninstall(root)
        assert (root/'data/postgres').exists()
        assert (root/'portal.env').exists()
        m.compose(root,'up','-d')
        m.health(root,version)
        with patch.object(m,'helper_install'):
            m.install(args)  # existing installation is never overwritten
        assert m.read_json(root/'credentials.json')==credentials
        print('Fresh install, login, stable CSRF, local helper, preserved-data uninstall and restore passed.')
    finally:
        if (root/'compose.yaml').exists():m.compose(root,'down','--remove-orphans')
