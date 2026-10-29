export const stores=['snapshots','bodies','bodyUsage','drafts','transactions','settings','meta','folders','cursor','tombstones']
export function open(name){return new Promise((resolve,reject)=>{const request=indexedDB.open(name,4);request.onupgradeneeded=event=>{for(const store of stores)if(!request.result.objectStoreNames.contains(store))request.result.createObjectStore(store,{keyPath:'key'});if(event.oldVersion>0&&event.oldVersion<4)request.transaction.objectStore('bodies').clear()};request.onsuccess=()=>{request.result.onversionchange=()=>request.result.close();resolve(request.result)};request.onblocked=()=>reject(new Error('Close older mail tabs to update local storage.'));request.onerror=()=>reject(request.error)})}
export function read(db,store,key){return new Promise((resolve,reject)=>{const request=db.transaction(store).objectStore(store).get(key);request.onsuccess=()=>resolve(request.result?.value);request.onerror=()=>reject(request.error)})}
export function all(db,store){return new Promise((resolve,reject)=>{const request=db.transaction(store).objectStore(store).getAll();request.onsuccess=()=>resolve(request.result.map(row=>row.value));request.onerror=()=>reject(request.error)})}
export function write(db,store,key,value){return new Promise((resolve,reject)=>{const tx=db.transaction(store,'readwrite');tx.objectStore(store).put({key,value});tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)})}
export function remove(db,store,key){return new Promise((resolve,reject)=>{const tx=db.transaction(store,'readwrite');tx.objectStore(store).delete(key);tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)})}
export function enqueueBatch(db,records,undoStack){return new Promise((resolve,reject)=>{
 const tx=db.transaction(['transactions','settings'],'readwrite'),counter=tx.objectStore('settings').get('queue-order')
 counter.onsuccess=()=>{let order=Math.max(Date.now(),(counter.result?.value||0)+1);for(const record of records){record.created=order++;tx.objectStore('transactions').put({key:record.id,value:record})};tx.objectStore('settings').put({key:'queue-order',value:order-1});if(undoStack!==undefined)tx.objectStore('settings').put({key:'undo',value:undoStack})}
 tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);tx.onabort=()=>reject(tx.error||new Error('Action could not be queued'))
})}
function updateQueued(db,record){return new Promise((resolve,reject)=>{
 const tx=db.transaction(['transactions','cursor'],'readwrite'),store=tx.objectStore('transactions'),request=store.get(record.id);let exists=false
 request.onsuccess=()=>{if(request.result){exists=true;const cursor=tx.objectStore('cursor').get('sync');cursor.onsuccess=()=>{if(record.status==='awaiting-sync'&&record.ackCursor&&record.ackCursor<=(cursor.result?.value||0))store.delete(record.id);else store.put({key:record.id,value:record})}}}
 tx.oncomplete=()=>resolve(exists);tx.onerror=()=>reject(tx.error)
})}
export async function drainQueue(db,{owner,account},emit){
  let progressed=0
  const queued=(await all(db,'transactions')).filter(tx=>tx.status==='pending').sort((a,b)=>a.created-b.created)
  for(const tx of queued){
    let response
    try{response=await fetch(tx.path,{method:tx.method||'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Mail-Owner':owner,'X-Mail-Account':account},body:JSON.stringify(tx.body)})}
    catch{emit({type:'offline'});break}
    if(response.status===401||response.status===403||response.status===409&&response.headers.get('X-Mail-Account-Changed')==='1'){emit({type:'auth-required'});break}
    if(response.status>=500){emit({type:'sync-disconnected',error:'Server is temporarily unavailable; actions remain queued.'});break}
    const result=response.status===204?null:await response.json().catch(()=>null)
    if(!response.ok){tx.status='rejected';tx.error=result?.error||`Request failed (${response.status})`;if(await updateQueued(db,tx))emit({type:'rejected',transaction:tx});progressed++;continue}
    if(tx.kind==='mutation'){tx.status='awaiting-sync';tx.ackCursor=result?.syncId;await updateQueued(db,tx)}else await remove(db,'transactions',tx.id)
    emit({type:'ack',transaction:tx,result})
    progressed++
  }
  const transactions=await all(db,'transactions')
  emit({type:'queue',pending:transactions.filter(tx=>['pending','awaiting-sync'].includes(tx.status)).length,rejected:transactions.filter(tx=>tx.status==='rejected')})
  return progressed
}
