export async function* lines(body,timeoutMS=0) {
  const reader=body.getReader(), decoder=new TextDecoder()
  let buffered='',finished=false
  try {
    while(true){
      let timer
      const {value,done}=await (timeoutMS?Promise.race([reader.read(),new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error('Mailbox stream heartbeat timed out')),timeoutMS)})]).finally(()=>clearTimeout(timer)):reader.read())
      finished=done
      buffered+=decoder.decode(value||new Uint8Array(),{stream:!done})
      let end
      while((end=buffered.indexOf('\n'))>=0){const line=buffered.slice(0,end);buffered=buffered.slice(end+1);yield line.endsWith('\r')?line.slice(0,-1):line}
      if(done){if(buffered)yield buffered;break}
    }
  }finally{if(!finished)await reader.cancel().catch(()=>{});reader.releaseLock()}
}
export async function readEvents(body,onEvent,timeoutMS=45000){
  let event='',id='',data=[]
  for await(const line of lines(body,timeoutMS)){
    if(line===''){if(data.length)await onEvent({event:event||'message',id,data:data.join('\n')});event='';data=[];continue}
    if(line.startsWith(':'))continue
    const separator=line.indexOf(':'),field=separator<0?line:line.slice(0,separator)
    let value=separator<0?'':line.slice(separator+1);if(value.startsWith(' '))value=value.slice(1)
    if(field==='event')event=value
    if(field==='id'&&!value.includes('\0'))id=value
    if(field==='data')data.push(value)
  }
}
