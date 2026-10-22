#!/usr/bin/env python3
"""One tiny native fixture through actual pinned GoReleaser, never publishing."""
import contextlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile

import qualify_release as release

ROOT = Path(__file__).resolve().parents[1]


def main():
    goreleaser = os.environ.get('GORELEASER', 'goreleaser')
    version = subprocess.check_output([goreleaser, '--version'], text=True, timeout=15)
    if not re.search(r'GitVersion:\s+2\.18\.1\b', version):
        raise ValueError('pinned GoReleaser2.18.1 required, no automatic install')
    system = {'Darwin': 'darwin', 'Linux': 'linux'}.get(platform.system())
    architecture = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine())
    if not system or not architecture:
        raise ValueError('unsupported native fixture platform')
    config = (ROOT / '.goreleaser.yml').read_text()
    checksum = config.split('\nchecksum:\n', 1)[1].split('\nsboms:\n', 1)[0]
    archive_id = re.search(r'(?m)^- id: (.+)$', config.split('\narchives:\n', 1)[1]).group(1)
    sbom_id = re.search(r'(?m)^  - id: (.+)$', config.split('\nsboms:\n', 1)[1]).group(1)
    with tempfile.TemporaryDirectory(prefix='provider-pipeline-', dir='/private/tmp' if Path('/private/tmp').is_dir() else None) as temporary, contextlib.ExitStack() as cleanup:
        fixture = Path(temporary)
        dist = fixture / 'dist'
        source = fixture / 'source'
        source.mkdir()
        (source / 'go.mod').write_text('module example.com/provider-pipeline-fixture\n\ngo 1.26.0\n')
        (source / 'main.go').write_text('package main\nfunc main() {}\n')
        manifest = fixture / 'terraform-registry-manifest.json'
        shutil.copyfile(ROOT / 'terraform-registry-manifest.json', manifest)
        home = fixture / 'gpg'
        home.mkdir(mode=0o700)
        env = {**os.environ, 'GNUPGHOME': str(home), 'GPG_PASSPHRASE': '', 'GOTOOLCHAIN': 'go1.26.10', 'GIT_OPTIONAL_LOCKS': '0',
               'GOWORK': 'off', 'GOENV': 'off', 'GOFLAGS': '', 'GOEXPERIMENT': '', 'PYTHONDONTWRITEBYTECODE': '1'}
        # Keep the immutable real Git context read-only while all fixture
        # programs/configuration/output live outside the product checkout.
        env['GIT_DIR'] = subprocess.check_output(['git', 'rev-parse', '--absolute-git-dir'], cwd=ROOT, text=True).strip()
        env['GIT_WORK_TREE'] = str(fixture)
        cleanup.callback(subprocess.run, ['gpgconf', '--kill', 'gpg-agent'], env=env, check=True, capture_output=True, timeout=10)
        for name in ('GITHUB_TOKEN', 'GH_TOKEN', 'GITHUB_ACTIONS'):
            env.pop(name, None)
        subprocess.run(['gpg', '--batch', '--pinentry-mode', 'loopback', '--passphrase', '', '--quick-generate-key',
                        'Pipeline test fixture <fixture@example.com>', 'rsa2048', 'sign', '0'], env=env, check=True, capture_output=True, timeout=30)
        keys = subprocess.check_output(['gpg', '--batch', '--with-colons', '--list-secret-keys'], env=env, text=True, stderr=subprocess.DEVNULL)
        fingerprint = next(line.split(':')[9] for line in keys.splitlines() if line.startswith('fpr:'))
        env['FIXTURE_FINGERPRINT'] = fingerprint
        helper = fixture / 'fixture.py'
        helper.write_text('''import importlib.util,json,os,sys
from pathlib import Path
sys.path.insert(0, ''' + repr(str(ROOT / 'scripts')) + ''')
import qualify_release as release
spec=importlib.util.spec_from_file_location('signer',Path(release.ROOT)/'scripts/sign-qualified-artifact.py')
signer=importlib.util.module_from_spec(spec);spec.loader.exec_module(signer)
if sys.argv[1]=='sbom':
    files={}
    for archive in Path.cwd().glob('*.zip'):
        for path in (archive,Path(str(archive)+'.spdx.json'),Path(str(archive)+'.provenance.json')):
            if path!=archive:path.write_text(json.dumps({'localPipelineFixture':True,'archiveSHA256':release.sha(archive)}))
            files[path.name]=release.sha(path)
    files['terraform-registry-manifest.json']=release.sha(Path(sys.argv[2]))
    Path('release-content.json').write_text(json.dumps({'source':{'version':os.environ['FIXTURE_VERSION']},'files':files,'localPipelineFixture':True}))
else:
    artifact,signature=map(Path,sys.argv[2:])
    receipt=artifact.parent/'release-content.json'
    release.verify_checksums(artifact,json.loads(receipt.read_text()),artifact.parent,registry=True)
    signer.sign_gpg(artifact,signature,os.environ['FIXTURE_FINGERPRINT'])
    signer.sign_gpg(receipt,receipt.with_name('release-content.json.sig'),os.environ['FIXTURE_FINGERPRINT'])
''')
        fixture_config = fixture / 'goreleaser.yaml'
        fixture_config.write_text('''version: 2
project_name: terraform-provider-platform-orchestrator
dist: ''' + str(dist) + '''
builds:
  - dir: ''' + str(source) + '''
    main: .
    binary: fixture
    env: [CGO_ENABLED=0]
    flags: [-trimpath]
    goos: [''' + system + ''']
    goarch: [''' + architecture + ''']
archives:
  - id: ''' + archive_id + '''
    formats: [zip]
    name_template: '{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}'
    files: []
checksum:
''' + checksum + '''
sboms:
  - id: ''' + sbom_id + '''
    artifacts: any
    documents: ["*.spdx.json"]
    cmd: ''' + sys.executable + '''
    args: [''' + json.dumps(str(helper)) + ''', sbom, ''' + json.dumps(str(manifest)) + ''']
    env: ['FIXTURE_VERSION={{ .Version }}']
signs:
  - artifacts: checksum
    cmd: ''' + sys.executable + '''
    args: [''' + json.dumps(str(helper)) + ''', sign, '${artifact}', '${signature}']
release:
  disable: true
''')
        # Snapshot is the normal pipeline with publication/announce/validation
        # disabled. No hosted identity, tag or source-build claim is fabricated.
        result = subprocess.run([goreleaser, 'release', '--snapshot', '--config', str(fixture_config), '--timeout', '3m'],
                                cwd=fixture, env=env, capture_output=True, text=True, timeout=200)
        if result.returncode:
            sys.stderr.write(result.stdout + result.stderr)
            raise ValueError('tiny real GoReleaser pipeline failed')
        artifacts = json.loads((dist / 'artifacts.json').read_text())
        if len([a for a in artifacts if a['type'] == 'SBOM']) != 1:
            raise ValueError('actual SBOM artifact registration missing')
        checksum_path, = dist.glob('*_SHA256SUMS')
        lines = checksum_path.read_text().splitlines()
        if len(lines) != 2 or any('.spdx.json' in line or '.provenance.json' in line or 'release-content.json' in line for line in lines):
            raise ValueError('actual checksum IDs did not exclude companion metadata')
        published = fixture / 'published'
        published.mkdir()
        receipt = json.loads((dist / 'release-content.json').read_text())
        for name in receipt['files']:
            if name == 'terraform-registry-manifest.json':
                target = checksum_path.name.removesuffix('_SHA256SUMS') + '_manifest.json'
                shutil.copyfile(manifest, published / target)
            else:
                shutil.copyfile(dist / name, published / name)
        for name in ('release-content.json', 'release-content.json.sig', checksum_path.name, checksum_path.name + '.sig'):
            shutil.copyfile(dist / name, published / name)
        spec = importlib.util.spec_from_file_location('signer', ROOT / 'scripts/sign-qualified-artifact.py')
        signer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(signer)
        old_home = os.environ.get('GNUPGHOME')
        os.environ['GNUPGHOME'] = str(home)
        try:
            signer.verify_gpg(published / 'release-content.json', published / 'release-content.json.sig', fingerprint)
            signer.verify_gpg(published / checksum_path.name, published / (checksum_path.name + '.sig'), fingerprint)
            release.verify_checksums(published / checksum_path.name, receipt, published, registry=True)
            if len(receipt['files']) != 4 or len(list(published.iterdir())) != 8:
                raise ValueError('tiny fixture payload/evidence mapping changed')
        finally:
            if old_home is None:
                os.environ.pop('GNUPGHOME', None)
            else:
                os.environ['GNUPGHOME'] = old_home
        negative_dist = fixture / 'dist-without-id-filter'
        negative_config = fixture / 'goreleaser-without-id-filter.yaml'
        negative_config.write_text(fixture_config.read_text().replace(str(dist), str(negative_dist)).replace('  ids: [provider-archives]\n', ''))
        negative = subprocess.run([goreleaser, 'release', '--snapshot', '--config', str(negative_config), '--timeout', '3m'],
                                  cwd=fixture, env=env, capture_output=True, text=True, timeout=200)
        negative_checksum, = negative_dist.glob('*_SHA256SUMS')
        if negative.returncode == 0 or '.spdx.json' not in negative_checksum.read_text() or (negative_dist / (negative_checksum.name + '.sig')).exists():
            raise ValueError('real missing-ID negative did not refuse metadata before signing')
        print(json.dumps({'pinnedGoReleaser': '2.18.1', 'nativeFixture': system + '/' + architecture,
                          'actualRegisteredSBOMs': 1, 'registryChecksumLines': 2, 'fullReceiptSubjects': 4,
                          'bothRealGPGSignaturesVerified': True, 'missingIDFilterMetadataNegativeRefusedBeforeSigning': True,
                          'publications': 0, 'hostedIdentityClaimed': False}))


if __name__ == '__main__':
    main()
