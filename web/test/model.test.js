import test from 'node:test'
import assert from 'node:assert/strict'
import { message, matches, visible, role, targetFolder, rebase, conversations,recipients,replyRecipients,snoozeOptions,unsubscribeLinks } from '../src/model.js'
import { authView, mailView, emailDocument } from '../src/views.js'

const raw={id:'42',uidValidity:7,from:'Mira Okafor <mira@example.com>',to:['me@icloud.com'],subject:'Launch <script>bad()</script>',snippet:'Review notes',date:'2026-10-28T11:30:00Z',unread:true,flagColor:'green',attachments:[{filename:'notes.pdf',size:1024}]}
test('reply recipients include original To peers and exclude own addresses and duplicates',()=>{
 const own=['me@icloud.com','alias@icloud.com'],incoming={from:'Doe, Mira <mira@example.com>',to:['ME@icloud.com','peer@example.com'],cc:['alias@icloud.com','copy@example.com','peer@example.com','mira@example.com']}
 assert.deepEqual(replyRecipients(incoming,own,true),{to:['mira@example.com'],cc:['peer@example.com','copy@example.com']})
 assert.deepEqual(replyRecipients(incoming,own),{to:['mira@example.com'],cc:[]})
 assert.deepEqual(replyRecipients({from:'Alias <alias@icloud.com>',to:['peer@example.com','me@icloud.com'],cc:['copy@example.com']},own,true),{to:['peer@example.com'],cc:['copy@example.com']})
})
test('snoozed mail groups and sorts by return time with Someday last',()=>{
 const early=message({...raw,snoozeUntil:'2026-11-01T10:00:00Z'},'Snoozed'),late=message({...raw,id:'43',snoozeUntil:'2026-11-03T10:00:00Z'},'Snoozed'),someday=message({...raw,id:'44'},'Snoozed')
 assert.notEqual(early.day,late.day);assert.equal(someday.day,'Someday')
 assert.deepEqual(visible({msgs:[someday,late,early],folder:'snoozed',filter:'all',prefs:{}}),[early,late,someday])
})
test('phone navigation selects one current destination',()=>{
 const base={account:{email:'me@example.com',icloud_email:'me@icloud.com'},msgs:[],folder:'inbox',folders:[],cur:-1,picked:[],prefs:{},counts:{},bodies:{},view:'mail',side:true,total:0,mobilePane:'list'}
 for(const [state,action]of [[{},'go:inbox'],[{folder:'snoozed'},'go:snoozed'],[{search:'from:mira'},'search'],[{view:'settings',tab:'appearance'},'settings']]){
  const nav=mailView({...base,...state}).match(/<nav class="tabbar"[\s\S]*?<\/nav>/)[0]
  assert.equal((nav.match(/aria-current="page"/g)||[]).length,1)
  assert(nav.includes(`data-a="${action}" class="on" aria-current="page"`))
 }
})
test('mail rows keep folder and UID identity and actual attachment/flag data',()=>{
  const m=message(raw,'INBOX')
  assert.equal(m.id,'INBOX|7|42');assert.equal(m.em,'mira@example.com');assert.equal(m.att,1);assert.equal(m.fl,'green')
  assert.notEqual(message(raw,'Archive').id,m.id)
  assert.equal(role('Sent Messages'),'sent');assert.equal(targetFolder({folders:['Deleted Messages']},'trash'),'Deleted Messages')
})
test('operator search combines quoted terms, dates, attachment and sender filters',()=>{
  const m=message(raw,'INBOX')
  assert.equal(matches(m,'from:mira has:attachment after:2026-10-27 subject:launch is:unread'),true)
  assert.equal(matches(m,'to:me@icloud.com "Review notes"'),true)
  assert.equal(matches(m,'from:someone'),false);assert.equal(matches(m,'before:2026-10-20'),false)
  assert.equal(visible({msgs:[m],folder:'flagged',filter:'unread',prefs:{}}).length,1)
})
test('untrusted message and account content cannot escape the UI',()=>{
  const m=message({...raw,from:'<img src=x onerror=bad()> <evil@example.com>',date:raw.date},'INBOX')
  const s={account:{email:'me@example.com',icloud_email:'me@icloud.com'},folder:'inbox',folders:['INBOX'],msgs:[m],cur:0,picked:[],filter:'all',prefs:{},counts:{},bodies:{},view:'mail',tab:'account',side:true,focus:'list',mobilePane:'list',total:1,pending:0,theme:'light'}
  const html=mailView(s)
  assert.equal(html.includes('<script>bad()'),false);assert.equal(html.includes('<img src=x'),false)
  assert.match(html,/&lt;script&gt;/)
  assert.match(authView({mode:'login',authEmail:'" autofocus onfocus="bad()',authPassword:''}),/&quot;/)
})
test('email frame denies scripts, remote images, forms and base URLs',()=>{
  const html=emailDocument('<p>Hello</p>')
  assert.match(html,/default-src 'none'/);assert.match(html,/img-src data:/);assert.match(html,/form-action 'none'/)

  for(const original of [emailDocument('<p>Hello</p>',true,'sans',{},true)]){assert.match(original,/default-src 'none'/);assert.match(original,/img-src data:/);assert.doesNotMatch(original,/IBM Plex/)}
})
test('pending mutations rebase by mailbox identity and leave server rows untouched',()=>{
  const row=message(raw,'INBOX')
  const tx={kind:'mutation',status:'pending',body:{folder:'INBOX',id:'42',validity:7,op:'read',read:true}}
  assert.equal(rebase([row],[tx])[0].un,false)
  assert.equal(row.un,true)
  assert.equal(rebase([row],[{...tx,body:{...tx.body,validity:8}}])[0].un,true)
  assert.equal(rebase([row],[{...tx,body:{...tx.body,op:'move'}}]).length,0)
  assert.equal(rebase([row],[{...tx,status:'rejected'}]).length,1)
})
test('conversations group matching rows and include replies from other folders',()=>{
  const inbox={...raw,key:'inbox',folder:'INBOX',threadId:'thread'},sent={...raw,key:'sent',id:'99',folder:'Sent Messages',threadId:'thread',date:'2026-10-28T12:30:00Z',unread:false}
  const groups=conversations([inbox],[inbox,sent])
  assert.equal(groups.length,1);assert.equal(groups[0].n,2);assert.equal(groups[0].key,'inbox');assert.equal(groups[0].threadMessages.at(-1).folder,'Sent Messages');assert.equal(groups[0].actionMessages.length,1)
})
test('recipient suggestions preserve quoted display names containing commas',()=>{
  assert.deepEqual(recipients('"Okafor, Mira" <mira@example.com>; juno@example.com'),['"Okafor, Mira" <mira@example.com>','juno@example.com'])
})
test('snooze choices are future local dates, including late nights and weekends',()=>{
  for(const now of [new Date(2026,9,28,23,55),new Date(2026,9,31,10),new Date(2026,10,2,7)]){
    const options=snoozeOptions(now)
    for(const option of options.filter(option=>option.date))assert.ok(option.date>now,option.key)
    assert.equal(options.find(option=>option.key==='tomorrow').date.getDate(),new Date(now.getFullYear(),now.getMonth(),now.getDate()+1).getDate())
    assert.equal(options.find(option=>option.key==='weekend').date.getDay(),6)
    assert.equal(options.find(option=>option.key==='week').date.getDay(),1)
  }
})
test('offline move undo restores a pending row and blocks stale-location actions',()=>{
  const row=message(raw,'INBOX'),move={id:'move-1',kind:'mutation',status:'pending',body:{op:'move',folder:'INBOX',id:'42',validity:7,target:'Archive'}},undo={id:'undo-1',kind:'mutation',status:'pending',body:{op:'undoMove',folder:'INBOX',originalTransaction:'move-1'}}
  assert.equal(rebase([row],[move]).length,0)
  assert.equal(rebase([row],[move,undo])[0].pendingLocation,true)
  const arrived={...row,folder:'Archive',moveTransaction:'move-1'}
  assert.equal(rebase([arrived],[undo])[0].folder,'INBOX');assert.equal(rebase([arrived],[undo])[0].pendingLocation,true)
})

