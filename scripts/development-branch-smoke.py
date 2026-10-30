#!/usr/bin/env python3
"""Dogfood packaged Core/GPT/Local MCP using an ephemeral, isolated Git project.

Usage: python3 scripts/development-branch-smoke.py --bin-dir build/bin \
    --expected-commit <full implementation SHA> --report /tmp/branch-smoke.json

Requires Python 3.9+, Git, and the three already-built binaries. Never builds,
uses a real Dev Bridge data root, or selects a branch in a real repository.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def git(repository, *args, data=None):
    env = os.environ.copy()
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
               GIT_OPTIONAL_LOCKS="0", GIT_TERMINAL_PROMPT="0")
    # Do not inherit a caller's alternate repository/index selection.
    for key in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
                "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"):
        env.pop(key, None)
    return subprocess.run(["git", "-C", str(repository), *args], input=data,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          check=True, env=env).stdout


def snapshot(repository):
    files = {}
    for path in sorted(repository.rglob("*")):
        relative = path.relative_to(repository)
        if relative.parts[0] == ".git":
            continue
        if path.is_symlink():
            files[str(relative)] = {"symlink": os.readlink(path)}
        elif path.is_file():
            files[str(relative)] = {"sha256": digest(path.read_bytes()),
                                    "mode": path.stat().st_mode & 0o777}
        elif path.is_dir():
            files[str(relative)] = {"directory": True}
    return {
        "head": git(repository, "rev-parse", "HEAD").decode().strip(),
        "symbolicHeadBytes": (repository / ".git/HEAD").read_bytes().hex(),
        "localRefs": git(repository, "for-each-ref", "--sort=refname",
                         "--format=%(refname) %(objectname)", "refs/heads/").decode(),
        "indexSHA256": digest((repository / ".git/index").read_bytes()),
        "stagedDiffSHA256": digest(git(repository, "diff", "--cached", "--binary")),
        "unstagedDiffSHA256": digest(git(repository, "diff", "--binary")),
        "statusPorcelainHex": git(repository, "status", "--porcelain=v1", "-z",
                                  "--untracked-files=all").hex(),
        "workingTree": files,
    }


def http(url, body=None, headers=None):
    request_headers = {"Accept": "application/json, text/event-stream"}
    request_headers.update(headers or {})
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        request_headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=request_headers)
    with urllib.request.urlopen(request, timeout=20) as response:
        raw = response.read().decode()
        if response.headers.get_content_type() == "text/event-stream":
            messages = [json.loads(line[5:].strip()) for line in raw.splitlines()
                        if line.startswith("data:") and line[5:].strip()]
            result = next((item for item in messages if "id" in item), None)
        else:
            result = json.loads(raw) if raw else None
        return result, response.headers


class MCP:
    def __init__(self, url, label, report):
        self.url, self.label, self.report = url + "/mcp", label, report
        self.headers, self.counter = {}, 0
        initialized = self.rpc("initialize", {
            "protocolVersion": "2025-03-26", "capabilities": {},
            "clientInfo": {"name": "development-branch-smoke", "version": "1"},
        })
        self.headers["MCP-Protocol-Version"] = initialized["protocolVersion"]
        self.report["sessions"].append({"adapter": label, "initialize": initialized,
                                         "transportSessionId": self.headers.get("Mcp-Session-Id")})
        http(self.url, {"jsonrpc": "2.0", "method": "notifications/initialized"}, self.headers)

    def rpc(self, method, params):
        self.counter += 1
        out, headers = http(self.url, {"jsonrpc": "2.0", "id": self.counter,
                                      "method": method, "params": params}, self.headers)
        if headers.get("Mcp-Session-Id"):
            self.headers["Mcp-Session-Id"] = headers["Mcp-Session-Id"]
        require(out is not None and "error" not in out, f"{method}: {out}")
        return out["result"]

    def call(self, name, arguments, expected_error=None):
        result = self.rpc("tools/call", {"name": name, "arguments": arguments})
        self.report["calls"].append({"adapter": self.label, "tool": name,
                                      "arguments": arguments, "result": result})
        if expected_error:
            require(result.get("isError") is True and expected_error in json.dumps(result),
                    f"expected {expected_error}: {result}")
            return result
        require(not result.get("isError"), f"{self.label} {name}: {result}")
        require(isinstance(result.get("structuredContent"), dict),
                f"{name} omitted authoritative structuredContent")
        return result["structuredContent"]


class Runtime:
    def __init__(self, binaries, root, report):
        self.binaries, self.root, self.report = binaries, root, report
        self.processes, self.logs, self.generation = [], [], 0

    def launch(self, binary, args, pattern):
        log_path = self.root / f"{binary}-{self.generation}.log"
        log = log_path.open("wb")
        self.logs.append(log)
        command = [str(self.binaries / binary), *args]
        process = subprocess.Popen(command, cwd=self.root, stdout=log, stderr=subprocess.STDOUT)
        self.processes.append(process)
        self.report["processes"].append({"command": command, "pid": process.pid})
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            output = log_path.read_text(errors="replace")
            match = re.search(pattern, output)
            if match:
                return match.group(1)
            require(process.poll() is None, f"{binary} exited: {output}")
            time.sleep(0.1)
        raise RuntimeError(f"{binary} startup timed out: {log_path.read_text()}")

    def start(self):
        self.generation += 1
        data_root = str(self.root / "data")
        self.core = self.launch("devbridge", ["-mode", "server", "-data-root", data_root,
                                              "-listen", "127.0.0.1:0"],
                                r"DEVBRIDGE_READY (http://127\.0\.0\.1:\d+)")
        self.adapters = {}
        for label, binary in (("GPT", "devbridge-mcp"), ("Local", "devbridge-local-mcp")):
            self.adapters[label] = self.launch(binary, ["-data-root", data_root,
                "-core-url", self.core, "-listen", "127.0.0.1:0"],
                r"listening at (http://127\.0\.0\.1:\d+)/mcp")
        identity, _ = http(self.core + "/_adapters/identity")
        require(Path(identity["dataRoot"]).resolve() == Path(data_root).resolve(),
                "Core identity does not match isolated data root")

    def stop(self):
        for process in reversed(self.processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        self.processes.clear()
        for log in self.logs:
            log.close()
        self.logs.clear()


def run(args, root, report):
    binaries = args.bin_dir.resolve()
    expected = args.expected_commit.lower()
    require(re.fullmatch(r"[0-9a-f]{40}", expected), "--expected-commit must be a full SHA")
    report["expectedCommit"] = expected
    report["binaries"] = {}
    for name in ("devbridge", "devbridge-mcp", "devbridge-local-mcp"):
        binary = (binaries / name).read_bytes()
        # -trimpath builds omit the linker command line from Go build settings.
        # Verify the injected full SHA bytes here, then independently verify
        # the running service's buildinfo.Version through both proxies below.
        require(expected.encode() in binary, f"{name} lacks expected full embedded commit")
        report["binaries"][name] = {"sha256": digest(binary), "embeddedCommit": expected}

    repository = root / "fixture"
    repository.mkdir()
    git(repository, "init", "--initial-branch=main")
    git(repository, "config", "user.name", "Dev Bridge fixture")
    git(repository, "config", "user.email", "fixture@example.invalid")
    (repository / "tracked.txt").write_text("base\n")
    git(repository, "add", "tracked.txt")
    git(repository, "-c", "commit.gpgsign=false", "commit", "-m", "fixture base")
    initial_head = git(repository, "rev-parse", "HEAD").decode().strip()
    for branch in ("dev/v0.1.0", "experiment/test"):
        git(repository, "branch", branch)
    (repository / "tracked.txt").write_text("staged\n")
    git(repository, "add", "tracked.txt")
    (repository / "tracked.txt").write_text("staged plus unstaged\n")
    (repository / "untracked.txt").write_text("preserve this untracked file\n")
    baseline = snapshot(repository)
    report["gitSnapshots"]["initial"] = baseline
    runtime = Runtime(binaries, root, report)

    def unchanged(label, before=baseline):
        after = snapshot(repository)
        report["gitSnapshots"][label] = after
        require(after == before, f"Git mutation detected at {label}")

    def clients():
        result = {label: MCP(url, label, report) for label, url in runtime.adapters.items()}
        schemas = []
        for client in result.values():
            diagnostics = client.call("bridge_diagnostics", {})
            for key in ("applicationVersion", "serverVersion"):
                require(diagnostics[key].endswith("+g" + expected[:7]),
                        f"{client.label} {key} mismatch: {diagnostics}")
            tools = {tool["name"]: tool for tool in client.rpc("tools/list", {})["tools"]}
            schemas.append({name: {key: tools[name][key] for key in ("inputSchema", "outputSchema")}
                            for name in ("development_branch_list", "development_branch_select")})
        require(schemas[0] == schemas[1], "GPT/Local development branch schemas differ")
        report["sharedToolSchemas"] = schemas[0]
        return result

    def project_state(client):
        opened = client.call("project_open", {"projectId": project_id})
        state = opened["currentState"]
        require(state["current"]["featureArea"] is None, "project_open selected a FeatureArea")
        context = state["current"]["project"]["developmentContext"]
        workflow = client.call("workflow_get", {"aiSessionId": opened["aiSessionId"]})
        require(workflow["current"]["featureArea"] is None, "workflow_get selected a FeatureArea")
        require(context == workflow["current"]["project"]["developmentContext"],
                "project_open/workflow_get development context mismatch")
        inventory = client.call("development_branch_list", {"aiSessionId": opened["aiSessionId"]})
        require(inventory == context, "branch list/context mismatch")
        console, _ = http(runtime.core + "/v1/development-context?project_id=" + project_id)
        require(console == context, "Console REST/MCP development context mismatch")
        return opened["aiSessionId"], context

    def selected(context, cycle, status="EXISTS", changed=False):
        require(context["developmentBranchSelectionRequired"] is False, "selection forgotten")
        item = context["repositories"][0]
        require(item["developmentCycle"]["id"] == cycle["id"], "current cycle changed implicitly")
        require(item["developmentRef"] == cycle["developmentRef"], "selected ref retargeted")
        require(item["selectedHeadSha"] == initial_head, "selection baseline rewritten")
        require(item["developmentRefStatus"] == status, f"unexpected ref status: {item}")
        require(item["refChanged"] == changed, f"unexpected drift fact: {item}")
        require(item["repositoryCheckoutMatchesDevelopmentRef"] is False, "checkout mismatch missing")
        require(item["repositoryCheckoutRef"] == "refs/heads/main", "repository checked out")
        if status == "MISSING":
            require(item["currentHeadSha"] == "" and item["statusCode"] == "DEVELOPMENT_REF_MISSING",
                    "missing ref fell back to another ref")

    try:
        runtime.start()
        registration, _ = http(runtime.core + "/v1/projects", {
            "name": "Development Branch packaged smoke", "repository_path": str(repository)})
        report["registration"] = registration
        project_id, repository_id = registration["project_id"], registration["repository_id"]
        transports = clients()
        sessions = {}
        contexts = []
        for label, client in transports.items():
            session, context = project_state(client)
            sessions[label] = session
            contexts.append(context)
            require(context["developmentBranchSelectionRequired"] is True, "registration auto-selected")
            item = context["repositories"][0]
            require(item["repositoryId"] == repository_id and item["developmentCycle"] is None,
                    "initial cycle or repository identity incorrect")
            require([b["fullRef"] for b in item["branches"]] == [
                "refs/heads/dev/v0.1.0", "refs/heads/experiment/test", "refs/heads/main"],
                "local branch inventory incorrect or not sorted")
        require(contexts[0] == contexts[1], "initial GPT/Local context mismatch")
        request = {"aiSessionId": sessions["GPT"], "repositoryId": repository_id,
                   "developmentRef": "refs/heads/dev/v0.1.0", "requestId": "smoke-first-selection"}
        first = transports["GPT"].call("development_branch_select", request)
        unchanged("firstSelection")
        replay = transports["Local"].call("development_branch_select", {
            **request, "aiSessionId": sessions["Local"]})
        require(first == replay, "cross-adapter request replay changed result")
        transports["Local"].call("development_branch_select", {
            **request, "aiSessionId": sessions["Local"], "developmentRef": "refs/heads/main"},
            expected_error="REQUEST_CONFLICT")
        try:
            http(runtime.core + "/v1/development-branch-selection", {
                "project_id": project_id, "repository_id": repository_id,
                "development_ref": "refs/heads/main", "request_id": request["requestId"]})
        except urllib.error.HTTPError as error:
            conflict = json.loads(error.read())
            require(error.code == 409 and conflict["code"] == "REQUEST_CONFLICT",
                    f"REST conflict lost domain semantics: {error.code} {conflict}")
            report["consoleConflict"] = {"httpStatus": error.code, "body": conflict}
        else:
            raise AssertionError("Console REST accepted conflicting request payload")
        for client in transports.values():
            selected(project_state(client)[1], first)
        unchanged("selectedReadsAndReplay")
        old_sessions = set(sessions.values())
        runtime.stop()
        runtime.start()
        transports = clients()
        for label, client in transports.items():
            sessions[label], context = project_state(client)
            require(sessions[label] not in old_sessions, "restart reused AI session")
            selected(context, first)
        replay = transports["Local"].call("development_branch_select", {
            **request, "aiSessionId": sessions["Local"]})
        require(first == replay, "restart request replay changed receipt")
        second = transports["Local"].call("development_branch_select", {
            **request, "aiSessionId": sessions["Local"], "requestId": "smoke-reselection",
            "developmentRef": "refs/heads/experiment/test"})
        require(second["id"] != first["id"], "reselection overwrote original cycle")
        for client in transports.values():
            selected(project_state(client)[1], second)
        unchanged("restartAndReselection")

        # Explicit fixture-only Git mutation, outside Bridge. No checkout needed.
        tree = git(repository, "rev-parse", "HEAD^{tree}").decode().strip()
        moved_head = git(repository, "-c", "commit.gpgsign=false", "commit-tree", tree,
                         "-p", initial_head, data=b"fixture drift\n").decode().strip()
        git(repository, "update-ref", second["developmentRef"], moved_head)
        drift_snapshot = snapshot(repository)
        report["gitSnapshots"]["fixtureMovedRef"] = drift_snapshot
        for client in transports.values():
            context = project_state(client)[1]
            selected(context, second, changed=True)
            require(context["repositories"][0]["currentHeadSha"] == moved_head, "moved head omitted")
        unchanged("driftReads", drift_snapshot)
        git(repository, "update-ref", "-d", second["developmentRef"])
        missing_snapshot = snapshot(repository)
        report["gitSnapshots"]["fixtureDeletedRef"] = missing_snapshot
        for client in transports.values():
            selected(project_state(client)[1], second, status="MISSING", changed=None)
        transports["GPT"].call("development_branch_select", {
            **request, "aiSessionId": sessions["GPT"], "requestId": "smoke-missing-ref",
            "developmentRef": second["developmentRef"]}, expected_error="DEVELOPMENT_BRANCH_NOT_FOUND")
        replay = transports["GPT"].call("development_branch_select", {
            **request, "aiSessionId": sessions["GPT"]})
        require(replay == first, "historical receipt changed after reselection/drift")
        selected(project_state(transports["GPT"])[1], second, status="MISSING", changed=None)
        unchanged("missingReadsAndHistoricalReplay", missing_snapshot)
        runtime.stop()

        # Read history only after Core stops, through a read-only SQLite handle.
        database = root / "data/state/devbridge.db"
        with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True) as connection:
            connection.row_factory = sqlite3.Row
            history = [dict(row) for row in connection.execute(
                "SELECT * FROM development_cycles WHERE project_id=? ORDER BY created_at,id", (project_id,))]
        report["cycleHistory"] = history
        require(len(history) == 2, f"unexpected cycle history: {history}")
        by_id = {row["id"]: row for row in history}
        original, current = by_id[first["id"]], by_id[second["id"]]
        require(original["superseded_at"] is not None and original["end_reason"] == "BRANCH_RESELECTED",
                "original cycle was not retained as historical")
        require(current["superseded_at"] is None, "reselected cycle is not current")
        for row, cycle in ((original, first), (current, second)):
            require(row["development_ref"] == cycle["developmentRef"] and
                    row["selected_head_sha"] == cycle["selectedHeadSha"] and
                    row["created_at"] == cycle["createdAt"], "historical provenance rewritten")
        report["status"] = "PASSED"
    finally:
        runtime.stop()
        report["runtimeLogs"] = {path.name: path.read_text(errors="replace")
                                  for path in sorted(root.glob("*.log"))}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--expected-commit", required=True)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    report = {"status": "FAILED", "sessions": [], "calls": [], "processes": [], "gitSnapshots": {}}
    try:
        with tempfile.TemporaryDirectory(prefix="devbridge-development-branch-smoke-") as directory:
            report["isolatedRoot"] = directory
            run(args, Path(directory).resolve(), report)
    except Exception as error:
        report["error"] = f"{type(error).__name__}: {error}"
    rendered = json.dumps(report, indent=2, ensure_ascii=False) + "\n"
    if args.report:
        args.report.write_text(rendered)
    sys.stdout.write(rendered)
    return 0 if report["status"] == "PASSED" else 1


if __name__ == "__main__":
    sys.exit(main())
