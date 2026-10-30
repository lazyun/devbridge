"use strict";
const {inspect}=require("node:util");
// Deliberately small DOM contract double: HTML trees, selectors and bubbling events.
// It does not model CSS, layout, focus trapping or any browser execution engine.
const decode=v=>String(v).replaceAll('&quot;','"').replaceAll('&#39;',"'").replaceAll('&lt;','<').replaceAll('&gt;','>').replaceAll('&amp;','&');
const encode=v=>String(v).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('\"','&quot;');
const camel=v=>v.replace(/-([a-z])/g,(_,c)=>c.toUpperCase());
class Node {
 constructor(tag,attrs={},doc){this.tagName=tag.toUpperCase();this.attrs={...attrs};this.children=[];this.parentElement=null;this.ownerDocument=doc;this.listeners={};this.scrollTop=0;this.inert=false;this._value='';this.raw='';this.style={};this.isFragment=this.tagName==='#DOCUMENT-FRAGMENT';if(this.tagName==='TEMPLATE'){this.content=new Node('#document-fragment',{},doc);this.content._templateHost=this;}for(const key of Object.keys(this))Object.defineProperty(this,key,{enumerable:false});}
 get dataset(){const self=this,values=Object.fromEntries(Object.entries(this.attrs).filter(([k])=>k.startsWith('data-')).map(([k,v])=>[camel(k.slice(5)),v]));return new Proxy(values,{set(target,key,value){target[key]=String(value);self.attrs['data-'+String(key).replace(/[A-Z]/g,c=>'-'+c.toLowerCase())]=String(value);return true;}});}
 hasAttribute(name){return Object.hasOwn(this.attrs,name);}
 get id(){return this.attrs.id||'';} set id(v){this.attrs.id=v;}
 get className(){return this.attrs.class||'';} set className(v){this.attrs.class=v;}
 get classList(){const self=this;return {contains:c=>self.className.split(/\s+/).includes(c),add:c=>{self.className=[...new Set([...self.className.split(/\s+/),c])].join(' ');},remove:c=>{self.className=self.className.split(/\s+/).filter(v=>v!==c).join(' ');},toggle(c,force){const yes=force??!this.contains(c);if(yes)this.add(c);else this.remove(c);return yes;}};}
 get open(){return Object.hasOwn(this.attrs,'open');} set open(v){if(v)this.attrs.open='';else delete this.attrs.open;}
 get disabled(){return Object.hasOwn(this.attrs,'disabled');} set disabled(v){if(v)this.attrs.disabled='';else delete this.attrs.disabled;}
 get hidden(){return Object.hasOwn(this.attrs,'hidden');} set hidden(v){if(v)this.attrs.hidden='';else delete this.attrs.hidden;}
 get name(){return this.attrs.name||'';} get type(){return this.attrs.type||'text';}
 get value(){return this._value||this.attrs.value||'';} set value(v){this._value=String(v);}
 get isConnected(){let n=this;while(n){if(n===this.ownerDocument?.root)return true;n=n.parentElement;}return false;}
 get innerHTML(){return (this.content||this).children.map(node=>node.outerHTML).join('');} set innerHTML(value){this.raw=String(value);const target=this.content||this;target.children=[];parse(this.raw,target,this.ownerDocument);}
 get textContent(){return this.tagName==='#TEXT'?this.raw:this.children.map(c=>c.textContent).join('');} set textContent(value){this.raw=String(value);this.children=[];const n=new Node('#text',{},this.ownerDocument);n.raw=String(value);this.append(n);}
 append(...nodes){for(const node of nodes){if(!node)continue;if(node.isFragment){for(const child of [...node.children])this.append(child);continue;}if(node===this||node.contains(this))throw new Error(`DOM hierarchy cycle: ${node.tagName} into ${this.tagName}`);node.remove();node.parentElement=this;this.children.push(node);}}
 get firstElementChild(){return this.children.find(n=>n.tagName!=='#TEXT')||null;}
 get nextElementSibling(){const nodes=this.parentElement?.children.filter(n=>n.tagName!=='#TEXT')||[];return nodes[nodes.indexOf(this)+1]||null;}
 get outerHTML(){if(this.tagName==='#TEXT')return encode(this.raw);if(this.isFragment)return this.innerHTML;const tag=this.tagName.toLowerCase(),attrs=Object.entries(this.attrs).map(([key,value])=>' '+key+'=\"'+encode(value)+'\"').join('');return '<'+tag+attrs+'>'+this.innerHTML+(/^(input|img|br|hr|meta|link)$/.test(tag)?'':'</'+tag+'>');}
 insertBefore(node,reference){if(node===reference)return node;if(reference&&reference.parentElement!==this)throw new Error('Reference is not a child');node.remove();const index=reference?this.children.indexOf(reference):this.children.length;node.parentElement=this;this.children.splice(index,0,node);return node;}
 replaceChildren(...nodes){for(const child of this.children)child.parentElement=null;this.children=[];this.raw='';this.append(...nodes);}
 cloneNode(deep=false){const copy=new Node(this.tagName,this.attrs,this.ownerDocument);copy.scrollTop=this.scrollTop;copy.inert=this.inert;copy._value=this._value;copy.raw=this.raw;copy.style={...this.style};if(this.content){copy.content=this.content.cloneNode(deep);copy.content._templateHost=copy;}if(deep)for(const child of this.children)copy.append(child.cloneNode(true));return copy;}
 before(node){const p=this.parentElement;if(p&&node!==this){node.remove();const index=p.children.indexOf(this);node.parentElement=p;p.children.splice(index<0?p.children.length:index,0,node);}}
 remove(){const p=this.parentElement;if(p)p.children=p.children.filter(n=>n!==this);this.parentElement=null;}
 insertAdjacentHTML(where,html){if(where==='beforeend'){this.raw+=html;parse(html,this,this.ownerDocument);}}
 setAttribute(k,v){this.attrs[k]=String(v);} getAttribute(k){return this.attrs[k]??null;} removeAttribute(k){delete this.attrs[k];}
 contains(n){while(n){if(n===this)return true;n=n.parentElement;}return false;}
 matches(selector){return selector.split(',').some(s=>matchPath(this,s.trim()));}
 closest(selector){let n=this;while(n){if(n.matches(selector))return n;n=n.parentElement;}return null;}
 querySelectorAll(selector){const out=[];function walk(n){for(const c of n.children){if(c.matches(selector))out.push(c);walk(c);}}walk(this);return out;}
 querySelector(selector){return this.querySelectorAll(selector)[0]||null;}
 addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}
 removeEventListener(type,fn){this.listeners[type]=(this.listeners[type]||[]).filter(listener=>listener!==fn);}
 dispatchEvent(event){event.target??=this;event.preventDefault??=()=>{};event.stopPropagation??=()=>{event.stopped=true;};event.stopImmediatePropagation??=()=>{event.stopped=true;};let n=this;while(n){event.currentTarget=n;for(const fn of n.listeners[event.type]||[])fn(event);if(event.stopped)break;n=n.parentElement;}return true;}
 click(){this.dispatchEvent({type:'click'});} focus(){this.ownerDocument.activeElement=this;} select(){} setSelectionRange(a,b){this.selectionStart=a;this.selectionEnd=b;} scrollIntoView(){} showModal(){this.open=true;} close(){this.open=false;}
 [inspect.custom](){const id=this.id?`#${this.id}`:"",classes=this.className?`.${this.className.split(/\s+/).filter(Boolean).join(".")}`:"";return `<${this.tagName.toLowerCase()}${id}${classes}>`;}
}
function simple(node,selector){
 if(node.tagName==='#TEXT')return false;
 if(selector.includes(':not(:disabled)')&&node.disabled)return false;
 selector=selector.replaceAll(':not(:disabled)','');
 const tag=selector.match(/^[a-zA-Z][\w-]*/)?.[0];if(tag&&node.tagName!==tag.toUpperCase())return false;
 const id=selector.match(/#([\w-]+)/)?.[1];if(id&&node.id!==id)return false;
 for(const [,cls] of selector.matchAll(/\.([\w-]+)/g))if(!node.classList.contains(cls))return false;
 for(const [,name,,value] of selector.matchAll(/\[([^=\]\s]+)(?:=(['"]?)(.*?)\2)?\]/g)){if(!Object.hasOwn(node.attrs,name))return false;if(value!==undefined&&node.attrs[name]!==value)return false;}
 return true;
}
function matchPath(node,selector){const parts=selector.split(/\s+(?![^\[]*\])/).filter(Boolean);if(!simple(node,parts.pop()||'*'))return false;while(parts.length){const next=parts.pop();node=node.parentElement;while(node&&!simple(node,next))node=node.parentElement;if(!node)return false;}return true;}
 function parse(html,parent,doc){const stack=[parent];for(const token of String(html).match(/<!--[^]*?-->|<[^>]*>|[^<]+/g)||[]){if(token.startsWith('<!--')||token.startsWith('<!'))continue;if(token.startsWith('</')){const tag=token.slice(2).match(/^[\w-]+/)?.[0]?.toUpperCase();let i=stack.length-1;while(i>0&&stack[i].tagName!==tag&&stack[i]._templateHost?.tagName!==tag)i--;if(i>0)stack.length=i;continue;}if(token.startsWith('<')){const tag=token.slice(1).match(/^[\w-]+/)?.[0];if(!tag)continue;const attrs={};const text=token.slice(tag.length+1,-1);for(const [,key,,value] of text.matchAll(/([^\s=\/]+)(?:\s*=\s*(["'])(.*?)\2|\s*=\s*([^\s>]+))?/g))attrs[key]=decode(value||'');const node=new Node(tag,attrs,doc);stack.at(-1).append(node);if(!/^(input|img|br|hr|meta|link)$/i.test(tag)&&!token.endsWith('/>'))stack.push(node.tagName==='TEMPLATE'?node.content:node);}else {const n=new Node('#text',{},doc);n.raw=decode(token);stack.at(-1).append(n);}}}
function createDocument(html){const doc={hidden:false,activeElement:null};doc.root=new Node('document',{},doc);parse(html,doc.root,doc);doc.documentElement=doc.root.querySelector('html')||doc.root;doc.body=doc.root.querySelector('body');doc.querySelector=s=>doc.root.querySelector(s);doc.querySelectorAll=s=>doc.root.querySelectorAll(s);doc.getElementById=id=>doc.querySelector('#'+id);doc.createElement=tag=>new Node(tag,{},doc);doc.addEventListener=(...args)=>doc.root.addEventListener(...args);doc.removeEventListener=(...args)=>doc.root.removeEventListener(...args);doc.dispatchEvent=e=>doc.root.dispatchEvent(e);return doc;}
module.exports={createDocument,Node};
