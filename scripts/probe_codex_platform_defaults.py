#!/usr/bin/env python3
"""Bounded new-factor control, not a production sandbox or candidate certificate.

Without --exporter: one installed-helper baseline, never a retry or escalation.
With an observation-only --exporter: export the generator's actual argv once,
replay A/B/A with only four temporary-directory grants removed
in B. No handwritten replacement policy. An unavailable helper stops the series.
"""
import argparse
import copy
import json
import os
import pathlib
import uuid

from probe_codex_workset import config_for, execute_cases, now, run, sha

COMMIT = '758ef40f50c1a458425c7cfbf1eb12cbc07af0b0'
TEMP_GRANTS = (
    '(allow file-read* file-test-existence file-write* (subpath "/tmp"))',
    '(allow file-read* file-write* (subpath "/private/tmp"))',
    '(allow file-read* file-write* (subpath "/var/tmp"))',
    '(allow file-read* file-write* (subpath "/private/var/tmp"))',
)


def remove_temp_grants(policy):
    # Fail closed on version drift, duplicate rules or an already-patched export.
    for line in TEMP_GRANTS:
        if policy.splitlines().count(line) != 1:
            raise ValueError('expected exactly one original platform rule: ' + line)
    return ''.join(line for line in policy.splitlines(keepends=True)
                   if line.rstrip('\r\n') not in TEMP_GRANTS)


def outcome(groups):
    if any(not g['entered'] for g in groups):
        return 'HELPER_STARTUP_BLOCKED'
    if any(not g['positiveControlPassed'] for g in groups):
        return 'POSITIVE_CONTROL_FAILED'
    if len(groups) != 3:
        return 'POLICY_EXPORT_REQUIRED'
    for group, expected in zip(groups, ('ACCESS_SUCCEEDED_WHEN_DENIED', 'DENY_MATCH',
                                       'ACCESS_SUCCEEDED_WHEN_DENIED')):
        if any(c['classification'] != expected for c in group['cases'] if c['expected'] == 'DENY'):
            return 'CAUSAL_CONTRAST_NOT_ESTABLISHED'
    return 'POLICY_MECHANISM_READ_WRITE_ABA_OBSERVED'


