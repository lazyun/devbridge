import {strict as assert} from "node:assert";
import test from "node:test";
import {reloadWorkbenchDocument} from "../src/console-workbench.js";
import {consoleHTML,sessionsHTML} from "../src/views.js";
test("Desktop console renders natively without a second web navigation or iframe",()=>{assert.match(consoleHTML(),/id="native-console"/);assert.doesNotMatch(consoleHTML(),/iframe|\/ui\/|打开控制台/);});
test("session document reload preserves its location and falls back after opaque errors",()=>{let reloads=0;const frame={contentWindow:{location:{reload(){reloads++;}}}};reloadWorkbenchDocument(frame);assert.equal(reloads,1);const fallback={dataset:{source:"/ui/?workbench=sessions"}};reloadWorkbenchDocument(fallback);assert.equal(fallback.src,"/ui/?workbench=sessions");assert.match(sessionsHTML(x=>x),/workbench=sessions/);});
