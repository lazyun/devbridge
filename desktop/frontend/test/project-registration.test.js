import { strict as assert } from "node:assert";
import test from "node:test";
import {
  chooseProjectDirectory,
  closeProjectRegistration,
  manageProjectRepositories,
  errorFromException,
  createProjectsState,
  loadProjects,
  submitProjectRegistration,
  prepareRepositoryUnlink,
  commitRepositoryUnlink,
  cancelRepositoryUnlink,
} from "../src/project-registration.js";

test("project flow loads zero-project state and chosen directory default", async () => {
  const state = createProjectsState();
  const api = {
    async ListProjects() { return { items: [], coreReady: true }; },
    async ChooseProjectDirectory() { return { repositoryPath: "/source/devbridge", defaultName: "devbridge" }; },
  };
  await loadProjects(state, api);
  assert.equal(state.coreReady, true);
  assert.deepEqual(state.items, []);
  assert.equal(await chooseProjectDirectory(state, api), true);
  assert.equal(state.addOpen, true);
  assert.equal(state.repositoryPath, "/source/devbridge");
  assert.equal(state.name, "devbridge");
  state.name = "Dev Bridge";
  api.ChooseProjectDirectory = async () => ({ repositoryPath: "/source/other", defaultName: "other" });
  await chooseProjectDirectory(state, api);
  assert.equal(state.name, "Dev Bridge", "an edited project name must survive reselect");
});

test("in-band structured errors are surfaced verbatim, never as CORE_UNAVAILABLE", async () => {
  const state = createProjectsState();
  const api = {
    async ListProjects() { return { items: [], coreReady: true, error: { code: "UNSUPPORTED_OPERATION", message: "HTTP 405" } }; },
  };
  await loadProjects(state, api);
  assert.equal(state.coreReady, true);
  assert.equal(state.loadError.code, "UNSUPPORTED_OPERATION");
  assert.notEqual(state.loadError.code, "CORE_UNAVAILABLE");
});

test("HTTP 405 and 404 failures are API contract mismatches, not Core unavailability", () => {
  for (const status of [404, 405]) {
    const error = errorFromException(`Project registration failed: Core returned HTTP ${status}`);
    assert.equal(error.code, "UNSUPPORTED_OPERATION");
    assert.notEqual(error.code, "CORE_UNAVAILABLE");
  }
});

test("HTTP 400, 409, and 5xx failures keep their distinct semantics", () => {
  assert.equal(errorFromException("Core returned HTTP 400").code, "INVALID_REQUEST");
  assert.equal(errorFromException("Core returned HTTP 409").code, "INVALID_REQUEST");
  assert.equal(errorFromException("Core returned HTTP 500").code, "CORE_ERROR");
  assert.equal(errorFromException("Core returned HTTP 503").code, "CORE_ERROR");
});

test("successful registration closes form, refreshes list, and highlights the project", async () => {
  const state = createProjectsState();
  state.addOpen = true;
  state.repositoryPath = "/source/devbridge";
  state.repositories = [{path:"/source/devbridge",alias:""}];
  state.name = "Dev Bridge";
  let listCalls = 0;
  const api = {
    async RegisterProject(input) {
      assert.ok(input.requestId);
      assert.deepEqual(input.repositories, [{path:"/source/devbridge",alias:""}]);
      assert.equal(input.name,"Dev Bridge");
      return { created: true, projectId: "project-new", repositoryId: "repo-new", name: "Dev Bridge", repositoryPath: "/source/devbridge" };
    },
    async ListProjects() {
      listCalls++;
      return { coreReady: true, items: [{ id: "project-new", name: "Dev Bridge", repositoryCount: 1 }] };
    },
  };
  assert.equal(await submitProjectRegistration(state, api), true);
  assert.equal(state.addOpen, false);
  assert.equal(state.selectedProjectId, "project-new");
  assert.equal(state.successMessage, "Dev Bridge");
  assert.equal(state.items[0].id, "project-new");
  assert.equal(listCalls, 1);
});

test("duplicate and validation errors stay actionable without losing the form", async () => {
  const state = createProjectsState();
  state.addOpen = true;
  state.repositoryPath = "/source/devbridge";
  state.repositories = [{path:"/source/devbridge",alias:""}];
  state.name = "Duplicate";
  const duplicateAPI = {
    async RegisterProject() {
      return {
        created: false, projectId: "project-existing",
        error: { code: "PROJECT_ALREADY_REGISTERED", message: "Repository is already registered as Dev Bridge." },
      };
    },
  };
  assert.equal(await submitProjectRegistration(state, duplicateAPI), false);
  assert.equal(state.addOpen, true);
  assert.equal(state.submitError.code, "PROJECT_ALREADY_REGISTERED");
  assert.equal(state.selectedProjectId, "project-existing");

  duplicateAPI.RegisterProject = async () => ({ created: false, error: { code: "NOT_GIT_REPOSITORY", message: "Selected folder is not a usable Git repository." } });
  assert.equal(await submitProjectRegistration(state, duplicateAPI), false);
  assert.equal(state.submitError.code, "NOT_GIT_REPOSITORY");
});

