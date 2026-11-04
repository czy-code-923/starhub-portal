"""Reuse a co-located Starhub certificate and owned reverse proxy, without exporting keys."""
import argparse
import json
import os
from pathlib import Path
import re
import urllib.request
import urllib.error


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        raise ValueError('Local control redirect refused')


def control_client(path=Path('/etc/harbor/control.env')):
    if os.name=='nt' or not path.exists():
        return None
    stat=path.stat()
    if path.is_symlink() or stat.st_uid!=0 or stat.st_mode&0o077:
        raise ValueError('Control environment must be private and root-owned')
    values=dict(line.split('=',1) for line in path.read_text().splitlines() if line and not line.startswith('#') and '=' in line)
    token=values.get('ADMIN_TOKEN','')
    if len(token)<32:
        raise ValueError('No local control credential')
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
    def api(resource,body=None,method=None):
        req=urllib.request.Request('http://127.0.0.1:8080/api/v1/'+resource,
            data=None if body is None else json.dumps(body).encode(),method=method,
            headers={'Content-Type':'application/json','Authorization':'Bearer '+token})
        try:
            with opener.open(req,timeout=20) as res:return json.loads(res.read(2<<20))
        except urllib.error.HTTPError as e:
            raise RuntimeError('Local control API '+resource.split('/')[0]+' returned HTTP '+str(e.code)) from None
    return api


def configure(root,domain,port,api=None):
    if not re.fullmatch(r'[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?',domain) or '.' not in domain or '..' in domain or not 1024<=port<=65535:
        raise ValueError('Valid domain and application port required')
    api=api or control_client()
    if api is None:return False
    site=api('site')
    if domain in {site.get('domain'),site.get('share_domain'),site.get('gateway_domain'),site.get('share_gateway_domain')}:
        raise ValueError('Domain belongs to the control plane or subscription gateway')
    certs=[c for c in api('certificates') if c['domain']==domain and not c.get('deleted_at')]
    if len(certs)!=1 or certs[0]['state']!='ready':
        raise ValueError('Create a ready certificate for this domain in Starhub before binding this existing gateway')
    target='http://127.0.0.1:'+str(port)
    matches=[p for p in api('reverse-proxies') if p['domain']==domain]
    if matches and (len(matches)!=1 or matches[0].get('deleted_at') or matches[0]['upstream']!=target):
        raise ValueError('Existing domain is owned by another upstream; refusing to replace it')
    body={'domain':domain,'upstream':target,'certificate_id':certs[0]['id'],'enabled':True,'options':{'connect_timeout':10,'read_timeout':60,'body_limit_mib':2,'websocket':False}}
    if matches:
        old=matches[0]
        if not old['enabled'] or old['certificate_id']!=body['certificate_id']:
            api('reverse-proxies/'+old['id'],{**body,'version':old['version']},'PUT')
        proxy_id=old['id']
    else:
        proxy_id=api('reverse-proxies',body,'POST')['id']
    directory=Path(root)/'gateway';directory.mkdir(mode=0o700,exist_ok=True)
    marker=directory/'control-proxy.json'
    if marker.is_symlink():raise ValueError('Unsafe gateway marker')
    fd=os.open(marker,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
    with os.fdopen(fd,'w') as f:json.dump({'domain':domain,'port':port,'proxy_id':proxy_id},f)
    return True


def disable(root,api=None):
    marker=Path(root)/'gateway/control-proxy.json'
    if not marker.exists():return False
    if marker.is_symlink():raise ValueError('Unsafe gateway marker')
    owned=json.loads(marker.read_text())
    api=api or control_client()
    if api is None:raise ValueError('Cannot disable the linked gateway without its control plane')
    proxies=[p for p in api('reverse-proxies') if p['id']==owned['proxy_id']]
    if proxies:
        p=proxies[0]
        if p['domain']!=owned['domain'] or p['upstream']!='http://127.0.0.1:'+str(owned['port']):
            raise ValueError('Linked proxy changed ownership; refusing to disable it')
        if p['enabled']:
            body={k:p[k] for k in ['domain','upstream','certificate_id','options','version']}
            api('reverse-proxies/'+p['id'],{**body,'enabled':False},'PUT')
    marker.unlink()
    return True


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('action',choices=['inspect','apply']);p.add_argument('domain');p.add_argument('--port',type=int,default=18088);p.add_argument('--root',type=Path,default=Path('/opt/starhub-portal'))
    a=p.parse_args();api=control_client()
    if api is None:raise ValueError('No co-located Starhub control plane')
    if a.action=='inspect':
        cert=[{k:c.get(k) for k in ['id','domain','state','expires_at','result']} for c in api('certificates') if c['domain']==a.domain]
        proxies=[{k:c.get(k) for k in ['id','domain','upstream','enabled','state','version','reported_version']} for c in api('reverse-proxies') if c['domain']==a.domain]
        print(json.dumps({'certificates':cert,'proxies':proxies},ensure_ascii=True))
    else:
        if os.geteuid()!=0:raise ValueError('Run as root')
        configure(a.root,a.domain,a.port,api)
        print('Matching Starhub certificate linked to local portal port; gateway applies asynchronously. No key was copied.')


if __name__=='__main__':
    try:main()
    except Exception as e:raise SystemExit(str(e))
