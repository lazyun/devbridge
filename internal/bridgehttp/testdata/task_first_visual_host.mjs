// Local fixture serving production Web assets. Every mutation is rejected.
import http from 'node:http';
import {readFile} from 'node:fs/promises';
import {resolve,basename} from 'node:path';
import fixtures from '../../mcpserver/testdata/card_visual_fixture.cjs';
const ui=resolve('internal/bridgehttp/ui');
const ids=['task-a','task-b','task-c'];
function task(id){const p=fixtures.fixture(id==='task-c'?'IMPLEMENTATION_RUNNING':'COMPLETE').presentation;p.developmentContext=fixtures.fixture('COMPLETE').developmentContext;p.taskView.id=p.details.taskId=p.taskStages.taskId=p.workflowGuidance.taskId=id;p.taskView.title=({ 'task-a':'Preserve canonical workspace','task-b':'Unify Task usage and results','task-c':'Verify Task-first presentation'})[id];return {id,title:p.taskView.title,state:p.taskView.status,feature_area_id:'area-a',feature_area_title:p.featureArea.name,updated_at:'2026-09-11T00:00:00Z',work:{type:'DEVELOPMENT'},presentation:p};}
const tasks=ids.map(task),area={id:'area-a',title:'Task-first presentation',status:'ACTIVE',state:'FEATURE_AREA_IDLE',summary:'A persistent workspace for successive engineering tasks.',reviews:[],presentation:fixtures.fixture('FEATURE_AREA_IDLE').presentation};
area.presentation.developmentContext=fixtures.fixture('COMPLETE').developmentContext;
// The list demonstrates two terminal Tasks followed by one current Task.
area.state=area.presentation.currentState=area.presentation.workflowStatus='IMPLEMENTATION_RUNNING';
area.presentation.currentTask=area.presentation.latestTask={id:'task-c',status:'RUNNING'};
area.presentation.capabilities=[];delete area.presentation.latestTaskView;
area.presentation.nextAction={code:'WAIT_FOR_IMPLEMENTATION_COMPLETION',message:'Current Task is running.'};
const workspace={project_id:'project',project_context:{state:'READY',module_count:1},featureAreas:[area],tasks,executions:[],documents:[],workspaces:[],active_agent_operations:[]};
const server=http.createServer(async(req,res)=>{try{const url=new URL(req.url,'http://localhost');if(req.method!=='GET'){res.writeHead(405);res.end('Fixture has no mutation backend');return;}
 if(url.pathname==='/readyz'){res.end('{}');return;}
 if(url.pathname.startsWith('/ui/api/')||url.pathname.startsWith('/v1/')){
  const path=url.pathname;let data={items:[]};
  if(path==='/v1/project-selection')data={selected_project_id:'project'};
  if(path==='/v1/projects')data={items:[{id:'project',name:'Dev Bridge',project_context_status:'READY',repository_count:1}]};
  if(path==='/ui/api/project-workspace')data=workspace;
  if(path==='/ui/api/featureAreas')data={items:[area]};
  if(path==='/v1/development-context')data={projectId:'project',repositories:[]};
  if(path.startsWith('/ui/api/tasks/')){const value=tasks.find(t=>t.id===path.split('/').pop());if(!value){res.writeHead(404);res.end('{}');return;}data=value;}
  res.setHeader('Content-Type','application/json');res.setHeader('Cache-Control','no-store');res.end(JSON.stringify(data));return;
 }
 const name=url.pathname==='/'?'index.html':basename(url.pathname);if(!['index.html','product.css','product-app.js','product-i18n.js','product-components.js','product-status.js','product-development.js','product-work-refresh.js','product-logformat.js','product-logview.js'].includes(name)){res.writeHead(404);res.end();return;}
 res.setHeader('Content-Type',name.endsWith('.js')?'application/javascript':name.endsWith('.css')?'text/css':'text/html');res.end(await readFile(resolve(ui,name)));
 }catch(e){res.writeHead(500);res.end(String(e));}});
server.listen(0,'127.0.0.1',()=>console.log('Web fixture: http://127.0.0.1:'+server.address().port));
