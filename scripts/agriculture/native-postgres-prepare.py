"""Download pinned Debian packages and extract locally; never install services."""

import argparse
import hashlib
import io
import json
import os
import platform
import subprocess
import tarfile
import urllib.request
from pathlib import Path

repo = Path(__file__).resolve().parents[2]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--destination', type=Path, required=True)
parser.add_argument('--cache', type=Path)
args = parser.parse_args()
if os.geteuid() == 0 or platform.machine() != 'x86_64':
    raise SystemExit('requires non-root Debian 13 amd64 development environment')
manifest_path = repo/'deploy/agriculture/native-postgres-packages.json'
manifest = json.loads(manifest_path.read_text())
destination = args.destination.resolve()
allowed = (repo/'.local').resolve()
if not allowed.is_relative_to(repo.resolve()) or not destination.is_relative_to(allowed) or destination.exists():
    raise SystemExit('destination must be a new directory inside repository .local')
cache = (args.cache or allowed/'native-postgres/debs').resolve()
if not cache.is_relative_to(allowed):
    raise SystemExit('cache must remain inside repository .local')
cache.mkdir(parents=True, exist_ok=True)
destination.mkdir(parents=True)
for package in manifest['packages']:
    relative = Path(package['Filename'])
    if relative.is_absolute() or '..' in relative.parts:
        raise SystemExit('invalid package path')
    archive = cache/relative.name
    if not archive.resolve().is_relative_to(cache):
        raise SystemExit('cached package path escapes workspace cache')
    if not archive.exists():
        request = urllib.request.Request(manifest['repository']+relative.as_posix())
        with urllib.request.urlopen(request, timeout=60) as response:
            data = response.read(100*1024*1024)
        if hashlib.sha256(data).hexdigest() != package['SHA256']:
            raise SystemExit('official package checksum mismatch')
        archive.write_bytes(data)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != package['SHA256']:
        raise SystemExit('cached package checksum mismatch')
    raw = subprocess.check_output(['dpkg-deb','--fsys-tarfile',str(archive)])
    with tarfile.open(fileobj=io.BytesIO(raw)) as tar:
        for member in tar:
            target = (destination/member.name).resolve()
            if not target.is_relative_to(destination):
                raise SystemExit('package extraction path escapes destination')
            if member.issym() or member.islnk():
                link = (target.parent/member.linkname).resolve() if member.issym() else (destination/member.linkname).resolve()
                if not link.is_relative_to(destination):
                    raise SystemExit('package link escapes destination')
            if member.isdev() or member.isfifo():
                raise SystemExit('special package entry not supported')
    subprocess.run(['dpkg-deb','-x',str(archive),str(destination)],check=True)
# These links mirror the package's alternatives without executing postinst or
# modifying the system alternatives database.
extensions=destination/'usr/share/postgresql/17/extension'
for name in ['postgis','postgis_raster','postgis_sfcgal','postgis_tiger_geocoder',
             'postgis_topology','address_standardizer','address_standardizer_data_us']:
    (extensions/(name+'.control')).symlink_to(name+'-3.control')
receipt={'manifest_sha256':hashlib.sha256(manifest_path.read_bytes()).hexdigest(),
         'files':{str(p.relative_to(destination)):hashlib.sha256(p.read_bytes()).hexdigest()
                  for p in destination.rglob('*') if p.is_file() and not p.is_symlink()},
         'links':{str(p.relative_to(destination)):os.readlink(p)
                  for p in destination.rglob('*') if p.is_symlink()}}
(destination/'prepared.json').write_text(json.dumps(receipt,indent=2)+'\n')
print('Verified workspace-only package extraction: '+str(destination))
