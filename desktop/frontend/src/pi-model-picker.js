const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function normalizePiCatalog(models){
 if(!Array.isArray(models)||models.some(m=>!m||typeof m.modelId!=='string'||!m.modelId.trim()))throw Error('Invalid Pi model catalog');
 const seen=new Set();return models.filter(m=>!seen.has(m.modelId)&&seen.add(m.modelId)).map(m=>({...m}));
}
export function filterPiCatalog(models,query=''){
 const terms=query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
 return models.filter(m=>terms.every(term=>`${m.modelId} ${m.provider||''}`.toLocaleLowerCase().includes(term)));
}
export function addPiCatalogModel(configured,catalog,id){
 const current=Array.isArray(configured)?configured:[];
 const existing=current.find(m=>m.modelId===id);
 if(existing)return existing.enabled?current:current.map(m=>m.modelId===id?{...m,enabled:true}:m);
 const model=catalog.find(m=>m.modelId===id);if(!model)throw Error('Selected Pi model is no longer available');
 return [...current,{...model,enabled:true,supportedReasoningEfforts:[...(model.supportedReasoningEfforts||[])]}];
}
export function piPickerOptionsHTML(t,state){
 const added=new Set((state.settings.piAgentModels||[]).filter(m=>m.enabled).map(m=>m.modelId));
 const models=filterPiCatalog(state.piModelCatalog||[],state.piModelSearch||'');
 return models.length?models.map(model=>`<button type="button" role="option" aria-selected="${state.piSelectedModel===model.modelId}" data-pi-picker-option="${esc(model.modelId)}" ${added.has(model.modelId)?'disabled':''}><span><strong>${esc(model.modelId)}</strong>${model.provider?`<small>${esc(model.provider)}</small>`:''}</span>${added.has(model.modelId)?`<small>${esc(t('settings.piModelAdded'))}</small>`:'<span class="pi-picker-check">＋</span>'}</button>`).join(''):`<p class="pi-picker-empty" role="status">${esc(t('settings.piModelNoMatch'))}</p>`;
}
export function piModelPickerHTML(t,state){
 const valid=state.piModelCatalogExecutable===(state.settings.piExecutable||''),catalog=valid?state.piModelCatalog||[]:[];
 const selected=catalog.find(m=>m.modelId===state.piSelectedModel),added=(state.settings.piAgentModels||[]).some(m=>m.enabled&&m.modelId===selected?.modelId);
 const locked=state.piModelCatalogLoading||state.saveStates?.pi?.state==='saving';
 return `<div class="pi-model-picker"><div class="pi-model-picker-row"><button type="button" class="pi-picker-trigger" data-pi-picker-toggle aria-haspopup="listbox" aria-controls="pi-model-options" aria-expanded="${!!state.piPickerOpen}" ${locked||!catalog.length?'disabled':''}><span>${esc(selected?.modelId||t(catalog.length?'settings.piChooseModel':'settings.piLoadFirst'))}</span><span aria-hidden="true">⌄</span></button><button type="button" class="primary" data-action="add-pi-model" ${locked||!selected||added?'disabled':''}>${esc(t('settings.piAddModel'))}</button></div><div class="pi-picker-menu" ${state.piPickerOpen&&valid?'':'hidden'}><div class="pi-picker-search"><input type="search" data-pi-picker-search role="combobox" aria-label="${esc(t('settings.piSearchModels'))}" aria-controls="pi-model-options" aria-expanded="true" aria-autocomplete="list" placeholder="${esc(t('settings.piSearchModels'))}" value="${esc(state.piModelSearch||'')}" autocomplete="off"></div><div id="pi-model-options" class="pi-picker-options" role="listbox" aria-label="${esc(t('settings.piChooseModel'))}">${piPickerOptionsHTML(t,state)}</div></div></div>`;
}

export function piSelectionChange(settings,models){
  const current=settings.piModel||"";
  return {piAgentModels:models,piModel:models.some(m=>m.enabled&&m.modelId===current)?current:""};
}
