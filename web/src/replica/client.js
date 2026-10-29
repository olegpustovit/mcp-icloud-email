export function openReplica(account, onEvent) {
  const name=`terminal-mail:${account.email}:${account.icloud_email}:${import.meta.url}`
  const worker=typeof SharedWorker==='function'
    ? new SharedWorker(new URL('./worker.js',import.meta.url),{type:'module',name})
    : new Worker(new URL('./worker.js',import.meta.url),{type:'module',name})
  const port=worker.port||worker
  const pending=new Map();let next=0,closed=false
  port.onmessage=({data})=>{
    if(data.reply){const callback=pending.get(data.reply);if(callback){pending.delete(data.reply);data.error?callback.reject(new Error(data.error)):callback.resolve(data.value)}}
    else onEvent(data)
  }
  const failed=event=>{for(const callback of pending.values())callback.reject(new Error(event.message||'Offline storage worker failed to start'));pending.clear();onEvent({type:'storage-error',error:event.message||'Offline worker unavailable'})}
  worker.onerror=failed
  port.start?.()
  const send=(op,args={})=>new Promise((resolve,reject)=>{if(closed){reject(new Error('Replica closed'));return}const id=++next;pending.set(id,{resolve,reject});port.postMessage({id,op,args})})
  const ready=send('init',{owner:account.email,account:account.icloud_email})
  const call=(op,args)=>ready.then(()=>send(op,args))
  return {call,ready,close(){closed=true;port.postMessage({id:0,op:'disconnect',args:{}});port.close?.();worker.terminate?.();for(const callback of pending.values())callback.reject(new Error('Replica closed'));pending.clear()}}
}
