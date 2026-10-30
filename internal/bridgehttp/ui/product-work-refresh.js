"use strict";
(function(root){
  // One scoped reader owns automatic and interactive Task refreshes.
  function create({read,selectTaskView,apply,changed=()=>{},hidden=()=>false,setTimer,clearTimer}) {
    let target=null,timer=null,flight=null,generation=0,error="",terminal=false,busy=false;
    function clear(){if(timer!==null)clearTimer(timer);timer=null;}
    function stop(){clear();generation++;flight?.controller.abort();flight=null;target=null;busy=false;error="";changed();}
    function schedule(){clear();if(target&&!terminal&&!hidden())timer=setTimer(()=>refresh(),5000);}
    function refresh(mode="BACKGROUND"){
      if(!target||hidden())return Promise.resolve({skipped:true});
      if(flight){if(mode==="BACKGROUND")return flight.promise;generation++;flight.controller.abort();flight=null;}
      clear();const mine=target,g=generation,controller=new AbortController();busy=mode==="INTERACTIVE";changed();
      const current={controller,promise:null};flight=current;
      current.promise=(async()=>{try {const payload=await read({...mine,signal:controller.signal});if(target!==mine||generation!==g)return {stale:true};
        const taskView=selectTaskView(payload);
        if(!taskView||typeof taskView!=="object"||Array.isArray(taskView))throw new Error("TASK_VIEW_UNAVAILABLE");
        terminal=taskView.terminal===true && taskView.commitActivity!=="RUNNING";
        error="";apply(mine,payload);return {ok:true,payload};
      }catch(e){if(g!==generation||e.name==="AbortError")return {stale:true};error=e.message;return {ok:false,error};}
      finally {if(g===generation){flight=null;busy=false;changed();schedule();}}})();return current.promise;
    }
    function enter(project,taskId,mode="INTERACTIVE"){stop();if(!project||!taskId)return Promise.resolve({skipped:true});target={project,taskId};terminal=false;return refresh(mode);}
    function visibility(){clear();if(hidden()){generation++;flight?.controller.abort();flight=null;busy=false;changed();}else if(target&&!terminal)return refresh();}
    return {enter,refresh,stop,visibility,error:()=>error,busy:()=>busy,freshAfterAction:()=>refresh("INTERACTIVE")};
  }
  if(typeof module!=="undefined"&&module.exports)module.exports={create};else root.DevBridgeWorkRefresh={create};
})(typeof window!=="undefined"?window:globalThis);
