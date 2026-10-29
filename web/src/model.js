export const SYSTEM_FOLDERS = [
  ['inbox', 'INBOX', 'inbox'], ['vip', 'VIP', 'star'], ['flagged', 'FLAGGED', 'flag'],
  ['snoozed', 'SNOOZED', 'clock'], ['drafts', 'DRAFTS', 'draft'], ['sent', 'SENT', 'send'],
  ['archive', 'ARCHIVE', 'archive'], ['junk', 'JUNK', 'junk'], ['trash', 'TRASH', 'trash'],
]
export const COLORS = ['red', 'orange', 'yellow', 'green', 'blue', 'purple', 'gray']
const timeFormat=new Intl.DateTimeFormat(undefined,{hour:'2-digit',minute:'2-digit'}),dateFormat=new Intl.DateTimeFormat(undefined,{month:'short',day:'numeric'}),monthFormat=new Intl.DateTimeFormat(undefined,{month:'long',year:'numeric'}),wakeFormat=new Intl.DateTimeFormat(undefined,{weekday:'long',month:'short',day:'numeric'})
export function snoozeOptions(now=new Date()){
  const at=(days,hour)=>{const date=new Date(now);date.setDate(date.getDate()+days);date.setHours(hour,0,0,0);return date}
  let later=at(0,18);if(later<=now)later=new Date(+now+3600000)
  let weekend=at((6-now.getDay()+7)%7,9);if(weekend<=now)weekend=at((6-now.getDay()+7)%7+7,9)
  const dateLabel=date=>`${date.toDateString()===now.toDateString()?'':date.toLocaleDateString([],{weekday:'short'})+' '}${date.toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'})}`
  return [['later',later.toDateString()===now.toDateString()?'Later today':'In an hour',later],['tomorrow','Tomorrow',at(1,8)],['weekend','This weekend',weekend],['week','Next week',at((1-now.getDay()+7)%7||7,8)],['someday','Someday',null],['custom','Pick a date…',null]].map(([key,label,date])=>({key,label,date,when:date?dateLabel(date):key==='someday'?'—':''}))
}
export function conversations(rows,all=rows){
  const groups=new Map(),members=new Map()
  for(const row of all){const key=row.threadId||row.key;if(!members.has(key))members.set(key,[]);members.get(key).push(row)}
  for(const row of rows){const key=row.threadId||row.key;if(!groups.has(key))groups.set(key,[]);groups.get(key).push(row)}
  return [...groups].map(([key,items])=>{
    items.sort((a,b)=>new Date(b.date)-new Date(a.date)||a.key.localeCompare(b.key))
    const thread=(members.get(key)||items).sort((a,b)=>new Date(a.date)-new Date(b.date)||a.key.localeCompare(b.key))
    return {...items[0],unread:items.some(m=>m.unread),flagColor:items.find(m=>m.flagColor)?.flagColor||'',n:thread.length,threadMessages:thread,actionMessages:items}
  }).sort((a,b)=>new Date(b.date)-new Date(a.date)||a.key.localeCompare(b.key))
}
export const TABS = [['account','ACCOUNT','user'],['identities','IDENTITIES','mail'],['appearance','APPEARANCE','eye'],['keyboard','KEYBOARD','key'],['notifications','NOTIFY','bolt'],['privacy','PRIVACY','lock'],['rules','RULES','gear'],['agents','AGENT_ACTIVITY','agent']]

