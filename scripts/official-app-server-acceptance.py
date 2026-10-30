#!/usr/bin/env python3
"""Replay official App Server interactions against a built, isolated Core.

No installed model turns, live database, fixed port, or production repository.
The fixture implements only the methods needed by this deterministic acceptance.
"""
import argparse,json,os,pathlib,re,selectors,shlex,sqlite3,subprocess,sys,threading,time,urllib.request,uuid

def fixture():
    thread="fixture-"+uuid.uuid4().hex;turn="turn-"+uuid.uuid4().hex;scenario="command";finished=False
    def send(v): print(json.dumps(v),flush=True)
    def event(method,params):send({"method":method,"params":params})
    def finish(status):
        nonlocal finished
        if finished:return
        finished=True
        event("serverRequest/resolved",{"threadId":thread,"requestId":"request-1"})
        event("item/completed",{"threadId":thread,"turnId":turn,"item":{"id":"cmd","type":"commandExecution","command":"printf fixture","cwd":"/tmp","status":"completed","commandActions":[],"exitCode":0,"durationMs":1,"aggregatedOutput":"fixture"}})
        result={"version":1,"summary":"Fixture review complete","workSpec":{"goal":"Fixture","constraints":[],"codexInstruction":"Inspect fixture","executionScope":{"kind":"REPOSITORY","paths":[]}},"verificationSpec":{"mode":"RUN","scope":"Fixture","checks":[{"kind":"CUSTOM","instruction":"Check fixture","required":True}],"acceptanceCriteria":["Fixture complete"],"codexInstruction":"Verify fixture"}}
        event("turn/completed",{"threadId":thread,"turn":{"id":turn,"status":status,"items":[{"id":"final","type":"agentMessage","phase":"final_answer","text":json.dumps(result)}]}})
    for line in sys.stdin:
        v=json.loads(line);method=v.get("method");rid=v.get("id");params=v.get("params",{})
        if rid=="request-1":
            if "error" in v:sys.exit(6)
            finish("interrupted" if v.get("result",{}).get("decision")=="cancel" else "completed");continue
        if rid is None:continue
        result={}
        if method=="initialize":result={"userAgent":"deterministic official-event fixture"}
        elif method in ("thread/start","thread/resume"):
            thread=params.get("threadId",thread);result={"thread":{"id":thread}}
        elif method=="turn/start":
            text=" ".join(x.get("text","") for x in params.get("input",[]));m=re.search(r"SCENARIO:(\w+)",text);scenario=m.group(1) if m else "command"
            event("turn/started",{"threadId":thread,"turn":{"id":turn,"status":"inProgress","items":[]}})
            event("turn/plan/updated",{"threadId":thread,"turnId":turn,"plan":[{"step":x,"status":"completed" if i==0 else "inProgress" if i==1 else "pending"} for i,x in enumerate(["Inspect","Implement","Validate","Review","Finish"])]})
            event("item/started",{"threadId":thread,"turnId":turn,"item":{"id":"cmd","type":"commandExecution","command":"printf fixture","cwd":"/tmp","status":"inProgress","commandActions":[]}})
            event("item/commandExecution/outputDelta",{"threadId":thread,"turnId":turn,"itemId":"cmd","delta":"safe <fixture> output"})
            p={"threadId":thread,"turnId":turn,"itemId":"cmd","cwd":"/tmp","startedAtMs":int(time.time()*1000)};request="item/commandExecution/requestApproval"
            if scenario=="command":p["networkApprovalContext"]={"host":"api.example.com","protocol":"https"}
            if scenario=="file":request="item/fileChange/requestApproval"
            if scenario=="permissions":request="item/permissions/requestApproval";p["permissions"]={"fileSystem":{"read":["/tmp/a","/tmp/b"]},"network":{"enabled":True}}
            if scenario=="input":request="item/tool/requestUserInput";p.update(isBlocking=True,questions=[{"id":"q","header":"Choice","question":"Proceed?","isOther":True}])
            if scenario=="elicitation":request="mcpServer/elicitation/request";p.update(serverName="fixture",mode="form",message="Name",requestedSchema={"type":"object","properties":{"name":{"type":"string"}},"required":["name"]})
            send({"id":"request-1","method":request,"params":p})
            result={"turn":{"id":turn,"status":"inProgress","items":[]}}
            if scenario=="loss":
                def lose():
                    marker=pathlib.Path(os.environ["DEVBRIDGE_AGENT_ACCEPTANCE_ROOT"])/thread
                    while not marker.exists():time.sleep(.02)
                    os._exit(0)
                threading.Thread(target=lose,daemon=True).start()
        elif method=="turn/interrupt":send({"id":rid,"result":{}});finish("interrupted");continue
        send({"id":rid,"result":result})

