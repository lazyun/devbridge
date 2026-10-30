#!/usr/bin/env python3
"""Exercise packaged Card/details contracts using only temporary repositories/data.

Usage: python3 scripts/card-details-polish-smoke.py --bin-dir /tmp/candidate/bin
"""
import argparse
import importlib.util
import json
from pathlib import Path
import sys
import tempfile

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("branch_smoke", Path(__file__).with_name("development-branch-smoke.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def run(binaries, root, report):
    runtime = smoke.Runtime(binaries.resolve(), root, report)
    try:
        runtime.start()
        projects = []
        for name in ("Project A", "Project B"):
            repository = root / name
            repository.mkdir()
            smoke.git(repository, "init", "--initial-branch=main")
            smoke.git(repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                      "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Fixture")
            value, _ = smoke.http(runtime.core + "/v1/projects", {"name": name, "repository_path": str(repository)})
            projects.append(value["project_id"])
        clients = {label: smoke.MCP(url, label, report) for label, url in runtime.adapters.items()}
        gpt = clients["GPT"]
        catalog = gpt.rpc("tools/list", {})
        report["tools"] = catalog
        tools = {tool["name"]: tool for tool in catalog["tools"]}
        wake_meta = tools["dev_bridge_review_wakeup_claim"]["_meta"]
        smoke.require(wake_meta["ui"]["visibility"] == ["app"] and "resourceUri" not in wake_meta["ui"],
                      "wakeup presentation reservation leaked into model catalog or emitted UI")
        read_meta = tools["feature_area_details_get"]["_meta"]
        smoke.require(read_meta["ui"]["visibility"] == ["app"], "details read must be app-only")
        smoke.require("resourceUri" not in read_meta["ui"] and "openai/outputTemplate" not in read_meta,
                      "hidden read has a template")
        show_meta = tools["feature_area_details_show"]["_meta"]
        uri = show_meta["ui"]["resourceUri"]
        smoke.require("model" in show_meta["ui"]["visibility"], "details show is hidden")
        resources = gpt.rpc("resources/list", {})
        report["resources"] = resources
        smoke.require(sum(r["uri"] == uri for r in resources["resources"]) == 1, "duplicate details resource")
        report["detailsResource"] = gpt.rpc("resources/read", {"uri": uri})
        pre = gpt.call("development_context_card_show", {})
        smoke.require(pre["selectionRequired"], "pre-session Card auto-selected Project")
        sessions = {}
        for label, client in clients.items():
            inventory = client.call("project_list", {})
            smoke.require(len(inventory["projects"]) == 2, "inventory omitted a Project")
            opened = client.call("project_open", {"projectId": projects[0]})
            sid = opened["aiSessionId"]
            sessions[label] = sid
            replay = client.call("project_open", {"aiSessionId": sid, "projectId": projects[0]})
            smoke.require(replay["aiSessionId"] == sid, "same Project replay created another session")
            client.call("project_open", {"aiSessionId": sid, "projectId": projects[1]},
                        expected_error="AI_SESSION_PROJECT_ALREADY_BOUND")
            workspace = gpt.call("dev_bridge_card_get", {"aiSessionId": sid})
            smoke.require(workspace["developmentContext"]["project"]["id"] == projects[0], "Refresh changed binding")
            client.call("development_branch_list", {"aiSessionId": sid})
        # A separate adapter must enforce the same durable session authority.
        clients["Local"].call("project_open", {"aiSessionId": sessions["GPT"], "projectId": projects[1]},
                              expected_error="AI_SESSION_PROJECT_ALREADY_BOUND")
        area = gpt.call("featureArea_create", {"aiSessionId": sessions["GPT"],
                        "title": "Details fixture", "problemStatement": "Read-only Details acceptance"})
        details_args = {"aiSessionId": sessions["GPT"], "featureAreaId": area["id"]}
        # GPT-like alias calls must share a single presentation window.
        first = gpt.rpc("tools/call", {"name": "development_context_card_show", "arguments": {"aiSessionId": sessions["GPT"]}})
        smoke.require(first.get("_meta", {}).get("emission") == "MAIN", "first show did not emit MAIN")
        for alias in ("feature_area_card_show", "development_context_card_show"):
            args = details_args if alias == "feature_area_card_show" else {"aiSessionId": sessions["GPT"]}
            duplicate = gpt.rpc("tools/call", {"name": alias, "arguments": args})
            smoke.require(duplicate["structuredContent"]["status"] == "ALREADY_SHOWN" and duplicate["_meta"]["emission"] == "SILENT", "duplicate MAIN emission")
            smoke.require(not duplicate["_meta"].get("openai/outputTemplate"), "duplicate template")
        retry_args = {**details_args, "presentationRequestId": "explicit-unavailable-retry"}
        retry = gpt.rpc("tools/call", {"name": "feature_area_card_show", "arguments": retry_args})
        smoke.require(retry["_meta"]["emission"] == "MAIN", "explicit Retry unavailable")
        duplicate = gpt.rpc("tools/call", {"name": "feature_area_card_show", "arguments": retry_args})
        smoke.require(duplicate["_meta"]["emission"] == "SILENT", "Retry spam")
        report["mainResource"] = gpt.rpc("resources/read", {"uri": first["_meta"]["openai/outputTemplate"]})
        candidate_card = (Path(__file__).resolve().parents[1] / "internal/mcpserver/dev_bridge_ui.html").read_text()
        smoke.require(report["mainResource"]["contents"][0]["text"] == candidate_card,
                      "resources/read did not serve the current candidate Card")
        smoke.require("ui/update-model-context" in candidate_card and "WAKEUP_SENT" in candidate_card,
                      "candidate Card lacks mounted Review wakeup transport")
        smoke.require("not approval" in tools["review_get"]["description"], "Review summary guidance missing")
        report["presentationLease"] = {"first": first["_meta"]["emission"], "duplicate": duplicate["_meta"]["emission"], "retry": retry["_meta"]["emission"]}
        details = gpt.call("feature_area_details_get", details_args)
        shown = gpt.call("feature_area_details_show", details_args)
        smoke.require(shown["schemaVersion"] == details["schemaVersion"], "show/read model mismatch")
        after = gpt.call("feature_area_details_get", details_args)
        smoke.require(after == details, "Details show changed durable data")
        navigated = gpt.call("dev_bridge_card_get", details_args)
        smoke.require(navigated["developmentContext"]["project"]["id"] == projects[0],
                      "Feature navigation changed binding")
        runtime.stop()
        runtime.start()
        for label, url in runtime.adapters.items():
            client = smoke.MCP(url, label, report)
            sid = sessions[label]
            reopened = client.call("project_open", {"aiSessionId": sid, "projectId": projects[0]})
            smoke.require(reopened["aiSessionId"] == sid and reopened["project"]["id"] == projects[0],
                          "restart lost binding")
            client.call("project_open", {"aiSessionId": sid, "projectId": projects[1]},
                        expected_error="AI_SESSION_PROJECT_ALREADY_BOUND")
        report["status"] = "PASSED"
    finally:
        runtime.stop()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    report = {"status": "FAILED", "calls": [], "sessions": [], "processes": []}
    try:
        with tempfile.TemporaryDirectory(prefix="devbridge-card-details-smoke-") as directory:
            run(args.bin_dir, Path(directory).resolve(), report)
    except Exception as error:
        report["error"] = f"{type(error).__name__}: {error}"
    if args.report:
        args.report.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")
    print(json.dumps({key: report[key] for key in ("status", "error") if key in report}))
    return 0 if report["status"] == "PASSED" else 1


if __name__ == "__main__":
    raise SystemExit(main())