export function role(name) {
  return ({inbox:'inbox', drafts:'drafts', sent:'sent', 'sent messages':'sent', archive:'archive', junk:'junk', spam:'junk', trash:'trash', 'deleted messages':'trash', snoozed:'snoozed'})[name.toLowerCase()] || name
}
export function address(from = '') { return from.match(/<([^>]+)>/)?.[1] || from.trim() }
export function replyRecipients(mail,ownAddresses,all=false){
 const own=new Set(ownAddresses.map(value=>address(value).toLowerCase())),ours=value=>own.has(address(value).toLowerCase()),self=ours(mail.from)
 const to=self?(mail.to||[]).filter(value=>!ours(value)):[mail.from],seen=new Set(to.map(value=>address(value).toLowerCase()))
 return {to:(to.length?to:[mail.from]).map(address),cc:(all?[...(self?[]:mail.to||[]),...mail.cc||[]]:[]).filter(value=>{const key=address(value).toLowerCase();if(own.has(key)||seen.has(key))return false;seen.add(key);return true}).map(address)}
}
export const recipients=text=>(text||'').match(/(?:"(?:[^"\\]|\\.)*"|[^,;])+/g)?.map(value=>value.trim()).filter(Boolean)||[]
// Rows synced before the server stopped re-encoding names still carry RFC 2047 words like =?utf-8?q?…?=.
export const decodeWords=text=>text.replace(/=\?([^?]+)\?([bq])\?([^?]*)\?=(?:\s+(?==\?))?/gi,(word,charset,encoding,data)=>{try{const binary=encoding.toLowerCase()==='b'?atob(data):data.replace(/_/g,' ').replace(/=([0-9a-f]{2})/gi,(_,hex)=>String.fromCharCode(parseInt(hex,16)));return new TextDecoder(charset).decode(Uint8Array.from(binary,char=>char.charCodeAt(0)))}catch{return word}})
export function message(raw, folder) {
  const date = new Date(raw.date)
  const today = new Date(); today.setHours(0,0,0,0)
  const who = decodeWords(raw.from?.replace(/\s*<[^>]+>/, '').replace(/^"|"$/g,'').replace(/\\(.)/g,'$1') || '') || '(unknown sender)'
  const wake=role(folder)==='snoozed'?(raw.snoozeUntil?new Date(raw.snoozeUntil):null):undefined
  return { ...raw, uid:raw.id, id:`${folder}|${raw.uidValidity}|${raw.id}`, folder, f:role(folder), who, em:address(raw.from), subj:raw.subject || '(no subject)', snip:raw.snippet || '', un:raw.unread, fl:raw.flagColor || null, att:raw.attachments?.length || 0,
    t:date >= today ? timeFormat.format(date) : dateFormat.format(date),
    day:wake===null?'Someday':wake?wakeFormat.format(wake):date >= today ? 'Today' : date >= new Date(today.getTime()-86400000) ? 'Yesterday' : monthFormat.format(date),
  }
}
export const searchTokens=query=>query.match(/(?:[^\s"]+|"[^"]*")+/g)||[]
export function matches(m, query, bodyText='', plainMatched=false) {
  const tokens = searchTokens(query)
  return tokens.every(token => {
    const i=token.indexOf(':'); const key=i<0?'':token.slice(0,i).toLowerCase()
    const value=(i<0?token:token.slice(i+1)).replace(/^"|"$/g,'').toLowerCase()
    if(key==='from')return `${m.who} ${m.em}`.toLowerCase().includes(value)
    if(key==='to')return (m.to||[]).join(' ').toLowerCase().includes(value)
    if(key==='subject')return m.subj.toLowerCase().includes(value)
    if(key==='has')return value==='attachment' && m.att>0
    if(key==='in')return ['all','anywhere'].includes(value)||[m.f,m.folder,m.folderRole].filter(Boolean).some(f=>f.toLowerCase()===value)
    if(key==='is')return value==='unread'?m.un:value==='read'?!m.un:value==='flagged'?!!m.fl:false
    if(key==='after'||key==='before')return key==='after'?new Date(m.date)>=new Date(value):new Date(m.date)<new Date(value)
    return plainMatched||`${m.subj} ${m.snip} ${m.who} ${m.em} ${(m.to||[]).join(' ')} ${bodyText}`.toLowerCase().includes(value)
  })
}
export function visible(s) {
  let rows=s.msgs
  if(!s.search&&s.folder==='flagged')rows=rows.filter(m=>m.fl)
  if(!s.search&&s.folder==='vip')rows=rows.filter(m=>s.prefs.vip?.includes(m.em))
  if(s.search)rows=rows.filter(m=>matches(m,s.search,'',m.searchQuery===s.search))
  if(s.filter==='unread')rows=rows.filter(m=>m.un)
  if(s.filter==='flagged')rows=rows.filter(m=>m.fl)
  if(s.filter==='att')rows=rows.filter(m=>m.att)
  if(!s.search&&role(s.folder)==='snoozed')rows=[...rows].sort((a,b)=>(a.snoozeUntil?Date.parse(a.snoozeUntil):Infinity)-(b.snoozeUntil?Date.parse(b.snoozeUntil):Infinity))
  return rows
}
export const current = s => visible(s)[s.cur]
export function rebase(rows, transactions) {
  let result=rows.map(row=>({...row}))
  const pending=transactions.filter(tx=>tx.kind==='mutation'&&['pending','awaiting-sync'].includes(tx.status)),inverted=new Set(pending.filter(tx=>tx.body.op==='undoMove').map(tx=>tx.body.originalTransaction))
  for(const tx of pending){
    const mutation=tx.body
    const matches=row=>row.folder===mutation.folder&&row.uid===mutation.id&&row.uidValidity===mutation.validity
    if(inverted.has(tx.id)){for(const row of result.filter(matches))row.pendingLocation=true;continue}
    if(mutation.op==='move'||mutation.op==='delete'||mutation.op==='unsnooze'||mutation.op==='snooze'&&role(mutation.folder)!=='snoozed')result=result.filter(row=>!matches(row))
    else if(mutation.op==='undoMove'){for(const row of result.filter(row=>row.moveTransaction===mutation.originalTransaction)){row.bodyKey=row.bodyKey||`${row.folder}|${row.uidValidity}|${row.uid||row.id}`;row.folder=mutation.folder;row.f=role(mutation.folder);row.pendingLocation=true}}
    else for(const row of result.filter(matches)){if(mutation.op==='read')row.un=!mutation.read;if(mutation.op==='flag')row.fl=mutation.color||null}
  }
  return result
}
export const targetFolder = (s, name) => s.folderInfo?.find(folder=>folder.role===name)?.name || s.folders.find(f=>role(f)===name) || ({inbox:'INBOX',drafts:'Drafts',sent:'Sent Messages',trash:'Deleted Messages',junk:'Junk',archive:'Archive',snoozed:'Snoozed'})[name] || name

export function unsubscribeLinks(header=''){
 return [...header.matchAll(/<([^<>]+)>/g)].map(match=>{try{const url=new URL(match[1]);if(url.protocol==='https:'&&!url.username&&!url.password)return {url:url.href,label:url.hostname};if(url.protocol==='mailto:'&&!/[\r\n]/.test(decodeURIComponent(url.pathname)))return {url:url.href,label:decodeURIComponent(url.pathname)}}catch{};return null}).filter(Boolean)
}
