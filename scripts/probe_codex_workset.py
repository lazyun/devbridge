#!/usr/bin/env python3
"""Offline helper evidence; never creates an app-server Thread/Turn or calls a model.

Only new test-owned directories are mutated. --mode minimal is the independent
terminal control; --dry-run describes its boundary without creating any files.
A matching expectation never authorizes production. All subprocess waits are bounded.
"""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import platform
import re
import shlex
import shutil
import subprocess
import tempfile
import uuid

VERSION = 2
PHASES = ('both-write', 'mixed-paths', 'read-only')


def sha(data):
    return hashlib.sha256(data).hexdigest()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def run(argv, env, cwd, timeout=30):
    result = dict(argv=list(map(str, argv)), cwd=str(cwd), startedAt=now())
    try:
        child = subprocess.run(result['argv'], env=env, cwd=cwd,
                               stdin=subprocess.DEVNULL, close_fds=True,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               text=True, timeout=timeout)
        result.update(exitCode=child.returncode, stdout=child.stdout, stderr=child.stderr)
    except subprocess.TimeoutExpired as error:
        result.update(exitCode=None, stdout=(error.stdout or b'').decode(),
                      stderr=(error.stderr or b'').decode(), launchError='TIMEOUT')
    except OSError as error:
        result.update(exitCode=None, stdout='', stderr=str(error), launchError='NOT_EXECUTED')
    result['endedAt'] = now()
    return result


def identity(path):
    path = pathlib.Path(path)
    value = {'path': str(path), 'canonical': str(path.resolve()), 'exists': path.exists()}
    if value['exists']:
        stat = path.stat()
        value.update(device=stat.st_dev, inode=stat.st_ino, mode=oct(stat.st_mode), size=stat.st_size)
        if path.is_file():
            data = path.read_bytes()
            value.update(sha256=sha(data), content=data.decode('utf-8', errors='replace'))
    return value


def config_for(root, phase):
    rules = {':root': 'deny', ':minimal': 'read', ':tmpdir': 'deny', ':slash_tmp': 'deny',
             '/tmp': 'deny', '/private/tmp': 'deny', str(root / 'scratch'): 'write'}
    for name in ('app', 'sdk'):
        rules[str(root / name)] = 'write' if phase == 'both-write' else 'read'
        for metadata in ('.git', '.codex', '.agents'):
            rules[str(root / name / metadata)] = 'read'
    if phase == 'mixed-paths':
        rules[str(root / 'app/src')] = 'write'
    header = ('default_permissions = "exact"\napproval_policy = "never"\n'
              'allow_login_shell = false\nweb_search = "disabled"\n'
              '[analytics]\nenabled = false\n[feedback]\nenabled = false\n')
    profile = '[permissions.exact.filesystem]\n'
    profile += ''.join(json.dumps(k) + ' = ' + json.dumps(v) + '\n' for k, v in rules.items())
    profile += '[permissions.exact.network]\nenabled = false\n'
    return header + profile, profile, rules


def matrix_cases(label, phase, root, tmp_root):
    specs = [('read', 'app', root / 'app/same.txt', True),
             ('read', 'sdk', root / 'sdk/same.txt', True),
             ('read', 'absent', root / 'backend/same.txt', False),
             ('read', 'symlink', root / 'app/escape/same.txt', False),
             ('write', 'app-path', root / 'app/src/allowed.txt', phase != 'read-only'),
             ('write', 'app-parent', root / 'app/same.txt', phase == 'both-write'),
             ('write', 'sdk', root / 'sdk/same.txt', phase == 'both-write'),
             ('write', 'absent', root / 'backend/same.txt', False),
             ('write', 'git-metadata', root / 'app/.git/config', False),
             ('write', 'scratch', root / 'scratch/write.txt', True),
             ('write', 'parent', root / 'outside.txt', False)]
    if label == 'workspace-local':
        specs += [('read', 'excluded-private-tmp', tmp_root / 'backend/same.txt', False),
                  ('write', 'excluded-private-tmp', tmp_root / 'backend/same.txt', False)]
    return [dict(caseId=f'{label}/{phase}/{op}:{role}', operation=op, role=role,
                 path=str(path), expected='ALLOW' if allow else 'DENY',
                 member=('app' if role in ('app', 'app-path', 'app-parent', 'git-metadata')
                         else 'sdk' if role == 'sdk' else 'backend' if role in
                         ('absent', 'symlink', 'excluded-private-tmp') else None))
            for op, role, path, allow in specs]


