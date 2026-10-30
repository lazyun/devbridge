"use strict";
// Card half of the shared rejected-retry fixture. Every envelope the scripted Host
// serves below is a byte-for-byte production capture: the exact structuredContent of
// confirmation_show / confirmation_options_get / confirmation_commit / confirmation_get
// over the real Streamable HTTP MCP wire, the production middleware stack and a real
// SQLite store, captured by
// internal/controlapi/confirmation_rejected_retry_lifecycle_test.go. The Go guard in
// confirmation_rejected_retry_fixture_test.go loads the SAME file, so the durable Core
// facts behind §3 and the Card-side state machine in §4/§5 are proven over one set of
// bytes that neither runtime restates by hand.
//
// Two families of bytes are re-anchored per replay, exactly the way the product
// re-anchors them:
//   * submission.request — a retry is a NEW user command (§3/§4), so the recorded
//     request has to be the one this Card actually submitted. It is echoed from the
//     Card's own request, never invented here.
//   * submission.payloadSHA256 — recomputed by the Card's own canonical requestHash()
//     over that same request.
// Every consumption, outcome, nextAction, retryDisposition, failure and identity fact
// stays as captured. A named mutation appears only in the negatives, and only ever
// weakens the §3 conjunction, which must then fail closed.
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),crypto=require("node:crypto");
const {createCardInstance,renderedText,RE_REVIEW_ACTION_LABEL}=require("./confirmation_card_instance_mount_test.js");
const file=JSON.parse(fs.readFileSync(`${__dirname}/testdata/confirmation_rejected_retry_v5.json`,"utf8"));
const clone=value=>JSON.parse(JSON.stringify(value));

const pendingEnvelope=file.pending.entry;
const rejectionEnvelope=file.rejection.entry;
const optionsEnvelope=file.pending.options.entry;
const readbackEnvelope=file.receiptReadback.entry;
const intent=pendingEnvelope.intent;
const frozen={aiSessionId:intent.aiSessionId,intentId:intent.intentId,version:intent.version,purpose:intent.purpose,contentHash:intent.contentHash};
const capturedRequest=file.rejection.facts.request;
const capturedCopy=file._meta.cardCopy;
// §9: a REPREPARE_REQUIRED rejection consumed nothing and is terminal for this
// intent, so the Card says exactly that instead of the EDIT_SELECTION copy that invites
// an immediate re-confirmation.
const TERMINAL_REPREPARE_COPY="本次确认已明确拒绝且未产生业务副作用。需要根据当前 Decision 状态重新准备新的确认。";
const LOCKED_REJECTION_COPY="原请求已明确拒绝且未消费。请修正选择后主动确认。";
// What the Card says once it has actually read the original request back and the record
// still does not prove non-consumption.
const UNPROVEN_READBACK_COPY="确认结果仍未确定；已保留原请求身份，不会重复提交。";
const ORIGINAL_RECEIPT_ACTION="读取原结果";
const EXPIRED_CHOICE_ERROR="本次待确认内容已过期，请在对话准备新版本。";
const CAPTURED_AT=Date.parse(intent.createdAt),EXPIRES_AT=Date.parse(intent.expiresAt);
assert.ok(EXPIRES_AT>CAPTURED_AT,"the captured intent must have had its own validity window");

// The Card reads the live clock for its expiry gates, so a replay of a 24-hour
// production window has to run inside that window. The clock is pinned one second after
// the capture instant; every other decision stays the Card's own.
function pinClock(instance,ms){instance.peek(`Date.now=()=>${Math.trunc(ms)}`);}

