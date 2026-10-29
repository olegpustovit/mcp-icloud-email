// Patch the live DOM toward new markup so unchanged nodes (iframes, images, scroll, hover) survive re-renders.
export function morph(target,html){
  const next=document.createElement('template');next.innerHTML=html
  patchChildren(target,next.content)
}
const key=node=>node.nodeType===1?node.id||node.dataset.id||node.dataset.threadKey||'':''
function same(a,b){return a.nodeType===b.nodeType&&a.nodeName===b.nodeName&&key(a)===key(b)}
function patchChildren(target,source){
  const old=[...target.childNodes],fresh=[...source.childNodes]
  fresh.forEach((node,i)=>{
    const live=old[i]
    if(live&&same(live,node))patch(live,node)
    else if(live)live.replaceWith(node)
    else target.append(node)
  })
  for(const extra of old.slice(fresh.length))extra.remove()
}
function patch(live,node){
  if(live.nodeType!==1){if(live.nodeValue!==node.nodeValue)live.nodeValue=node.nodeValue;return}
  for(const {name} of [...live.attributes])if(!node.hasAttribute(name))live.removeAttribute(name)
  for(const {name,value} of node.attributes)if(live.getAttribute(name)!==value)live.setAttribute(name,value)
  const editing=live===document.activeElement
  if(live.nodeName==='INPUT'){if(!editing){live.checked=node.hasAttribute('checked');if(live.type!=='file')live.value=node.getAttribute('value')??''};return}
  if(live.nodeName==='TEXTAREA'){if(!editing)live.value=node.textContent;return}
  if(editing&&live.isContentEditable)return
  if(live.nodeName==='SELECT'){const value=live.value;patchChildren(live,node);if(editing)live.value=value;else live.value=node.querySelector('[selected]')?.value??live.options[0]?.value??'';return}
  patchChildren(live,node)
}
