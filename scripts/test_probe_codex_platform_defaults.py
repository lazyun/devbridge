#!/usr/bin/env python3
"""Controller regressions use simulated process results, never isolation evidence."""
import json
import contextlib
import io
import pathlib
import shutil
import sys
import unittest
import uuid
from unittest.mock import patch

import probe_codex_platform_defaults as probe


class PlatformFactorTests(unittest.TestCase):
    def test_exact_removal_preserves_every_other_byte(self):
        source = '(version 1)\n; untouched\n' + '\n'.join(probe.TEMP_GRANTS) + '\n(deny default)\n'
        self.assertEqual(probe.remove_temp_grants(source), '(version 1)\n; untouched\n(deny default)\n')
        for wrong in (source + probe.TEMP_GRANTS[0] + '\n', source.replace(probe.TEMP_GRANTS[0], ''), ''):
            with self.assertRaises(ValueError):
                probe.remove_temp_grants(wrong)

    def test_startup_and_missing_contrast_never_count_as_success(self):
        self.assertEqual(probe.outcome([dict(entered=False)]), 'HELPER_STARTUP_BLOCKED')
        self.assertEqual(probe.outcome([dict(entered=True, positiveControlPassed=False)]), 'POSITIVE_CONTROL_FAILED')
        self.assertEqual(probe.outcome([dict(entered=True, positiveControlPassed=True)]), 'POLICY_EXPORT_REQUIRED')

    def run_simulation(self, blocked=False, corrupt_export=None):
        output = pathlib.Path('/private/tmp') / ('devbridge-factor-unit-' + uuid.uuid4().hex)
        calls = []
        executable = pathlib.Path('/bin/sh')
        def fake_run(argv, env, cwd):
            argv = list(map(str, argv))
            result = dict(argv=argv, cwd=str(cwd), exitCode=0, stdout='', stderr='')
            if argv[-1] == '--version':
                result['stdout'] = 'SIMULATED VERSION\n'
            elif 'CODEX_OFFLINE_POLICY_EXPORT' in env:
                export = dict(sourceCommit=probe.COMMIT, profile='Process', networkEnabled=False,
                              managedNetwork=False, sandboxExecutable='/usr/bin/sandbox-exec',
                              cwd=argv[argv.index('-C') + 1], sandboxPolicyCwd=argv[argv.index('-C') + 1],
                              environment=dict({k: v for k, v in env.items() if k != 'CODEX_OFFLINE_POLICY_EXPORT'},
                                               CODEX_SANDBOX='seatbelt', CODEX_SANDBOX_NETWORK_DISABLED='1'),
                              argv=['-p', '\n'.join(probe.TEMP_GRANTS) + '\n', '-DTEST=synthetic', '--']
                                   + argv[argv.index('--') + 1:])
                if corrupt_export:
                    corrupt_export(export)
                pathlib.Path(env['CODEX_OFFLINE_POLICY_EXPORT']).write_text(json.dumps(export))
            else:
                calls.append(argv)
                if blocked:
                    result.update(exitCode=71, stderr='sandbox-exec: sandbox_apply: Operation not permitted\n')
                    return result
                # No command execution: simulate externally observed effects and status files.
                scratch = output / 'fixtures/scratch'
                script = (scratch / 'probe.sh').read_text()
                import re
                import shlex
                operations = [shlex.split(line) for line in
                              re.findall(r'^(/bin/.*? 2> [^\n]+)\n', script, re.M | re.S)]
                result['stdout'] = 'HELPER_ENTERED\n'
                for i, op in enumerate(operations):
                    denied = len(calls) == 2 and i >= 2
                    target = pathlib.Path(op[1] if op[0] == '/bin/cat' else op[4])
                    stdout = ''
                    if not denied:
                        if op[0] == '/bin/cat':
                            stdout = target.read_text()
                        else:
                            target.write_text(op[5])
                    (scratch / f'{i}.out').write_text(stdout)
                    (scratch / f'{i}.err').write_text(str(target) + ': Operation not permitted\n' if denied else '')
                    result['stdout'] += f'BEGIN {i}\nEND {i} {1 if denied else 0}\n'
            return result
        args = ['probe', '--executable', str(executable), '--native-binary', str(executable),
                '--exporter', str(executable), '--output', str(output)]
        try:
            with patch.object(sys, 'argv', args), patch.object(probe, 'run', fake_run), contextlib.redirect_stdout(io.StringIO()):
                code = probe.main()
            return code, json.loads((output / 'report.json').read_text()), calls
        finally:
            if output.exists():
                shutil.rmtree(output)

    def test_control_replays_identical_payload_parameters_paths_and_restores_factor(self):
        code, report, calls = self.run_simulation()
        self.assertEqual(code, 0, report.get('error'))
        self.assertEqual(report['status'], 'POLICY_MECHANISM_READ_WRITE_ABA_OBSERVED')
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls[0], calls[2])
        self.assertEqual(calls[0][:2] + calls[0][3:], calls[1][:2] + calls[1][3:])
        self.assertEqual(calls[1][2], probe.remove_temp_grants(calls[0][2]))
        self.assertEqual(len({g['probeSha256'] for g in report['helpers']}), 1)
        for original, restored in zip(report['helpers'][0]['cases'], report['helpers'][2]['cases']):
            self.assertEqual(original['before'], restored['before'])
            self.assertEqual(original['writeMarker'], restored['writeMarker'])

    def test_stops_after_first_blocked_helper(self):
        code, report, calls = self.run_simulation(blocked=True)
        self.assertEqual(code, 2)
        self.assertEqual(len(calls), 1)
        self.assertEqual(report['status'], 'HELPER_STARTUP_BLOCKED')
        self.assertTrue(all(not c['pass'] for c in report['helpers'][0]['cases']))

    def test_mismatched_export_never_launches_a_sandbox(self):
        changes = [lambda v: v.update(sandboxPolicyCwd='/other'),
                   lambda v: v['environment'].update(TMPDIR='/other'),
                   lambda v: v['environment'].update(PATH='/other'),
                   lambda v: v.update(sourceCommit='wrong'),
                   lambda v: v.update(networkEnabled=True),
                   lambda v: v['argv'].append('extra-command-argument')]
        for change in changes:
            code, report, calls = self.run_simulation(corrupt_export=change)
            self.assertEqual(code, 2)
            self.assertEqual(report['status'], 'EVIDENCE_ERROR')
            self.assertEqual(calls, [])


if __name__ == '__main__':
    unittest.main()
