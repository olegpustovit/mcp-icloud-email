// Recreate only editor markup; mail HTML and restored outbox bodies never enter the live DOM verbatim.
export function safeRichHTML(html){
  const doc=new DOMParser().parseFromString(html,'text/html'),allowed=new Set(['P','BR','DIV','SPAN','B','STRONG','I','EM','U','UL','OL','LI','BLOCKQUOTE','PRE','CODE','A','H1','H2','H3'])
  function clean(node){
    if(node.nodeType===3)return document.createTextNode(node.textContent)
    if(node.nodeType!==1||['SCRIPT','STYLE','IFRAME','OBJECT','TEMPLATE'].includes(node.tagName))return document.createTextNode('')
    const result=allowed.has(node.tagName)?document.createElement(node.tagName):document.createDocumentFragment()
    if(node.tagName==='A'&&/^(https?:\/\/|mailto:)/i.test(node.getAttribute('href')||'')){result.href=node.getAttribute('href');result.target='_blank';result.rel='noopener noreferrer'}
    for(const child of node.childNodes)result.append(clean(child))
    return result
  }
  const body=document.createElement('div');for(const node of doc.body.childNodes)body.append(clean(node));return body.innerHTML
}
