#!/usr/bin/env python3
"""Exercise installer failures in a temporary filesystem; never touch the host service."""
import hashlib
import pathlib
import subprocess
import tempfile
import zipfile

repo = pathlib.Path(__file__).resolve().parent.parent
source = (repo / 'install.sh').read_text()
function = source[source.index('install_V2bX() {'):source.index('\necho -e "${green}开始安装')]
files = 'V2bX V2bX.sh V2bX.service initconfig.sh config.json dns.json route.json custom_inbound.json custom_outbound.json geoip.dat geosite.dat geoip.db geosite.db'.split()
for scenario in ['download-failure', 'checksum-failure', 'missing-file', 'valid-update']:
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        for directory in ['usr/local/V2bX', 'etc/V2bX', 'etc/systemd/system', 'usr/bin', 'assets']:
            (root / directory).mkdir(parents=True)
        core = root / 'usr/local/V2bX/V2bX'
        config = root / 'etc/V2bX/config.json'
        core.write_text('old-core')
        config.write_text('existing-config')
        asset = root / 'assets/V2bX-linux-64.zip'
        with zipfile.ZipFile(asset, 'w') as z:
            for name in files:
                if scenario == 'missing-file' and name == 'V2bX.sh':
                    continue
                z.writestr(name, 'new-core' if name == 'V2bX' else 'release-data')
        digest = hashlib.sha256(asset.read_bytes()).hexdigest()
        if scenario == 'checksum-failure':
            digest = '0' * 64
        (root / 'assets/SHA256SUMS').write_text(f'{digest}  {asset.name}\n')
        # All absolute install paths are redirected before executing the function.
        isolated = function.replace('/usr/', f'{root}/usr/').replace('/etc/', f'{root}/etc/')
        mocks = '''
curl() {
    local url output
    while [[ $# -gt 0 ]]; do
        case "$1" in
            https:*) url=$1 ;;
            -o) shift; output=$1 ;;
        esac
        shift
    done
    [[ "$scenario" == download-failure ]] && return 22
    cp "$root/assets/${url##*/}" "$output"
}
sha256sum() { shasum -a 256 "$@"; }
systemctl() { echo "$*" >> "$root/service.log"; }
service() { echo "$*" >> "$root/service.log"; }
sleep() { :; }
check_status() { return 0; }
'''
        script = f"root='{root}'\nscenario='{scenario}'\narch=64\nrelease=debian\ncur_dir='{root}'\n" + mocks + isolated + '\ninstall_V2bX test-release\n'
        result = subprocess.run(['bash'], input=script, text=True, errors="replace", capture_output=True)
        assert config.read_text() == 'existing-config', (scenario, 'configuration overwritten')
        if scenario == 'valid-update':
            assert result.returncode == 0, result.stdout + result.stderr
            assert core.read_text() == 'new-core'
            assert core.with_name('V2bX.previous').read_text() == 'old-core'
            assert (root / 'usr/bin/V2bX').read_text() == 'release-data'
        else:
            assert result.returncode != 0, (scenario, result.stdout + result.stderr)
            assert core.read_text() == 'old-core'
            assert not (root / 'service.log').exists(), 'old service stopped before validation'
        print('PASS', scenario)