def classify(case, record, helper, positive_control):
    """Fail closed on invalid evidence; nonzero alone is NEVER a successful denial."""
    if not case['before']['exists']:
        return 'FIXTURE_INVALID'
    if not helper.get('entered'):
        return 'HELPER_STARTUP_FAILED'
    if record is None:
        return 'COMMAND_NOT_EXECUTED'
    if helper['exitCode'] != 0 or record.get('parseError'):
        return 'RESULT_INCONSISTENT'
    rc, stdout, stderr = record['exitCode'], record['stdout'], record['stderr']
    if rc in (126, 127):
        return 'TOOL_UNAVAILABLE'
    before, after = case['before'], case['after']
    if case['operation'] == 'read':
        success = rc == 0 and stdout == before['content'] and not stderr
        unchanged = True  # Later write cases may address the same file; read evidence is stdout.
    else:
        success = rc == 0 and after.get('content') == case['writeMarker'] and not stdout and not stderr
        unchanged = (after.get('sha256') == before.get('sha256') and
                     after.get('inode') == before.get('inode'))
    if rc == 0 or success:
        if not success:
            return 'RESULT_INCONSISTENT'
        return 'ALLOW_MATCH' if case['expected'] == 'ALLOW' else 'ACCESS_SUCCEEDED_WHEN_DENIED'
    denied = (('Permission denied' in stderr or 'Operation not permitted' in stderr)
              and case['path'] in stderr and not stdout and unchanged)
    if not denied:
        return 'RESULT_INCONSISTENT' if not unchanged else 'COMMAND_FAILED_OTHER'
    if not positive_control:
        return 'POSITIVE_CONTROL_FAILED'
    return 'DENY_MATCH' if case['expected'] == 'DENY' else 'ALLOWED_ACCESS_DENIED'


