import {editableRule} from './rules.js'
import {wantsNotification} from './notifications.js'
import {keyBindings,keyStroke,bindingConflict} from './keyboard.js'
import './style.css'
import { request, setOwner, downloadAttachment, attachmentBlob, proxyImage } from './api.js'
import { authView, connectView, syncView, mailView, messageRows, paintSelection, paletteItems } from './views.js'
import {revealRow} from './list-window.js'
import {readerFonts} from './reader-fonts.js'
import {startupWindow} from './startup-window.js'
import { current, message, visible, targetFolder, role, COLORS, rebase, recipients,replyRecipients,snoozeOptions,unsubscribeLinks,searchTokens } from './model.js'
import { W, prog, esc } from './design.js'
import { openReplica } from './replica/client.js'
import {safeRichHTML} from './rich-text.js'
import {morph} from './morph.js'

const root=document.getElementById('root')
const params=new URLSearchParams(location.search)
const s={account:null,mode:params.has('reset_token')?'reset':'login',authEmail:'',authPassword:'',icloudEmail:'',busy:false,error:'',notice:params.get('verified')==='1'?'Email verified. Sign in to continue.':params.get('error')==='verification'?'That verification link is invalid or expired. Register again for a fresh link.':'',view:'mail',tab:'account',folder:'inbox',folders:[],msgs:[],total:0,counts:{},prefs:{},bodies:{},cur:0,picked:[],filter:'all',search:'',q:'',focus:'list',overlay:null,pal:'',palI:0,pickI:0,pickQuery:'',chord:null,compose:null,draft:null,draftStatus:'',loading:false,sending:false,bodyError:'',pending:0,offline:!navigator.onLine,side:true,max:false,mobilePane:'list',signal:localStorage.getItem('mail.signal')==='true',theme:localStorage.getItem('mail.theme')||(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light')}
let replica=null,replicaAccount=''
let folderGeneration=0, toastTimer, countdownTimer, chordTimer, draftTimer,searchTimer
let undoStack=[]
let rulePreviewTimer
let savingDraft=false, draftRevision=0
let bodyRequests=new Map(),bodyRevision=0
const inflightTransactions=new Map()
let renderedOverlay,renderedCompose,renderedToast,pointerBusy=false,drawPending=false
let listFrame
root.addEventListener('scroll',event=>{
  if(!event.target.classList.contains('rows'))return
  const list=event.target
  if(list.scrollTop===(s.listScroll||0)&&list.clientHeight===s.listHeight)return
  s.listScroll=list.scrollTop;s.listHeight=list.clientHeight
  cancelAnimationFrame(listFrame)
  listFrame=requestAnimationFrame(()=>{if(list.isConnected&&!pointerBusy)morph(list,messageRows(s))})
},true)

function draw() {
  if(pointerBusy){drawPending=true;return}
  drawPending=false
  const listHeight=root.querySelector('.rows')?.clientHeight;if(listHeight)s.listHeight=listHeight
  const rowHeight=root.querySelector('.rows .row')?.getBoundingClientRect().height;if(rowHeight)s.rowHeight=rowHeight
  if(s.overlay!=='preview'&&s.preview){if(s.preview.url)URL.revokeObjectURL(s.preview.url);s.preview.pdf?.destroy().catch(()=>{});s.preview=null}
  document.documentElement.dataset.theme=s.theme
  document.documentElement.dataset.density=s.prefs.density||'default'
  document.documentElement.dataset.readingFont=s.prefs.readingFont||'sans'
  document.documentElement.dataset.motion=s.prefs.motion||'system'
  const focus=document.activeElement, id=focus?.id
  const selection=typeof focus?.selectionStart==='number'?[focus.selectionStart,focus.selectionEnd]:null
  const richSelection=focus?.isContentEditable&&window.getSelection()?.rangeCount?window.getSelection().getRangeAt(0):null
  let richOffset=null
  if(richSelection&&focus.contains(richSelection.startContainer)){const range=richSelection.cloneRange();range.selectNodeContents(focus);range.setEnd(richSelection.startContainer,richSelection.startOffset);richOffset=range.toString().length}
  const scrolls=[...root.querySelectorAll('.rows,.rc,.sc .wb')].map(el=>[el.className,el.scrollTop])
  morph(root,s.account===null?`<main class="loading-screen dots">${W('SYSTEM_STATUS',`<div class="form-grid"><div class="disp">${s.error?'CONNECTION: FAILED':'LOADING_ACCOUNT'}</div>${s.error?`<div role="alert">${esc(s.error)}</div><button class="btn" data-a="retry-start">TRY AGAIN</button>`:prog(0)}</div>`,{on:1,cls:'pop',noctl:true})}</main>`:s.account===false?authView(s):!s.account.connected||s.view==='link'?connectView(s):s.sync&&!s.enteredInbox&&(!s.sync.ready||s.sync.phase==='importing')?syncView(s):mailView(s))
  if(renderedOverlay===s.overlay)root.querySelectorAll('.ov:not(.compose)').forEach(el=>el.classList.remove('m-open'))
  if(renderedCompose===s.compose)root.querySelector('.compose')?.classList.remove('m-open')
  if(renderedToast===s.toast)root.querySelector('.toast')?.classList.remove('m-toast')
  renderedOverlay=s.overlay;renderedCompose=s.compose;renderedToast=s.toast
  for(const [cls,y] of scrolls){const el=[...root.querySelectorAll('.rows,.rc,.sc .wb')].find(el=>el.className===cls);if(el)el.scrollTop=cls==='rows'?(s.listScroll||0):y}
  const list=root.querySelector('.rows');if(list){s.listHeight=list.clientHeight;list.scrollTop=s.listScroll||0}
  const authWindow=root.querySelector('.auth-window')
  if(authWindow){authWindow.classList.toggle('collapsed',!!s.authMin);authWindow.classList.toggle('expanded',!!s.authMax);const title=authWindow.querySelector('.wt');if(s.authMin){title.setAttribute('role','button');title.tabIndex=0;title.setAttribute('aria-label','Restore sign-in form')}}
  const active=id&&document.getElementById(id)
  if(active){active.focus({preventScroll:true});if(selection)active.setSelectionRange?.(...selection)}
  else if(s.keyEdit)document.getElementById('key-capture')?.focus()
  else if(s.overlay==='palette')document.getElementById('pq')?.focus()
  else if(s.overlay==='search')document.getElementById('sq')?.focus()
  else if(s.overlay==='move')document.getElementById('pick-query')?.focus()
  else if(s.overlay==='new-folder')document.getElementById('folder-name')?.focus()
  else if(s.overlay==='snooze-date')document.getElementById('snooze-at')?.focus()
  else if(s.overlay==='folder-settings')document.getElementById('rename-folder')?.focus()
  const dialog=root.querySelector('[role="dialog"][aria-modal="true"]')
  if(dialog&&!dialog.contains(document.activeElement)){dialog.tabIndex=-1;(dialog.querySelector('input,textarea,button,a[href],select')||dialog).focus({preventScroll:true})}
  if(active?.isContentEditable&&richOffset!==null){const walker=document.createTreeWalker(active,NodeFilter.SHOW_TEXT);let node;while((node=walker.nextNode())){if(richOffset<=node.length){const range=document.createRange();range.setStart(node,richOffset);range.collapse(true);window.getSelection().removeAllRanges();window.getSelection().addRange(range);break}richOffset-=node.length}}
}
function say(text,error=false,undo=false) {clearTimeout(toastTimer);clearInterval(countdownTimer);s.toast={text,error,undo,created:Date.now(),duration:error?10000:5000};draw();toastTimer=setTimeout(()=>{s.toast=null;draw()},s.toast.duration)}
function queuedSend(job){
 const remaining=Date.parse(job.sendAt)-Date.now()
 say(remaining>31000?'MESSAGE SCHEDULED // '+new Date(job.sendAt).toLocaleString():'MESSAGE QUEUED ON SERVER')
 s.toast.cancelSend=job.id
 if(remaining>0&&remaining<=31000){
  s.toast.sendAt=job.sendAt;s.toast.duration=remaining
  clearTimeout(toastTimer);toastTimer=setTimeout(()=>say('DELIVERY STARTING // CHECK OUTBOX'),remaining)
  countdownTimer=setInterval(()=>{const label=root.querySelector('[data-toast-countdown]');if(label)label.textContent='SENDING IN '+Math.max(0,Math.ceil((Date.parse(job.sendAt)-Date.now())/1000))+'S'},250)
 }
 draw()
}
function errorText(err) {
  return ({invalid_credentials:'Email or password is incorrect.',email_not_allowed:'Access is limited to @olegpustovit.com and ursus95@gmail.com.',password_must_be_12_to_128_characters:'Choose a password between 12 and 128 characters.',invalid_or_expired_reset_token:'This reset link is invalid or expired. Request a fresh link.',icloud_login_failed_check_app_specific_password:'iCloud rejected the login. Use an app-specific password from account.apple.com.',mailbox_unavailable_check_connection:'Could not open your mailbox. Check your iCloud connection or try again.',mail_operation_failed_try_refreshing:'The mail operation failed. Refresh to check its status before retrying.',email_unavailable:'Email delivery is unavailable. Please try again.'})[err.message]||err.message
}
async function start() {
  s.error='';if(params.has('reset_token')){s.account=false;draw();return}draw()
  try {s.account=await request('/api/me');setOwner(s.account.email,s.account.icloud_email);s.icloudEmail=s.account.icloud_email||'';if(s.account.connected){await initReplica();localStorage.setItem('mail.lastAccount',JSON.stringify(s.account));await Promise.all([loadFolders(),loadPreferences(),loadOutbox(),loadNotifications(true),loadOperations()])}draw()}
  catch(err){if(err.status===401){s.account=false;localStorage.removeItem('mail.lastAccount');replica?.close();replica=null}else if(localStorage.getItem('mail.lastAccount')){s.account=JSON.parse(localStorage.getItem('mail.lastAccount'));setOwner(s.account.email,s.account.icloud_email);s.offline=true;await initReplica();s.folders=await replica.call('get',{store:'settings',key:'folders'})||[];await loadFolder(s.folder)}else s.error=errorText(err);draw()}
}
async function onReplicaEvent(event){
  if(event.type==='folder-remapped'){
    const renamed=name=>typeof name==='string'&&(name===event.from||event.delimiter&&name.startsWith(event.from+event.delimiter))?event.to+name.slice(event.from.length):name
    s.folder=renamed(s.folder)
    if(s.ruleEditor)for(const action of s.ruleEditor.actions)if(action.action==='move')action.target=renamed(action.target)
    if(s.draft){for(const field of ['replyFolder','draftFolder'])s.draft[field]=renamed(s.draft[field]);replica.call('put',{store:'drafts',key:'current',value:s.draft}).catch(err=>say(errorText(err),true))}
    for(const [key,value]of Object.entries(s.bodies)){const parts=key.match(/^(.*)\|(\d+)\|(\d+)$/);if(parts){const folder=renamed(parts[1]);if(folder!==parts[1]){s.bodies[`${folder}|${parts[2]}|${parts[3]}`]={...value,folder};delete s.bodies[key]}}}
    undoStack=await replica.call('get',{store:'settings',key:'undo'})||[];await loadFolder(s.folder,false,true);return
  }
  if(event.type==='search-ready'&&s.search===event.search){await loadFolder(s.folder,false,true);return}
  if(event.type==='bodies-cleared'){bodyRevision++;bodyRequests.clear();s.bodies={};draw();loadBody();return}
  if(event.type==='mail-arrival'){
    if(s.notifyEnabled&&wantsNotification(s.prefs,s.account.icloud_email,event.message)&&globalThis.Notification?.permission==='granted'){const registration=await navigator.serviceWorker?.getRegistration();registration?.active?.postMessage({type:'mail-arrival',notification:{title:event.message.from,body:event.message.subject,tag:event.message.key,data:{folder:event.message.folder,id:event.message.id,validity:event.message.uidValidity,owner:s.account.email,account:s.account.icloud_email}}})}return
  }
  if(event.type==='mirror'){
    s.sync=event.status;s.syncID=event.syncId;s.folderInfo=event.folders;s.folders=event.folders.map(folder=>folder.name)
    s.counts={};for(const folder of event.folders)s.counts[folder.role||folder.name]=folder.unread
    const unread=s.counts.inbox||s.counts.INBOX||0;try{if(unread)navigator.setAppBadge?.(unread)?.catch(()=>{});else navigator.clearAppBadge?.()?.catch(()=>{})}catch{}
    if(event.prefs){s.prefs=event.prefs;s.theme=event.prefs.theme==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):event.prefs.theme;s.signal=event.prefs.signal}
    if(!replica)return;await loadFolder(s.folder,false,true);return
  }
  if(event.type==='sync-connected'){s.offline=false;s.error='';draw()}
  if(event.type==='sync-disconnected'){s.offline=true;draw()}
  if(event.type==='storage-error'){s.error='Local storage could not be updated: '+event.error;draw()}
  if(event.type==='agent-change'){s.agentChanges=[event.change,...(s.agentChanges||[]).filter(change=>change.syncId!==event.change.syncId)].slice(0,100);draw()}
  if(event.type==='queue'){const previous=s.pending;s.pending=event.pending;s.rejected=event.rejected;draw();if(previous>0&&event.pending===0&&navigator.onLine)loadFolder(s.folder,false,true)}
  if(event.type==='offline'){s.offline=true;draw()}
  if(event.type==='auth-required'){s.error='Your session or account changed. Sign in again before syncing queued changes.';draw()}
  if(event.type==='rejected'){say('Queued action rejected: '+event.transaction.error+' // OPEN PENDING ACTIONS',true);if(event.transaction.kind==='send'&&!s.draft){await restoreDraft(event.transaction.body);s.compose='float';draw()}}
  if(event.type==='ack'&&event.transaction.kind==='send'){await loadOutbox();const status=event.result.status;if(status==='pending')queuedSend(event.result);else say(({sent:'MESSAGE ALREADY SENT',sending:'MESSAGE IS SENDING',cancelled:'SEND WAS CANCELLED',uncertain:'DELIVERY IS UNCONFIRMED // CHECK OUTBOX'})[status]||'MESSAGE QUEUED ON SERVER')}
  if(event.type==='cleared'){s.msgs=[];s.bodies={};s.draft=null;draw()}
}
async function initReplica(){
 const binding=s.account.email+':'+s.account.icloud_email
 if(replicaAccount&&replicaAccount!==binding){bodyRevision++;folderGeneration++;bodyRequests.clear();clearTimeout(draftTimer);s.draft=null;s.compose=null;s.overlay=null;s.outbox=[];s.search='';s.q='';s.bodies={};s.msgs=[];s.folders=[];s.folderInfo=[];s.picked=[];undoStack=[];s.enteredInbox=false}
 replicaAccount=binding;replica?.close();const client=openReplica(s.account,event=>{if(replica===client)onReplicaEvent(event).catch(err=>{if(replica===client)say(errorText(err),true)})});replica=client
 try{
   const cached=await startupWindow(s.account,s.folder)
   if(cached&&client===replica&&!s.msgs.length){s.msgs=rebase(cached.snapshot.msgs,cached.transactions);s.total=cached.snapshot.total;s.folderInfo=cached.folders;s.folders=cached.folders.map(folder=>folder.name);s.prefs=cached.prefs||s.prefs;s.theme=s.prefs.theme==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):s.prefs.theme||s.theme;s.signal=!!s.prefs.signal;s.sync=cached.sync;s.enteredInbox=true;s.counts={};for(const folder of cached.folders)s.counts[folder.role||folder.name]=folder.unread;draw()}
 }catch{ /* The replica reports storage failures; startup can continue without a snapshot. */ }
 await client.ready
 const draft=await replica.call('get',{store:'drafts',key:'current'});if(draft)s.draft=draft
 undoStack=await replica.call('get',{store:'settings',key:'undo'})||[]
}
async function wakeReplay(){
 try{const registration=await navigator.serviceWorker?.getRegistration();if(registration?.sync)await registration.sync.register('terminal-mail-replay')}catch{}
}
function restoreDraft(draft){s.draft={...draft,to:(draft.to||[]).join(', '),cc:(draft.cc||[]).join(', '),bcc:(draft.bcc||[]).join(', ')};delete s.draft.transactionId;delete s.draft.sendAt;return replica?.call('put',{store:'drafts',key:'current',value:s.draft})}
async function loadPreferences() {
 try {s.prefs=await request('/api/mail/preferences');s.theme=s.prefs.theme==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):s.prefs.theme;s.signal=s.prefs.signal;await replica?.call('put',{store:'settings',key:'preferences',value:s.prefs});draw()}
 catch(err){say('Preferences could not be loaded. '+errorText(err),true)}
}
async function savePreference(key,value) {
 const old=s.prefs[key];s.prefs[key]=value
 if(key==='theme')s.theme=value==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):value
 if(key==='signal')s.signal=value
 draw()
 let saved=false
 try{await request('/api/mail/preferences',{method:'PATCH',body:JSON.stringify({[key]:value})});saved=true}
 catch(err){s.prefs[key]=old;if(key==='theme')s.theme=old==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):old;if(key==='signal')s.signal=old;say('Preference not saved. '+errorText(err),true)}
 if(saved){try{if(key==='stripTracking'){await replica.call('clear-bodies');await loadBody()}if(key==='remoteContent'||key==='allowedSenders')await loadRemoteAutomatically()}catch(err){say(errorText(err),true)}}
 return saved
}
async function loadSettings(tab){
 try{if(tab==='agents')[s.clients,s.agentChanges]=await Promise.all([request('/api/clients'),request('/api/mail/activity')]);if(tab==='account')s.sessions=await request('/api/sessions');if(tab==='identities')s.identitySuggestions=await replica.call('identity-suggestions');if(tab==='notifications')await loadNotifications();if(tab==='rules')await loadRules();draw()}
 catch(err){say('Settings could not be loaded: '+errorText(err),true)}
}
async function loadRules(){s.rulesStatus=await request('/api/mail/rules');draw()}
function editRule(index){clearTimeout(rulePreviewTimer);s.ruleIndex=index;s.ruleEditor=editableRule(s.prefs.rules?.[index]);s.rulePreview=null;s.ruleApply=false;draw();queueRulePreview()}
function queueRulePreview(){clearTimeout(rulePreviewTimer);s.rulePreview=null;s.rulePreviewLoading=false;s.rulePreviewRevision=(s.rulePreviewRevision||0)+1;rulePreviewTimer=setTimeout(()=>previewRule(false),300)}
async function previewRule(report=true){
 const editor=s.ruleEditor,revision=s.rulePreviewRevision,form=document.getElementById('rule-form');if(!editor||!form||!(report?form.reportValidity():form.checkValidity()))return
 if(editor.actions.some(action=>action.action==='move'&&!action.target))return
 s.rulePreviewLoading=true;if(report)draw()
 try{const result=await request('/api/mail/rules/preview',{method:'POST',body:JSON.stringify({rule:editor})});if(s.ruleEditor===editor&&revision===s.rulePreviewRevision)s.rulePreview=result}
 catch(err){if(report)say('Dry run failed: '+errorText(err),true)}finally{if(s.ruleEditor===editor&&revision===s.rulePreviewRevision){s.rulePreviewLoading=false;draw()}}
}
async function saveRule(){
 if(s.ruleSaving||!s.ruleEditor)return
 if(s.ruleEditor.actions.filter(action=>['move','archive'].includes(action.action)).length>1){say('Use one Move or Archive destination per rule.',true);return}
 const editor=s.ruleEditor,existing=s.prefs.rules||[],index=existing.findIndex(rule=>rule.id===editor.id),rules=index<0?[...existing,editor]:existing.map(rule=>rule.id===editor.id?editor:rule)
 s.ruleSaving=true;draw()
 try{if(!await savePreference('rules',rules))return;await loadRules();if(s.ruleApply){await request('/api/mail/rules/apply',{method:'POST',body:JSON.stringify({digest:s.rulesStatus.digest})});await loadRules()}s.ruleEditor=null;say(s.ruleApply?'RULE SAVED // EXISTING INBOX MAIL QUEUED':'RULE SAVED')}
 catch(err){say('Rule saved; existing mail was not queued. '+errorText(err),true)}finally{s.ruleSaving=false;draw()}
}
function pushDeviceName(){const browser=/Firefox/.test(navigator.userAgent)?'Firefox':/Edg\//.test(navigator.userAgent)?'Edge':/Chrome/.test(navigator.userAgent)?'Chrome':'Safari';return browser+' · '+(navigator.userAgentData?.platform||navigator.platform||'This device')}
async function loadNotifications(renew=false){
 const account=s.account
 s.pushSupported='PushManager' in window&&'serviceWorker' in navigator&&'Notification' in window
 try{
  const data=await request('/api/mail/push');if(s.account!==account)return;s.push=data
  const current=JSON.parse(localStorage.getItem('mail.pushDevice')||'null');s.pushID=current?.owner===account.email&&current?.account===account.icloud_email?current.id:null
  s.notifyEnabled=!!data.devices.find(device=>device.id===s.pushID)
  if(renew&&s.notifyEnabled&&s.pushSupported&&Notification.permission==='granted'){const registration=await navigator.serviceWorker.getRegistration(),subscription=await registration?.pushManager.getSubscription();if(subscription&&s.account===account)await registerPush(subscription)}
  draw()
 }catch(err){s.pushError=errorText(err);draw()}
}
async function registerPush(subscription){
 const device=await request('/api/mail/push',{method:'POST',body:JSON.stringify({subscription:subscription.toJSON(),name:pushDeviceName(),zone:Intl.DateTimeFormat().resolvedOptions().timeZone})})
 s.pushID=device.id;s.notifyEnabled=true;localStorage.setItem('mail.pushDevice',JSON.stringify({id:device.id,owner:s.account.email,account:s.account.icloud_email}));return device
}
async function enablePush(){
 if(!s.pushSupported||!s.push?.publicKey)return
 const permission=await Notification.requestPermission();if(permission!=='granted'){say('Notifications are blocked. Allow them in your browser settings.',true);return}
 try{const registration=await navigator.serviceWorker.ready,encoded=s.push.publicKey.replace(/-/g,'+').replace(/_/g,'/'),key=Uint8Array.from(atob(encoded),char=>char.charCodeAt(0));const subscription=await registration.pushManager.getSubscription()||await registration.pushManager.subscribe({userVisibleOnly:true,applicationServerKey:key});await registerPush(subscription);s.pushError='';await loadNotifications();say('NOTIFICATIONS ENABLED')}catch(err){say('Notifications could not be enabled. '+errorText(err),true)}
}
async function disablePush(id){
 try{await request('/api/mail/push/'+encodeURIComponent(id),{method:'DELETE',body:'{}'});if(id===s.pushID){s.notifyEnabled=false;localStorage.removeItem('mail.pushDevice');const registration=await navigator.serviceWorker?.getRegistration(),subscription=await registration?.pushManager.getSubscription();await subscription?.unsubscribe()}await loadNotifications();say('DEVICE NOTIFICATIONS DISABLED')}catch(err){say(errorText(err),true)}
}
async function loadOperations(){
 const account=s.account,client=replica
 try{const local=await client?.call('transactions')||[],renames=navigator.onLine?await request('/api/mail/operations'):s.renames;if(s.account!==account||client!==replica)return;s.localOperations=local;s.renames=renames;draw()}catch(err){if(s.overlay==='operations')say(errorText(err),true)}
}
async function loadOutbox() {const account=s.account;try{const outbox=await request('/api/mail/outbox');if(s.account!==account)return;s.outbox=outbox;draw()}catch(err){if(s.account?.connected)say('Could not load scheduled messages.',true)}}
async function loadFolders() {
  try {const data=await replica.call('folders');s.folders=data.folders||[];s.folderInfo=data.details||[];s.sync=data.sync;await loadFolder(s.folder)}
  catch(err){s.loading=false;s.error=errorText(err);draw()}
}
async function loadFolder(folder,more=false,quiet=false) {
  if(!replica)return
  const generation=++folderGeneration,client=replica
  try{
  s.folder=folder;s.bodyError=''
  if(!more&&!quiet){s.msgs=[];s.total=0;s.cur=0;s.listScroll=0;s.picked=[];s.mobilePane='list'}
  const result=await client.call('query',{folder,filter:s.filter,search:s.search,offset:more?s.msgs.length:0,limit:more?100:Math.max(100,s.msgs.length),vip:s.prefs.vip||[]})
  if(generation!==folderGeneration||client!==replica)return
  const rows=result.emails.map(raw=>message(raw,raw.folder||result.folder)),transactions=await client.call('transactions')
  if(generation!==folderGeneration||client!==replica)return
  const focused=current(s)?.id,oldPosition=s.cur
  s.msgs=rebase(more?[...s.msgs,...rows]:rows,transactions);s.total=result.total;s.loaded=result.loaded;s.searchInfo=result.search;s.partial=result.partial;s.loading=false
  if(!rows.length){const cached=await client.call('get',{store:'snapshots',key:folder});if(cached&&!s.sync?.ready){s.msgs=rebase(cached.msgs,await client.call('transactions'));s.total=cached.total}}
  if(generation!==folderGeneration||client!==replica)return
  const index=focused?visible(s).findIndex(message=>message.id===focused):-1
  s.cur=quiet&&focused?(index>=0?index:Math.min(oldPosition,visible(s).length-1)):visible(s).length?0:-1
  if(s.msgs.length)s.enteredInbox=s.enteredInbox||s.sync?.phase!=='importing'
  await client.call('put',{store:'snapshots',key:folder,value:{msgs:s.msgs,total:s.total}})
  draw();loadBody()
  const latest=current(s)?.threadMessages?.at(-1);if(latest)loadBody(false,message(latest,latest.folder))
  client.call('prefetch',{keys:visible(s).slice(Math.max(0,s.cur-2),s.cur+4).map(m=>m.id)}).catch(()=>{})
  }catch(err){if(client===replica){s.error=errorText(err);s.loading=false;draw()}}
}
function searchMail(text){
 s.q=text;s.search=text;s.cur=0;s.picked=[];clearTimeout(searchTimer);replica?.call('cancel-search');draw();loadFolder(s.folder,false,true)
 if(text)searchTimer=setTimeout(async()=>{try{await replica.call('search-bodies',{search:text});if(s.search===text){await loadFolder(s.folder,false,true);if(navigator.onLine)await replica.call('remote-search',{search:text})}}catch(err){if(s.search===text)say('Search could not read cached bodies. '+errorText(err),true)}},150)
}
async function loadBody(force=false,target=current(s)) {
  const latest=target?.threadMessages?.at(-1),m=latest?message(latest,latest.folder):target;if(!m||m.pendingLocation||target?.pendingLocation||bodyRequests.has(m.id)||s.bodies[m.id]&&!force)return
  const client=replica,version=bodyRevision,token={};bodyRequests.set(m.id,token);s.bodyError=''
  try{if(force)await client.call('remove',{store:'bodies',key:m.id});const body=await client.call('body',{key:m.id,folder:m.folder,id:m.uid,validity:m.uidValidity});if(body.bodyHTML&&!s.readerFonts)s.readerFonts=await readerFonts();if(client!==replica||version!==bodyRevision||!s.account?.connected)return;s.bodies[m.id]=body;prepareBodyHTML(body);loadRemoteAutomatically();if(current(s)?.id===m.id||current(s)?.threadId===m.threadId)draw()}
  catch(err){if(client===replica&&version===bodyRevision&&current(s)?.id===m.id){s.bodyError=s.offline?'This message body has not been cached on this device.':errorText(err);draw()}}
  finally{if(bodyRequests.get(m.id)===token)bodyRequests.delete(m.id)}
}
async function submitAuth(form) {
  const data=new FormData(form)
  if(data.has('email'))s.authEmail=data.get('email').trim()
  if(data.has('password'))s.authPassword=data.get('password')
  s.busy=true;s.error='';s.notice='';draw()
  try{
    const next=params.get('next')||undefined
    if(s.mode==='login'){
      const result=await request('/auth/login',{method:'POST',body:JSON.stringify({email:s.authEmail,password:s.authPassword,next})})
      s.authPassword='';location.assign(result.next||'/');return
    }
    if(s.mode==='verify'){
      const result=await request('/auth/resend',{method:'POST',body:JSON.stringify({email:s.authEmail,next})});s.notice=result.message
    }else if(s.mode==='register'){
      await request('/auth/register',{method:'POST',body:JSON.stringify({email:s.authEmail,password:s.authPassword,next})})
      s.mode='verify';s.authPassword='';s.notice='Verification link sent. Check your inbox.'
    }else if(s.mode==='forgot'){
      const result=await request('/auth/forgot',{method:'POST',body:JSON.stringify({email:s.authEmail})});s.mode='login';s.notice=result.message
    }else{
      await request('/auth/reset',{method:'POST',body:JSON.stringify({token:params.get('reset_token'),password:s.authPassword})});s.mode='login';s.authPassword='';s.notice='Password updated. Sign in with your new password.'
    }
  }catch(err){s.error=errorText(err)}finally{s.busy=false;draw()}
}
async function linkAccount(form) {
  const data=new FormData(form);s.icloudEmail=data.get('icloud-email').trim();s.busy=true;s.error='';draw()
  try{await request('/api/account',{method:'PUT',body:JSON.stringify({email:s.icloudEmail,password:data.get('icloud-password')})});s.account=await request('/api/me');setOwner(s.account.email,s.account.icloud_email);s.view='mail';await initReplica();localStorage.setItem('mail.lastAccount',JSON.stringify(s.account));await Promise.all([loadFolders(),loadPreferences(),loadOutbox(),loadNotifications(true),loadOperations()])}
  catch(err){s.error=errorText(err)}finally{s.busy=false;draw()}
}
async function mutate(op,extra={},inverse) {
  const selected=s.picked.length?s.msgs.filter(m=>s.picked.includes(m.id)):[current(s)].filter(Boolean)
  if(selected.some(m=>m.pendingLocation||m.actionMessages?.some(raw=>raw.pendingLocation))){say('Wait for the pending move to finish before changing this conversation.');return}
  const items=selected.flatMap(m=>m.actionMessages?.map(raw=>message(raw,raw.folder))||[m]).filter(m=>op!=='move'||m.folder!==extra.target)
  if(!items.length)return
  s.overlay=null;s.swipeID=null
  const done=items.map(m=>({...m,transactionId:crypto.randomUUID()})),reversible=!!inverse||op==='move'||op==='snooze'
  if(reversible){undoStack.push(done.map(m=>inverse?{op,folder:m.folder,id:m.uid,validity:m.uidValidity,...inverse(m)}:op==='snooze'&&m.f==='snoozed'?{op:'snooze',folder:m.folder,id:m.uid,validity:m.uidValidity,until:m.snoozeUntil||null}:{op:'undoMove',folder:m.folder,originalTransaction:m.transactionId}));if(undoStack.length>20)undoStack.shift()}
  for(const m of selected){if(op==='read')m.un=!extra.read;if(op==='flag')m.fl=extra.color||null}
  if(['move','delete','unsnooze'].includes(op)||op==='snooze'&&role(targetFolder(s,s.folder))!=='snoozed')s.msgs=s.msgs.filter(m=>!selected.includes(m))
  s.picked=[];s.cur=Math.min(s.cur,visible(s).length-1);draw()
  const transactions=done.map(m=>({id:m.transactionId,path:'/api/mail/mutate',body:{op,folder:m.folder,id:m.uid,validity:m.uidValidity,...extra},kind:'mutation',messageKey:m.id}))
  const persistence=replica.call('queue-batch',{transactions,...(reversible?{undoStack}: {})});for(const m of done)inflightTransactions.set(m.transactionId,persistence)
  try{await persistence;wakeReplay();await loadFolder(s.folder,false,true);say(`${op.toUpperCase()}: ${done.length} MESSAGE${done.length===1?'':'S'}${s.offline?' // QUEUED':''}`,false,reversible)}
  catch(err){undoStack=await replica.call('get',{store:'settings',key:'undo'}).catch(()=>[])||[];await loadFolder(s.folder,false,true);say('Could not save the action locally. '+errorText(err),true)}
  finally{for(const m of done)inflightTransactions.delete(m.transactionId)}
}
function newDraft(kind) {
  if(kind&&(current(s)?.pendingLocation||current(s)?.actionMessages?.some(raw=>raw.pendingLocation))){say('Wait for the pending move before replying to this conversation.');return}
  if(s.draft&&!window.confirm('Replace the open draft? Save it to iCloud first to keep a copy.'))return
  const conversation=current(s),selected=conversation?.threadMessages?.find(raw=>raw.key===s.threadMessage),m=selected?message(selected,selected.folder):conversation,body=m&&s.bodies[m.id]
  s.draft={to:'',cc:'',bcc:'',subject:'',body:'',ccVisible:false,from:s.account.icloud_email,attachments:[]}
  if(kind&&m){const envelope=replyRecipients(body||m,[s.account.icloud_email,...(s.prefs.identities||[]).map(identity=>identity.address)],kind==='reply-all');s.draft.subject=(kind==='forward'?'Fwd: ':'Re: ')+m.subj.replace(/^(re|fwd):\s*/i,'');s.draft.to=kind==='forward'?'':envelope.to.join(', ');s.draft.replyId=kind==='forward'?undefined:m.uid;s.draft.replyFolder=m.folder;s.draft.replyValidity=m.uidValidity;s.draft.replyAll=kind==='reply-all';if(kind==='reply-all'){s.draft.cc=envelope.cc.join(', ');s.draft.ccVisible=true}if(kind==='forward')s.draft.body=`\n\n---------- Forwarded message ----------\nFrom: ${m.from}\nSubject: ${m.subj}\n\n${body?.bodyPlain||m.snip}`}
  const delivered=(body?.to||m?.to||[]).map(raw=>raw.match(/<([^>]+)>/)?.[1]||raw)
  const identity=s.prefs.identities?.find(identity=>delivered.includes(identity.address))||s.prefs.identities?.find(identity=>identity.address===s.account.icloud_email)
  if(identity){s.draft.from=identity.address;s.draft.signature=identity.signature||'';if(identity.signature)s.draft.body+='\n\n'+identity.signature}
  s.compose=kind&&kind!=='forward'?'inline':'float';s.overlay=null;s.draftStatus='UNSAVED';replica?.call('put',{store:'drafts',key:'current',value:s.draft});draw();document.getElementById('draft-'+(kind?'body':'to'))?.focus()
}
const addresses=recipients
const composePayload=()=>({to:addresses(s.draft.to),cc:addresses(s.draft.cc),bcc:addresses(s.draft.bcc),subject:s.draft.subject,body:s.draft.html?safeRichHTML(s.draft.body):s.draft.body,from:s.draft.from||s.account.icloud_email,html:!!s.draft.html,attachments:s.draft.attachments||[],replyId:s.draft.replyId,replyFolder:s.draft.replyFolder,replyValidity:s.draft.replyValidity||0,replyAll:s.draft.replyAll||false,draftId:s.draft.serverId||s.draft.draftId||undefined,draftFolder:s.draft.draftFolder||targetFolder(s,'drafts'),draftValidity:s.draft.draftValidity||s.folderInfo?.find(f=>f.role==='drafts')?.uidValidity||0})
const base64File=file=>new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(reader.result.split(',')[1]);reader.onerror=()=>reject(reader.error);reader.readAsDataURL(file)})
async function attachFiles(files){
  if(!s.draft)return
  const draft=s.draft
  s.draft.attachments=s.draft.attachments||[]
  const total=(s.draft.attachments||[]).reduce((sum,file)=>sum+Math.floor(file.content.length*3/4),0)+[...files].reduce((sum,file)=>sum+file.size,0)
  if(total>10*1024*1024||(s.draft.attachments||[]).length+files.length>30){say('Attachments must total at most 10 MB, with at most 30 files.',true);return}
  s.attaching=true;draw()
  try{for(const file of files){if(/[\\/\r\n\0]/.test(file.name))throw new Error('That filename contains unsupported characters.');const content=await base64File(file);if(s.draft!==draft)return;draft.attachments.push({filename:file.name,mimeType:file.type||'application/octet-stream',content})}await draftChanged();draw()}catch(err){say('Attachment could not be saved: '+errorText(err),true)}finally{s.attaching=false;draw()}
}
async function draftChanged(){
  s.draftStatus='UNSAVED';draftRevision++;await replica?.call('put',{store:'drafts',key:'current',value:s.draft});clearTimeout(draftTimer);draftTimer=setTimeout(saveDraft,3000)
}
async function editDraft(){
  const m=current(s);if(!m)return
  if(s.draft&&!window.confirm('Replace the open draft? Save it to iCloud first to keep a copy.'))return
  try{
    const body=await replica.call('body',{key:m.id,folder:m.folder,id:m.uid,validity:m.uidValidity}),files=[]
    for(const file of body.attachments||[]){const blob=await attachmentBlob('/api/mail/attachment?'+new URLSearchParams({folder:m.folder,id:m.uid,validity:m.uidValidity,filename:file.filename}));files.push({filename:file.filename,mimeType:file.mimeType||blob.type||'application/octet-stream',content:await base64File(blob)})}
    s.draft={to:(body.to||[]).join(', '),cc:(body.cc||[]).join(', '),bcc:(body.bcc||[]).join(', '),subject:body.subject,body:body.bodyHTML||body.bodyPlain||'',html:!!body.bodyHTML,attachments:files,from:s.account.icloud_email,serverId:m.uid,draftFolder:m.folder,draftValidity:m.uidValidity,ccVisible:!!body.cc?.length||!!body.bcc?.length}
    await replica.call('put',{store:'drafts',key:'current',value:s.draft});s.compose='float';s.draftStatus='SAVED // ICLOUD/DRAFTS';draw()
  }catch(err){say('Draft could not be opened: '+errorText(err),true)}
}
async function saveDraft(){
  if(!s.draft||s.sending||savingDraft)return
  const draft=s.draft, revision=draftRevision;savingDraft=true
  await replica?.call('put',{store:'drafts',key:'current',value:draft})
  if(!navigator.onLine){savingDraft=false;s.draftStatus='SAVED LOCALLY // OFFLINE';draw();return}
  clearTimeout(draftTimer);s.draftStatus='SAVING…';draw()
  try{const result=await request('/api/mail/draft',{method:'POST',body:JSON.stringify(composePayload())});if(s.draft===draft){s.draft.serverId=result.id;s.draft.draftFolder=result.folder||targetFolder(s,'drafts');s.draft.draftValidity=result.validity||s.draft.draftValidity||s.folderInfo?.find(f=>f.role==='drafts')?.uidValidity;await replica?.call('put',{store:'drafts',key:'current',value:s.draft});s.draftStatus=result.warning?'SAVED // PREVIOUS COPY REMAINS':revision===draftRevision?'SAVED // ICLOUD/DRAFTS':'UNSAVED';draw()}}
  catch(err){s.draftStatus='SAVE FAILED';say(errorText(err),true)}finally{savingDraft=false;if(s.draft===draft&&revision!==draftRevision)draftTimer=setTimeout(saveDraft,3000)}
}
async function sendDraft(){
 if(s.draft?.deliveryUncertain&&!confirm('The original delivery is unconfirmed. Check Sent first. Send another copy?'))return
  if(!s.draft||s.sending)return
  if(s.attaching){say('Wait for attachments to finish loading.');return}
  if(s.sync?.phase==='auth_failed'){say('Relink iCloud before sending. Your draft remains saved locally.',true);return}
  if(savingDraft){say('Wait for the current draft save to finish.');return}
  clearTimeout(draftTimer);s.sending=true;draw()
  try{const payload={...composePayload(),...(s.sendAt?{sendAt:s.sendAt}:{}),transactionId:s.draft.transactionId||(s.draft.transactionId=crypto.randomUUID())};await replica.call('queue',{path:'/api/mail/outbox',body:payload,kind:'send'});wakeReplay();s.compose=null;s.draft=null;s.sendAt=null;await replica.call('remove',{store:'drafts',key:'current'});say(s.offline?'SEND QUEUED LOCALLY // REPLAYS ON RECONNECT':'MESSAGE QUEUED')}

  catch(err){say('Send could not be confirmed. Check Sent before retrying. '+errorText(err),true)}finally{s.sending=false;draw()}
}
async function rebindKey(key){
 const action=s.keyEdit,bindings=keyBindings(s.prefs),error=bindingConflict(action,key,bindings)
 if(error){say(error,true);return}
 const old={keyPreset:s.prefs.keyPreset,keymap:s.prefs.keymap},patch={keyPreset:'custom',keymap:{...bindings,[action]:key}}
 Object.assign(s.prefs,patch);s.keyEdit=null;draw()
 try{await request('/api/mail/preferences',{method:'PATCH',body:JSON.stringify(patch)})}catch(err){Object.assign(s.prefs,old);say('Shortcut could not be saved. '+errorText(err),true)}
}
async function act(action){
  if(!action.startsWith('edit-key:'))s.keyEdit=null
  if(action.startsWith('restore-send:')){if(s.draft&&!confirm('Replace the current local draft with this unconfirmed message?'))return;try{const draft=await request('/api/mail/outbox/'+encodeURIComponent(action.slice(13))+'/restore',{method:'POST',body:'{}'});await restoreDraft({...draft,deliveryUncertain:true});s.compose='float';s.overlay=null;await loadOutbox();say('DRAFT RESTORED // CHECK SENT BEFORE SENDING',true)}catch(err){say(errorText(err),true)}return}
  if(action.startsWith('dismiss-operation:')){await replica.call('discard-rejected',{id:action.slice(18)});await loadOperations();return}
  if(action.startsWith('restore-operation:')){if(s.draft&&!confirm('Replace the current local draft?'))return;const tx=(await replica.call('transactions')).find(tx=>tx.id===action.slice(18)&&tx.kind==='send'&&tx.status==='rejected');if(!tx)return;await restoreDraft(tx.body);await replica.call('discard-rejected',{id:tx.id});s.compose='float';s.overlay=null;draw();return}
  if(action.startsWith('retry-rename:')||action.startsWith('cancel-rename:')){const cancel=action.startsWith('cancel-'),id=action.slice(13+(cancel?1:0));try{await request('/api/mail/operations/'+encodeURIComponent(id)+'/'+(cancel?'cancel':'retry'),{method:'POST',body:'{}'});await loadOperations();say(cancel?'RENAME CANCELLED':'RENAME RECOVERY QUEUED')}catch(err){say('Rename already changed. Refresh and retry recovery.',true)}return}
  if(action.startsWith('edit-rule:')){editRule(Number(action.slice(10)));return}
  if(action.startsWith('toggle-rule:')){const index=Number(action.slice(12));await savePreference('rules',s.prefs.rules.map((rule,i)=>i===index?{...rule,enabled:!rule.enabled}:rule));return}
  if(action.startsWith('rule-remove-condition:')||action.startsWith('rule-remove-action:')){const condition=action.startsWith('rule-remove-condition:'),index=Number(action.slice(condition?22:19));s.ruleEditor[condition?'conditions':'actions'].splice(index,1);queueRulePreview();draw();return}
  if(action.startsWith('retry-rule:')){try{await request('/api/mail/rules/jobs/'+encodeURIComponent(action.slice(11))+'/retry',{method:'POST',body:'{}'});await loadRules()}catch(err){say(errorText(err),true)}return}
  if(action.startsWith('disable-push:')){await disablePush(action.slice(13));return}
  if(action.startsWith('suggest-identity:')){s.identityIndex=-1;s.identityForm={address:action.slice(17),name:'',signature:''};draw();return}
  if(action.startsWith('edit-identity:')){const index=Number(action.slice(14));s.identityIndex=index>=0?index:-1;s.identityForm=index>=0?{...s.prefs.identities[index]}:{address:index===-2?s.account.icloud_email:'',name:'',signature:''};draw();return}
  if(action.startsWith('remove-identity:')){if(!window.confirm('Remove this sender identity?'))return;await savePreference('identities',s.prefs.identities.filter((_,index)=>index!==Number(action.slice(16))));return}
  if(action.startsWith('revoke-client:')){if(!window.confirm('Revoke this MCP client’s access?'))return;try{await request('/api/clients/'+encodeURIComponent(action.slice(14)),{method:'DELETE'});await loadSettings('agents');say('CLIENT ACCESS REVOKED')}catch(err){say(errorText(err),true)}return}
  if(action.startsWith('undo-activity:')){try{await request('/api/mail/activity/'+action.slice(14)+'/undo',{method:'POST',body:'{}'});await loadSettings('agents');say('AGENT CHANGE UNDONE')}catch(err){say(errorText(err),true)}return}
  if(action.startsWith('edit-key:')){s.keyEdit=action.slice(9);draw();return}
  if(action.startsWith('auth:')){s.mode=action.slice(5);s.error='';s.notice='';s.authPassword='';draw();return}
  if(action.startsWith('cancel-send:')){try{const draft=await request('/api/mail/outbox/'+encodeURIComponent(action.slice(12)),{method:'DELETE',body:'{}'});await restoreDraft(draft);s.compose='float';s.overlay=null;await loadOutbox();say('SEND CANCELLED // DRAFT RESTORED')}catch(err){say(errorText(err),true)}return}
  if(action.startsWith('go:')){s.view='mail';s.search='';s.q='';replica?.call('cancel-search');s.overlay=null;s.filter='all';await loadFolder(action.slice(3));return}
  if(action.startsWith('settings:')){s.view='settings';s.tab=action.slice(9);s.overlay=null;draw();loadSettings(s.tab);return}
  const m=current(s)
  switch(action){
    case'retry-start':await start();return
    case'refresh':s.view='mail';await replica.call('refresh');await loadFolders();return
    case'enter-inbox':s.enteredInbox=true;s.view='mail';draw();loadBody();return
    case'load-more':if(!s.loading)await loadFolder(s.folder,true);return
    case'retry-body':loadBody(true);return
    case'expand-thread':{const thread=current(s)?.threadMessages||[];s.expandedThread=s.expandedThread===m.threadId?null:m.threadId;draw();if(s.expandedThread)for(const raw of thread)loadBody(false,message(raw,raw.folder));return}
    case'thread-next':case'thread-prev':{
      const thread=m?.threadMessages||[];if(!thread.length)return
      const old=thread.findIndex(raw=>raw.key===s.threadMessage),index=Math.max(0,Math.min(thread.length-1,(old<0?thread.length-1:old)+(action==='thread-next'?1:-1))),raw=thread[index]
      s.threadMessage=raw.key;s.openMessages=[...new Set([...(s.openMessages||[]),raw.key])];draw();root.querySelector(`[data-thread-key="${CSS.escape(raw.key)}"]`)?.scrollIntoView({block:'nearest'});loadBody(false,message(raw,raw.folder));return
    }
    case'next':case'prev':s.cur=Math.max(0,Math.min(visible(s).length-1,s.cur+(action==='next'?1:-1)));s.bodyError='';revealRow(visible(s),s);if(s.overlay||s.compose||pointerBusy||!paintSelection(root,s))draw();loadBody();return
    case'open':if(m?.f==='drafts'){await editDraft();return}s.focus='reader';s.mobilePane='reader';if(m?.un)mutate('read',{read:true});break
    case'edit-draft':await editDraft();return
    case'back':s.focus='list';s.mobilePane='list';s.max=false;break
    case'archive':case'trash':case'junk':{
      if(action==='trash'&&role(targetFolder(s,s.folder))==='trash'){if(!window.confirm('Permanently delete the selected messages? This cannot be undone.'))return;await mutate('delete',{permanent:true});return}
      const folder=targetFolder(s,action==='trash'?'trash':action)
      if(!s.folders.includes(folder)){say(`Create the ${folder} folder before moving messages.`,true);return}
      await mutate('move',{target:folder});return
    }
    case'read':case'unread':await mutate('read',{read:action==='read'},m=>({read:!m.un}));return
    case'flag':await mutate('flag',{color:m?.fl?'':'red'},m=>({color:m.fl||''}));return
    case'select-all':s.picked=visible(s).map(mail=>mail.id);break
    case'select-none':s.picked=[];break
    case'select-unread':s.picked=visible(s).filter(mail=>mail.un).map(mail=>mail.id);break
    case'filter-files':s.filter='att';s.cur=0;draw();loadFolder(s.folder,false,true);return
    case'extend-next':case'extend-prev':{const rows=visible(s),next=Math.max(0,Math.min(rows.length-1,s.cur+(action==='extend-next'?1:-1)));s.picked=[...new Set([...s.picked,rows[s.cur]?.id,rows[next]?.id].filter(Boolean))];s.cur=next;revealRow(rows,s);draw();loadBody();return}
    case'select':if(m)s.picked=s.picked.includes(m.id)?s.picked.filter(id=>id!==m.id):[...s.picked,m.id];break
    case'undo':{const mutations=undoStack.pop();if(!mutations)return;try{await Promise.all(mutations.map(m=>inflightTransactions.get(m.originalTransaction)).filter(Boolean));await replica.call('queue-batch',{transactions:mutations.map(body=>({path:'/api/mail/mutate',body,kind:'mutation'})),undoStack});wakeReplay();await loadFolder(s.folder,false,true);say(s.offline?'UNDO QUEUED LOCALLY':'UNDO QUEUED')}catch(err){undoStack=await replica.call('get',{store:'settings',key:'undo'}).catch(()=>[])||[];say(errorText(err),true)}return}
    case'compose':if(!s.draft)newDraft();else{s.compose='float';draw()}return
    case'reply':case'reply-all':case'forward':newDraft(action);return
    case'restore-compose':s.compose='float';break
    case'min-compose':s.compose=null;break
    case'full-compose':s.compose=s.compose==='full'?'float':'full';break
    case'discard-draft':if(window.confirm('Discard this draft?')){clearTimeout(draftTimer);if(savingDraft){say('Wait for the current save to finish.');return}const payload=composePayload();if(payload.draftId)await replica.call('queue',{path:'/api/mail/mutate',body:{op:'delete',folder:payload.draftFolder,id:payload.draftId,validity:payload.draftValidity,permanent:true},kind:'mutation'});s.compose=null;s.draft=null;await replica?.call('remove',{store:'drafts',key:'current'})}break
    case'attach':document.getElementById('attachment-input')?.click();return
    case'format':if(!s.draft.html){s.draft.html=true;s.draft.body='<p>'+esc(s.draft.body||'').replace(/\n/g,'<br>')+'</p>'}s.formatting=!s.formatting;draw();return
    case'toggle-cc':s.draft.ccVisible=true;break
    case'save-draft':await saveDraft();return
    case'send':await sendDraft();return
    case'palette':s.overlay='palette';s.pal='';s.palI=0;break
    case'help':s.overlay=s.overlay==='help'?null:'help';break
    case'search':s.view='mail';s.mobilePane='list';s.overlay='search';s.q=s.search;break
    case'search-retry':await replica.call('remote-search',{search:s.search,retry:true});return
    case'clear-search':s.overlay=null;searchMail('');return
    case'move':s.overlay='move';s.pickI=0;s.pickQuery='';break
    case'flagc':s.overlay='flag';s.pickI=0;break
    case'new-folder':s.overlay='new-folder';s.folderName='';break
    case'delete-folder':{if(s.folderBusy)return;const folder=s.managedFolder;if(!window.confirm(`Delete the empty folder “${folder}”?`))return;s.folderBusy=true;draw();try{await request('/api/mail/mutate',{method:'POST',body:JSON.stringify({op:'deleteFolder',folder,validity:s.folderInfo?.find(info=>info.name===folder)?.uidValidity})});s.overlay=null;if(s.folder===folder)await loadFolder('inbox');await loadFolders();say('FOLDER DELETED')}catch(err){say('Folder could not be deleted. Empty it first, then try again.',true)}finally{s.folderBusy=false;draw()}return}
    case'close-overlay':s.overlay=null;break
    case'preview-prev':case'preview-next':await previewAttachment(s.preview.index+(action==='preview-prev'?-1:1));return
    case'preview-page-prev':case'preview-page-next':await previewPDFPage(s.preview.page+(action==='preview-page-prev'?-1:1));return
    case'preview-download':{const file=s.preview?.files[s.preview.index];if(file)try{await downloadAttachment(file.href,file.filename)}catch(err){say(errorText(err),true)}return}
    case'install':if(s.installEvent){await s.installEvent.prompt();s.installEvent=null}else say('Use your browser’s Install app or Add to Home Screen action.');return
    case'outbox':s.overlay='outbox';await loadOutbox();break
    case'operations':s.overlay='operations';await loadOperations();break
    case'retry-operations':await replica.call('retry');await loadOperations();break
    case'refresh-operations':await loadOperations();break
    case'schedule-send':s.overlay='schedule-send';break
    case'snooze':if(!m)return;s.overlay='snooze';s.pickI=0;break
    case'unsnooze':await mutate('unsnooze');return
    case'settings':s.view='settings';s.tab='account';s.overlay=null;loadSettings(s.tab);break
    case'rule-from-message':if(!m)return;s.view='settings';s.tab='rules';s.overlay=null;editRule(-1);s.ruleEditor.conditions=[{field:'from',match:'equals',value:m.em}];draw();queueRulePreview();return
    case'close-identity':s.identityForm=null;break
    case'revoke-sessions':try{await request('/api/sessions',{method:'DELETE'});await loadSettings('account');say('OTHER BROWSERS SIGNED OUT')}catch(err){say(errorText(err),true)}return
    case'mail':s.view='mail';s.mobilePane='list';break
    case'relink':s.view='link';s.error='';break
    case'unside':s.side=true;s.max=false;break
    case'unmax':s.max=false;break
    case'theme':await savePreference('theme',s.theme==='light'?'dark':'light');return
    case'signal':await savePreference('signal',!s.signal);return
    case'copy-mcp':try{await navigator.clipboard.writeText('https://mail.microapps.space/mcp');say('MCP URL COPIED')}catch{say('Copy this URL: https://mail.microapps.space/mcp',true)}return
    case'new-rule':editRule(-1);return
    case'close-rule':clearTimeout(rulePreviewTimer);s.ruleEditor=null;break
    case'rule-add-condition':s.ruleEditor.conditions.push({field:'subject',match:'contains',value:''});queueRulePreview();break
    case'rule-add-action':s.ruleEditor.actions.push({action:'read',target:''});queueRulePreview();break
    case'rule-preview':await previewRule();return
    case'refresh-rules':await loadRules();return
    case'delete-rule':if(!confirm('Delete this rule?'))return;if(await savePreference('rules',s.prefs.rules.filter(rule=>rule.id!==s.ruleEditor.id))){s.ruleEditor=null;draw()}return
    case'enable-push':await enablePush();return
    case'quiet-hours':s.quietStart=s.prefs.quietStart;s.quietEnd=s.prefs.quietEnd;s.overlay='quiet-hours';break
    case'clear-cache':await replica.call('clear-bodies');say('BODY CACHE CLEARED');return
    case'toggle-tracking':await savePreference('stripTracking',!s.prefs.stripTracking);return
    case'manage-senders':s.allowedSendersText=(s.prefs.allowedSenders||[]).join('\n');s.overlay='allowed-senders';break
    case'unsubscribe':s.unsubscribe={header:m.unsubscribe,from:m.who};s.overlay='unsubscribe';break
    case'unlink':if(!window.confirm('Unlink iCloud? Your mail on iCloud stays untouched.'))return;try{await request('/api/account',{method:'DELETE'});s.msgs=[];bodyRevision++;bodyRequests.clear();s.bodies={};await replica?.call('clear');replica?.close();replica=null;localStorage.removeItem('mail.lastAccount');s.account.connected=false;s.account.icloud_email='';draw()}catch(err){say(errorText(err),true)}return
    case'logout':if((s.pending||s.draft)&&!window.confirm('Sign out and clear local drafts and pending changes from this device? Saved iCloud drafts stay in your mailbox.'))return;try{await request('/auth/logout',{method:'POST'});s.authPassword='';navigator.clearAppBadge?.()?.catch(()=>{});bodyRevision++;bodyRequests.clear();s.bodies={};await replica?.call('clear');replica?.close();localStorage.removeItem('mail.lastAccount');location.assign('/')}catch(err){say(errorText(err),true)}return
  }
  draw()
}
root.addEventListener('submit',async e=>{
  e.preventDefault()
  if(e.target.id==='rule-form'){await saveRule();return}
  if(e.target.id==='quiet-hours-form'){const patch={quietStart:s.quietStart,quietEnd:s.quietEnd};try{await request('/api/mail/preferences',{method:'PATCH',body:JSON.stringify(patch)});Object.assign(s.prefs,patch);s.overlay=null;draw()}catch(err){say(errorText(err),true)}return}
  if(e.target.id==='allowed-senders-form'){const senders=new FormData(e.target).get('senders').split(/[\n,;]/).map(value=>value.trim().toLowerCase()).filter(Boolean);if(await savePreference('allowedSenders',[...new Set(senders)])){s.overlay=null;draw()}return}
  if(e.target.id==='auth-form')await submitAuth(e.target)
  if(e.target.id==='link-form')await linkAccount(e.target)
  if(e.target.id==='compose-form')await sendDraft()
  if(e.target.id==='schedule-form'){const time=new Date(new FormData(e.target).get('send-at'));if(!Number.isFinite(+time)||time<=new Date()){say('Choose a future send time.',true);return}s.sendAt=time.toISOString();s.overlay=null;await sendDraft()}
  if(e.target.id==='snooze-form'){const time=new Date(new FormData(e.target).get('snooze-at'));if(!Number.isFinite(+time)||time<=new Date()){say('Choose a future snooze time.',true);return}await mutate('snooze',{until:time.toISOString()})}
  if(e.target.id==='identity-form'){
    const data=new FormData(e.target),identity={address:data.get('identity-address').trim().toLowerCase(),name:data.get('identity-name').trim(),signature:data.get('identity-signature')},identities=[...s.prefs.identities||[]]
    if(s.identityIndex>=0)identities[s.identityIndex]=identity;else identities.push(identity)
    s.identityForm=null;await savePreference('identities',identities)
  }
  if(e.target.id==='folder-form'){
    if(s.folderBusy)return
    const name=new FormData(e.target).get('folder-name').trim()
    s.folderBusy=true;draw();try{await request('/api/mail/mutate',{method:'POST',body:JSON.stringify({op:'createFolder',folder:name})});s.overlay=null;await loadFolders();say('FOLDER CREATED')}catch(err){say(errorText(err),true)}finally{s.folderBusy=false;draw()}
  }
  if(e.target.id==='rename-folder-form'){
    if(s.folderBusy)return
    const folder=s.managedFolder,target=new FormData(e.target).get('rename-folder').trim(),delimiter=s.folderInfo?.find(info=>info.name===folder)?.delimiter||'/'
    if(target===folder){s.overlay=null;draw();return}
    s.folderBusy=true;draw();try{await request('/api/mail/mutate',{method:'POST',body:JSON.stringify({op:'renameFolder',folder,target,validity:s.folderInfo?.find(info=>info.name===folder)?.uidValidity})});s.overlay=null;await replica.call('remap-folder',{from:folder,to:target,delimiter});await replica.call('refresh');await loadFolders();say('FOLDER RENAMED')}catch(err){say('Folder could not be renamed. '+errorText(err),true)}finally{s.folderBusy=false;draw()}
  }
})
root.addEventListener('input',e=>{
  const el=e.target
  if(el.name==='email')s.authEmail=el.value
  if(el.name==='password')s.authPassword=el.value
  if(el.id==='sq'){searchMail(el.value)}
  if(el.id==='pq'){s.pal=el.value;s.palI=0;draw()}
  if(el.id==='pick-query'){s.pickQuery=el.value;s.pickI=0;draw()}
  if(el.id==='folder-name')s.folderName=el.value
  if(el.id==='rename-folder')s.renameFolder=el.value
  if(el.id==='snooze-at')s.snoozeAt=el.value
  if(el.dataset.ruleField&&s.ruleEditor){s.ruleEditor[el.dataset.ruleGroup][Number(el.dataset.ruleIndex)][el.dataset.ruleField]=el.value;queueRulePreview()}
  if(el.name==='quietStart'||el.name==='quietEnd')s[el.name]=el.value
  if(el.id==='allowed-senders')s.allowedSendersText=el.value
  if(el.name==='send-at')s.scheduleAt=el.value
  if(el.name?.startsWith('identity-'))s.identityForm[el.name.slice(9)]=el.value
  if(el.id.startsWith('draft-')){s.draft[el.name||el.dataset.field]=el.isContentEditable?el.innerHTML:el.value;draftChanged().catch(err=>say('Draft could not be saved locally: '+errorText(err),true));if(['to','cc','bcc'].includes(el.name)){const text=el.value,token=text.split(/[,;]+/).at(-1).trim();replica.call('contacts',{query:token}).then(contacts=>{if(s.draft?.[el.name]!==text)return;s.contacts=contacts.map(value=>text.slice(0,text.length-text.split(/[,;]+/).at(-1).length)+value);draw()}).catch(()=>{})}}
})
root.addEventListener('paste',e=>{if(e.target.closest('[contenteditable]')){e.preventDefault();document.execCommand('insertText',false,e.clipboardData.getData('text/plain'))}})
root.addEventListener('change',e=>{
  if(e.target.id==='rule-apply'){s.ruleApply=e.target.checked;draw();return}
  if(e.target.matches('select[data-rule-field]')&&s.ruleEditor){const el=e.target,entry=s.ruleEditor[el.dataset.ruleGroup][Number(el.dataset.ruleIndex)];entry[el.dataset.ruleField]=el.value;if(el.dataset.ruleField==='action')entry.target=el.value==='flag'?'red':'';queueRulePreview();draw();return}
  if(e.target.id==='allowed-senders'){s.allowedSendersText=e.target.value;return}
  if(e.target.id==='attachment-input'){attachFiles(e.target.files);return}
  if(e.target.id==='draft-from'){
    const old=s.draft.signature||'',identity=s.prefs.identities?.find(identity=>identity.address===e.target.value)
    s.draft.from=e.target.value;s.draft.signature=identity?.signature||''
    if(!s.draft.html){if(old&&s.draft.body.endsWith(old))s.draft.body=s.draft.body.slice(0,-old.length).trimEnd();if(s.draft.signature)s.draft.body+='\n\n'+s.draft.signature}
    draftChanged().catch(err=>say(errorText(err),true));draw()
  }
})
root.addEventListener('pointerdown',()=>{pointerBusy=true})
const finishPointer=()=>setTimeout(()=>{pointerBusy=false;if(drawPending){drawPending=false;draw()}},0)
for(const event of ['pointerup','pointercancel','dragend','blur'])window.addEventListener(event,finishPointer,true)
root.addEventListener('click',e=>{
  pointerBusy=false
  if(Date.now()<s.suppressClickUntil){e.preventDefault();return}
  const searchChip=e.target.closest('[data-search-remove]');if(searchChip){const tokens=searchTokens(s.search);tokens.splice(Number(searchChip.dataset.searchRemove),1);searchMail(tokens.join(' '));return}
  const remote=e.target.closest('[data-remote]');if(remote){loadRemote(remote.dataset.remote);return}
  const attachment=e.target.closest('[data-attachment]');if(attachment){e.preventDefault();const links=[...attachment.closest('.files').querySelectorAll('[data-attachment]')].map(link=>({href:link.getAttribute('href'),filename:link.dataset.attachment}));previewAttachment(links.findIndex(link=>link.href===attachment.getAttribute('href')),links);return}
  const snooze=e.target.closest('[data-snooze]');if(snooze){chooseSnooze(snooze.dataset.snooze);return}
  const manageFolder=e.target.closest('[data-manage-folder]');if(manageFolder){s.managedFolder=manageFolder.dataset.manageFolder;s.renameFolder=s.managedFolder;s.overlay='folder-settings';draw();return}
  const actionMessage=e.target.closest('[data-message-index]');if(actionMessage)s.cur=Number(actionMessage.dataset.messageIndex)
  const remove=e.target.closest('[data-remove-file]');if(remove){s.draft.attachments.splice(Number(remove.dataset.removeFile),1);draftChanged().catch(err=>say(errorText(err),true));draw();return}
  const format=e.target.closest('[data-format]');if(format){const editor=document.getElementById('draft-body');editor?.focus();if(format.dataset.format==='createLink'){const url=window.prompt('Link URL');if(url&&!/^(https?:\/\/|mailto:)/i.test(url)){say('Use an https, http or mailto link.',true);return}if(url)document.execCommand('createLink',false,url)}else document.execCommand(format.dataset.format);s.draft.body=editor.innerHTML;draftChanged().catch(err=>say(errorText(err),true));return}
  if(s.authMin&&e.target.closest('.auth-window .wt')&&!e.target.closest('[data-ctl-a]')){s.authMin=false;draw();return}
  const row=e.target.closest('.row')
  if(row){s.cur=Number(row.dataset.i);s.bodyError='';if(e.target.closest('.mk')){act('select');return}}
  const thread=e.target.closest('[data-thread-message]');if(thread){const raw=current(s)?.threadMessages?.find(m=>m.key===thread.dataset.threadMessage);if(raw){s.openMessages=s.openMessages||[];s.openMessages=s.openMessages.includes(raw.key)?s.openMessages.filter(key=>key!==raw.key):[...s.openMessages,raw.key];draw();loadBody(false,message(raw,raw.folder))}return}
  const ctl=e.target.closest('[data-ctl-a]')
  if(ctl){const [op,w]=ctl.dataset.ctlA.split('-');if(w==='auth'){if(op==='max'){s.authMax=!s.authMax;s.authMin=false}else{s.authMin=true;if(op==='close')s.authPassword=''}draw();return}if(w==='compose'){act(op==='max'?'full-compose':'min-compose');return}if(w==='side')s.side=op==='max'?true:false;if(w==='reader'){if(op==='max')s.max=!s.max;else if(op==='close')s.cur=-1;else{s.max=false;s.mobilePane='list'}}if(w==='list'&&op==='max')s.max=false;if(w==='settings')s.view='mail';if(w==='preview'){s.overlay=null}draw();return}
  const a=e.target.closest('[data-a]');if(a){act(a.dataset.a).catch(err=>say(errorText(err),true));return}
  const folder=e.target.closest('[data-f]');if(folder){act('go:'+folder.dataset.f);return}
  const tab=e.target.closest('[data-tab]');if(tab){s.keyEdit=null;s.tab=tab.dataset.tab;draw();loadSettings(s.tab);return}
  const filter=e.target.closest('[data-filter]');if(filter){s.filter=filter.dataset.filter;s.cur=0;draw();loadFolder(s.folder,false,true);return}
  const theme=e.target.closest('[data-theme-value]');if(theme){savePreference('theme',theme.dataset.themeValue);return}
  const pref=e.target.closest('[data-pref]');if(pref){s.keyEdit=null;let value=pref.dataset.value;if(value==='toggle')value=!s.prefs[pref.dataset.pref];if(pref.dataset.pref==='undoSeconds')value=Number(value);savePreference(pref.dataset.pref,value);return}
  const pick=e.target.closest('[data-pick]');if(pick){choose(pick.dataset.pick);return}
  if(row){s.focus='list';s.mobilePane='reader';draw();loadBody()}
})
function prepareBodyHTML(body){
 if(!body.bodyHTML)return
 const template=document.createElement('template');template.innerHTML=body.bodyHTML
 for(const image of template.content.querySelectorAll('img')){
  image.removeAttribute('srcset')
  const id=image.getAttribute('data-remote-id'),source=image.getAttribute('src')||''
  if(id!==null){const loaded=body.loadedImages?.[id];if(loaded)image.src=loaded;else image.removeAttribute('src')}
  else if(!/^data:image\/(png|jpeg|gif|webp);/i.test(source))image.removeAttribute('src')
 }
 body.displayHTML=template.innerHTML
}
async function loadRemoteAutomatically(){
 const mail=current(s);if(!mail)return
 for(const m of mail.threadMessages?.map(raw=>message(raw,raw.folder))||[mail]){
  const body=s.bodies[m.bodyKey||m.id]
  if(body?.remoteImages?.length&&!body.remoteLoading&&!body.remoteAttempted&&(['proxy','original'].includes(s.prefs.remoteContent)||s.prefs.allowedSenders?.some(sender=>sender.toLowerCase()===m.em.toLowerCase())))loadRemote(m.id)
 }
}
async function loadRemote(key){
 const body=s.bodies[key];if(!body||body.remoteLoading)return
 body.remoteLoading=true;body.remoteAttempted=true;body.remoteError='';body.loadedImages=body.loadedImages||{};draw()
 try{
  let bytes=Object.values(body.loadedImages).reduce((sum,data)=>sum+data.length*3/4,0),failures=0
  for(const image of body.remoteImages||[]){
   if(s.bodies[key]!==body)return
   if(body.loadedImages[image.id])continue
   try{const blob=await proxyImage(image.url);if(s.bodies[key]!==body)return
   if(bytes+blob.size>40*1024*1024){body.remoteError='Remote images exceed the 40 MB message limit.';break}
   bytes+=blob.size;body.loadedImages[image.id]='data:'+blob.type+';base64,'+await base64File(blob)}catch{failures++}
  }
  if(failures)body.remoteError=`${failures} remote image${failures===1?'':'s'} could not be loaded. Retry to try again.`
 }catch(err){body.remoteError=errorText(err)}finally{body.remoteLoading=false;prepareBodyHTML(body);draw()}
}
async function previewAttachment(index,files=s.preview?.files){
 if(!files||index<0||index>=files.length)return
 if(s.preview?.url)URL.revokeObjectURL(s.preview.url)
  s.preview?.pdf?.destroy().catch(()=>{})
 const p=s.preview={files,index,loading:true};s.overlay='preview';draw()
 try{
  const blob=await attachmentBlob(files[index].href+'&preview=1')
  if(s.preview!==p)return
  p.mime=blob.type.split(';')[0];p.size=blob.size
  if(/^image\/(png|jpeg|gif|webp)$/.test(p.mime))p.url=URL.createObjectURL(blob)
  else if(p.mime==='application/pdf'){
    const {openPDF}=await import('./pdf-preview.js');p.pdf=await openPDF(blob)
    if(s.preview!==p){await p.pdf.destroy();return}
    p.page=1;const rendered=await p.pdf.render(1);p.url=rendered.url;p.pdfText=rendered.text
  }
  else if(p.mime==='text/plain')p.text=await blob.text()
 }catch(err){p.error=errorText(err)}finally{if(s.preview===p){p.loading=false;draw()}}
}
async function previewPDFPage(number){
 const p=s.preview;if(!p?.pdf||p.loading||number<1||number>p.pdf.pages)return
 p.loading=true;draw()
 try{const rendered=await p.pdf.render(number);if(s.preview===p){p.page=number;p.url=rendered.url;p.pdfText=rendered.text}}
 catch(err){if(s.preview===p)p.error=errorText(err)}finally{if(s.preview===p){p.loading=false;draw()}}
}
async function choose(value){const overlay=s.overlay;s.overlay=null;if(overlay==='flag')await mutate('flag',{color:value},m=>({color:m.fl||''}));else await mutate('move',{target:value})}
async function chooseSnooze(key){const option=snoozeOptions().find(option=>option.key===key);if(!option)return;if(key==='custom'){s.overlay='snooze-date';draw();return}await mutate('snooze',{until:option.date?.toISOString()||null})}
window.addEventListener('keydown',e=>{
 if(e.key==='Tab'){
  const dialog=root.querySelector('[role="dialog"][aria-modal="true"]')
  if(dialog){const controls=[...dialog.querySelectorAll('button:not(:disabled),input:not(:disabled),textarea:not(:disabled),select:not(:disabled),a[href],[tabindex="0"]')].filter(el=>el.getClientRects().length),first=controls[0],last=controls.at(-1);if(!first){e.preventDefault();dialog.focus();return}if(e.shiftKey&&(!dialog.contains(document.activeElement)||document.activeElement===first)||!e.shiftKey&&(!dialog.contains(document.activeElement)||document.activeElement===last)){e.preventDefault();(e.shiftKey?last:first).focus();return}}
 }
 const key=e.key,meta=e.metaKey||e.ctrlKey,typing=e.target.matches('input,textarea,[contenteditable="true"]'),stroke=keyStroke(e),bindings=keyBindings(s.prefs)
 if(s.keyEdit){if(key==='Escape'){e.preventDefault();s.keyEdit=null;draw();return}if(stroke){e.preventDefault();rebindKey(stroke)}return}
 if(stroke===bindings.settings){e.preventDefault();act('settings');return}
 if(s.compose&&stroke===bindings.send){e.preventDefault();sendDraft();return}
 if(s.compose&&stroke===bindings['full-compose']){e.preventDefault();act('full-compose');return}
 if(stroke===bindings.palette&&(!typing||meta)&&s.overlay!=='palette'){e.preventDefault();act('palette');return}
 if(key==='Escape'){e.preventDefault();if(s.overlay){s.overlay=null}else if(s.compose){s.compose=null}else if(s.view==='settings'){s.view='mail'}else if(s.picked.length){s.picked=[]}else if(s.search){searchMail('')}else{s.mobilePane='list';s.max=false}draw();return}
  if(s.overlay==='preview'){if(key==='ArrowLeft'||key==='ArrowRight'||key==='d'){e.preventDefault();act(key==='d'?'preview-download':key==='ArrowLeft'?'preview-prev':'preview-next')}return}
  if(s.overlay==='palette'){
    const options=paletteItems(s)
    if(key==='ArrowDown'||key==='ArrowUp'){e.preventDefault();s.palI=options.length?(s.palI+(key==='ArrowDown'?1:-1)+options.length)%options.length:0;draw()}
    if(key==='Enter'){e.preventDefault();const command=options[s.palI];s.overlay=null;if(command)act(command[1]);else{searchMail(s.pal)}}return
  }
  if(s.overlay==='move'||s.overlay==='flag'||s.overlay==='snooze'){
    const options=s.overlay==='snooze'?snoozeOptions().map(option=>option.key):s.overlay==='flag'?COLORS:s.folders.filter(f=>f.toLowerCase().includes(s.pickQuery.toLowerCase()))
    if(key==='ArrowDown'||key==='ArrowUp'){e.preventDefault();s.pickI=options.length?(s.pickI+(key==='ArrowDown'?1:-1)+options.length)%options.length:0;draw()}
    if(key==='Enter'||!typing&&/^[1-9]$/.test(key)){e.preventDefault();const value=options[key==='Enter'?s.pickI:Number(key)-1];if(value){if(s.overlay==='snooze')chooseSnooze(value);else choose(value)}}return
  }
  if(typing){if(e.target.id==='sq'&&key==='Enter'){e.preventDefault();s.overlay=null;draw()}return}
 if(s.overlay||s.compose||!stroke)return
 const sequence=s.chord?s.chord+' '+stroke:stroke,action=Object.entries(bindings).find(([action,value])=>!['send','full-compose','min-compose'].includes(action)&&value===sequence)?.[0]
 if(action){if(s.view!=='mail'&&!['help','palette','search','settings'].includes(action))return;e.preventDefault();clearTimeout(chordTimer);s.chord=null;act(action).catch(err=>say(errorText(err),true));return}
 if(Object.values(bindings).some(binding=>binding.startsWith(sequence+' '))&&s.view==='mail'){e.preventDefault();s.chord=sequence;draw();clearTimeout(chordTimer);chordTimer=setTimeout(()=>{s.chord=null;draw()},900);return}
 if(s.chord){s.chord=null;draw();return}
 if(s.view==='mail'&&!meta&&(key==='ArrowDown'||key==='ArrowUp')){e.preventDefault();act(key==='ArrowDown'?'next':'prev')}

})
let swipeGesture,dragGhost,dragKeys=[]
root.addEventListener('pointerdown',event=>{
  const row=event.target.closest('.row');if(!row||!matchMedia('(max-width:760px)').matches||event.pointerType==='mouse'||event.target.closest('button,a'))return
  swipeGesture={row,id:row.dataset.id,x:event.clientX,y:event.clientY,pointer:event.pointerId,horizontal:false,dx:0}
})
root.addEventListener('pointermove',event=>{
  const gesture=swipeGesture;if(!gesture||gesture.pointer!==event.pointerId)return
  const dx=event.clientX-gesture.x,dy=event.clientY-gesture.y
  if(!gesture.horizontal){if(Math.abs(dy)>12&&Math.abs(dy)>Math.abs(dx)){swipeGesture=null;return}if(Math.abs(dx)<12)return;gesture.horizontal=true;gesture.row.setPointerCapture(event.pointerId)}
  event.preventDefault();gesture.dx=dx;gesture.row.style.transform=`translateX(${Math.max(-130,Math.min(0,dx))}px)`
},{passive:false})
const finishSwipe=event=>{const gesture=swipeGesture;if(!gesture||event.pointerId!==gesture.pointer)return;swipeGesture=null;if(gesture.horizontal){s.swipeID=event.type==='pointercancel'?null:gesture.dx<-72?gesture.id:null;s.suppressClickUntil=Date.now()+350;draw()}}
root.addEventListener('pointerup',finishSwipe);root.addEventListener('pointercancel',finishSwipe)
function endDrag(){dragGhost?.remove();dragGhost=null;dragKeys=[];root.querySelectorAll('.drag-target').forEach(el=>el.classList.remove('drag-target'))}
root.addEventListener('dragstart',event=>{
  const row=event.target.closest('.row');if(!row)return
  const m=visible(s)[Number(row.dataset.i)];if(!m||m.pendingLocation){event.preventDefault();return}
  s.cur=Number(row.dataset.i);dragKeys=s.picked.includes(m.id)?[...s.picked]:[m.id]
  event.dataTransfer.setData('application/x-terminal-mail',JSON.stringify({account:replicaAccount,keys:dragKeys}));event.dataTransfer.effectAllowed='move'
  const blank=document.createElement('canvas');blank.width=blank.height=1;event.dataTransfer.setDragImage(blank,0,0)
  dragGhost=document.createElement('div');dragGhost.className='dragghost';dragGhost.textContent=`${dragKeys.length} CONVERSATION${dragKeys.length===1?'':'S'}`;document.body.append(dragGhost);dragGhost.style.left=event.clientX+12+'px';dragGhost.style.top=event.clientY+12+'px'
})
root.addEventListener('dragover',event=>{
  if(!event.dataTransfer.types.includes('application/x-terminal-mail'))return
  if(dragGhost){dragGhost.style.left=event.clientX+12+'px';dragGhost.style.top=event.clientY+12+'px'}
  const folder=event.target.closest('[data-f]');root.querySelectorAll('.drag-target').forEach(el=>{if(el!==folder)el.classList.remove('drag-target')})
  if(!folder||['vip','flagged'].includes(folder.dataset.f))return
  event.preventDefault();event.dataTransfer.dropEffect='move';folder.classList.add('drag-target');if(dragGhost)dragGhost.textContent=`${dragKeys.length} CONVERSATION${dragKeys.length===1?'':'S'} → ${folder.dataset.f}`
})
root.addEventListener('drop',async event=>{
  const folder=event.target.closest('[data-f]');if(!folder||!event.dataTransfer.types.includes('application/x-terminal-mail')){endDrag();return}
  event.preventDefault();let payload;try{payload=JSON.parse(event.dataTransfer.getData('application/x-terminal-mail'))}catch{endDrag();return}
  if(payload.account!==replicaAccount||!Array.isArray(payload.keys)||['vip','flagged'].includes(folder.dataset.f)){endDrag();return}
  s.picked=s.msgs.filter(m=>payload.keys.includes(m.id)).map(m=>m.id);endDrag();if(!s.picked.length)return
  if(folder.dataset.f==='snoozed'){s.overlay='snooze';s.pickI=0;draw();return}
  await mutate('move',{target:targetFolder(s,folder.dataset.f)})
})
document.addEventListener('dragend',endDrag)
window.addEventListener('offline',()=>{s.offline=true;draw()})
window.addEventListener('online',()=>{s.offline=false;replica?.call('retry');if(s.account?.connected){loadFolder(s.folder,false,true);if(s.search)replica?.call('remote-search',{search:s.search});if(s.draft)saveDraft()}})
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&s.account?.connected)replica?.call('retry')})
window.addEventListener('pagehide',()=>replica?.close())
window.addEventListener('resize',()=>{cancelAnimationFrame(listFrame);listFrame=requestAnimationFrame(draw)})
window.addEventListener('pageshow',event=>{if(event.persisted&&s.account?.connected)initReplica().then(loadFolders).catch(err=>say(errorText(err),true))})
window.addEventListener('beforeinstallprompt',event=>{event.preventDefault();s.installEvent=event;draw()})
if('serviceWorker' in navigator){
 navigator.serviceWorker.addEventListener('message',event=>{if(s.account?.connected)onReplicaEvent(event.data)})
 if(import.meta.env.PROD)navigator.serviceWorker.register('/sw.js',{type:'module'}).catch(err=>console.error('Offline shell registration failed',err))
}
start().then(async()=>{
 if(s.account?.connected&&params.has('notice')){try{
  const registration=await navigator.serviceWorker.ready,intent=await new Promise((resolve,reject)=>{const channel=new MessageChannel(),timer=setTimeout(()=>{channel.port1.close();reject(new Error('This notification expired.'))},5000);channel.port1.onmessage=({data})=>{clearTimeout(timer);channel.port1.close();resolve(data)};registration.active.postMessage({type:'notification-intent',id:params.get('notice')},[channel.port2])})
  if(!intent||intent.account!==s.account.icloud_email||intent.owner!==s.account.email)throw new Error('This notification no longer matches your mailbox.')
  const raw=await replica.call('find-message',intent);if(!raw)throw new Error('This message has moved or is no longer available.')
  await loadFolder(raw.folder);if(!s.msgs.some(m=>m.uid===raw.id&&m.uidValidity===raw.uidValidity))s.msgs.unshift(message(raw,raw.folder));s.cur=visible(s).findIndex(m=>m.uid===raw.id&&m.uidValidity===raw.uidValidity);s.mobilePane='reader';s.focus='reader';draw();await loadBody();if(['reply','read','archive'].includes(intent.action))await act(intent.action)
 }catch(err){say(errorText(err),true)}finally{history.replaceState(null,'',location.pathname)}}
 if(s.account?.connected&&params.has('mailto')){try{const url=new URL(params.get('mailto'));if(url.protocol==='mailto:'){newDraft();s.draft.to=decodeURIComponent(url.pathname);s.draft.subject=url.searchParams.get('subject')||'';s.draft.body=url.searchParams.get('body')||'';draw();replica?.call('put',{store:'drafts',key:'current',value:s.draft})}}catch{say('The mailto link could not be opened.',true)}}
})