// The scripted Host behaves like Core: it answers with captured production bytes and
// records what the Card actually asked for, so "never auto-resends" is a count of
// observed calls rather than an absence of assertions.
function start(scenario={}){
 const host={commits:[],reads:[],optionReads:0,rejections:[],lastRefusal:null};
 let instance;
 const answer=async request=>{
  if(request.method==="ui/initialize")return{result:{protocolVersion:"2026-01-26",serverInfo:{name:"rejected-retry-host",version:"1"}}};
  if(request.method!=="tools/call")return{result:{}};
  const name=request.params?.name,arguments_=request.params?.arguments||{};
  // §7 from the Card side: every read and the commit itself are addressed by the exact
  // five-field identity, including the frozen content digest.
  for(const [key,want]of Object.entries(frozen))assert.equal(arguments_[key],want,`${name} did not carry the exact ${key} read identity`);
  if(name==="confirmation_options_get"){
   host.optionReads+=1;
   return{result:{structuredContent:clone(optionsEnvelope)}};
  }
  if(name==="confirmation_get"){
   host.reads.push(clone(arguments_));
   // A refresh in the unlocked scenario re-reads the same durable refusal from the
   // capture. Every other read returns this Card's own durable record: the refusal the
   // Host last gave it — which, weakened or not, is still the only evidence §3 allows —
   // or a PENDING intent with no record at all when nothing was answered.
   if(scenario.readbackRejection)return{result:{structuredContent:clone(readbackEnvelope)}};
   if(host.lastRefusal)return{result:{structuredContent:clone(host.lastRefusal),isError:file.rejection.isError===true}};
   return{result:{structuredContent:clone(pendingEnvelope)}};
  }
  if(name==="confirmation_commit"){
   host.commits.push(clone(arguments_));
   if(scenario.transportFailure)return{error:{code:-32603,message:"host transport failure"}};
   const envelope=clone(rejectionEnvelope);
   const submission=envelope.intent.submission;
   if(scenario.foreignRequest){
    // §5: a refusal recorded against another request is not this Card's receipt.
    submission.request=clone(capturedRequest);
    submission.payloadSHA256=file.rejection.facts.payloadSHA256;
   }else{
    submission.request=clone(arguments_);
    submission.payloadSHA256=await instance.peek(`requestHash(${JSON.stringify(arguments_)})`);
   }
   assert.match(submission.payloadSHA256,/^[a-f0-9]{64}$/,"a replayed refusal must carry a real payload digest");
   if(scenario.weaken)scenario.weaken(envelope,submission);
   host.rejections.push({requestId:arguments_.requestId,payloadSHA256:submission.payloadSHA256});
   // The refusal this Card must read back if its own outcome is left unproven.
   host.lastRefusal=clone(envelope);
   // The captured wire fact: Core refused, so the result is an error carrying the typed
   // failure plus the still-PENDING intent and its recorded refusal.
   return{result:{structuredContent:envelope,isError:file.rejection.isError===true}};
  }
  assert.fail(`the Card asked for a tool the fixture cannot serve: ${name}`);
 };
 instance=createCardInstance(scenario.label||"rejected-retry",answer);
 pinClock(instance,CAPTURED_AT+1000);
 return {instance,host};
}

async function bind(card){
 await card.instance.settle();
 const input={aiSessionId:frozen.aiSessionId,intentId:frozen.intentId,version:frozen.version,purpose:frozen.purpose,action:intent.action,requestId:"host-tool-input-"+frozen.intentId};
 await card.instance.pushToolInput(input);
 await card.instance.pushToolResult(pendingEnvelope);
 assert.equal(card.instance.peek("entry.intentId"),frozen.intentId,"the Card must bind its own entry");
 return card;
}

function button(card,label){
 const stack=[card.instance.peek("root")];
 while(stack.length){
  const node=stack.pop();
  if(!node||typeof node!=="object")continue;
  if(node.nodeType===1&&node.tagName==="BUTTON"&&renderedText(node).join(" ").trim()===label)return node;
  for(const child of node.childNodes||[])stack.push(child);
 }
 return null;
}

// The user command: the Card's own rendered confirmation control, wired to its own
// commit(), is what drives a submission here.
//
// An unlock/refusal flow can keep issuing Host calls after the first reply (the Card
// re-reads its own options, or reads an unproven request back), and the harness only
// answers while its reply queue is pumped. So the pump runs beside the awaited call
// instead of before it.
async function drive(card,invoke){
 let pumping=true;
 const pump=(async()=>{while(pumping){await card.instance.settle();await new Promise(resolve=>setImmediate(resolve));}})();
 try{await invoke(card);}finally{pumping=false;await pump;}
}

async function confirm(card){
 const control=button(card,RE_REVIEW_ACTION_LABEL);
 assert.ok(control,`the Card must render its own ${RE_REVIEW_ACTION_LABEL} control`);
 assert.equal(control.disabled,!1,"the confirmation control must be live");
 await drive(card,async()=>await control.onclick());
}

