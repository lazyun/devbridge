"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const Development = require("./ui/product-development.js");
const I18n = require("./ui/product-i18n.js");
const Components = require("./ui/product-components.js");

function repository(id, name, selected = false) {
  return {repositoryId: id, repositoryName: name, branchShortName: selected ? "dev/test" : "", developmentRef: selected ? "refs/heads/dev/test" : "", branches: [{fullRef: "refs/heads/dev/test", shortName: "dev/test", repositoryCheckout: false}]};
}
function storage() {
 const values = new Map();
 return {getItem: (key) => values.get(key), setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key)};
}
function actionHarness() {
 const source = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
 const calls = [];
 const context = {state: {developmentReadVersion: 0, developmentProjectVersion: 0, selectedProjectID: "project-a", developmentContext: {repositories: [repository("repo-a", "One")]}}, DB: Development, window: {localStorage: storage(), crypto: {randomUUID: () => "command-1"}}, render: () => {}, toast: () => {}, t: (key) => key, api: async (path, options) => {calls.push({path, options}); return {projectId: "project-a", repositories: [repository("repo-a", "One", true)]}; }, qs: (values) => new URLSearchParams(values).toString()};
 vm.createContext(context);
 vm.runInContext(source.slice(source.indexOf("  async function loadDevelopmentContext()"), source.indexOf("  async function refreshLogs(")), context);
 return {context, calls};
}

test("branch selector distinguishes repository identities and has bilingual explicit checkout copy", () => {
 for (const locale of ["en", "zh-CN"]) {
  const i18n = I18n.createI18n({navigator: {languages: [locale]}});
  const html = Development.render({repositories: [repository("repo-a", "Same"), repository("repo-b", "Same", true)]}, i18n.t, Components.esc);
  assert.match(html, /data-repository-id="repo-a"/);
  assert.match(html, /data-repository-id="repo-b"/);
  assert.ok(html.includes(i18n.t("development.choose")));
  assert.match(html, /value="refs\/heads\/dev\/test" selected/);
  assert.ok(html.includes(i18n.t("development.note")));
  assert.doesNotMatch(html, /\[development\./);
 }
});
test("branch command identity survives retries and is specific to its complete payload", () => {
 const local = storage(); let n = 0; const id = () => `request-${++n}`;
 const first = Development.command(local, "p", "r", "refs/heads/a", id);
 assert.equal(Development.command(local, "p", "r", "refs/heads/a", id).body.request_id, first.body.request_id);
 assert.notEqual(Development.command(local, "p", "other", "refs/heads/a", id).body.request_id, first.body.request_id);
 local.removeItem(first.key);
 assert.notEqual(Development.command(local, "p", "r", "refs/heads/a", id).body.request_id, first.body.request_id);
});
test("refresh is read-only and explicit branch submit re-reads server authority", async () => {
 const {context, calls} = actionHarness();
 await context.loadDevelopmentContext();
 assert.equal(calls.length, 1); assert.equal(calls[0].options, undefined);
 await context.selectDevelopmentBranch("repo-a", "refs/heads/dev/test");
 assert.equal(calls[1].options.method, "POST");
 assert.deepEqual(JSON.parse(calls[1].options.body), {project_id: "project-a", repository_id: "repo-a", development_ref: "refs/heads/dev/test", request_id: "command-1"});
 assert.equal(calls[2].options, undefined);
 assert.equal(context.state.developmentContext.repositories[0].developmentRef, "refs/heads/dev/test");
});
test("late branch reads cannot publish another Project's selection", async () => {
 const {context} = actionHarness(); let resolve;
 context.api = () => new Promise((done) => {resolve = done;});
 const loading = context.loadDevelopmentContext();
 context.state.selectedProjectID = "project-b";
 context.state.developmentContext = {projectId: "project-b", repositories: []};
 resolve({projectId: "project-a", repositories: [repository("repo-a", "One", true)]});
 await loading;
 assert.equal(context.state.developmentContext.projectId, "project-b");
});
test("failed explicit mutation retries the same command identity", async () => {
 const {context} = actionHarness(); const commands = [];
 context.api = async (_, options) => {commands.push(JSON.parse(options.body)); throw new Error("transport failed");};
 await context.selectDevelopmentBranch("repo-a", "refs/heads/dev/test");
 await context.selectDevelopmentBranch("repo-a", "refs/heads/dev/test");
 assert.equal(commands.length, 2); assert.equal(commands[0].request_id, commands[1].request_id);
});
test("a newer refresh wins even when an older response arrives last", async () => {
 const {context} = actionHarness(); const pending = [];
 context.api = () => new Promise((done) => pending.push(done));
 const oldRead = context.loadDevelopmentContext();
 const newRead = context.loadDevelopmentContext();
 pending[1]({projectId: "project-a", repositories: [repository("repo-a", "New", true)]});
 await newRead;
 pending[0]({projectId: "project-a", repositories: [repository("repo-a", "Old")]});
 await oldRead;
 assert.equal(context.state.developmentContext.repositories[0].repositoryName, "New");
});
test("late mutation acknowledgement does not replace the newly viewed Project", async () => {
 const {context} = actionHarness(); let resolve; let requests = 0;
 context.api = () => {requests++; return new Promise((done) => {resolve = done;});};
 const mutation = context.selectDevelopmentBranch("repo-a", "refs/heads/dev/test");
 context.state.selectedProjectID = "project-b";
 context.state.developmentProjectVersion++;
 context.state.developmentContext = {projectId: "project-b", repositories: []};
 resolve({id: "original-command-cycle"});
 await mutation;
 assert.equal(requests, 1);
 assert.equal(context.state.developmentContext.projectId, "project-b");
});
test("missing ref and checkout drift are presented as separate factual states", () => {
 const i18n = I18n.createI18n({navigator: {languages: ["en"]}});
 const repo = {...repository("repo-a", "One", true), developmentRefStatus: "MISSING", repositoryCheckoutMatchesDevelopmentRef: false, branches: []};
 const html = Development.render({repositories: [repo]}, i18n.t, Components.esc);
 assert.match(html, /Selected ref is missing/);
 assert.match(html, /Git checkout differs/);
 assert.match(html, /dev\/test/);
 assert.match(html, /type="submit" disabled/);
});