def execute_cases(executable, output, env, root, label, phase, cases, config, profile, rules,
                  profile_name='exact', payload=None, runner=run):
    scratch = root / 'scratch'
    # The controller resets matrix fixtures before each policy. Minimal read
    # controls never mutate target bytes between helper invocations.
    for case in cases:
        case.update(probeVersion=VERSION, layout=label, policy=phase)
        case.setdefault('writeMarker', 'write-' + uuid.uuid4().hex + '\n')
        case['before'] = identity(case['path'])
        target = pathlib.Path(case['before']['canonical'])
        case['explicitGrantAncestors'] = {p: access for p, access in rules.items()
                                          if p.startswith('/') and access != 'deny'
                                          and (pathlib.Path(p).resolve() == target or
                                               pathlib.Path(p).resolve() in target.parents)}
    script = 'set -u\nprintf "HELPER_ENTERED\\n"\n'
    for index, case in enumerate(cases):
        out, err = scratch / f'{index}.out', scratch / f'{index}.err'
        for p in (out, err):
            if p.exists():
                p.unlink()
        op = ['/bin/cat', case['path']] if case['operation'] == 'read' else [
            '/bin/sh', '-c', 'printf "%s" "$2" > "$1"', 'probe-write', case['path'], case['writeMarker']]
        case['commandArgv'] = op
        script += f'printf "BEGIN {index}\\n"\n'
        script += shlex.join(op) + ' > ' + shlex.quote(str(out)) + ' 2> ' + shlex.quote(str(err)) + '\n'
        script += f'rc=$?\nprintf "END {index} %s\\n" "$rc"\n'
    script += 'exit 0\n'
    if payload is not None:
        # A single fixed payload takes only the target argument as the path control.
        script = payload
    probe = scratch / 'probe.sh'
    probe.write_text(script)
    saved = output / f'{label}-{phase}'
    saved.mkdir()
    (saved / 'probe.sh').write_text(script)
    (saved / 'config.toml').write_text(config)
    (pathlib.Path(env['CODEX_HOME']) / 'config.toml').write_text(config)
    argv = [str(executable), 'sandbox', '-P', profile_name, '-C', str(root / 'app'), '--', '/bin/sh', str(probe)]
    if payload is not None:
        argv += [cases[-1]['path']]
    result = runner(argv, env, output)
    result.update(layout=label, phase=phase, caseCount=len(cases),
                  suppliedConfigSha256=sha(config.encode()), profileSha256=sha(profile.encode()),
                  effectiveConfig='NOT_INTROSPECTED: explicit isolated config + -P; no runtime provenance API called',
                  environment=dict(env), probeSha256=sha(script.encode()),
                  entered=result['stdout'].startswith('HELPER_ENTERED\n'),
                  rules=rules, cases=cases, source='official offline sandbox helper, external controller observations')
    records = {}
    expected_lines = ['HELPER_ENTERED']
    for index, case in enumerate(cases):
        case['after'] = identity(case['path'])
        begin = f'BEGIN {index}'
        ends = re.findall(rf'^END {index} (-?\d+)$', result['stdout'], re.M)
        if begin in result['stdout'].splitlines() and len(ends) == 1:
            out, err = scratch / f'{index}.out', scratch / f'{index}.err'
            records[index] = dict(exitCode=int(ends[0]), stdout=out.read_text() if out.exists() else '',
                                  stderr=err.read_text() if err.exists() else '',
                                  parseError=not out.exists() or not err.exists())
            expected_lines += [begin, f'END {index} {ends[0]}']
        case['observation'] = records.get(index)
        case['operationExecuted'] = index in records
        case['targetIdentityUnchanged'] = (case['before'].get('inode') == case['after'].get('inode') and
                                           case['before'].get('device') == case['after'].get('device'))
    if result['entered'] and result['stdout'].splitlines() != expected_lines:
        for record in records.values():
            record['parseError'] = True
    positive = all(classify(c, records.get(i), result, True) == 'ALLOW_MATCH'
                   for i, c in enumerate(cases) if c['expected'] == 'ALLOW')
    result['positiveControlPassed'] = positive and any(c['expected'] == 'ALLOW' for c in cases)
    for index, case in enumerate(cases):
        case['classification'] = classify(case, records.get(index), result, result['positiveControlPassed'])
        case['pass'] = case['classification'] in ('ALLOW_MATCH', 'DENY_MATCH')
        case['operationSucceeded'] = (True if case['classification'] in ('ALLOW_MATCH', 'ACCESS_SUCCEEDED_WHEN_DENIED')
                                      else False if case['classification'] in ('DENY_MATCH', 'ALLOWED_ACCESS_DENIED') else None)
    result['pass'] = all(c['pass'] for c in cases)
    (saved / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
    return result


def make_fixtures(parent, env, output):
    root = pathlib.Path(tempfile.mkdtemp(prefix='.devbridge-offline-workset-', dir=parent))
    try:
        for name in ('app', 'sdk', 'backend'):
            repo = root / name
            (repo / 'src').mkdir(parents=True)
            for sub in ('.codex', '.agents'):
                (repo / sub).mkdir()
            (repo / 'same.txt').write_text(name + '-' + uuid.uuid4().hex + '\n')
            (repo / 'src/allowed.txt').write_text(name + '-source-' + uuid.uuid4().hex + '\n')
            for cmd in (['init', '-q', '-b', name + '-development'], ['add', 'same.txt', 'src/allowed.txt'],
                        ['-c', 'user.name=Offline probe', '-c', 'user.email=probe@example.invalid',
                         '-c', 'core.hooksPath=/dev/null', 'commit', '-qm', name + ' fixture']):
                r = run(['/usr/bin/git', '-C', str(repo)] + cmd, env, output)
                if r['exitCode'] != 0:
                    raise RuntimeError('test-owned git fixture failed: ' + json.dumps(r))
        (root / 'scratch/tmp').mkdir(parents=True)
        (root / 'scratch/write.txt').write_text('scratch-' + uuid.uuid4().hex + '\n')
        (root / 'outside.txt').write_text('outside-' + uuid.uuid4().hex + '\n')
        (root / 'app/escape').symlink_to(root / 'backend', target_is_directory=True)
        return root
    except BaseException:
        shutil.rmtree(root)
        raise


def reset_targets(normal, temporary):
    # Git objects may be readonly; only restore actual operation targets.
    paths = {pathlib.Path(c['path']).resolve()
             for label, root in [('workspace-local', normal), ('private-tmp', temporary)]
             for c in matrix_cases(label, 'both-write', root, temporary)
             if c['operation'] == 'write'}
    return {p: p.read_bytes() for p in paths}


def preserve_original(path, output, report):
    raw = path.read_bytes()
    original = json.loads(raw)
    if original['launcherSha256'] != report['launcherSha256'] or original.get('nativeBinary', {}).get('sha256') != report.get('nativeBinary', {}).get('sha256'):
        raise ValueError('original evidence binary identity does not match')
    dest = output / 'original'
    dest.mkdir()
    (dest / 'report.json').write_bytes(raw)
    cases = {}
    for group in original['helpers']:
        config = path.parent / (group['layout'] + '-' + group['phase'] + '.toml')
        data = config.read_bytes()
        if sha(data) != group['profileSha256']:
            raise ValueError('original config digest mismatch')
        (dest / config.name).write_bytes(data)
        for line in group['output'].splitlines():
            match = re.fullmatch(r'(read|write):([\w-]+) (ALLOW|DENY) expected=(ALLOW|DENY)', line)
            if not match:
                raise ValueError('unrecognized original case line: ' + line)
            op, role, actual, expected = match.groups()
            key = f"{group['layout']}/{group['phase']}/{op}:{role}"
            if key in cases:
                raise ValueError('duplicate original case')
            cases[key] = dict(rawLine=line, actual=actual, expected=expected,
                              oldProbeVersion=1, oldPerOperationEvidence='NOT_RECORDED')
            cases[key]['pass'] = actual == expected
    if len(cases) != 72 or sum(not c['pass'] for c in cases.values()) != 26:
        raise ValueError('not the expected original 72/26 evidence')
    report['original'] = dict(path=str(path), sha256=sha(raw), cases=cases,
                              timestamp='NOT_RECORDED_BY_V1', identityMatched=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--executable', required=True)
    parser.add_argument('--native-binary', required=True, help='Audited installed native path; hashed, not inferred')
    parser.add_argument('--output', required=True, help='NEW evidence directory; never reused or deleted')
    parser.add_argument('--fixture-parent', required=True, help='Existing normal-directory parent; only new children mutated')
    parser.add_argument('--sandbox', action='store_true')
    parser.add_argument('--mode', choices=('matrix', 'minimal'), default='matrix')
    parser.add_argument('--original-report', type=pathlib.Path)
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    executable = pathlib.Path(args.executable).absolute()
    native = pathlib.Path(args.native_binary).resolve(strict=True)
    parent = pathlib.Path(args.fixture_parent).resolve(strict=True)
    output = pathlib.Path(args.output).absolute()
    if not executable.is_file() or not os.access(executable, os.X_OK) or not parent.is_dir():
        parser.error('existing executable, native binary, and fixture parent required')
    if output.exists():
        parser.error('output must not exist')
    boundary = dict(mode=args.mode, output=str(output), fixtureParent=str(parent),
                    commands=['--version', 'sandbox --help', 'test-owned git init/add/commit', 'sandbox -P exact'],
                    model=False, appServer=False, networkCommands=False, existingFilesWritten=False,
                    cleanup='only two mkdtemp-owned fixture roots; output retained', subprocessTimeoutSeconds=30)
    if args.dry_run:
        print(json.dumps(boundary, indent=2))
        return 0
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    (output / 'probe-controller.py').write_bytes(pathlib.Path(__file__).read_bytes())
    for name in ('home', 'codex-home', 'tmp'):
        (output / name).mkdir(mode=0o700)
    env = dict(HOME=str(output / 'home'), CODEX_HOME=str(output / 'codex-home'), TMPDIR=str(output / 'tmp'),
               PATH=str(executable.parent) + ':/usr/bin:/bin:/usr/sbin:/sbin',
               GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null', LC_ALL='C')
    report = dict(probeVersion=VERSION, probeSha256=sha(pathlib.Path(__file__).read_bytes()), startedAt=now(),
                  evidenceKind='OFFLINE_HELPER_ONLY', authorizesProduction=False, completeIsolationProven=False,
                  realModelE2E='NOT_RUN', threadResumeNarrowing='NOT_RUN', sourceCommit='UNVERIFIED',
                  independentLaunchContext='EVIDENCE_PENDING', boundary=boundary, environment=env,
                  executable=str(executable), resolvedLauncher=str(executable.resolve()),
                  launcherSha256=sha(executable.read_bytes()), nativeBinary=dict(path=str(native), sha256=sha(native.read_bytes())),
                  platform=dict(system=platform.system(), release=platform.release(), machine=platform.machine()),
                  commands={}, helpers=[], aliases=[identity('/tmp'), identity('/private/tmp')],
                  processContext=dict(stdin='DEVNULL', close_fds=True, stdout='PIPE', stderr='PIPE',
                                      parentContext='inherited; env allowlist does not remove sandbox extensions'))
    if args.original_report:
        preserve_original(args.original_report, output, report)
    for name, command in [('version', ['--version']), ('sandboxHelp', ['sandbox', '--help'])]:
        report['commands'][name] = run([str(executable)] + command, env, output)
    roots = []
    try:
        if args.sandbox:
            normal = make_fixtures(parent, env, output)
            roots.append(normal)
            temporary = make_fixtures(pathlib.Path('/private/tmp') if platform.system() == 'Darwin' else pathlib.Path(tempfile.gettempdir()), env, output)
            roots.append(temporary)
            report['fixtureRoots'] = [identity(p) for p in roots]
            report['pathAudit'] = {'fixtureRootsDisjoint': not any(a == b or a in b.parents or b in a.parents for a, b in [(normal.resolve(), temporary.resolve())]),
                                   'configOutsideFixtures': all(r.resolve() not in output.resolve().parents for r in roots),
                                   'scratchSeparateFromMembers': all((r / 'scratch').resolve() not in (r / m).resolve().parents and (r / m).resolve() not in (r / 'scratch').resolve().parents for r in roots for m in ('app', 'sdk', 'backend')),
                                   'note': 'Only exact app/sdk/scratch rules grant writes; excluded targets are under no such canonical ancestor.'}
            initial = reset_targets(normal, temporary)
            if args.mode == 'matrix':
                for label, root in [('workspace-local', normal), ('private-tmp', temporary)]:
                    for phase in PHASES:
                        for path, data in initial.items():
                            path.write_bytes(data)
                        config, profile, rules = config_for(root, phase)
                        cases = matrix_cases(label, phase, root, temporary)
                        report['helpers'].append(execute_cases(executable, output, env, root, label, phase, cases, config, profile, rules))
                if args.original_report:
                    new = {c['caseId']: c for h in report['helpers'] for c in h['cases']}
                    old = report['original']['cases']
                    if set(new) != set(old) or any(new[k]['expected'] != old[k]['expected'] for k in old):
                        raise ValueError('matrix IDs or safety expectations changed')
                    report['caseMapping'] = {k: dict(old=old[k], newClassification=new[k]['classification']) for k in old}
            else:
                # Keep both excluded objects' bytes identical; path/object location
                # is the only target variable, and both identities are recorded.
                (normal / 'backend/same.txt').write_bytes((temporary / 'backend/same.txt').read_bytes())
                config, profile, rules = config_for(normal, 'read-only')
                # Script/config/cwd/fixtures stay byte-identical. Only the target argv,
                # TMPDIR, or selected profile changes in the named control.
                payload = 'set -u\nprintf "HELPER_ENTERED\\n"\n'
                for i, target in [(0, shlex.quote(str(normal / 'app/same.txt'))), (1, '"$1"')]:
                    payload += f'printf "BEGIN {i}\\n"\n/bin/cat {target} > {shlex.quote(str(normal / "scratch" / f"{i}.out"))} 2> {shlex.quote(str(normal / "scratch" / f"{i}.err"))}\nrc=$?\nprintf "END {i} %s\\n" "$rc"\n'
                payload += 'exit 0\n'
                targets = [('baseline', temporary / 'backend/same.txt', env, 'exact'),
                           ('repeat', temporary / 'backend/same.txt', env, 'exact'),
                           ('canonical-alias', pathlib.Path('/tmp') / temporary.name / 'backend/same.txt', env, 'exact'),
                           ('normal-target', normal / 'backend/same.txt', env, 'exact'),
                           ('tmpdir-only', temporary / 'backend/same.txt', dict(env, TMPDIR=str(normal / 'scratch/tmp')), 'exact'),
                           ('undefined-profile', temporary / 'backend/same.txt', env, 'not-a-profile')]
                for phase, target, child_env, profile_name in targets:
                    cases = [dict(caseId=f'minimal/{phase}/read:{role}', operation='read', role=role, member=member,
                                  path=str(path), expected=expected)
                             for role, member, path, expected in [('app', 'app', normal / 'app/same.txt', 'ALLOW'), ('excluded', 'backend', target, 'DENY')]]
                    report['helpers'].append(execute_cases(executable, output, child_env, normal, 'minimal', phase, cases, config, profile, rules, profile_name, payload))
                report['controls'] = {h['phase']: dict(config=h['suppliedConfigSha256'], payload=h['probeSha256'], argv=h['argv'], environment=h['environment']) for h in report['helpers']}
    finally:
        report['endedAt'] = now()
        (output / 'report.json').write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
        for root in roots:
            shutil.rmtree(root)
    print(str(output / 'report.json'))
    for group in report['helpers']:
        print(group['layout'], group['phase'], sum(c['pass'] for c in group['cases']), '/', len(group['cases']), 'helper_rc', group['exitCode'])
    return int(any(not h['pass'] for h in report['helpers']) or any(c['exitCode'] != 0 for c in report['commands'].values()))


if __name__ == '__main__':
    raise SystemExit(main())
