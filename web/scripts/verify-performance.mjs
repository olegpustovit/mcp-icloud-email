import assert from 'node:assert/strict'
import {createServer} from 'node:http'
import {readFile,writeFile,mkdir} from 'node:fs/promises'
import {resolve,extname} from 'node:path'
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE||'playwright')
const root=resolve('.'),streams=new Set(),bodyRequests=[]
const server=createServer(async(req,res)=>{
 const url=new URL(req.url,'http://localhost')
 if(url.pathname==='/api/mail/sync/stream'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write(': connected\n\n');streams.add(res);req.on('close',()=>streams.delete(res));return}
 if(url.pathname==='/api/mail/sync/history'){res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify({messages:[],complete:true}));return}
 if(url.pathname==='/test/requests'){res.setHeader('Content-Type','application/json');res.end(JSON.stringify(bodyRequests));return}
 if(url.pathname==='/test/sync'){for(const stream of streams)stream.write('event: change\ndata: '+JSON.stringify({syncId:50001,entity:'status',status:{phase:'live',ready:true}})+'\n\n');res.end();return}
 if(url.pathname==='/api/mail/message'){
  const id=url.searchParams.get('id');bodyRequests.push({id,prefetch:url.searchParams.get('prefetch')})
  if(id==='100000'){res.writeHead(404,{'Content-Type':'application/json'});res.end('{}');return}
  if(['100001','100005'].includes(id))await new Promise(resolve=>setTimeout(resolve,400))
  res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify({bodyPlain:'Cached message body.'}));return
 }
 if(url.pathname==='/'){res.end('<!doctype html><html><link rel="stylesheet" href="/src/style.css"><body><main id="root"></main></body></html>');return}
 const path=resolve(root,'.'+url.pathname);if(!path.startsWith(root+'/')){res.writeHead(403);res.end();return}
 try{const data=await readFile(path);res.setHeader('Content-Type',({'.js':'text/javascript','.css':'text/css','.woff2':'font/woff2'})[extname(path)]||'application/octet-stream');res.end(data)}catch{res.writeHead(404);res.end()}
})
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
const browser=await chromium.launch({...(process.env.CHROME_BIN?{executablePath:process.env.CHROME_BIN}:{}),args:['--no-sandbox']})
try{
 const page=await browser.newPage({viewport:{width:1440,height:900}})
 await page.goto('http://127.0.0.1:'+server.address().port)
 const result=await page.evaluate(async()=>{
  const {open}=await import('/src/replica/storage.js'),{openReplica}=await import('/src/replica/client.js'),{mailView,paintSelection}=await import('/src/views.js'),{message}=await import('/src/model.js'),{revealRow}=await import('/src/list-window.js'),{startupWindow}=await import('/src/startup-window.js')
  const account={email:'performance@olegpustovit.com',icloud_email:'performance@icloud.com',connected:true},db=await open('terminal-mail:'+account.email+':'+account.icloud_email),now=Date.now(),count=50000
  await new Promise((resolve,reject)=>{const tx=db.transaction(['meta','folders','settings','cursor','bodies','snapshots'],'readwrite');for(let i=0;i<count;i++){const row={key:'msg-'+i,id:String(i+1),uidValidity:7,folder:i<10000?'INBOX':'Archive',from:'Sender '+(i%100)+' <sender'+(i%100)+'@example.com>',to:[account.icloud_email],subject:(i%9?'Build report ':'Needle report ')+i,snippet:'A readable message preview with realistic search terms.',date:new Date(now-i*60000).toISOString(),unread:i%3===0,threadId:'thread-'+Math.floor(i/2),syncId:i+1};tx.objectStore('meta').put({key:row.key,value:row})}
   for(const [name,role,total]of [['INBOX','inbox',10000],['Archive','archive',40000]])tx.objectStore('folders').put({key:name,value:{key:name,name,role,total,unread:3334,uidValidity:7,complete:true,ready:true}})
   for(const [key,value]of Object.entries({identity:{owner:account.email,account:account.icloud_email},bootstrapped:true,history:{complete:true},sync:{phase:'live',ready:true},preferences:{theme:'light',density:'default',keyPreset:'gmail',vip:[]}}))tx.objectStore('settings').put({key,value})
   tx.objectStore('snapshots').put({key:'inbox',value:{msgs:Array.from({length:100},(_,i)=>message({key:'msg-'+i,id:String(i+1),uidValidity:7,folder:'INBOX',from:'Sender <sender@example.com>',subject:'Cached inbox '+i,date:new Date(now-i*60000).toISOString(),unread:true},'INBOX')),total:10000}});tx.objectStore('cursor').put({key:'sync',value:count});tx.objectStore('bodies').put({key:'INBOX|7|1',value:{bodyPlain:'Cached message body.'}});tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)
  });db.close()
  const updates=[]
  const firstAt=performance.now(),cached=await startupWindow(account,'inbox');if(!cached)throw new Error('Cached window unavailable');document.getElementById('root').innerHTML=mailView({account,folder:'inbox',msgs:cached.snapshot.msgs,folders:cached.folders.map(f=>f.name),folderInfo:cached.folders,total:cached.snapshot.total,prefs:cached.prefs,counts:{inbox:3334},bodies:{},cur:0,picked:[],filter:'all',search:'',focus:'list',view:'mail',side:true,theme:'light',outbox:[]});document.querySelector('.row')?.getBoundingClientRect();const firstWindowMS=performance.now()-firstAt;const started=performance.now(),replica=openReplica(account,event=>updates.push(event));await replica.ready;const hydrateMS=performance.now()-started
  const timings=[],queries=[];let rows
  for(const search of ['', 'needle','from:sender7','subject:"Build report"','needle']){const at=performance.now();rows=await replica.call('query',{folder:'inbox',filter:'all',search,limit:100});queries.push({search,ms:performance.now()-at,total:rows.loaded,workerMS:rows.search?.localMS})}
  rows=await replica.call('query',{folder:'inbox',filter:'all',search:'',limit:1000})
  const state={account,folder:'inbox',msgs:rows.emails.map(row=>message(row,row.folder)),folders:['INBOX','Archive'],folderInfo:[],total:rows.total,loaded:rows.loaded,prefs:{theme:'light',keyPreset:'gmail'},counts:{inbox:3334},bodies:{},cur:0,picked:[],filter:'all',search:'',focus:'list',view:'mail',side:true,theme:'light',sync:{phase:'live',mode:'IDLE'},outbox:[]}
  const root=document.getElementById('root');root.innerHTML=mailView(state);state.listHeight=root.querySelector('.rows').clientHeight
  for(let i=0;i<30;i++){state.cur=i;const at=performance.now();revealRow(state.msgs,state);if(!paintSelection(root,state))throw new Error('Selection render unavailable');const selected=document.querySelector('.row.cur');if(Number(selected?.dataset.i)!==i)throw new Error('Selected row was not mounted');selected.getBoundingClientRect();timings.push(performance.now()-at)}
  const bodyAt=performance.now();await replica.call('body',{key:'INBOX|7|1',folder:'INBOX',id:'1',validity:7});const cachedBodyMS=performance.now()-bodyAt
  const mountedRows=document.querySelectorAll('.row').length;if(document.querySelector('.row').getBoundingClientRect().height!==44)throw new Error('Row geometry does not match virtual offsets')
  const until=async(check)=>{const deadline=performance.now()+5000;while(!await check()){if(performance.now()>deadline)throw new Error('Prefetch/sync regression timed out');await new Promise(resolve=>setTimeout(resolve,10))}}
  await until(()=>updates.some(event=>event.type==='sync-connected'))
  const syncAt=performance.now();await fetch('/test/sync');await until(()=>updates.some(event=>event.type==='mirror'&&event.syncId===50001));const syncMS=performance.now()-syncAt
  const raw=id=>({folder:'INBOX',uidValidity:7,id}),slow=raw('100001'),latest=raw('100002')
  await replica.call('prefetch',{messages:[raw('100000'),slow,{...raw('100003'),threadMessages:[raw('100003'),latest]}]})
  const requests=()=>fetch('/test/requests').then(response=>response.json())
  await until(async()=>(await requests()).some(row=>row.id===slow.id))
  await replica.call('retry')
  await Promise.all([replica.call('body',{key:'INBOX|7|100001',folder:'INBOX',id:slow.id,validity:7}),replica.call('body',{key:'INBOX|7|100001',folder:'INBOX',id:slow.id,validity:7})])
  await until(()=>replica.call('get',{store:'bodies',key:'INBOX|7|100002'}))
  const warmAt=performance.now();await replica.call('body',{key:'INBOX|7|100002',folder:'INBOX',id:latest.id,validity:7});const prefetchedBodyMS=performance.now()-warmAt
  const fetched=(await requests()).filter(row=>Number(row.id)>=100000)
  if(fetched.filter(row=>row.id===slow.id).length!==1)throw new Error('Concurrent opens duplicated the prefetch request')
  if(fetched.some(row=>row.id==='100003'||row.prefetch!=='1'))throw new Error('Prefetch fetched the wrong thread member or used foreground priority')
  await replica.call('prefetch',{messages:[raw('100005')]});await until(async()=>(await requests()).some(row=>row.id==='100005'))
  await replica.call('clear-bodies');await new Promise(resolve=>setTimeout(resolve,500))
  if(await replica.call('get',{store:'bodies',key:'INBOX|7|100005'}))throw new Error('Clearing bodies allowed an in-flight prefetch to repopulate private content')
  replica.close();return {messages:count,firstWindowMS,hydrateMS,queries,keyPaintP95MS:timings.sort((a,b)=>a-b)[Math.floor(timings.length*.95)],cachedBodyMS,prefetchedBodyMS,syncMS,mountedRows}
 })
 await mkdir('../.impeccable/review',{recursive:true});await writeFile('../.impeccable/review/performance.json',JSON.stringify(result,null,2)+'\n');console.log(JSON.stringify(result,null,2))
 assert(result.firstWindowMS<300,'cached inbox exceeds 300ms');assert(result.mountedRows<=60,'list DOM grows with loaded history')
 assert(result.cachedBodyMS<50,'cached message exceeds 50ms')
 assert(result.prefetchedBodyMS<50,'prefetched message exceeds 50ms');assert(result.syncMS<250,'live sync notification exceeds 250ms')
 assert(result.keyPaintP95MS<16,'keypress paint exceeds 16ms')
}finally{await browser.close();for(const stream of streams)stream.end();server.closeAllConnections();await new Promise(resolve=>server.close(resolve))}
