import { open, read, write, remove, drainQueue } from './replica/storage.js'

const CACHE='terminal-mail-shell-__BUILD_VERSION__'
const PRECACHE=JSON.parse('__PRECACHE_MANIFEST__')
self.addEventListener('install',event=>event.waitUntil(caches.open(CACHE).then(cache=>cache.addAll(PRECACHE)).then(()=>self.skipWaiting())))
self.addEventListener('activate',event=>event.waitUntil(caches.keys().then(keys=>Promise.all(keys.filter(key=>key.startsWith('terminal-mail-shell-')&&key!==CACHE).map(key=>caches.delete(key)))).then(()=>self.clients.claim())))
self.addEventListener('fetch',event=>{
  const url=new URL(event.request.url)
  if(url.origin!==self.location.origin||event.request.method!=='GET'||url.pathname.startsWith('/api/')||url.pathname.startsWith('/auth/')||url.pathname.startsWith('/oauth/')||url.pathname==='/mcp'||url.pathname==='/healthz')return
  if(event.request.mode==='navigate'){
    event.respondWith(fetch(event.request).catch(()=>caches.match('/index.html')));return
  }
  if(PRECACHE.includes(url.pathname))event.respondWith(caches.match(event.request).then(cached=>cached||fetch(event.request)))
})
async function replay(){
  const clients=await self.clients.matchAll({type:'window'})
  const emit=data=>{for(const client of clients)client.postMessage(data)}
  for(const database of await indexedDB.databases()){
    if(!database.name?.startsWith('terminal-mail:'))continue
    const db=await open(database.name)
    try{const identity=await read(db,'settings','identity');if(identity)await drainQueue(db,identity,emit)}finally{db.close()}
  }
}
self.addEventListener('sync',event=>{if(event.tag==='terminal-mail-replay')event.waitUntil(replay())})
self.addEventListener('message',event=>{if(event.data?.type==='replay')event.waitUntil(replay())})
let notificationTask=Promise.resolve()
async function showMail(data){
 if(!data?.tag||!data.data?.owner||!data.data?.account)return
 const response=await fetch('/api/me',{credentials:'same-origin'});if(!response.ok)return
 const account=await response.json();if(account.email!==data.data.owner||account.icloud_email!==data.data.account)return
 const db=await open('terminal-mail-notifications')
 try{
  const shown=await read(db,'settings','shown')||[];if(shown.includes(data.tag))return
  await self.registration.showNotification(data.title||'Terminal Mail',{body:data.body||'New mail',icon:'/icons/icon-192.png',badge:'/icons/icon-192.png',tag:data.tag,data:data.data,actions:[{action:'archive',title:'Archive'},{action:'reply',title:'Reply'},{action:'read',title:'Read'}]})
  await write(db,'settings','shown',[...shown.slice(-199),data.tag])
  if(Number.isInteger(data.unread)){if(data.unread>0)await navigator.setAppBadge?.(data.unread);else await navigator.clearAppBadge?.()}
 }finally{db.close()}
}
function notify(data){const task=notificationTask.catch(()=>{}).then(()=>showMail(data));notificationTask=task;return task}
self.addEventListener('message',event=>{if(event.data?.type==='mail-arrival')event.waitUntil(notify(event.data.notification))})
self.addEventListener('push',event=>{
 let data;try{data=event.data.json()}catch{return}
 event.waitUntil(notify(data))
})
self.addEventListener('notificationclick',event=>{
 event.notification.close()
 event.waitUntil((async()=>{
  const id=crypto.randomUUID(),db=await open('terminal-mail-notifications')
  try{await write(db,'settings','intent:'+id,{...event.notification.data,action:event.action,expires:Date.now()+300000})}finally{db.close()}
  const url=new URL('/',self.location.origin);url.searchParams.set('notice',id)
  const clients=await self.clients.matchAll({type:'window',includeUncontrolled:true}),client=clients.find(client=>new URL(client.url).origin===self.location.origin)
  if(client){await client.navigate(url.href);return client.focus()}return self.clients.openWindow(url.href)
 })())
})
self.addEventListener('message',event=>{
 if(event.data?.type!=='notification-intent'||!event.ports[0]||!event.source?.url||new URL(event.source.url).origin!==self.location.origin)return
 event.waitUntil((async()=>{const db=await open('terminal-mail-notifications');try{const key='intent:'+event.data.id,intent=await read(db,'settings',key);await remove(db,'settings',key);event.ports[0].postMessage(intent?.expires>Date.now()?intent:null)}finally{db.close()}})())
})