async function fire(card,expression){await drive(card,async()=>await card.instance.peek(expression));}

function state(card){
 const peek=name=>card.instance.peek(name);
 return {
  pendingCommit:peek("pendingCommit"),pendingHash:peek("pendingHash"),editableRetry:peek("editableRetry"),
  unknown:peek("unknown"),stale:peek("stale"),flight:peek("flight"),readback:peek("readbackInProgress"),
  confirmedOutcome:peek("confirmedOutcome"),submissionNext:peek("submissionNext"),failure:peek("lastFailure"),
  rejected:peek("lastSubmission"),receipt:peek("lastReceipt"),rejectedRequestId:peek("lastRejectedRequestId"),
  rejectedPayloadSHA256:peek("lastRejectedPayloadSHA256"),notice:peek("notice"),intentState:peek("intent.state"),
  submission:peek("intent.submission ?? null"),events:peek("events"),
 };
}

function visibleText(card){return renderedText(card.instance.peek("root"));}

// The Card keeps a bounded diagnostic ring of what it actually learned. A fail-closed
// branch that the Card later smooths over in its user-facing notice (an unproven read
// clears the outstanding failure once the original receipt has been re-read) is still
// recorded there, so the negatives assert on the ring rather than on a transient copy.
function lastEvent(card,stage){
 const events=card.instance.peek("events");
 for(let index=events.length-1;index>=0;index-=1)if(events[index].stage===stage)return events[index];
 return null;
}

// §5's fail-closed shape, asserted for every refusal the conjunction does not cover.
function assertStillHolds(card,state_,submitted){
 assert.notEqual(state_.pendingCommit,null,"a refusal that is not an editable retry must keep its held request");
 assert.equal(state_.pendingCommit.requestId,submitted.requestId,"the held request must stay the one this Card sent");
 assert.ok(state_.pendingHash,"the held payload digest must stay available to the original receipt");
 assert.equal(state_.editableRetry,!1,"only the §3 conjunction may open the retry path");
 assert.equal(button(card,RE_REVIEW_ACTION_LABEL),null,"a Card holding an unproven request must not offer confirmation again");
 assert.equal(card.host.commits.length,1,"a locked refusal may never turn into an automatic resend");
}

test("the shared rejected-retry fixture is captured from this exact embedded Card script",()=>{
 const main=fs.readFileSync(process.env.DEVBRIDGE_CONFIRMATION_UI_HTML||`${__dirname}/confirmation_ui.html`,"utf8")
  .match(/<script id="confirmation-main">([\s\S]*?)<\/script>/)[1];
 assert.equal(crypto.createHash("sha256").update(main,"utf8").digest("hex"),file._meta.mainScriptSHA256,"both runtimes must be reasoning about the same Card");
 assert.equal(file.pending.meta.mainScriptSHA256,file._meta.mainScriptSHA256);
 assert.equal(file._meta.componentVersion,"PURPOSE_CONFIRMATION_V14");
 assert.equal(file._meta.staticResourceUri,"ui://devbridge/confirmation/v14");
 assert.ok(file._meta.singleSourceOfTruth.includes("Never hand-edit an envelope"),"the fixture must keep its capture provenance");
 // §7 and §3 as captured facts: the refusal was addressed by the frozen digest, and a
 // rejection carries no consumption pointer of its own.
 assert.equal(capturedRequest.contentHash,frozen.contentHash);
 assert.equal(file.rejection.facts.receiptRequestId,"");
 assert.equal(file.rejection.facts.retryDisposition,"EDIT_SELECTION_AND_RESUBMIT");
 assert.match(file.rejection.facts.payloadSHA256,/^[a-f0-9]{64}$/);
 assert.ok(file.pending.presentationInstanceId,"the captured Card instance identity is part of the fixture");
});

