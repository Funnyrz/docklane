#!/usr/bin/env python3
"""Post-publication acceptance: real public curl pipeline in an isolated guest.

No release API, asset download or dependency operation is mocked. Credentials
remain in the inherited private acceptance state. Reboot the guest before
running the resume phase.
"""
import argparse
import importlib.util
import os
from pathlib import Path
import secrets
import sys
import urllib.request


URL = 'https://raw.githubusercontent.com/Funnyrz/docklane/main/scripts/install-online.sh'
COMMAND = 'set -o pipefail; curl -fsSL ' + URL + ' | sudo bash -s --'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--phase', choices=('install', 'resume'), required=True)
    args = parser.parse_args()
    if sys.platform != 'linux' or os.geteuid() != 0 or os.getenv('DM_PLATFORM_ISOLATED') != '1':
        parser.error('Requires root in an explicitly isolated Linux guest')
    if Path('/proc/1/comm').read_text().strip() != 'systemd':
        parser.error('Requires real systemd as PID1')
    args.source = args.source.resolve(strict=True)
    args.report = args.report.absolute()
    args.report.parent.mkdir(parents=True, exist_ok=True)
    os.umask(0o077)
    args.assets = args.report.parent / 'public-bootstrap'
    args.assets.mkdir(exist_ok=True)
    args.allow_missing_docker = True
    if args.phase == 'install':
        # The inherited harness also checks that the public entrypoint is the
        # reviewed source. Its subsequent invocation independently downloads it.
        with urllib.request.urlopen(URL, timeout=30) as response:
            (args.assets / 'install-online.sh').write_bytes(response.read())
    spec = importlib.util.spec_from_file_location('online_systemd', args.source / 'scripts/test-online-systemd.py')
    harness = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(harness)

    class PublicAcceptance(harness.Acceptance):
        def invoke_online(self):
            result = self.helper.command(['bash', '-c', COMMAND], env=self.env,
                                         timeout=2400, allow_failure=True)
            log = self.state / ('public-installer-' + secrets.token_hex(4) + '.log')
            log.write_bytes(result.stdout + b'\n' + result.stderr)
            log.chmod(0o600)
            self.check(result.returncode == 0, 'public curl installation failed; inspect private log: ' + str(log) + '\n' +
                       self.helper.clean_message(result.stderr.decode(errors='replace')[-2500:]))
            return log.name

        def run(self):
            try:
                super().run()
            finally:
                for phase in self.report.get('phases', []):
                    for check in phase.get('checks', []):
                        check['name'] = check['name'].replace('local candidate online install', 'public curl installation')
                self.report['transport'] = {'public_url': URL, 'command': COMMAND, 'mocked_operations': []}
                self.report['limitations'] = ['A disposable native arm64 Ubuntu 24.04 systemd VM; other distribution/architecture combinations have separate candidate evidence.',
                                              'No firewall rule is changed; remote network exposure is not asserted.']
                harness.private_json(self.args.report, self.report)

    PublicAcceptance(args).run()


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('FAIL public installation acceptance: ' + str(error), file=sys.stderr)
        sys.exit(1)
