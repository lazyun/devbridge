#!/usr/bin/env python3
"""Offline real-App-Server reconnect probe. Never uses a remote model/provider.

Only the Responses provider is a fixture; Thread/Turn/request processing is the
installed executable. Each invocation owns its temporary home, socket and child.
Wire logs contain only synthetic prompts and explicit fixture answers.
"""
import argparse
import base64
import hashlib
import http.server
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import threading
import time
import uuid


def encoded(value):
    return json.dumps(value, separators=(',', ':'), ensure_ascii=False).encode()


class Wire:
    def __init__(self, path, log, name):
        self.name, self.log = name, log
        self.s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.s.settimeout(12)
        self.s.connect(str(path))
        key = base64.b64encode(os.urandom(16)).decode()
        self.s.sendall(('GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: '+key+'\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
        head = b''
        while not head.endswith(b'\r\n\r\n'):
            head += self.s.recv(1)
        assert b' 101 ' in head, head
        self.seq = 0
        self.seen = []
        self.call('initialize', {'clientInfo': {'name':'devbridge_offline_continuity','version':'1'}, 'capabilities':{'experimentalApi':True}})
        self.send({'method':'initialized'})

    def frame(self, data, opcode=1):
        mask=os.urandom(4); size=len(data)
        prefix=bytes([0x80|opcode,0x80|(size if size<126 else 126 if size<65536 else 127)])
        if size>=126: prefix += struct.pack('!H' if size<65536 else '!Q',size)
        self.s.sendall(prefix+mask+bytes(v^mask[i%4] for i,v in enumerate(data)))

    def send(self, value):
        self.log(self.name,'send',value)
        self.frame(encoded(value))

    def exact(self,n):
        result=b''
        while len(result)<n:
            part=self.s.recv(n-len(result))
            if not part: raise EOFError('websocket closed')
            result+=part
        return result

    def recv(self):
        body=b''
        while True:
            a,b=self.exact(2);size=b&127
            if size==126:size=struct.unpack('!H',self.exact(2))[0]
            if size==127:size=struct.unpack('!Q',self.exact(8))[0]
            assert size<32*1024*1024
            mask=self.exact(4) if b&128 else None
            payload=self.exact(size)
            if mask:payload=bytes(v^mask[i%4] for i,v in enumerate(payload))
            opcode=a&15
            if opcode==8:raise EOFError('websocket close frame')
            if opcode==9:self.frame(payload,10);continue
            if opcode==10:continue
            body+=payload
            if a&128:break
        value=json.loads(body);self.seen.append(value);self.log(self.name,'recv',value);return value

    def until(self,predicate):
        deadline=time.monotonic()+15
        while time.monotonic()<deadline:
            v=self.recv()
            if predicate(v):return v
        raise TimeoutError('bounded wire observation expired')

    def call(self,method,params):
        self.seq+=1;i=f'{self.name}-{self.seq}'
        self.send({'id':i,'method':method,'params':params})
        response=self.until(lambda v:v.get('id')==i and 'method' not in v)
        if 'error' in response:raise RuntimeError(response)
        return response['result']

    def close(self):
        self.s.close()


def run(executable, output, scenario):
    output.mkdir(parents=True,exist_ok=False)
    home=output/'home';home.mkdir();workspace=output/'workspace';workspace.mkdir();temporary=output/'tmp';temporary.mkdir()
    env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':str(home),'CODEX_HOME':str(home),'TMPDIR':str(temporary),'LANG':'en_US.UTF-8'}
    version=subprocess.check_output([str(executable),'--version'],env=env,timeout=10).decode().strip()
    executable_hash=hashlib.sha256(executable.read_bytes()).hexdigest()
    schema=output/'schema'
    subprocess.run([str(executable),'app-server','generate-json-schema','--experimental','--out',str(schema)],env=env,check=True,stdout=subprocess.DEVNULL,timeout=20)
    schema_hash=hashlib.sha256()
    for path in sorted(schema.rglob('*.json')):
        schema_hash.update(str(path.relative_to(schema)).encode()+b'\0')
        schema_hash.update(json.dumps(json.loads(path.read_text()),sort_keys=True,separators=(',', ':'),ensure_ascii=False).encode())
        schema_hash.update(b'\0')
    instance_id=str(uuid.uuid4())
    wire_file=(output/'wire.jsonl').open('w');lock=threading.Lock()
    provider_started=threading.Event();provider_release=threading.Event()
    def log(client,direction,value):
        with lock:
            wire_file.write(json.dumps({'at':time.time(),'client':client,'direction':direction,'value':value},ensure_ascii=False)+'\n');wire_file.flush()
    class Provider(http.server.BaseHTTPRequestHandler):
        count=0
        def log_message(self,*_):pass
        def do_POST(self):
            data=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            log('provider','request',data)
            Provider.count+=1;n=Provider.count
            events=[{'type':'response.created','response':{'id':f'resp-{n}'}}]
            if scenario not in ('inflight','modern') and n <= (2 if scenario == 'multi' else 1):
                args={'questions':[{'id':'choice','header':'Fixture decision','question':'Choose fixture route?','options':[{'label':'yes','description':'Explicit fixture option'},{'label':'no','description':'Other fixture option'}]}]}
                item={'type':'function_call','call_id':'fixture-question','name':'request_user_input','arguments':encoded(args).decode()}
            else:
                item={'id':'fixture-message','type':'message','role':'assistant','content':[{'type':'output_text','text':'fixture complete'}]}
            events += [{'type':'response.output_item.done','item':item},{'type':'response.completed','response':{'id':f'resp-{n}','usage':{'input_tokens':1,'output_tokens':1,'total_tokens':2}}}]
            body=b''.join(b'event: '+v['type'].encode()+b'\ndata: '+encoded(v)+b'\n\n' for v in events)
            self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Content-Length',str(len(body)));self.end_headers()
            if scenario=='inflight' or scenario=='modern' and n==1:
                first=b'event: '+events[0]['type'].encode()+b'\ndata: '+encoded(events[0])+b'\n\n'
                self.wfile.write(first);self.wfile.flush();provider_started.set()
                if not provider_release.wait(15):raise TimeoutError('inflight provider was not released')
                self.wfile.write(body[len(first):])
            else:self.wfile.write(body)
    provider=http.server.ThreadingHTTPServer(('127.0.0.1',0),Provider)
    threading.Thread(target=provider.serve_forever,daemon=True).start()
    (home/'config.toml').write_text(f'''model="mock-model"
model_provider="fixture"
approval_policy="never"
sandbox_mode="read-only"
[model_providers.fixture]
name="Offline fixture"
base_url="http://127.0.0.1:{provider.server_port}/v1"
wire_api="responses"
request_max_retries=0
stream_max_retries=0
[analytics]
enabled=false
[feedback]
enabled=false
''')
    socket_directory=tempfile.TemporaryDirectory(prefix='dbc-',dir='/tmp')
    endpoint=Path(socket_directory.name)/'app.sock'
    assert len(str(endpoint).encode())<100
    stderr=(output/'app.stderr').open('w');stdout=(output/'app.stdout').open('w')
    process=subprocess.Popen([str(executable),'app-server','--listen','unix://'+str(endpoint)],env=env,cwd=workspace,stdout=stdout,stderr=stderr)
    facts={'instanceId':instance_id,'instanceEpoch':1,'protocolSchemaSHA256':schema_hash.hexdigest(),'scenario':scenario,'pid':process.pid,'launchedAt':time.time(),'executable':str(executable),'version':version,'sha256':executable_hash,'endpoint':str(endpoint)}
    a=b=None
    try:
        for _ in range(100):
            if endpoint.exists():break
            if process.poll() is not None:raise RuntimeError('App Server exited: '+(output/'app.stderr').read_text())
            time.sleep(.05)
        st=endpoint.stat();facts['socketIdentity']={'dev':st.st_dev,'ino':st.st_ino}
        facts['processStart']=subprocess.check_output(['ps','-p',str(process.pid),'-o','lstart=']).decode().strip()
        a=Wire(endpoint,log,'A')
        thread=a.call('thread/start',{'model':'mock-model','modelProvider':'fixture','cwd':str(workspace),'approvalPolicy':'never','sandbox':'read-only'})['thread'];tid=thread['id'];facts['threadId']=tid
        turn=a.call('turn/start',{'threadId':tid,'input':[{'type':'text','text':'Ask the synthetic fixture question before continuing.','text_elements':[]}],'collaborationMode':{'mode':'plan','settings':{'model':'mock-model','reasoning_effort':'medium','developer_instructions':None}}})['turn'];facts['turnId']=turn['id']
        q=None if scenario in ('inflight','modern') else a.until(lambda v:v.get('method')=='item/tool/requestUserInput');facts['request']=q
        if scenario == 'modern':
            assert provider_started.wait(10), 'fixture response never started'
            facts['metadata']=a.call('thread/read',{'threadId':tid,'includeTurns':False})
            assert not facts['metadata']['thread'].get('turns'), 'metadata hydrated history'
            assert facts['metadata']['thread']['status']['type']=='active'
            facts['activePage']=a.call('thread/turns/list',{'threadId':tid,'itemsView':'full','sortDirection':'desc','limit':1})
            assert facts['activePage']['data'][0]['id']==turn['id']
            assert facts['activePage']['data'][0]['status']=='inProgress'
            a.close();a=None
            b=Wire(endpoint,log,'B')
            facts['resume']=b.call('thread/resume',{'threadId':tid,'excludeTurns':True})
            assert facts['resume']['thread']['id']==tid and not facts['resume']['thread'].get('turns')
            try:
                b.call('turn/steer',{'threadId':tid,'expectedTurnId':'foreign-turn','input':[{'type':'text','text':'must reject'}]})
                raise AssertionError('foreign turn steering accepted')
            except RuntimeError as error:
                facts['foreignTurnRejected']=True
            facts['steering']=b.call('turn/steer',{'threadId':tid,'expectedTurnId':turn['id'],'input':[{'type':'text','text':'synthetic-steering-marker'}]})
            assert facts['steering']['turnId']==turn['id']
            provider_release.set()
            terminal=lambda v:v.get('method')=='turn/completed' and v['params']['threadId']==tid
            facts['terminal']=next((v for v in b.seen if terminal(v)),None) or b.until(terminal)
            assert facts['terminal']['params']['turn']['id']==turn['id']
            facts['terminalPage']=b.call('thread/turns/list',{'threadId':tid,'itemsView':'full','sortDirection':'desc','limit':1})
            observed=facts['terminalPage']['data'][0]
            assert observed['id']==turn['id'] and observed['status']=='completed'
            facts['items']=b.call('thread/items/list',{'threadId':tid,'turnId':turn['id'],'limit':1,'sortDirection':'asc'})
            assert len(facts['items']['data'])==1 and facts['items']['data'][0]['turnId']==turn['id']
            facts['statusNotifications']=[v for v in b.seen if v.get('method')=='thread/status/changed']
            assert facts['statusNotifications'], 'native status notifications absent'
            facts['processStartAfter']=subprocess.check_output(['ps','-p',str(process.pid),'-o','lstart=']).decode().strip()
            # Only the synthetic loopback provider was used. The accepted user
            # message must appear in this same native turn's persisted history.
            assert any(item.get('type')=='userMessage' and any(part.get('text')=='synthetic-steering-marker' for part in item.get('content',[])) for item in observed['items']), 'accepted steering missing from native turn'
        elif scenario == 'inflight':
            assert provider_started.wait(10),'provider did not start the original response'
            facts['beforeDisconnect']=a.call('thread/read',{'threadId':tid,'includeTurns':True})
            assert facts['beforeDisconnect']['thread']['status']['type']=='active'
            assert facts['beforeDisconnect']['thread']['turns'][-1]['id']==turn['id']
            assert facts['beforeDisconnect']['thread']['turns'][-1]['status']=='inProgress'
            a.close();a=None;facts['disconnectedAt']=time.time()
            b=Wire(endpoint,log,'B');facts['processStartAfter']=subprocess.check_output(['ps','-p',str(process.pid),'-o','lstart=']).decode().strip()
            facts['loaded']=b.call('thread/loaded/list',{});assert tid in facts['loaded']['data']
            facts['readBeforeResume']=b.call('thread/read',{'threadId':tid,'includeTurns':True})
            facts['resume']=b.call('thread/resume',{'threadId':tid})
            for key in ['readBeforeResume','resume']:
                observed=facts[key]['thread']
                assert observed['id']==tid and observed['status']['type']=='active'
                assert not observed['status'].get('activeFlags'), 'inflight model unexpectedly waits on a client decision'
                assert observed['turns'][-1]['id']==turn['id'] and observed['turns'][-1]['status']=='inProgress'
            provider_release.set()
            facts['terminal']=b.until(lambda v:v.get('method')=='turn/completed' and v['params']['threadId']==tid)
            assert facts['terminal']['params']['turn']['id']==turn['id']
            facts['readAfter']=b.call('thread/read',{'threadId':tid,'includeTurns':True})
            assert facts['readAfter']['thread']['turns'][-1]['id']==turn['id']
            assert facts['readAfter']['thread']['turns'][-1]['status']=='completed'
            assert not any(v.get('method')=='item/tool/requestUserInput' for v in b.seen)
            assert Provider.count==1, 'reconnect started an extra provider response'
        elif scenario == 'multi':
            thread2=a.call('thread/start',{'model':'mock-model','modelProvider':'fixture','cwd':str(workspace),'approvalPolicy':'never','sandbox':'read-only'})['thread']
            tid2=thread2['id']
            turn2=a.call('turn/start',{'threadId':tid2,'input':[{'type':'text','text':'Ask the synthetic fixture question before continuing.','text_elements':[]}],'collaborationMode':{'mode':'plan','settings':{'model':'mock-model','reasoning_effort':'medium','developer_instructions':None}}})['turn']
            q_other=a.until(lambda v:v.get('method')=='item/tool/requestUserInput' and v['params']['threadId']==tid2)
            assert q['params']['questions']==q_other['params']['questions'], 'fixture questions differ'
            assert tid!=tid2 and turn['id']!=turn2['id'] and q['id']!=q_other['id'], 'requests do not have distinct ownership'
            facts['secondRequest']=q_other;facts['secondThreadId']=tid2;facts['secondTurnId']=turn2['id']
            a.close();a=None
            b=Wire(endpoint,log,'B');facts['processStartAfter']=subprocess.check_output(['ps','-p',str(process.pid),'-o','lstart=']).decode().strip()
            facts['loaded']=b.call('thread/loaded/list',{})
            assert tid in facts['loaded']['data'] and tid2 in facts['loaded']['data']
            replay=[]
            for original in [q,q_other]:
                thread_id=original['params']['threadId']
                observed=b.call('thread/read',{'threadId':thread_id,'includeTurns':True})
                assert observed['thread']['status']['type']=='active'
                marker=len(b.seen)
                b.call('thread/resume',{'threadId':thread_id})
                matches=[v for v in b.seen[marker:] if v.get('method')=='item/tool/requestUserInput' and v['params']['threadId']==thread_id]
                repeated=matches[0] if matches else b.until(lambda v:v.get('method')=='item/tool/requestUserInput' and v['params']['threadId']==thread_id)
                assert repeated['id']==original['id'] and repeated['params']==original['params'], 'multi-thread replay mismatch'
                replay.append(repeated)
            facts['replayedRequests']=replay
            b.send({'id':replay[0]['id'],'result':{'answers':{'choice':{'answers':['yes']}}}})
            facts['firstResolved']=b.until(lambda v:v.get('method')=='serverRequest/resolved' and v['params']['threadId']==tid)
            facts['firstTerminal']=b.until(lambda v:v.get('method')=='turn/completed' and v['params']['threadId']==tid)
            facts['secondStillPending']=b.call('thread/read',{'threadId':tid2,'includeTurns':True})
            assert facts['secondStillPending']['thread']['status']['type']=='active'
            assert 'waitingOnUserInput' in facts['secondStillPending']['thread']['status']['activeFlags']
            assert not any(v.get('method')=='serverRequest/resolved' and v['params']['threadId']==tid2 for v in b.seen), 'answer leaked to other thread'
            b.close();b=Wire(endpoint,log,'C')
            loaded=b.call('thread/loaded/list',{});assert tid2 in loaded['data']
            b.call('thread/read',{'threadId':tid2,'includeTurns':True})
            marker=len(b.seen);b.call('thread/resume',{'threadId':tid2})
            matches=[v for v in b.seen[marker:] if v.get('method')=='item/tool/requestUserInput' and v['params']['threadId']==tid2]
            again=matches[0] if matches else b.until(lambda v:v.get('method')=='item/tool/requestUserInput' and v['params']['threadId']==tid2)
            assert again['id']==q_other['id'] and again['params']==q_other['params']
            b.send({'id':again['id'],'result':{'answers':{'choice':{'answers':['no']}}}})
            facts['secondResolved']=b.until(lambda v:v.get('method')=='serverRequest/resolved' and v['params']['threadId']==tid2)
            facts['secondTerminal']=b.until(lambda v:v.get('method')=='turn/completed' and v['params']['threadId']==tid2)
            facts['firstReadAfter']=b.call('thread/read',{'threadId':tid,'includeTurns':True})
            facts['secondReadAfter']=b.call('thread/read',{'threadId':tid2,'includeTurns':True})
            assert facts['firstReadAfter']['thread']['turns'][-1]['status']=='completed'
            assert facts['secondReadAfter']['thread']['turns'][-1]['status']=='completed'
            assert facts['firstReadAfter']['thread']['id']==tid and facts['firstReadAfter']['thread']['turns'][-1]['id']==turn['id']
            assert facts['secondReadAfter']['thread']['id']==tid2 and facts['secondReadAfter']['thread']['turns'][-1]['id']==turn2['id']
            assert Provider.count==4, 'reconnect started an extra turn or provider response'
        else:
            (output/'durable-request.json').write_bytes(encoded(q))
            answer={'answers':{'choice':{'answers':['no']}}}
            if scenario!='waiting':(output/'durable-answer.json').write_bytes(encoded(answer))
            if scenario=='consumed':
                a.send({'id':q['id'],'result':answer})
                for _ in range(100):
                    if Provider.count>=2:break
                    time.sleep(.02)
                assert Provider.count>=2,'provider did not observe answer consumption'
            a.close();a=None;facts['disconnectedAt']=time.time()
            assert process.poll() is None
            b=Wire(endpoint,log,'B');facts['processStartAfter']=subprocess.check_output(['ps','-p',str(process.pid),'-o','lstart=']).decode().strip()
            facts['loaded']=b.call('thread/loaded/list',{})
            assert tid in facts['loaded']['data']
            facts['readBeforeResume']=b.call('thread/read',{'threadId':tid,'includeTurns':True})
            marker=len(b.seen)
            facts['resume']=b.call('thread/resume',{'threadId':tid})
            if scenario!='consumed':
                for key in ['readBeforeResume','resume']:
                    observed=facts[key]['thread']
                    assert observed['id']==tid and observed['status']['type']=='active'
                    assert 'waitingOnUserInput' in observed['status']['activeFlags']
                    assert observed['turns'][-1]['id']==turn['id'] and observed['turns'][-1]['status']=='inProgress'
                matches=[v for v in b.seen[marker:] if v.get('method')=='item/tool/requestUserInput' and v['params']['threadId']==tid]
                q2=matches[0] if matches else b.until(lambda v:v.get('method')=='item/tool/requestUserInput' and v['params']['threadId']==tid);facts['replayedRequest']=q2
                assert q2['id']==q['id'] and q2['params']==q['params'],'request identity changed'
                b.send({'id':q2['id'],'result':answer})
                facts['resolved']=b.until(lambda v:v.get('method')=='serverRequest/resolved')
                facts['terminal']=b.until(lambda v:v.get('method')=='turn/completed')
                assert facts['resolved']['params']['threadId']==tid
                assert facts['terminal']['params']['threadId']==tid and facts['terminal']['params']['turn']['id']==turn['id']
            else:
                time.sleep(.25)
            facts['readAfter']=b.call('thread/read',{'threadId':tid,'includeTurns':True})
            assert facts['readAfter']['thread']['turns'][-1]['id']==turn['id']
            assert facts['readAfter']['thread']['turns'][-1]['status']=='completed'
            assert Provider.count==2, 'fixture did not complete exactly one original turn'
            if scenario=='consumed':
                assert not any(v.get('method') in ['item/tool/requestUserInput','serverRequest/resolved'] for v in b.seen), 'protocol now exposes consumption proof; update the consumed outcome'
        facts['sameProcess']=process.poll() is None and facts['processStartAfter']==facts['processStart']
        facts['sameSocket']={'dev':endpoint.stat().st_dev,'ino':endpoint.stat().st_ino}==facts['socketIdentity']
        assert facts['sameProcess'] and facts['sameSocket'], 'reconnect replaced the owning process or socket'
        facts['providerRequestCount']=Provider.count
        facts['recoverability']=scenario!='consumed'
        facts['decisionDeliveryState']='NOT_APPLICABLE' if scenario in ('inflight','modern') else 'UNKNOWN' if scenario=='consumed' else 'RESPONDED'
        facts['consumedNotProvable']=scenario=='consumed'
        facts['result']='UNKNOWN' if scenario=='consumed' else 'PASS'
        facts['probeAssertionsPassed']=True
    except Exception as e:
        facts['result']='FAIL';facts['error']=str(e)
    finally:
        provider_release.set()
        for wire in [a,b]:
            if wire:wire.close()
        if process.poll() is None:
            process.terminate()
            try:process.wait(timeout=4)
            except subprocess.TimeoutExpired:process.kill();process.wait(timeout=4)
        facts['exitCode']=process.returncode
        provider.shutdown();provider.server_close();stderr.close();stdout.close();wire_file.close()
        socket_directory.cleanup()
        (output/'result.json').write_text(json.dumps(facts,ensure_ascii=False,indent=2))
    print(json.dumps({'output':str(output),'result':facts['result'],'recoverability':facts.get('recoverability',False),'error':facts.get('error')}))
    return facts.get('probeAssertionsPassed',False)

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--executable',required=True,type=Path);p.add_argument('--output',required=True,type=Path);p.add_argument('--scenario',choices=['waiting','recorded','consumed','multi','inflight','modern'],default='waiting');a=p.parse_args()
    raise SystemExit(0 if run(a.executable.resolve(),a.output.resolve(),a.scenario) else 1)