test("a pending Card from the capture renders its own live confirmation with nothing held",async()=>{
 const card=await bind(start());
 const view=state(card);
 assert.equal(view.intentState,"PENDING");
 assert.equal(view.submission,null,"a never-submitted Card has no consumption facts");
 assert.equal(view.pendingCommit,null);
 assert.equal(view.editableRetry,!1,"the retry path is not open before anything was refused");
 assert.equal(view.unknown,!1);
 assert.equal(card.instance.peek("submissionState()"),"AWAITING_USER_CONFIRMATION");
 assert.equal(button(card,RE_REVIEW_ACTION_LABEL).disabled,!1);
 assert.ok(card.host.optionReads>0,"the Card must read its own legal options");
 for(const call of card.host.reads)assert.equal(call.contentHash,frozen.contentHash);
});

test("an explicit REJECTED_NOT_CONSUMED with EDIT_SELECTION releases this Card's held request (§3/§4)",async()=>{
 const card=await bind(start());
 await confirm(card);
 assert.equal(card.host.commits.length,1,"the user sent exactly one command");
 const submitted=card.host.commits[0];
 assert.match(submitted.requestId,/^confirm_/,"a Card submission allocates its own request identity");
 assert.equal(submitted.contentHash,frozen.contentHash,"the submission stays bound to the frozen content");
 for(const injected of ["consumed","consumptionDisposition","retryDisposition","manualRetryAllowed","redispatchSameIntent","reprepareAllowed","receiptRequestId","force","retryAnyway","assumeNotConsumed","sourceIntentId"])
  assert.ok(!(injected in submitted),`the Card injected the closed-DTO field ${injected} into a commit`);

 const view=state(card);
 // §4: exactly the held-request facts are released, and nothing else.
 assert.equal(view.pendingCommit,null,"the released Card must let the user submit again");
 assert.equal(view.pendingHash,"");
 assert.equal(view.editableRetry,!0,"the §3 conjunction is what opened the retry path");
 assert.equal(view.unknown,!1,"a durable refusal is a known outcome, not an unproven one");
 assert.equal(view.stale,!1,"an editable refusal does not degrade into a stale Card");
 assert.equal(view.flight,!1);
 assert.equal(view.readback,!1);
 assert.equal(view.confirmedOutcome,"REJECTED_NOT_CONSUMED");
 assert.equal(view.submissionNext,"EDIT_SELECTION");
 assert.equal(view.intentState,"PENDING");
 assert.equal(view.submission.retryDisposition,"EDIT_SELECTION_AND_RESUBMIT");
 assert.equal(view.submission.redispatchSameIntent,!1,"the same intent may never be re-dispatched automatically");
 // §4's preserved history.
 assert.equal(view.rejectedRequestId,submitted.requestId,"the rejected request stays visible as history");
 assert.equal(view.rejectedPayloadSHA256,card.host.rejections[0].payloadSHA256);
 assert.equal(view.rejected.requestId,submitted.requestId);
 assert.equal(view.rejected.outcome,"REJECTED_NOT_CONSUMED");
 assert.equal(view.rejected.consumed,!1);
 assert.equal(view.rejected.retryDisposition,"EDIT_SELECTION_AND_RESUBMIT");
 assert.equal(view.receipt.requestId,submitted.requestId);
 assert.equal(view.receipt.payloadSHA256,card.host.rejections[0].payloadSHA256);
 assert.equal(view.receipt.receiptRequestId,"");
 assert.equal(view.failure.code,file.rejection.failureCode);
 assert.equal(view.failure.nextAction,"EDIT_SELECTION");
 // The user-visible release: its own confirmation control is live again.
 assert.equal(button(card,RE_REVIEW_ACTION_LABEL)?.disabled,!1,"the unlocked Card offers confirmation again");
 // submissionState() keeps reporting the durable outcome — the release is the live
 // control plus the retry copy, never a claim that nothing was refused.
 assert.equal(card.instance.peek("submissionState()"),"REJECTED","the refusal stays the reported outcome");
 assert.equal(view.notice,capturedCopy,"the retry copy is the frozen §4 sentence");
 assert.ok(visibleText(card).some(text=>text.includes(capturedCopy)),`the Card must show ${capturedCopy}`);
 assert.ok(visibleText(card).some(text=>text.includes(`原请求 ${submitted.requestId} 的拒绝回执已保留`)),"the rejected request stays named as history");
 assert.equal(card.host.commits.length,1,"unlocking is never an automatic resend");
 assert.equal(card.instance.peek("fatal"),!1);
 assert.equal(card.instance.peek("renderFailed"),!1);
 // The widget snapshot the Host may restore from carries the release, not the hold.
 const snapshot=card.instance.widgetStates.at(-1);
 assert.equal(snapshot.pendingRequestId,"","a released Card has no pending request to restore");
 assert.equal(snapshot.outcomeUnknown,!1);
 assert.equal(snapshot.identity.contentHash,frozen.contentHash);
});

