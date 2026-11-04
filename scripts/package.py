"""Build deterministic deployable Linux-container bundles for both host architectures."""
import hashlib
import os
from pathlib import Path
import subprocess
import zipfile

ROOT=Path(__file__).resolve().parents[1]
VERSION=(ROOT/'VERSION').read_text().strip()
OUT=ROOT/'dist'
OUT.mkdir(exist_ok=True)
for arch in ('amd64','arm64'):
    binary=OUT/('portal-'+arch)
    subprocess.run(['go','build','-trimpath','-ldflags=-s -w','-o',str(binary),'./cmd/portal'],cwd=ROOT,env={**os.environ,'GOOS':'linux','GOARCH':arch,'CGO_ENABLED':'0'},check=True)
    name='starhub-portal-linux-'+arch+'.zip'
    with zipfile.ZipFile(OUT/name,'w',zipfile.ZIP_DEFLATED,compresslevel=6) as z:
        z.write(binary,'portal')
        for item in ['VERSION','README.md','LICENSE','Runtime.Dockerfile','compose.yaml']:
            z.write(ROOT/item,item)
        for folder in ('web/dist','scripts','docs'):
            for p in sorted((ROOT/folder).rglob('*')):
                if p.is_file() and '__pycache__' not in p.parts and not p.name.endswith('.pyc'):
                    z.write(p,p.relative_to(ROOT).as_posix())
    binary.unlink()
with zipfile.ZipFile(OUT/('starhub-portal-source-'+VERSION+'.zip'),'w',zipfile.ZIP_DEFLATED) as z:
    for path in sorted(ROOT.rglob('*')):
        relative=path.relative_to(ROOT)
        if path.is_file() and not any(x in relative.parts for x in ['.git','node_modules','dist','data','__pycache__']) and not path.name.endswith(('.env','.log','.pyc','.tsbuildinfo')) and path.name not in ['credentials.json','installation.json']:
            z.write(path,relative.as_posix())
(OUT/'SHA256SUMS.txt').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in sorted(OUT.glob('*.zip'))),encoding='utf-8')
