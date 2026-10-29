import assert from 'node:assert/strict'
import {createServer} from 'node:http'
import {readFile,writeFile,mkdir} from 'node:fs/promises'
import {resolve,extname} from 'node:path'
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE||'playwright')
const root=resolve('.'),streams=new Set()
const server=createServer(async(req,res)=>{
 const url=new URL(req.url,'http://localhost')
 if(url.pathname==='/api/mail/sync/stream'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write(': connected\n\n');streams.add(res);req.on('close',()=>streams.delete(res));return}
 if(url.pathname==='/api/mail/sync/history'){res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify({messages:[],complete:true}));return}
 if(url.pathname==='/api/mail/message'){res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify({bodyPlain:'Cached message body.'}));return}
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
  const firstAt=performance.now(),cached=await startupWindow(account,'inbox');if(!cached)throw new Error('Cached window unavailable');document.getElementById('root').innerHTML=mailView({account,folder:'inbox',msgs:cached.snapshot.msgs,folders:cached.folders.map(f=>f.name),folderInfo:cached.folders,total:cached.snapshot.total,prefs:cached.prefs,counts:{inbox:3334},bodies:{},cur:0,picked:[],filter:'all',search:'',focus:'list',view:'mail',side:true,theme:'light',outbox:[]});document.querySelector('.row')?.getBoundingClientRect();const firstWindowMS=performance.now()-firstAt;const started=performance.now(),replica=openReplica(account,()=>{});await replica.ready;const hydrateMS=performance.now()-started
  const timings=[],queries=[];let rows
  for(const search of ['', 'needle','from:sender7','subject:"Build report"','needle']){const at=performance.now();rows=await replica.call('query',{folder:'inbox',filter:'all',search,limit:100});queries.push({search,ms:performance.now()-at,total:rows.loaded,workerMS:rows.search?.localMS})}
  rows=await replica.call('query',{folder:'inbox',filter:'all',search:'',limit:1000})
  const state={account,folder:'inbox',msgs:rows.emails.map(row=>message(row,row.folder)),folders:['INBOX','Archive'],folderInfo:[],total:rows.total,loaded:rows.loaded,prefs:{theme:'light',keyPreset:'gmail'},counts:{inbox:3334},bodies:{},cur:0,picked:[],filter:'all',search:'',focus:'list',view:'mail',side:true,theme:'light',sync:{phase:'live',mode:'IDLE'},outbox:[]}
  const root=document.getElementById('root');root.innerHTML=mailView(state);state.listHeight=root.querySelector('.rows').clientHeight
  for(let i=0;i<30;i++){state.cur=i;const at=performance.now();revealRow(state.msgs,state);if(!paintSelection(root,state))throw new Error('Selection render unavailable');const selected=document.querySelector('.row.cur');if(Number(selected?.dataset.i)!==i)throw new Error('Selected row was not mounted');selected.getBoundingClientRect();timings.push(performance.now()-at)}
  const bodyAt=performance.now();await replica.call('body',{key:'INBOX|7|1',folder:'INBOX',id:'1',validity:7});const cachedBodyMS=performance.now()-bodyAt
  const mountedRows=document.querySelectorAll('.row').length;if(document.querySelector('.row').getBoundingClientRect().height!==60)throw new Error('Row geometry does not match virtual offsets')
  replica.close();return {messages:count,firstWindowMS,hydrateMS,queries,keyPaintP95MS:timings.sort((a,b)=>a-b)[Math.floor(timings.length*.95)],cachedBodyMS,mountedRows}
 })
 await mkdir('../.impeccable/review',{recursive:true});await writeFile('../.impeccable/review/performance.json',JSON.stringify(result,null,2)+'\n');console.log(JSON.stringify(result,null,2))
 assert(result.firstWindowMS<300,'cached inbox exceeds 300ms');assert(result.mountedRows<=60,'list DOM grows with loaded history')
 assert(result.cachedBodyMS<50,'cached message exceeds 50ms')
 assert(result.keyPaintP95MS<16,'keypress paint exceeds 16ms')
}finally{await browser.close();for(const stream of streams)stream.end();server.closeAllConnections();await new Promise(resolve=>server.close(resolve))}
