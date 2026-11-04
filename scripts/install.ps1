param([string]$Root="$env:ProgramData\StarhubPortal",[int]$Port=18088,[string]$Origin='',[string]$AdminEmail='admin@starhub.local')
$ErrorActionPreference='Stop'
$python=Get-Command python -ErrorAction SilentlyContinue
if (-not $python -or -not (& $python.Source -c 'import sys; print(sys.version_info >= (3,10))' 2>$null | Select-String 'True')) {
  if (-not (Get-Command winget -ErrorAction SilentlyContinue)) { throw 'Install Python 3.10+ from python.org, then retry.' }
  winget install --id Python.Python.3.13 --exact --source winget
  throw 'Python has been installed. Open a new Administrator PowerShell and run this command again.'
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  if (-not (Get-Command winget -ErrorAction SilentlyContinue)) { throw 'Install Docker Desktop from docker.com and select Linux containers.' }
  winget install --id Docker.DockerDesktop --exact --source winget
  throw 'Docker Desktop has been installed. Complete its first-run setup, select Linux containers, then rerun.'
}
$f=Join-Path ([IO.Path]::GetTempPath()) ('starhub-portal-'+[guid]::NewGuid()+'.py')
try { Invoke-WebRequest https://raw.githubusercontent.com/czy-code-923/starhub-portal/main/scripts/manage.py -OutFile $f; $a=@($f,'install','--root',$Root,'--port',"$Port",'--admin-email',$AdminEmail); if($Origin){$a+=@('--origin',$Origin)}; & $python.Source @a; if($LASTEXITCODE){throw 'Portal installation failed'} } finally { Remove-Item -LiteralPath $f -ErrorAction SilentlyContinue }
