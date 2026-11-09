const observers=new WeakMap()

export function sizeEmailFrame(frame){
  observers.get(frame)?.disconnect()
  const doc=frame.contentDocument
  if(!doc?.body)return
  const resize=()=>{
    if(!frame.isConnected){observer.disconnect();return}
    const style=getComputedStyle(doc.body)
    const height=Math.ceil(Math.max(doc.body.scrollHeight,doc.body.getBoundingClientRect().height)+parseFloat(style.marginTop)+parseFloat(style.marginBottom)+frame.clientHeight-doc.documentElement.clientHeight)+'px'
    if(frame.style.height!==height)frame.style.height=height
  }
  const observer=new ResizeObserver(resize)
  frame.contentWindow.addEventListener('pagehide',()=>observer.disconnect(),{once:true})
  observer.observe(doc.body);observers.set(frame,observer);resize()
}