def validate_export(value, command, cwd, supplied_env):
    argv = value['argv']
    if (value.get('sourceCommit') != COMMIT or value.get('profile') != 'Process'
            or value.get('sandboxExecutable') != '/usr/bin/sandbox-exec'
            or value.get('networkEnabled') is not False or value.get('managedNetwork') is not False
            or value.get('cwd') != str(cwd) or value.get('sandboxPolicyCwd') != str(cwd)
            or argv[:1] != ['-p']):
        raise ValueError('export identity, network, profile or cwd mismatch')
    end = argv.index('--', 2)
    if argv[end + 1:] != command or any(not x.startswith('-D') for x in argv[2:end]):
        raise ValueError('export command or parameter mismatch')
    if not all(isinstance(k, str) and isinstance(v, str) for k, v in value['environment'].items()):
        raise ValueError('invalid exported child environment')
    expected_env = dict(supplied_env, CODEX_SANDBOX='seatbelt', CODEX_SANDBOX_NETWORK_DISABLED='1')
    actual_path = value['environment'].get('PATH', '')
    # Fixed arg0/src/lib.rs prepends a single test-owned apply_patch alias dir.
    # It is held fixed for all replays; no installed helper or PATH replacement.
    suffix = ':' + supplied_env['PATH']
    if actual_path != supplied_env['PATH'] and actual_path.endswith(suffix):
        prefix = actual_path[:-len(suffix)]
        alias_root = pathlib.Path(supplied_env['CODEX_HOME']) / 'tmp/arg0'
        alias = pathlib.Path(prefix)
        if ':' not in prefix and alias.is_dir() and alias.resolve().parent == alias_root.resolve():
            expected_env['PATH'] = actual_path
    if value['environment'] != expected_env:
        raise ValueError('unexpected exported child environment; only sandbox markers and test-owned arg0 PATH prefix are allowed')
    remove_temp_grants(argv[1])
    return argv


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--executable', required=True, type=pathlib.Path)
    parser.add_argument('--native-binary', required=True, type=pathlib.Path)
    parser.add_argument('--exporter', type=pathlib.Path, help='UNINSTALLED observation-only exporter; source claim self-reported')
    parser.add_argument('--output', required=True, type=pathlib.Path, help='new directory under /private/tmp')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    output = args.output.absolute()
    if output.exists() or output.parent.resolve() != pathlib.Path('/private/tmp'):
        parser.error('output must be a new immediate child of /private/tmp')
    for binary in (args.executable, args.native_binary, args.exporter):
        if binary is not None and (not binary.is_file() or not os.access(binary, os.X_OK)):
            parser.error('existing executable required: ' + str(binary))
    boundary = dict(network=False, model=False, appServer=False, existingFilesWritten=False,
                    mode='exported-policy-ABA' if args.exporter else 'installed-baseline-once',
                    output=str(output), maxSandboxInvocations=3 if args.exporter else 1,
                    stopOnStartupOrPositiveControlFailure=True, authorizesProduction=False)
    if args.dry_run:
        print(json.dumps(boundary, indent=2))
        return 0
    output.mkdir(mode=0o700)
    root = output / 'fixtures'
    for name in ('home', 'codex-home', 'fixtures/app', 'fixtures/sdk', 'fixtures/backend', 'fixtures/scratch/tmp'):
        (output / name).mkdir(parents=True, exist_ok=True)
    for name in ('app', 'sdk'):
        for metadata in ('.git', '.codex', '.agents'):
            (root / name / metadata).mkdir()
    for name in ('app/same.txt', 'backend/same.txt', 'scratch/write.txt'):
        (root / name).write_text(name + '-' + uuid.uuid4().hex + '\n')
    env = dict(HOME=str(output / 'home'), CODEX_HOME=str(output / 'codex-home'),
               TMPDIR=str(root / 'scratch/tmp'), LC_ALL='C',
               PATH=str(args.executable.absolute().parent) + ':/usr/bin:/bin:/usr/sbin:/sbin')
    config, profile, rules = config_for(root, 'read-only')
    cases = [dict(caseId='platform-factor/' + role, operation=op, role=role,
                  path=str(root / target), expected=expect, writeMarker='write-' + uuid.uuid4().hex + '\n')
             for role, op, target, expect in (
                 ('app-positive', 'read', 'app/same.txt', 'ALLOW'),
                 ('scratch-positive', 'write', 'scratch/write.txt', 'ALLOW'),
                 ('excluded-read', 'read', 'backend/same.txt', 'DENY'),
                 ('excluded-write', 'write', 'backend/same.txt', 'DENY'))]
    initial = {pathlib.Path(c['path']): pathlib.Path(c['path']).read_bytes() for c in cases}
    def binary_identity(path):
        return dict(path=str(path.absolute()), canonical=str(path.resolve()), sha256=sha(path.read_bytes()))
    report = dict(startedAt=now(), boundary=boundary, helpers=[], environment=env,
                  installedLauncher=binary_identity(args.executable), installedNative=binary_identity(args.native_binary),
                  actualInstalledPolicy='NOT_OBSERVED', candidateMatrix='NOT_RUN',
                  exporterSourceVerification='SELF_REPORTED_NOT_BUILD_VERIFIED',
                  sourceCommit=COMMIT, authorizesProduction=False, completeIsolationProven=False,
                  controllerSha256=sha(pathlib.Path(__file__).read_bytes()))
    for path in (pathlib.Path(__file__), pathlib.Path(__file__).with_name('probe_codex_workset.py')):
        (output / path.name).write_bytes(path.read_bytes())
    exported = None
    report['version'] = run([str(args.executable.absolute()), '--version'], env, output)
    try:
        for phase in (('A', 'B', 'A-restored') if args.exporter else ('A',)):
            for path, data in initial.items():
                path.write_bytes(data)  # retain inode and exact starting bytes
            def runner(cli_argv, child_env, controller_cwd):
                nonlocal exported
                if args.exporter is None:
                    return run(cli_argv, child_env, controller_cwd)
                if exported is None:
                    export_path = output / 'original-generator-export.json'
                    export_env = dict(child_env, CODEX_OFFLINE_POLICY_EXPORT=str(export_path))
                    report['exporter'] = binary_identity(args.exporter)
                    report['exportCommand'] = run([str(args.exporter.absolute())] + cli_argv[1:], export_env, controller_cwd)
                    if report['exportCommand']['exitCode'] != 0 or not export_path.is_file():
                        raise ValueError('policy export unavailable; no sandbox replay')
                    exported = json.loads(export_path.read_text())
                    command = cli_argv[cli_argv.index('--') + 1:]
                    validate_export(exported, command, root / 'app', child_env)
                    # This export-only observation option must not affect the replay environment.
                    if 'CODEX_OFFLINE_POLICY_EXPORT' in exported['environment']:
                        raise ValueError('export observation variable leaked into child environment')
                argv = list(exported['argv'])
                if phase == 'B':
                    argv[1] = remove_temp_grants(argv[1])
                saved = output / ('factor-' + phase)
                (saved / 'policy.sbpl').write_text(argv[1])
                (saved / 'argv.json').write_text(json.dumps(argv, indent=2) + '\n')
                result = run(['/usr/bin/sandbox-exec'] + argv, exported['environment'], exported['cwd'])
                result.update(policySha256=sha(argv[1].encode()),
                              policySource='modified-experiment-copy' if phase == 'B' else 'unverified-exporter-output',
                              policyParameters=argv[2:argv.index('--', 2)],
                              appliedEnvironment=exported['environment'])
                return result
            group = execute_cases(args.executable.absolute(), output, env, root, 'factor', phase,
                                  copy.deepcopy(cases), config, profile, rules, runner=runner)
            group['source'] = ('system Seatbelt replay; exporter source binding not attested' if args.exporter
                               else 'installed official offline CLI; policy not observed')
            report['helpers'].append(group)
            (output / ('factor-' + phase) / 'result.json').write_text(json.dumps(group, indent=2) + '\n')
            if not group['entered'] or not group['positiveControlPassed']:
                break
        report['status'] = outcome(report['helpers'])
        if len(report['helpers']) > 1:
            if len({g['probeSha256'] for g in report['helpers']}) != 1:
                raise ValueError('payload changed across contrast')
            if len({g['suppliedConfigSha256'] for g in report['helpers']}) != 1:
                raise ValueError('configuration changed across contrast')
    except (OSError, ValueError, KeyError, TypeError) as error:
        report.update(status='EVIDENCE_ERROR', error=str(error))
    finally:
        report['endedAt'] = now()
        (output / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(dict(status=report['status'], report=str(output / 'report.json'))))
    return 0 if report['status'] == 'POLICY_MECHANISM_READ_WRITE_ABA_OBSERVED' else 2


if __name__ == '__main__':
    raise SystemExit(main())
