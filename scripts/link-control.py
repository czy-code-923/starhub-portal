"""Local root-only connection to a co-located Starhub control plane.

Reads the existing control token only on this VPS, creates a separate narrow API
key, and sends that key to the portal CLI over stdin. No secret is printed.
"""
import argparse
import datetime
import importlib.util
import json
import os
from pathlib import Path
import re
import urllib.error
import urllib.request

spec=importlib.util.spec_from_file_location('portal_manage',Path(__file__).with_name('manage.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args):raise ValueError('Local API redirect refused')


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('action',choices=['inspect','request-update','connect'])
    p.add_argument('--root',type=Path,default=m.DEFAULT_ROOT)
    p.add_argument('--control-env',type=Path,default=Path('/etc/harbor/control.env'))
    p.add_argument('--control-origin',default='')
    args=p.parse_args()
    if os.name=='nt' or os.geteuid()!=0:raise ValueError('Run on the Linux VPS as root')
    env_path=args.control_env.resolve()
    if args.control_env.is_symlink() or env_path!=args.control_env or env_path.stat().st_uid!=0 or env_path.stat().st_mode&0o077:
        raise ValueError('Root-owned private control environment required')
    env=m.environment(env_path)
    token=env.get('ADMIN_TOKEN','')
    if len(token)<32:raise ValueError('Control administrator token not found in the local environment')
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
    def api(path,body=None,method=None,key=None):
        req=urllib.request.Request('http://127.0.0.1:8080/api/v1/'+path,data=None if body is None else json.dumps(body).encode(),method=method,headers={'Authorization':'Bearer '+(key or token),'Content-Type':'application/json'})
        try:
            with opener.open(req,timeout=20) as res:
                return json.loads(res.read(2<<20))
        except urllib.error.HTTPError as e:
            raise RuntimeError('Local control API returned HTTP '+str(e.code)) from None
    session=api('session')
    if args.action=='inspect':
        capabilities=api('api-capabilities')
        print(json.dumps({'version':session.get('version'),'commerce_v1':'commerce_v1' in capabilities.get('features',[]),'hosts':len(api('hosts')),'nodes':len(api('live'))},ensure_ascii=False))
        return
    if args.action=='request-update':
        current=session['version']
        release=api('panel-release')
        print(json.dumps({k:release.get(k) for k in ['current','latest','update_available','automatic_update']},ensure_ascii=False))
        if release.get('update_available'):
            if not release.get('automatic_update'):raise RuntimeError('Existing control-plane local updater is not ready')
            result=api('panel-updates',{'current':current,'version':release['latest']})
            print('Control-plane update queued: '+result['id'])
        else:
            print('Control plane already at the latest official version')
        return
    root=m.validate_root(args.root)
    origin=m.origin_valid(args.control_origin)
    if not origin.startswith('https://'):raise ValueError('Control connection requires a valid HTTPS origin')
    if api('commerce/groups').get('feature')!='commerce_v1':raise RuntimeError('Upgrade the control plane to commerce_v1 first')
    # Re-running must not create unbounded active keys.
    state_path=root/'control-link.json'
    if state_path.exists():
        state=m.read_json(state_path)
        if state.get('origin')==origin:
            print('Existing dedicated control key preserved. To rotate it, revoke the key and update the portal settings explicitly.')
            return
        raise RuntimeError('A dedicated key already exists. Edit the connection in administrator settings before relinking.')
    scopes=['commerce:read','commerce:write','users:read','devices:read','subscriptions:read','subscriptions:links','profiles:read']
    expires=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=365)).isoformat().replace('+00:00','Z')
    key=api('api-keys',{'name':'Starhub Portal on this VPS','scopes':scopes,'expires_at':expires,'rate_limit':600})
    if not re.fullmatch(r'xs_[a-f0-9]{64}',key.get('token','')):raise ValueError('Invalid restricted API key response')
    try:
        m.compose(root,'exec','-T','app','/app/portal','control-configure',origin,input=(key['token']+'\n').encode())
    except Exception:
        api('api-keys/'+key['id'],method='DELETE')
        raise
    m.write_private(state_path,{'origin':origin,'key_id':key['id'],'expires_at':expires,'scopes':scopes})
    print('Dedicated restricted Starhub API connection configured and verified. Key value was not printed.')


if __name__=='__main__':
    try:main()
    except Exception as e:
        print('Control connection: '+str(e))
        raise SystemExit(1)
