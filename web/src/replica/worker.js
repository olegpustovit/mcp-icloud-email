import {stores,open,read as storageRead,all as storageAll,write as storageWrite,remove as storageRemove,drainQueue,enqueueBatch} from './storage.js'
import {lines,readEvents} from './stream.js'
import {address,matches,role,conversations,rebase,searchTokens} from '../model.js'
import {cacheBody} from './body-cache.js'
let db,owner='',account='',draining=false,historyTask,streamController,syncTask,announceTimer,cursor=0,localWrites=Promise.resolve()
let status={phase:'connecting',ready:false,imported:0,total:0},prefs={vip:[]}
const ports=new Set(),metadata=new Map(),folders=new Map(),indexed=new Map(),words=new Map(),bodyRequests=new Map(),tombstones=new Map()
let prefetchTimer,prefetching=false,prefetchQueue=[],cacheWarning=false,bodyGeneration=0
const remoteSearches=new Map(),portQueries=new Map(),bodySearches=new Map()
const broadcast=data=>{for(const port of ports)port.postMessage(data)}
const read=(store,key)=>storageRead(db,store,key)
const all=store=>storageAll(db,store)
const write=(store,key,value)=>storageWrite(db,store,key,value)
const remove=(store,key)=>storageRemove(db,store,key)
const headers=()=>({'X-Mail-Owner':owner,'X-Mail-Account':account})
const rawKey=m=>`${m.folder}|${m.uidValidity}|${m.id}`
const tokenWords=text=>text.toLowerCase().match(/[\p{L}\p{N}@._-]+/gu)||[]
function searchRow(raw){const who=raw.from?.replace(/\s*<[^>]+>/,'')||'';return {...raw,who,em:address(raw.from),subj:raw.subject||'',snip:raw.snippet||'',f:role(raw.folder),un:raw.unread,fl:raw.flagColor,att:raw.attachments?.length||0}}
function indexMessage(raw){
  const old=indexed.get(raw.key)
  if(old)for(const word of old.words){const entries=words.get(word);entries?.delete(raw.key);if(!entries?.size)words.delete(word)}
  const row=searchRow(raw)
  row.words=[...new Set(tokenWords(`${row.subj} ${row.snip} ${row.who} ${row.em} ${(row.to||[]).join(' ')}`))]
  for(const word of row.words){let set=words.get(word);if(!set){set=new Set();words.set(word,set)};set.add(raw.key)}
  metadata.set(raw.key,raw);indexed.set(raw.key,row)
}
function deleteMessage(key){const row=indexed.get(key);if(row)for(const word of row.words){const set=words.get(word);set?.delete(key);if(!set?.size)words.delete(word)};indexed.delete(key);metadata.delete(key)}
function announce(){clearTimeout(announceTimer);announceTimer=setTimeout(()=>broadcast({type:'mirror',syncId:cursor,status,folders:[...folders.values()],prefs}),35)}
async function queueStatus(){const txs=await all('transactions');broadcast({type:'queue',pending:txs.filter(tx=>['pending','awaiting-sync'].includes(tx.status)).length,rejected:txs.filter(tx=>tx.status==='rejected')})}
async function drain(){if(draining||!db)return;draining=true;let progressed=0;try{progressed=await drainQueue(db,{owner,account},broadcast)}catch(err){broadcast({type:'storage-error',error:err.message})}finally{draining=false}if(progressed&&(await all('transactions')).some(tx=>tx.status==='pending'))setTimeout(drain,0)}
function localWrite(run){const pending=localWrites.then(run);localWrites=pending.catch(()=>{});return pending}
async function hydrate(){
  metadata.clear();indexed.clear();words.clear();folders.clear();tombstones.clear()
  cursor=await read('cursor','sync')||0;status=await read('settings','sync')||status;prefs=await read('settings','preferences')||prefs
  for(const row of await all('meta'))indexMessage(row)
  for(const folder of await all('folders'))folders.set(folder.key,folder)
  const records=await new Promise((resolve,reject)=>{const request=db.transaction('tombstones').objectStore('tombstones').getAll();request.onsuccess=()=>resolve(request.result);request.onerror=()=>reject(request.error)})
  for(const row of records)tombstones.set(row.key,row.value)
}
function remapFolder(tx,args){
 const renamed=name=>typeof name==='string'&&(name===args.from||args.delimiter&&name.startsWith(args.from+args.delimiter))?args.to+name.slice(args.from.length):name
 const remap=value=>{const copy={...value};for(const field of ['folder','target','replyFolder','draftFolder'])if(copy[field]!==undefined)copy[field]=renamed(copy[field]);return copy}
 for(const store of ['transactions','drafts']){const scan=tx.objectStore(store).openCursor();scan.onsuccess=()=>{const row=scan.result;if(!row)return;const value=store==='transactions'?{...row.value.value,body:remap(row.value.value.body)}:remap(row.value.value);row.update({key:row.key,value});row.continue()}}
 const history=tx.objectStore('settings').get('undo');history.onsuccess=()=>{if(history.result)tx.objectStore('settings').put({key:'undo',value:history.result.value.map(batch=>batch.map(remap))})}
}
function persistChanges(changes){return localWrite(()=>persist(changes))}
async function persist(changes){
  if(!changes.length)return
  const agents=[],renames=[],arrivals=[];let stale=false,structureChanged=false
  try{
  await new Promise((resolve,reject)=>{
    const tx=db.transaction(['meta','folders','cursor','settings','bodies','bodyUsage','tombstones','transactions','drafts'],'readwrite')
    const stored=tx.objectStore('cursor').get('sync')
    stored.onsuccess=()=>{
    if((stored.result?.value||0)!==cursor){stale=true;tx.abort();return}
    for(const change of changes){
      if(change.syncId<=cursor)continue
      cursor=change.syncId
      if(change.entity==='rename'&&change.rename){remapFolder(tx,change.rename);renames.push(change.rename)}
      if(change.entity==='tx'&&change.txId)tx.objectStore('transactions').delete(change.txId)
      if(change.entity==='msg'){
        if(change.op==='del'){const old=metadata.get(change.key);tombstones.set(change.key,change.syncId);tx.objectStore('tombstones').put({key:change.key,value:change.syncId});if(old?.syncId>change.syncId)continue;if(old){tx.objectStore('bodies').delete(rawKey(old));tx.objectStore('bodyUsage').delete(rawKey(old))}deleteMessage(change.key);tx.objectStore('meta').delete(change.key)}
        else if(change.message){if((metadata.get(change.key)?.syncId||0)>change.syncId)continue;indexMessage(change.message);tx.objectStore('meta').put({key:change.key,value:change.message})}
      }
      if(change.entity==='folder'){
        const previous=folders.get(change.key);if(change.op==='reset'||change.op==='del'||!previous||previous.uidValidity!==change.folder?.uidValidity||previous.role!==change.folder?.role)structureChanged=true
        if((folders.get(change.key)?.syncId||0)>change.syncId)continue
        if(change.op==='reset'||change.op==='del'){
          const name=change.folder?.name||folders.get(change.key)?.name,scan=tx.objectStore('meta').openCursor()
          for(const [key,message]of metadata)if(message.folder===name&&(message.syncId||0)<=change.syncId)deleteMessage(key)
          scan.onsuccess=()=>{const row=scan.result;if(!row)return;const message=row.value.value;if(message.folder===name&&(message.syncId||0)<=change.syncId){tx.objectStore('bodies').delete(rawKey(message));tx.objectStore('bodyUsage').delete(rawKey(message));row.delete()};row.continue()}
        }
        if(change.op==='del'){folders.delete(change.key);tx.objectStore('folders').delete(change.key)}
        else if(change.folder){folders.set(change.key,change.folder);tx.objectStore('folders').put({key:change.key,value:change.folder})}
      }
      if(change.entity==='status'&&change.status){status=change.status;tx.objectStore('settings').put({key:'sync',value:status})}
      if(change.entity==='pref'&&change.preferences){prefs=change.preferences;tx.objectStore('settings').put({key:'preferences',value:prefs})}
      cursor=change.syncId
      if(change.actor?.startsWith('mcp:'))agents.push(change)
      if(change.arrival&&change.message&&Date.now()-Date.parse(change.at)<300000)arrivals.push(change.message)
    }
    tx.objectStore('cursor').put({key:'sync',value:cursor})
    }
    tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);tx.onabort=()=>reject(tx.error||new Error('Local sync transaction failed'))
  })
  }catch(err){await hydrate();if(stale)return persist(changes);throw err}
  if(structureChanged)restartSearches()
  for(const rename of renames)broadcast({type:'folder-remapped',...rename})
  for(const change of agents)broadcast({type:'agent-change',change})
  for(const message of arrivals)broadcast({type:'mail-arrival',message})
  await queueStatus();announce()
}
async function bootstrap(signal){
  const response=await fetch('/api/mail/sync/bootstrap',{credentials:'same-origin',headers:headers(),signal})
  if(!response.ok)throw new Error(`Mailbox bootstrap failed (${response.status})`)
  const records=[]
  for await(const line of lines(response.body)){if(line.trim())records.push(JSON.parse(line))}
  const start=records.find(row=>row.type==='bootstrap'),end=records.find(row=>row.type==='ready')
  if(!start||!end)throw new Error('Mailbox bootstrap was interrupted')
  const mail=records.filter(row=>row.type==='msg').map(row=>row.message),boxes=records.filter(row=>row.type==='folder').map(row=>row.folder)
  await new Promise((resolve,reject)=>{
    const tx=db.transaction(['meta','folders','cursor','settings','transactions','tombstones'],'readwrite');tx.objectStore('meta').clear();tx.objectStore('folders').clear();tx.objectStore('tombstones').clear()
    for(const message of mail)tx.objectStore('meta').put({key:message.key,value:message})
    for(const folder of boxes)tx.objectStore('folders').put({key:folder.key,value:folder})
    const queued=tx.objectStore('transactions').openCursor();queued.onsuccess=()=>{const row=queued.result;if(!row)return;const transaction=row.value.value;if(transaction.status==='awaiting-sync'&&transaction.ackCursor<=end.syncId)row.delete();row.continue()};tx.objectStore('cursor').put({key:'sync',value:end.syncId});tx.objectStore('settings').put({key:'sync',value:start.status});tx.objectStore('settings').put({key:'preferences',value:start.prefs})
    tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)
  })
  metadata.clear();indexed.clear();words.clear();folders.clear();tombstones.clear();await write('settings','history',{complete:false,before:''});for(const message of mail)indexMessage(message);for(const folder of boxes)folders.set(folder.key,folder)
  cursor=end.syncId;status=start.status;prefs=start.prefs;announce()
}
async function synchronize(){
  if(syncTask)return syncTask
  streamController=new AbortController();const signal=streamController.signal
  syncTask=(async()=>{
    let backoff=500,needsBootstrap=!(await read('settings','bootstrapped'))
    while(!signal.aborted){
      try{
        if(needsBootstrap){await historyTask?.catch(()=>{});await localWrite(()=>bootstrap(signal));await write('settings','bootstrapped',true);needsBootstrap=false}
        const response=await fetch('/api/mail/sync/stream?'+new URLSearchParams({cursor}),{credentials:'same-origin',headers:headers(),signal})
        if(response.status===401||response.status===403||response.headers.get('X-Mail-Account-Changed')==='1'){broadcast({type:'auth-required'});return}
        if(!response.ok)throw new Error(`Mailbox stream failed (${response.status})`)
        backoff=500;broadcast({type:'sync-connected'});backfillHistory(signal).catch(err=>{if(!signal.aborted)broadcast({type:'sync-disconnected',error:err.message})})
        await readEvents(response.body,async event=>{
          if(event.event==='reset'){needsBootstrap=true;return}
          if(event.event==='auth_required'||event.event==='account_changed'){broadcast({type:'auth-required'});streamController.abort();return}
          if(event.event==='change')await persistChanges([JSON.parse(event.data)])
        })
      }catch(err){if(signal.aborted)return;broadcast({type:'sync-disconnected',error:err.message})}
      if(signal.aborted)return
      await new Promise(resolve=>{const timer=setTimeout(resolve,needsBootstrap?0:backoff);signal.addEventListener('abort',()=>{clearTimeout(timer);resolve()},{once:true})})
      backoff=Math.min(backoff*2,30000)
    }
  })().finally(()=>{syncTask=null})
  return syncTask
}
async function backfillHistory(signal){
 if(historyTask)return historyTask
 historyTask=(async()=>{
  let progress=await read('settings','history')||{complete:false,before:''}
  while(!progress.complete&&!signal.aborted){
   const response=await fetch('/api/mail/sync/history?'+new URLSearchParams({before:progress.before||''}),{credentials:'same-origin',headers:headers(),signal})
   if(!response.ok)throw new Error(`Mailbox history failed (${response.status})`)
   const page=await response.json();if(signal.aborted)return
   await localWrite(async()=>{
    if(signal.aborted)return
    const incoming=(page.messages||[]).filter(message=>(metadata.get(message.key)?.syncId||0)<=(message.syncId||0)&&(tombstones.get(message.key)||0)<=(message.syncId||0)&&[...folders.values()].some(folder=>folder.name===message.folder&&folder.uidValidity===message.uidValidity))
    progress={complete:page.complete,before:page.next}
    await new Promise((resolve,reject)=>{const tx=db.transaction(['meta','settings'],'readwrite');for(const message of incoming)tx.objectStore('meta').put({key:message.key,value:message});tx.objectStore('settings').put({key:'history',value:progress});tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);tx.onabort=()=>reject(tx.error||new Error('History storage failed'))})
    for(const message of incoming)indexMessage(message)
   })
   announce()
  }
 })().finally(()=>{historyTask=null});return historyTask
}
async function query(args){
  const started=performance.now(),name=args.folder||'inbox',smart=!!args.search||['flagged','vip','*'].includes(name),physical=[...folders.values()].find(folder=>folder.role===name)?.name||name
  const text=args.search||'',plain=tokenWords(text.replace(/\b\w+:(?:"[^"]*"|\S+)/g,''))
  const localBody=bodySearches.get(text),remote=remoteSearches.get(text)
  let candidates
  for(const term of plain){const found=new Set();for(const [word,keys]of words)if(word.includes(term))for(const key of keys)found.add(key);candidates=candidates?new Set([...candidates].filter(key=>found.has(key))):found}
  const matchesRow=row=>{
    if(!smart&&row.folder!==physical)return false
    if(!text&&name==='flagged'&&!row.fl)return false
    if(!text&&name==='vip'&&!(args.vip||prefs.vip||[]).includes(row.em))return false
    if(text&&!searchTokens(text).some(token=>token.toLowerCase().startsWith('in:'))&&['trash','junk'].includes(row.f))return false
    if(args.filter==='unread'&&!row.un||args.filter==='flagged'&&!row.fl||args.filter==='att'&&!row.att)return false
    return !text||matches(row,text,'',localBody?.hits.has(rawKey(row))||remote?.rows.has(row.key))
  }
  const pending=await all('transactions')
  const corpus=new Map(indexed),roles=new Map([...folders.values()].map(folder=>[folder.name,folder]))
  if(remote)for(const [key,raw]of remote.rows){if((tombstones.get(key)||0)>raw.syncId||roles.get(raw.folder)?.uidValidity!==raw.uidValidity)continue;if(!corpus.has(key)||(corpus.get(key).syncId||0)<=raw.syncId)corpus.set(key,searchRow(raw))}
  const projected=rebase([...corpus.values()].map(row=>({...row,uid:row.id,f:roles.get(row.folder)?.role||row.f,folderRole:roles.get(row.folder)?.role||row.f})),pending).map(row=>({...row,unread:row.un,flagColor:row.fl}))
  const result=conversations(projected.filter(row=>(!candidates||candidates.has(row.key)||localBody?.hits.has(rawKey(row))||remote?.rows.has(row.key))&&matchesRow(row)),projected)
  if(!text&&name==='snoozed')result.sort((a,b)=>(a.snoozeUntil?Date.parse(a.snoozeUntil):Infinity)-(b.snoozeUntil?Date.parse(b.snoozeUntil):Infinity))

  const folder=[...folders.values()].find(f=>f.name===physical)
  const unfiltered=!text&&(!args.filter||args.filter==='all')&&!smart
  const total=unfiltered&&folder?folder.total:result.length
  prefetchQueue=result.filter(row=>!row.pendingLocation).slice(0,30);clearTimeout(prefetchTimer);prefetchTimer=setTimeout(prefetch,1500)
  return {emails:result.slice(args.offset||0,(args.offset||0)+(args.limit||100)).map(row=>text?{...row,searchQuery:text}:row),search:text?{localMS:Math.round(performance.now()-started),bodyMS:localBody?.ms??null,server:remote?{running:remote.running,ms:remote.ms,error:remote.error}:null}:null,total,loaded:result.length,partial:!!text&&remote?.running||(!smart&&folder?!folder.complete:status.phase==='importing'),folder:physical,unread:folder?.unread||0}
}
async function searchBodies(text){
 const started=performance.now(),roles=new Map([...folders.values()].map(folder=>[folder.name,folder.role])),locations=new Map([...indexed.values()].map(row=>[rawKey(row),{...row,folderRole:roles.get(row.folder)}])),found=new Set()
 await new Promise((resolve,reject)=>{const request=db.transaction('bodies').objectStore('bodies').openCursor();request.onsuccess=()=>{const cursor=request.result;if(!cursor){resolve();return};const row=locations.get(cursor.key),body=cursor.value.value;if(row&&matches(row,text,body.bodyPlain||body.bodyHTML?.replace(/<[^>]*>/g,' ').replace(/&(?:amp|lt|gt|quot|#39);/g,entity=>({'&amp;':'&','&lt;':'<','&gt;':'>','&quot;':'"','&#39;':"'"})[entity])||''))found.add(rawKey(row));cursor.continue()};request.onerror=()=>reject(request.error)})
 bodySearches.set(text,{hits:found,ms:Math.round(performance.now()-started)});pruneSearches()
 return true
}
let searchRestartTimer
function restartSearches(){
 for(const entry of remoteSearches.values()){entry.controller.abort();entry.running=false}
 clearTimeout(searchRestartTimer);searchRestartTimer=setTimeout(()=>{
  const queries=new Map();for(const [port,text]of portQueries)queries.set(text,port)
  for(const [text,port]of queries)searchBodies(text).then(()=>{if(ports.has(port)&&portQueries.get(port)===text){broadcast({type:'search-ready',search:text});if(navigator.onLine)searchRemote(text,port,true)}}).catch(err=>broadcast({type:'storage-error',error:err.message}))
 },150)
}
function pruneSearches(){
 const active=new Set(portQueries.values())
 for(const cache of [bodySearches,remoteSearches])for(const [key,entry]of cache)if(cache.size>20&&!active.has(key)){entry.controller?.abort();cache.delete(key)}
}
function cancelPortSearch(port){
 const text=portQueries.get(port);portQueries.delete(port);const entry=remoteSearches.get(text)
 if(entry){entry.ports.delete(port);if(!entry.ports.size){entry.controller.abort();entry.running=false}}
}
async function searchRemote(text,port,retry=false){
 let entry=remoteSearches.get(text)
 if(portQueries.get(port)!==text)cancelPortSearch(port)
 portQueries.set(port,text)
 if(entry?.running&&!retry){entry.ports.add(port);return}
 entry?.controller.abort()
 const controller=new AbortController(),started=performance.now(),consumers=new Set([...portQueries].filter(([,query])=>query===text).map(([port])=>port));consumers.add(port)
 entry={controller,ports:consumers,rows:new Map(),running:true,ms:null,error:''};remoteSearches.set(text,entry);pruneSearches();broadcast({type:'search-ready',search:text})
 try{
  let next='',resets=0
  do{
   const response=await fetch('/api/mail/search?'+new URLSearchParams({q:text,cursor:next}),{credentials:'same-origin',headers:headers(),signal:controller.signal})
   if(!response.ok)throw new Error(response.status===400?'Check the search operators and date format.':'Server search is unavailable. Local results are still shown.')
   const page=await response.json();if(controller.signal.aborted)return
   if(page.reset){if(++resets>3)throw new Error('The mailbox changed during search. Retry after sync finishes.');entry.rows.clear();next='';entry.running=true;continue}
   for(const raw of page.messages||[])if((tombstones.get(raw.key)||0)<=(page.syncId||0))entry.rows.set(raw.key,{...raw,syncId:page.syncId||0})
   next=page.next||'';entry.running=!!next;entry.ms=Math.round(performance.now()-started);broadcast({type:'search-ready',search:text})
   if(next)await new Promise(resolve=>setTimeout(resolve,150))
  }while((next||entry.running)&&!controller.signal.aborted&&entry.ports.size)
 }catch(err){if(!controller.signal.aborted){entry.running=false;entry.ms=Math.round(performance.now()-started);entry.error=err.message;broadcast({type:'search-ready',search:text})}}
}
async function prefetch(){
  if(prefetching||!ports.size||!navigator.onLine)return
  prefetching=true
  try{while(prefetchQueue.length&&ports.size&&navigator.onLine){const m=prefetchQueue.shift();await body({key:rawKey(m),folder:m.folder,id:m.id,validity:m.uidValidity});await new Promise(resolve=>setTimeout(resolve,80))}}catch{}finally{prefetching=false}
}
async function body(args){
  const generation=bodyGeneration,cached=await read('bodies',args.key);if(generation!==bodyGeneration)return body(args);if(cached){const usage=await read('bodyUsage',args.key);if(usage)await write('bodyUsage',args.key,{...usage,at:Date.now()});return cached}
  if(bodyRequests.get(args.key)?.generation===generation)return bodyRequests.get(args.key)
  const pending=(async()=>{
    const response=await fetch('/api/mail/message?'+new URLSearchParams({folder:args.folder,id:args.id,validity:args.validity}),{credentials:'same-origin',headers:headers(),signal:streamController?.signal})
    if(!response.ok){const problem=await response.json().catch(()=>({}));throw new Error(problem.error||'Message body could not be loaded')}
    const value=await response.json();if(generation!==bodyGeneration)throw new Error('Body cache changed; reopen the message.');try{await cacheBody(db,args.key,value);cacheWarning=false}catch(err){if(!cacheWarning)broadcast({type:'storage-error',error:'Message bodies could not be cached. '+err.message});cacheWarning=true}return value
  })().finally(()=>{if(bodyRequests.get(args.key)===pending)bodyRequests.delete(args.key)});pending.generation=generation;bodyRequests.set(args.key,pending);return pending
}
async function handle(op,args,port){
  if(op==='init'){
    if(db&&(owner!==args.owner||account!==args.account))throw new Error('Replica account mismatch')
    owner=args.owner;account=args.account;if(!db)db=await open('terminal-mail:'+owner+':'+account)
    await write('settings','identity',{owner,account})
    await localWrite(hydrate)
    await queueStatus();announce();drain();synchronize();return true
  }
  if(!db)throw new Error('Replica is not ready')
  if(op==='get')return read(args.store,args.key)
  if(op==='transactions')return all('transactions')
  if(op==='query')return query(args)
  if(op==='search-bodies'){if(portQueries.get(port)!==args.search)cancelPortSearch(port);portQueries.set(port,args.search);return searchBodies(args.search)}
  if(op==='remote-search'){searchRemote(args.search,port,args.retry);return true}
  if(op==='cancel-search'){cancelPortSearch(port);return true}
  if(op==='folders')return {folders:[...folders.values()].map(folder=>folder.name),details:[...folders.values()],sync:status}
  if(op==='contacts'){
    const contacts=new Map(),query=(args.query||'').toLowerCase()
    for(const m of metadata.values())for(const raw of [m.from,...m.to||[],...m.cc||[]]){if(raw&&raw.toLowerCase().includes(query))contacts.set(address(raw).toLowerCase(),raw)}
    return [...contacts.values()].sort((a,b)=>a.localeCompare(b)).slice(0,12)
  }
  if(op==='identity-suggestions'){
    const counts=new Map();for(const m of metadata.values())for(const raw of [...m.to||[],...m.cc||[]]){const who=address(raw);if(who&&who.toLowerCase()!==account.toLowerCase())counts.set(who,(counts.get(who)||0)+1)}
    return [...counts].sort((a,b)=>b[1]-a[1]).slice(0,12).map(([address,count])=>({address,count}))
  }
  if(op==='find-message')return [...metadata.values()].find(m=>m.folder===args.folder&&m.id===args.id&&m.uidValidity===args.validity)||null
  if(op==='body')return body(args)
  if(op==='prefetch'){prefetchQueue=args.keys.map(key=>[...metadata.values()].find(m=>rawKey(m)===key)).filter(Boolean).concat(prefetchQueue);clearTimeout(prefetchTimer);prefetchTimer=setTimeout(prefetch,80);return true}
  if(op==='clear-bodies'){bodyGeneration++;bodySearches.clear();await localWrite(()=>new Promise((resolve,reject)=>{const tx=db.transaction(['bodies','bodyUsage'],'readwrite');tx.objectStore('bodies').clear();tx.objectStore('bodyUsage').clear();tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)}));broadcast({type:'bodies-cleared'});return true}
  if(op==='remap-folder'){
    await localWrite(()=>new Promise((resolve,reject)=>{const tx=db.transaction(['transactions','drafts','settings'],'readwrite');remapFolder(tx,args);tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);tx.onabort=()=>reject(tx.error||new Error('Folder references could not be updated'))}))
    broadcast({type:'folder-remapped',...args});return true
  }
  if(op==='put'){await write(args.store,args.key,args.value);return true}
  if(op==='remove'){await remove(args.store,args.key);return true}
  if(op==='queue'||op==='queue-batch'){
    const records=(op==='queue'?[args]:args.transactions).map(input=>{const id=input.id||crypto.randomUUID(),tx={...input,id,status:'pending'};if(tx.kind==='mutation')tx.body={...tx.body,transactionId:id};return tx})
    await enqueueBatch(db,records,op==='queue-batch'?args.undoStack:undefined);await queueStatus();drain();return op==='queue'?records[0].id:records.map(tx=>tx.id)
  }
  if(op==='retry'){drain();if(!syncTask)synchronize();return true}
  if(op==='refresh'){streamController?.abort();await syncTask;await historyTask?.catch(()=>{});await remove('settings','bootstrapped');synchronize();return true}
  if(op==='discard-rejected'){await remove('transactions',args.id);await queueStatus();return true}
  if(op==='clear'){
    streamController?.abort();for(const entry of remoteSearches.values())entry.controller.abort();remoteSearches.clear();portQueries.clear();bodySearches.clear();await syncTask
    await Promise.all(stores.map(store=>new Promise((resolve,reject)=>{const tx=db.transaction(store,'readwrite');tx.objectStore(store).clear();tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)})))
    metadata.clear();indexed.clear();words.clear();folders.clear();cursor=0;broadcast({type:'cleared'});return true
  }
  throw new Error('Unknown replica operation')
}
function connect(port){ports.add(port);port.onmessage=async({data})=>{
  if(data.op==='disconnect'){cancelPortSearch(port);ports.delete(port);if(!ports.size)streamController?.abort();port.close?.();return}
  try{const value=await handle(data.op,data.args,port);port.postMessage({reply:data.id,value})}catch(err){port.postMessage({reply:data.id,error:err.message})}
};port.start?.()}
self.onconnect=event=>connect(event.ports[0])
self.onmessage=event=>{if(!ports.has(self))connect(self);self.onmessage(event)}
setInterval(()=>{if(db)drain()},10000)