test("Core-not-ready list and thrown backend calls map to stable state", async () => {
  const state = createProjectsState();
  await loadProjects(state, { async ListProjects() { return { items: [], coreReady: false, error: { code: "CORE_UNAVAILABLE", message: "Core is not ready." } }; } });
  assert.equal(state.coreReady, false);
  assert.equal(state.loadError.code, "CORE_UNAVAILABLE");
  state.addOpen = true;
  state.repositoryPath = "/source/repo";
  state.name = "Repo";
  assert.equal(await submitProjectRegistration(state, { async RegisterProject() { throw new Error("offline"); } }), false);
  assert.equal(state.submitError.code, "DESKTOP_ERROR");
});


test("empty logical project and append use explicit repository sets", async () => {
 const state=createProjectsState();state.name="Logical";const calls=[];
 const api={async RegisterProject(input){calls.push(structuredClone(input));return {created:true,projectId:"p",name:"Logical"};},async ListProjects(){return {items:[],coreReady:true};}};
 assert.equal(await submitProjectRegistration(state,api),true);assert.deepEqual(calls[0].repositories,[]);
 state.projectId="p";state.repositories=[{path:"/independent/app",alias:"app"},{path:"/other/sdk",alias:"sdk"}];
 assert.equal(await submitProjectRegistration(state,api),true);assert.equal(calls[1].projectId,"p");assert.equal(calls[1].name,"");assert.equal(calls[1].repositories.length,2);
});

test("response loss retains exact request across edits and page state reload", async () => {
 const values=new Map();globalThis.localStorage={getItem:k=>values.get(k),setItem:(k,v)=>values.set(k,v),removeItem:k=>values.delete(k)};
 try {
 const state=createProjectsState();state.name="Logical";state.repositories=[{path:"/app",alias:"app"}];const calls=[];
 const api={async RegisterProject(input){calls.push(structuredClone(input));if(calls.length===1)throw Error("response lost");return {created:true,projectId:"p"};},async ListProjects(){return {items:[],coreReady:true};}};
 assert.equal(await submitProjectRegistration(state,api),false);
 const reload=createProjectsState();reload.name="Another";reload.repositories=[{path:"/sdk",alias:"sdk"}];
 assert.equal(await submitProjectRegistration(reload,api),true);assert.deepEqual(calls[0],calls[1]);assert.equal(values.size,0);
 }finally{delete globalThis.localStorage;}
});


test("unknown registration stays visibly bound to original project when another is opened",async()=>{
 const state=createProjectsState();state.projectId="project-A";state.repositories=[{path:"/app",alias:"app"}];
 await submitProjectRegistration(state,{async RegisterProject(){throw Error("lost");}});
 closeProjectRegistration(state);assert.equal(state.projectId,"project-A");
 let requested;await manageProjectRepositories(state,{async ListProjectRepositories(id){requested=id;return {projectId:id,repositories:[]};}},"project-B");
 assert.equal(requested,"project-A");assert.equal(state.projectId,"project-A");assert.deepEqual(state.repositories,[{path:"/app",alias:"app"}]);
});

test("repository unlink carries Core-frozen generations and never invents a blocker override", async () => {
 const state=createProjectsState(); state.projectId="project-A";
 const repository={repositoryId:"repo-A"}; let prepared; let committed;
 const api={
  async PrepareRepositoryUnlink(input){prepared=input;return {requestId:input.requestId,projectId:input.projectId,repositoryId:input.repositoryId,expectedRegistrationGeneration:7,expectedTopologyRevision:11,eligibility:{canUnlink:true}};},
  async CommitRepositoryUnlink(input){committed=structuredClone(input);return {...input};},
  async ListProjectRepositories(){return {projectId:"project-A",repositories:[]};},
 };
 assert.equal(await prepareRepositoryUnlink(state,api,repository),true);
 assert.equal(prepared.projectId,"project-A");assert.equal(prepared.repositoryId,"repo-A");assert.ok(prepared.requestId);
 assert.equal(state.unlinkConfirmation.expectedRegistrationGeneration,7);
 assert.equal(await commitRepositoryUnlink(state,api),true);
 assert.equal(committed.expectedRegistrationGeneration,7);assert.equal(committed.expectedTopologyRevision,11);
 assert.equal("force" in committed,false);assert.equal("outcome" in committed,false);assert.equal(state.unlinkConfirmation,null);
 state.unlinkConfirmation={repositoryId:"repo-A"};cancelRepositoryUnlink(state);assert.equal(state.unlinkConfirmation,null);
});