test('unsubscribe only exposes explicit HTTPS and mail links',()=>{
 assert.deepEqual(unsubscribeLinks('<https://list.example/unsubscribe?token=abc>, <mailto:leave@example.com>'),[{url:'https://list.example/unsubscribe?token=abc',label:'list.example'},{url:'mailto:leave@example.com',label:'leave@example.com'}])
 assert.deepEqual(unsubscribeLinks('<javascript:bad()>, <https://user:pass@example.com>, <mailto:leave@example.com%0aBcc:evil@example.com>'),[])
})

test('global search keeps body matches and ignores the previously selected smart folder',()=>{
 const archived=message({...raw,id:'43',key:'archived',snippet:'No matching snippet',subject:'A plain subject'},'Archive')
 assert.equal(matches(archived,'in:archive "a phrase in the body"','This has a phrase in the body.'),true)
 assert.equal(matches(archived,'in:inbox "a phrase in the body"','This has a phrase in the body.'),false)
 const state={msgs:[{...archived,searchQuery:'in:archive body'}],search:'in:archive body',folder:'flagged',filter:'all',prefs:{}}
 assert.equal(visible(state).length,1)
 assert.equal(matches(archived,'to:me@icloud.com'),true)
 assert.equal(matches(archived,'in:all'),true)
})
test('sender names decode RFC 2047 words left by older syncs',()=>{
  assert.equal(message({from:'=?utf-8?q?=F0=9F=94=B3_Turing_Post?= <news@example.com>',date:new Date().toISOString()},'INBOX').who,'🔳 Turing Post')
  assert.equal(message({from:'"Nico \\"N\\" M" <n@example.com>',date:new Date().toISOString()},'INBOX').who,'Nico "N" M')
  assert.equal(message({from:'=?bogus?q?x?= <n@example.com>',date:new Date().toISOString()},'INBOX').who,'=?bogus?q?x?=')
})