def run(binary,root):
    root.mkdir(parents=True,exist_ok=True);repo=root/"repository";repo.mkdir()
    subprocess.run(["git","init","-b","main",str(repo)],check=True,stdout=subprocess.DEVNULL)
    (repo/"fixture.txt").write_text("disposable fixture\n")
    (repo/"go.mod").write_text("module fixture\n\ngo 1.25\n")
    (repo/"main.go").write_text("package main\nfunc main() {}\n")
    subprocess.run(["git","-C",str(repo),"add","fixture.txt","go.mod","main.go"],check=True)
    subprocess.run(["git","-C",str(repo),"-c","user.name=Fixture","-c","user.email=fixture@example.invalid","commit","-m","Fixture"],check=True,stdout=subprocess.DEVNULL)
    stub=root/"codex-fixture";stub.write_text("#!/bin/sh\nexec "+shlex.quote(sys.executable)+" "+shlex.quote(str(pathlib.Path(__file__).resolve()))+" --fixture \"$@\"\n");stub.chmod(0o700)
    selection=root/"codex-selection.json";selection.write_text(json.dumps({"configured":str(stub),"executable":str(stub)}))
    env=dict(os.environ);env.update(DEVBRIDGE_CODEX_SELECTION_FILE=str(selection),DEVBRIDGE_AGENT_ACCEPTANCE_ROOT=str(root))
    log=(root/"core-stderr.log").open("w")
    core=subprocess.Popen([str(binary),"-mode","server","-data-root",str(root/"data"),"-listen","127.0.0.1:0","-admin-test","-allowed-models","gpt-5.6-luna","-allowed-reasoning-efforts","low"],stdout=subprocess.PIPE,stderr=log,text=True,env=env)
    report={};session="";serial=0
    try:
        selector=selectors.DefaultSelector();selector.register(core.stdout,selectors.EVENT_READ);deadline=time.monotonic()+30;base=None
        while time.monotonic()<deadline:
            if selector.select(1):
                line=core.stdout.readline();m=re.search(r"DEVBRIDGE_READY (http://\S+)",line)
                if m:base=m.group(1);break
            if core.poll() is not None:raise RuntimeError("Core failed: "+(root/"core-stderr.log").read_text()[-2000:])
        if not base:raise RuntimeError("Core readiness timeout")
        (root/"url.txt").write_text(base)
        def http(path,body=None,headers=None):
            req=urllib.request.Request(base+path,data=json.dumps(body).encode() if body is not None else None,headers={"Content-Type":"application/json",**(headers or {})})
            try:
                with urllib.request.urlopen(req,timeout=20) as response:return response.read().decode(),response.headers
            except urllib.error.HTTPError as e:raise RuntimeError(str(e)+": "+e.read().decode()) from e
        def rpc(method,params):
            nonlocal serial,session
            serial+=1;headers={"Accept":"application/json, text/event-stream","MCP-Protocol-Version":"2025-03-26"}
            if session:headers["Mcp-Session-Id"]=session
            text,h=http("/_adapters/gpt/mcp",{"jsonrpc":"2.0","id":serial,"method":method,"params":params},headers);session=h.get("Mcp-Session-Id",session)
            if text.startswith("event:") or text.startswith("data:"):text=next(x[5:].strip() for x in text.splitlines() if x.startswith("data:"))
            value=json.loads(text)
            if "error" in value:raise RuntimeError(value)
            return value["result"]
        def call(name,args):
            value=rpc("tools/call",{"name":name,"arguments":args})
            if value.get("isError"):raise RuntimeError(value)
            return value.get("structuredContent") or json.loads(value["content"][0]["text"])
        provision=json.loads(http("/admin/test/v1/fixture",{"source_repository":str(repo),"codex_path":str(stub)})[0]);sid=provision["ai_session_id"]
        rpc("initialize",{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"candidate-acceptance","version":"1"}})
        sid=call("project_open",{"projectId":provision["project_id"]})["aiSessionId"]
        call("development_branch_select",{"aiSessionId":sid,"repositoryId":provision["repository_id"],"developmentRef":"refs/heads/main","requestId":uuid.uuid4().hex})
        for scenario in ["command","file","permissions","input","elicitation","cancel","retire","loss"]:
            area=json.loads(http("/v1/feature-areas",{"aiSessionId":sid,"title":"Official "+scenario,"problemStatement":"Isolated official protocol acceptance"})[0]);aid=area.get("ID",area.get("id"))
            review=call("featureArea_review_start",{"aiSessionId":sid,"featureAreaId":aid,"repositoryId":provision["repository_id"],"requestId":uuid.uuid4().hex,"reviewInstruction":"SCENARIO:"+scenario+" Observe only the fixture repository.","reviewPlan":{"mode":"REPOSITORY"},"model":"gpt-5.6-luna","reasoningEffort":"low"})
            (root/(scenario+"-start.json")).write_text(json.dumps(review,indent=2))
            deadline=time.monotonic()+20;state=None;run_id=None
            while time.monotonic()<deadline:
                values=json.loads(http("/v1/runs?project_id="+provision["project_id"])[0])["items"]
                matches=[x for x in values if x.get("feature_area_id")==aid]
                if matches:
                    run_id=matches[0]["id"];state=json.loads(http(f"/v1/runs/{run_id}/agent-runtime?ai_session_id={sid}")[0])
                    if state.get("requests"):break
                time.sleep(.02)
            if not state or not state.get("requests"):raise RuntimeError("No pending request: "+str(matches if 'matches' in locals() else review))
            assert state["latestPlan"]["total"]==5 and state["currentItem"]["outputBytes"]>0
            assert call("agent_runtime_state",{"aiSessionId":sid,"runId":run_id})["runId"]==run_id
            card=call("feature_area_presentation_get",{"aiSessionId":sid,"featureAreaId":aid})
            assert card["agentRuntime"]["planTotal"]==5 and card["agentRuntime"]["runId"]==run_id
            (root/(scenario+"-card.json")).write_text(json.dumps(card,indent=2))
            if scenario=="command" and os.environ.get("DEVBRIDGE_ACCEPTANCE_UI_HOLD"):
                (root/"ui-ready.json").write_text(json.dumps({"url":base,"taskId":state["taskId"],"runId":run_id}))
                wait_until=time.monotonic()+180
                while not (root/"ui-continue").exists() and time.monotonic()<wait_until:time.sleep(.1)
            assert matches[0]["status"]=="RUNNING"
            workspace=json.loads(http("/ui/api/project-workspace?project_id="+provision["project_id"])[0]);(root/(scenario+"-web.json")).write_text(json.dumps(workspace,indent=2))
            assert 'latestPlan' in json.dumps(workspace)
            request=state["requests"][0]
            answered_in_ui=scenario=="command" and os.environ.get("DEVBRIDGE_ACCEPTANCE_UI_HOLD") and json.loads(http(f"/v1/runs/{run_id}/agent-runtime?ai_session_id={sid}")[0])["requests"][0]["state"]!="PENDING"
            if scenario=="loss":(root/state["threadId"]).touch()
            elif scenario=="retire":call("featureArea_retire",{"aiSessionId":sid,"featureAreaId":aid,"requestId":uuid.uuid4().hex})
            elif not answered_in_ui:
                response={"decision":"cancel" if scenario=="cancel" else "decline"}
                if scenario=="permissions":response={"permissions":{},"scope":"turn"}
                if scenario=="input":response={"answers":{"q":{"answers":["fixture answer"]}}}
                if scenario=="elicitation":response={"action":"accept","content":{"name":"Ada"}}
                identity={k:request.get(k,"") for k in ["projectId","featureAreaId","taskId","runId","threadId","turnId","connectionId","itemId","requestId"]}
                if scenario=="command":http(f"/ui/api/runs/{run_id}/agent-requests/resolve",{**identity,"response":response})
                else:call("codex_request_resolve",{"aiSessionId":sid,"resolution":{**identity,"response":response}})
            deadline=time.monotonic()+25
            while time.monotonic()<deadline:
                observed=json.loads(http(f"/v1/runs/{run_id}/observation?ai_session_id={sid}")[0])
                if observed["status"] in ["SUCCEEDED","FAILED","INFRASTRUCTURE_FAILED","CANCELED"]:break
                time.sleep(.03)
            after=json.loads(http(f"/v1/runs/{run_id}/agent-runtime?ai_session_id={sid}")[0]);report[scenario]={"runStatus":observed["status"],"waitingState":after["waitingState"],"requestState":after["requests"][0]["state"],"turnStatus":after.get("turnStatus"),"sequence":after["sequence"]}
            (root/(scenario+"-final.json")).write_text(json.dumps({"observation":observed,"agentRuntime":after},indent=2))
            (root/"acceptance.json").write_text(json.dumps(report,indent=2)+"\n")
            if scenario=="loss":
                assert after["requests"][0]["state"] in ["CONNECTION_LOST","ABANDONED_RUNTIME"] and not after["requests"][0].get("resolution")
                with sqlite3.connect("file:"+str(root/"data/state/devbridge.db")+"?mode=ro",uri=True) as db:
                    boundary=db.execute("SELECT advance_state,quiesced FROM agent_operation_boundaries WHERE run_id=?",(run_id,)).fetchone()
                    assert boundary==("UNKNOWN",0)
                    report[scenario]["boundary"]={"advance":"UNKNOWN","quiesced":False}
            elif scenario in ["cancel","retire"]:assert observed["status"]=="CANCELED" and after["waitingState"]=="RUNNING"
            else:assert observed["status"]=="SUCCEEDED" and after["requests"][0]["state"]=="CONFIRMED_RESOLVED"
        (root/"acceptance.json").write_text(json.dumps(report,indent=2)+"\n");print(json.dumps(report))
    finally:
        core.terminate()
        try:core.wait(timeout=15)
        except subprocess.TimeoutExpired:core.kill();core.wait()
        log.close()

if __name__=="__main__":
    if "--fixture" in sys.argv:fixture()
    else:
        parser=argparse.ArgumentParser();parser.add_argument("--core",type=pathlib.Path,required=True);parser.add_argument("--output",type=pathlib.Path,required=True);args=parser.parse_args();run(args.core.resolve(),args.output.resolve())
