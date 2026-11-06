#!/bin/sh
set -eu
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1; then exec sudo sh "$0" "$@"; fi
  echo 'Root access required. Log in as root (or run su -), then rerun: sh install.sh [options]' >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y python3 ca-certificates curl;
  elif command -v dnf >/dev/null 2>&1; then dnf install -y python3 ca-certificates curl;
  elif command -v apk >/dev/null 2>&1; then apk add python3 ca-certificates curl;
  else echo 'Install Python 3.10+ and curl using your system package manager.'; exit 1; fi
fi
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3,10) else "Python 3.10+ required; upgrade the OS Python package before retrying")'
f=$(mktemp)
trap 'rm -f "$f"' EXIT HUP INT TERM
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/czy-code-923/starhub-portal/main/scripts/manage.py -o "$f"
python3 "$f" install "$@"
