import test from 'node:test'
import assert from 'node:assert/strict'
import {quietNow,wantsNotification} from '../src/notifications.js'
test('quiet hours cross midnight and direct notifications exclude newsletters',()=>{
 const prefs={notifications:'vip',quietHours:true,quietStart:'22:00',quietEnd:'07:30',vip:['vip@example.com'],identities:[{address:'alias@icloud.com'}]},at=(h,m)=>new Date(2026,9,28,h,m)
 assert.equal(quietNow(prefs,at(22,0)),true);assert.equal(quietNow(prefs,at(7,29)),true);assert.equal(quietNow(prefs,at(7,30)),false)
 const mail={from:'Sender <sender@example.com>',to:['Me <alias@icloud.com>'],unread:true}
 assert.equal(wantsNotification(prefs,'me@icloud.com',mail,at(9,0)),true)
 assert.equal(wantsNotification(prefs,'me@icloud.com',{...mail,listId:'newsletter'},at(9,0)),false)
 assert.equal(wantsNotification(prefs,'me@icloud.com',{...mail,from:'VIP <vip@example.com>',listId:'newsletter'},at(9,0)),true)
 assert.equal(wantsNotification(prefs,'me@icloud.com',mail,at(23,0)),false)
 assert.equal(wantsNotification(prefs,'me@icloud.com',{...mail,unread:false},at(9,0)),false)
})
