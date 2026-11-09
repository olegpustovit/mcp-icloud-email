const observers=new WeakMap()

export function sizeEmailFrame(frame){
  observers.get(frame)?.disconnect()
  const doc=frame.contentDocument
  if(!doc?.body)return
  const resize=()=>{
    if(!frame.isConnected){observer.disconnect();return}
    const style=getComputedStyle(doc.body)
    frame.style.height=Math.ceil(Math.max(doc.body.scrollHeight,doc.body.getBoundingClientRect().height)+parseFloat(style.marginTop)+parseFloat(style.marginBottom))+'px'
  }
  const observer=new ResizeObserver(resize)
  frame.contentWindow.addEventListener('pagehide',()=>observer.disconnect(),{once:true})
  observer.observe(doc.body);observers.set(frame,observer);resize()
}