test("the retry is a new user command with a new request identity, not a replay (§3/§4)",async()=>{
 const card=await bind(start());
 await confirm(card);
 const first=card.host.commits[0];
 assert.equal(state(card).editableRetry,!0);

 await confirm(card);
 assert.equal(card.host.commits.length,2,"only the second user click may send a second command");
 const second=card.host.commits[1];
 assert.notEqual(second.requestId,first.requestId,"a retry must allocate a new requestId");
 assert.notEqual(card.host.rejections[1].payloadSHA256,card.host.rejections[0].payloadSHA256,"a new payload identity must follow from the new request");
 for(const key of ["intentId","version","purpose","action","contentHash","variantId"])assert.deepEqual(second[key],first[key],`the retry changed the frozen ${key}`);
 assert.notEqual(second.requestId,capturedRequest.requestId,"neither replayed command reuses the captured request");

 const view=state(card);
 assert.equal(view.pendingCommit,null,"the Card released the second refusal as well");
 assert.equal(view.editableRetry,!0);
 assert.equal(view.rejectedRequestId,second.requestId,"the newest rejection is what the retry is armed for");
 // The Card keeps a bounded diagnostic ring, so the Host's own call log is the record
 // that both commands went out; the ring must still name the newest one.
 assert.deepEqual(card.host.commits.map(call=>call.requestId),[first.requestId,second.requestId],"both user commands reached the Host");
 const sent=view.events.filter(event=>event.stage==="COMMIT_SENT").map(event=>event.requestId);
 assert.equal(sent.at(-1),second.requestId,"the newest submission stays on the Card's own record");
 const results=view.events.filter(event=>event.stage==="COMMIT_RESULT").map(event=>`${event.requestId}:${event.result}`);
 assert.equal(results.at(-1),`${second.requestId}:REJECTED_NOT_CONSUMED`);
 assert.equal(view.rejected.requestId,second.requestId);
 assert.equal(card.host.commits.length,2,"no automatic redispatch joined the two commands");
});

test("a refresh over the durable refusal stays unlocked and sends nothing (§5)",async()=>{
 const card=await bind(start({readbackRejection:!0}));
 await confirm(card);
 const submitted=card.host.commits[0];
 assert.equal(state(card).editableRetry,!0);
 const readsBefore=card.host.reads.length,optionsBefore=card.host.optionReads;

 // The Host re-delivers the captured production receipt read-back for the same intent.
 await card.instance.pushToolResult(readbackEnvelope);
 const view=state(card);
 assert.equal(view.editableRetry,!0,"the unlock is a durable fact, not a one-shot");
 assert.equal(view.pendingCommit,null);
 assert.equal(view.unknown,!1);
 assert.equal(view.intentState,"PENDING");
 assert.equal(view.rejectedRequestId,submitted.requestId,"history survives the refresh");
 assert.equal(view.submission.retryDisposition,"EDIT_SELECTION_AND_RESUBMIT");
 assert.equal(card.host.commits.length,1,"a refresh may never resend");
 assert.ok(card.host.reads.length>=readsBefore&&card.host.optionReads>=optionsBefore);

 // The same refusal still unlocks through a real Card refresh of its own.
 await fire(card,"refresh()");
 assert.equal(state(card).editableRetry,!0,"refresh alone does not relock a durable non-consumption");
 assert.equal(button(card,RE_REVIEW_ACTION_LABEL)?.disabled,!1);
 assert.equal(card.host.commits.length,1);
});

