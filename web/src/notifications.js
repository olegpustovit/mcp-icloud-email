import {address} from './model.js'
export function quietNow(prefs,date=new Date()){
 if(!prefs.quietHours)return false
 const clock=String(date.getHours()).padStart(2,'0')+':'+String(date.getMinutes()).padStart(2,'0'),start=prefs.quietStart||'22:00',end=prefs.quietEnd||'07:30'
 return start<end?clock>=start&&clock<end:clock>=start||clock<end
}
export function wantsNotification(prefs,account,mail,date=new Date()){
 if(!mail.unread||!prefs.notifications||prefs.notifications==='none'||quietNow(prefs,date))return false
 if(prefs.notifications==='all'||prefs.vip?.some(sender=>sender.toLowerCase()===address(mail.from).toLowerCase()))return true
 return !mail.listId&&mail.to?.some(recipient=>[account,...(prefs.identities||[]).map(identity=>identity.address)].some(identity=>identity.toLowerCase()===address(recipient).toLowerCase()))
}
