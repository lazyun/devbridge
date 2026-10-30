#!/usr/bin/env python3
"""Regression tests for evidence interpretation; these do not claim isolation."""
import copy
import pathlib
import tempfile
import unittest

from probe_codex_workset import classify, matrix_cases, reset_targets


class ProbeEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.case = dict(operation='read', path='/owned/target', expected='DENY',
                         before=dict(exists=True, content='unique\n', sha256='old', inode=1),
                         after=dict(exists=True, content='unique\n', sha256='old', inode=1),
                         writeMarker='new\n')
        self.helper = dict(entered=True, exitCode=0)
        self.denied = dict(exitCode=1, stdout='', stderr='/owned/target: Operation not permitted\n')

    def test_nonzero_is_not_denial(self):
        for error in ('No such file or directory', 'cat: command not found', 'unknown failure'):
            record = dict(self.denied, stderr='/owned/target: ' + error)
            self.assertEqual(classify(self.case, record, self.helper, True), 'COMMAND_FAILED_OTHER')
        self.assertEqual(classify(self.case, dict(self.denied, exitCode=127), self.helper, True), 'TOOL_UNAVAILABLE')

    def test_denial_requires_fixture_entry_execution_and_positive_control(self):
        missing = copy.deepcopy(self.case)
        missing['before']['exists'] = False
        self.assertEqual(classify(missing, self.denied, self.helper, True), 'FIXTURE_INVALID')
        self.assertEqual(classify(self.case, self.denied, dict(entered=False), True), 'HELPER_STARTUP_FAILED')
        self.assertEqual(classify(self.case, None, self.helper, True), 'COMMAND_NOT_EXECUTED')
        self.assertEqual(classify(self.case, self.denied, self.helper, False), 'POSITIVE_CONTROL_FAILED')
        self.assertEqual(classify(self.case, self.denied, self.helper, True), 'DENY_MATCH')

    def test_read_requires_exact_unique_content(self):
        record = dict(exitCode=0, stdout='wrong\n', stderr='')
        self.assertEqual(classify(self.case, record, self.helper, True), 'RESULT_INCONSISTENT')
        record['stdout'] = 'unique\n'
        self.assertEqual(classify(self.case, record, self.helper, True), 'ACCESS_SUCCEEDED_WHEN_DENIED')
        self.case['expected'] = 'ALLOW'
        self.assertEqual(classify(self.case, self.denied, self.helper, True), 'ALLOWED_ACCESS_DENIED')

    def test_write_requires_external_effect_and_consistent_status(self):
        self.case['operation'] = 'write'
        record = dict(exitCode=0, stdout='', stderr='')
        self.assertEqual(classify(self.case, record, self.helper, True), 'RESULT_INCONSISTENT')
        self.case['after'].update(content='new\n', sha256='new')
        self.assertEqual(classify(self.case, record, self.helper, True), 'ACCESS_SUCCEEDED_WHEN_DENIED')
        self.assertEqual(classify(self.case, self.denied, self.helper, True), 'RESULT_INCONSISTENT')
        self.assertEqual(classify(self.case, record, dict(entered=True, exitCode=1), True), 'RESULT_INCONSISTENT')
        self.assertEqual(classify(self.case, dict(record, parseError=True), self.helper, True), 'RESULT_INCONSISTENT')

    def test_reset_excludes_readonly_git_objects(self):
        with tempfile.TemporaryDirectory() as directory:
            normal, temporary = [pathlib.Path(directory) / n for n in ('normal', 'tmp')]
            for label, root in [('workspace-local', normal), ('private-tmp', temporary)]:
                for case in matrix_cases(label, 'both-write', root, temporary):
                    if case['operation'] == 'write':
                        path = pathlib.Path(case['path'])
                        path.parent.mkdir(parents=True, exist_ok=True)
                        path.write_text('initial')
            obj = normal / 'app/.git/objects/readonly'
            obj.parent.mkdir()
            obj.write_text('object')
            obj.chmod(0o444)
            snapshots = reset_targets(normal, temporary)
            self.assertNotIn(obj, snapshots)
            for path, data in snapshots.items():
                path.write_text('changed')
                path.write_bytes(data)
                self.assertEqual(path.read_text(), 'initial')
            self.assertEqual(obj.read_text(), 'object')

    def test_matrix_preserves_all_six_groups_and_72_unique_ids(self):
        cases = []
        for layout, count in [('workspace-local', 13), ('private-tmp', 11)]:
            for phase in ('both-write', 'mixed-paths', 'read-only'):
                group = matrix_cases(layout, phase, pathlib.Path('/normal'), pathlib.Path('/private/tmp/new'))
                self.assertEqual(len(group), count)
                cases += group
        self.assertEqual(len({c['caseId'] for c in cases}), 72)
        self.assertEqual(sum(c['expected'] == 'DENY' for c in cases), 46)


if __name__ == '__main__':
    unittest.main()