test("a Host transport error alone never proves non-consumption (§5)",async()=>{
 const card=await bind(start({transportFailure:!0}));
 await confirm(card);
 const submitted=card.host.commits[0];
 const view=state(card);
 // The lost RPC is the only fact this Card has, and it stays on its own record even
 // after the receipt read-back succeeds and clears the outstanding failure.
 const lost=lastEvent(card,"COMMIT_OUTCOME_UNKNOWN");
 assert.equal(lost?.errorSource,"HOST","the only fact this Card has is a Host transport failure");
 assert.equal(lost?.errorCode,-32603,"the Host's own RPC code is recorded verbatim");
 assert.equal(lost?.requestId,submitted.requestId);
 assert.ok(lastEvent(card,"HOST_TRANSPORT"),"the transport stage is on the Card's record");
 assert.equal(view.failure,null,"a successful read-back leaves no outstanding failure, not a proven outcome");
 assertStillHolds(card,view,submitted);
 assert.equal(view.unknown,!0,"an unproven outcome stays unknown, never a cleared submission");
 assert.equal(view.stale,!1);
 assert.equal(view.confirmedOutcome,"","a transport error proves no outcome in either direction");
 assert.equal(view.submission,null,"nothing was ever recorded against this request");
 // §7: what the Card does instead is re-read the original request by exact identity.
 const read=card.host.reads.at(-1);
 assert.equal(read.requestId,submitted.requestId,"the Card reads the original request back instead of resending");
 assert.equal(read.contentHash,frozen.contentHash,"the read is bound to the frozen content digest");
 assert.equal(view.notice,UNPROVEN_READBACK_COPY);
 assert.equal(button(card,ORIGINAL_RECEIPT_ACTION)?.disabled,!1,"the only offered action is the original receipt read");
 await fire(card,"commit()");
 assert.equal(card.host.commits.length,1,"an unproven outcome may never be resent, by the user or by the Card");
});

