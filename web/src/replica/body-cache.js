export const BODY_CACHE_LIMIT=200*1024*1024
export function evictedBodies(records,limit=BODY_CACHE_LIMIT){
  let bytes=records.reduce((sum,row)=>sum+row.bytes,0)
  const evicted=[]
  for(const row of [...records].sort((a,b)=>a.at-b.at||a.key.localeCompare(b.key))){
    if(bytes<=limit)break
    bytes-=row.bytes;evicted.push(row.key)
  }
  return evicted
}
export function cacheBody(db,key,value){return new Promise((resolve,reject)=>{
  const tx=db.transaction(['bodies','bodyUsage'],'readwrite'),usage=tx.objectStore('bodyUsage')
  tx.objectStore('bodies').put({key,value});usage.put({key,value:{key,bytes:new TextEncoder().encode(JSON.stringify(value)).byteLength,at:Date.now()}})
  const request=usage.getAll()
  request.onsuccess=()=>{for(const old of evictedBodies(request.result.map(row=>row.value))){tx.objectStore('bodies').delete(old);usage.delete(old)}}
  tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);tx.onabort=()=>reject(tx.error||new Error('Body cache could not be written'))
})}
