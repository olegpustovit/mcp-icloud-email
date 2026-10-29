export const KEY_GROUPS=[
 ['Navigate',[['Next','next'],['Previous','prev'],['Open','open'],['Back to list','back'],['Next message','thread-next'],['Previous message','thread-prev'],['Expand thread','expand-thread'],['Go to inbox','go:inbox'],['Go to drafts','go:drafts'],['Go to sent','go:sent'],['Go to flagged','go:flagged']]],
 ['Triage',[['Archive','archive'],['Trash','trash'],['Move','move'],['Snooze','snooze'],['Flag','flag'],['Flag color','flagc'],['Junk','junk'],['Mark read','read'],['Mark unread','unread'],['Undo','undo']]],
 ['Select',[['Toggle','select'],['Extend next','extend-next'],['Extend previous','extend-prev'],['All','select-all'],['None','select-none'],['Unread','select-unread']]],
 ['Write',[['Compose','compose'],['Reply','reply'],['Reply all','reply-all'],['Forward','forward'],['Send','send'],['Fullscreen compose','full-compose'],['Minimize','min-compose']]],
 ['System',[['Command palette','palette'],['Search','search'],['Settings','settings'],['Keymap','help']]],
]
export const EDITABLE_KEYS=['archive','trash','move','snooze','flag','palette','undo']
const gmail={'thread-next':'n','thread-prev':'p',next:'j',prev:'k',open:'Enter',back:'u','expand-thread':';','go:inbox':'g i','go:drafts':'g d','go:sent':'g s','go:flagged':'g f','go:archive':'g a','go:trash':'g t','go:junk':'g j','go:vip':'g v','go:snoozed':'g z',archive:'e',trash:'#',move:'v',snooze:'h',flag:'s',flagc:'Shift+s',junk:'!',read:'Shift+i',unread:'Shift+u',undo:'z',select:'x','extend-next':'Shift+j','extend-prev':'Shift+k','select-all':'* a','select-none':'* n','select-unread':'* u',compose:'c',reply:'r','reply-all':'a',forward:'f',send:'Mod+Enter','full-compose':'Mod+Shift+f','min-compose':'Escape',palette:'Mod+k',search:'/',settings:'Mod+,',help:'?'}
const apple={...gmail,next:'ArrowDown',prev:'ArrowUp',back:'Alt+ArrowLeft',archive:'Mod+e',trash:'Backspace',reply:'Mod+r','reply-all':'Mod+Shift+r',forward:'Mod+Shift+f',unread:'Mod+Shift+u','extend-next':'Shift+ArrowDown','extend-prev':'Shift+ArrowUp'}
export const keyBindings=prefs=>({...((prefs?.keyPreset==='apple')?apple:gmail),...(prefs?.keyPreset==='custom'?prefs.keymap||{}:{})})
export function keyStroke(event){
 if(event.isComposing||['Control','Meta','Alt','Shift','AltGraph','CapsLock','Dead','Unidentified'].includes(event.key))return ''
 const key=event.key===' '?'Space':event.key==='+'?'Plus':/^[A-Z]$/i.test(event.key)?event.key.toLowerCase():event.key
 return (event.ctrlKey||event.metaKey?'Mod+':'')+(event.altKey?'Alt+':'')+((event.shiftKey&&key.length>1||/^[A-Z]$/.test(event.key)||event.shiftKey&&/^[a-z]$/.test(key))?'Shift+':'')+key
}
export function keyParts(binding=''){
 const labels={Mod:'⌘',Shift:'⇧',Alt:'⌥',Enter:'↵',Escape:'esc',ArrowDown:'↓',ArrowUp:'↑',ArrowLeft:'←',ArrowRight:'→',Backspace:'⌫',Space:'space',Plus:'+'}
 return binding.split(/[+ ]/).filter(Boolean).map(key=>labels[key]||key)
}
export function bindingConflict(action,key,bindings){
 if(['Escape','Tab','Shift+Tab','Mod+w','Mod+Shift+w','Mod+t','Mod+n','Mod+Shift+n','Mod+q','Mod+l','Alt+F4'].includes(key))return 'This shortcut belongs to the browser. Choose another.'
 const conflict=Object.entries(bindings).find(([other,value])=>other!==action&&value===key)
 if(conflict)return `${KEY_GROUPS.flatMap(([,rows])=>rows).find(([,id])=>id===conflict[0])?.[0]||conflict[0]} already uses this shortcut.`
 if(key==='ArrowDown'||key==='ArrowUp')return 'Arrow keys are reserved for navigation.'
 if(Object.values(bindings).some(value=>value.startsWith(key+' ')))return 'This key starts a navigation chord. Choose another.'
 return ''
}