const negatives=[
 {name:"an unrecorded read is never non-consumption evidence",
  weaken:(envelope,submission)=>{
   Object.assign(submission,{recorded:!1,outcome:"UNKNOWN",businessOutcome:"OUTCOME_UNKNOWN",consumptionDisposition:"UNKNOWN",nextAction:"READ_ORIGINAL_RECEIPT",reprepareAllowed:!1,retryDisposition:"READ_ORIGINAL_RECEIPT"});
   delete submission.consumed;delete submission.failure;
   envelope.failure.nextAction="READ_ORIGINAL_RECEIPT";
  },
  locks:{outcome:"UNKNOWN",businessOutcome:"OUTCOME_UNKNOWN",consumptionDisposition:"UNKNOWN",nextAction:"READ_ORIGINAL_RECEIPT",
   retryDisposition:"READ_ORIGINAL_RECEIPT",consumed:"ABSENT",confirmedOutcome:"",unknown:!0,stale:!1,notice:UNPROVEN_READBACK_COPY}},
 {name:"a re-prepare refusal is not an editable retry",
  weaken:(envelope,submission)=>{
   for(const fact of [submission,submission.failure,envelope.failure])if(fact)fact.nextAction="PREPARE_NEW_VERSION";
   submission.retryDisposition="REPREPARE_REQUIRED";
   submission.reprepareAllowed=!0;
  },
  locks:{outcome:"REJECTED_NOT_CONSUMED",businessOutcome:"REJECTED_NOT_CONSUMED",consumptionDisposition:"NOT_CONSUMED",nextAction:"PREPARE_NEW_VERSION",
   retryDisposition:"REPREPARE_REQUIRED",consumed:!1,confirmedOutcome:"REJECTED_NOT_CONSUMED",unknown:!1,stale:!0,notice:TERMINAL_REPREPARE_COPY,reprepareCopy:!0}},
 {name:"a rejection may not carry a consumption pointer",
  weaken:(_envelope,submission)=>{submission.receiptRequestId="another-request-id";},
  conflict:{code:"REJECTION_RECEIPT_CONFLICT"}},
 {name:"a refusal recorded against another request is not this Card's receipt",
  foreignRequest:!0,conflict:{code:"SUBMISSION_REQUEST_CONFLICT"}},
];
for(const negative of negatives){
 test(negative.name,async()=>{
  const card=await bind(start(negative));
  await confirm(card);
  const submitted=card.host.commits[0];
  const view=state(card);
  assert.equal(view.editableRetry,!1,"only §3 may open the retry path");
  assert.equal(visibleText(card).some(text=>text.includes(capturedCopy)),!1,"the retry copy belongs to the unlocked Card only");
  // Either way the Card may not invent a second command.
  assert.equal(card.host.commits.length,1,"a locked refusal may never turn into an automatic resend");
  if(negative.conflict){
   // The whole envelope is refused by the Card's own validator, so it cannot even adopt
   // its facts; the typed refusal survives on the Card and on its record.
   assert.equal(card.host.reads.at(-1)?.requestId,submitted.requestId,"an unproven result is re-read, never resent");
   assert.equal(view.submission,null,"a refused envelope must not rebind the intent");
   assert.equal(lastEvent(card,"COMMIT_OUTCOME_UNKNOWN")?.errorCode,negative.conflict.code,`refused with ${JSON.stringify(view.events.slice(-4))}`);
   assert.equal(view.failure.code,negative.conflict.code,"the typed refusal stays on the Card's diagnostics");
   assert.equal(view.failure.source,"CARD","the Card refused the bytes, the Host did not");
   assert.equal(view.unknown,!0,"a refused result is unproven, never a released submission");
   assert.equal(view.notice,"","an unproven result earns no retry copy");
   assertStillHolds(card,view,submitted);
   return;
  }
  const {locks}=negative;
  if(locks.unknown){
   // An unproven result sends the Card to the original receipt, addressed by exact identity.
   assert.equal(card.host.reads.at(-1)?.requestId,submitted.requestId,"an unproven result is re-read, never resent");
  }else{
   // A proven rejection needs no receipt hunt: the durable outcome is already known, so
   // the Card just holds its request and waits for the user to re-prepare.
   assert.equal(card.host.reads.some(read=>read.requestId===submitted.requestId),!1,"a known outcome is not re-read as if it were unproven");
   assert.equal(view.readback,!1);
  }
  assert.equal(view.submission.outcome,locks.outcome);
  assert.equal(view.submission.businessOutcome,locks.businessOutcome);
  assert.equal(view.submission.consumptionDisposition,locks.consumptionDisposition);
  assert.equal(view.submission.nextAction,locks.nextAction);
  assert.equal(view.submission.retryDisposition,locks.retryDisposition,"the durable fact must survive verbatim");
  if(locks.consumed==="ABSENT")assert.ok(!("consumed"in view.submission),"an unrecorded outcome may not claim non-consumption");
  else assert.equal(view.submission.consumed,locks.consumed);
  assert.equal(view.confirmedOutcome,locks.confirmedOutcome);
  assert.equal(view.unknown,locks.unknown);
  assert.equal(view.stale,locks.stale);
  assert.equal(view.notice,locks.notice);
  if(locks.reprepareCopy){
   // §9: the terminal rejection names the replacement prepare as the only way forward
   // on every part of the Card, and keeps the locked request held.
   assert.equal(view.submissionNext,"PREPARE_NEW_VERSION","the rejected Card must report the replacement prepare as next");
   assert.equal(view.submission.reprepareAllowed,!0,"the receipt still permits a fresh prepare");
   assert.notEqual(view.notice,LOCKED_REJECTION_COPY,"the re-prepare shape has its own §9 copy");
   assert.ok(visibleText(card).some(text=>text.includes("请返回对话读取准确目标或准备新版本")),"the visible guidance must point at the formal replacement prepare");
  }
  assertStillHolds(card,view,submitted);
 });
}

test("expired content may not be re-submitted from an unlocked Card (§5 clock negative)",async()=>{
 const card=await bind(start());
 await confirm(card);
 assert.equal(state(card).editableRetry,!0,"the refusal itself is still an editable retry inside its window");
 pinClock(card.instance,EXPIRES_AT+1000);
 await fire(card,"safeRender()");
 assert.equal(card.instance.peek("choiceError()"),EXPIRED_CHOICE_ERROR);
 assert.equal(button(card,RE_REVIEW_ACTION_LABEL)?.disabled,!0,"the released Card must not submit past its frozen window");
 assert.ok(visibleText(card).some(text=>text.includes("已过期")),"the expiry must be the visible reason");
 await fire(card,"commit()");
 assert.equal(card.host.commits.length,1,"an expired intent may never be re-submitted");
});
