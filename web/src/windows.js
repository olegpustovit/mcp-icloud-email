// Window manager state for the mail panes. modes: tiled | collapsed | docked | max | floating
export const WINS=['side','list','reader']
export const TITLES={side:'MAILBOXES.SYS',list:'INBOX // LIST',reader:'MESSAGE_VIEWER'}
const WIDTH={side:'200px',list:'var(--wl,400px)'}
const MODES=['tiled','collapsed','docked','max','floating']
export const freshWins=()=>({side:'tiled',list:'tiled',reader:'tiled'})
export const unmax=wins=>Object.fromEntries(WINS.map(w=>[w,wins[w]==='max'?'tiled':wins[w]]))
export function winOp(wins,w,op){
  if(op==='max')return {...unmax(wins),[w]:wins[w]==='max'?'tiled':'max'}
  if(op==='tiled')return {...unmax(wins),[w]:'tiled'}
  return {...wins,[w]:op==='collapse'?(wins[w]==='collapsed'?'tiled':'collapsed'):op}
}
// effective mode per window (max folds the tiled ones), grid order and grid-template-columns; fill is the pane that takes the free space
export function layout(wins){
  const mx=WINS.some(w=>wins[w]==='max'),mode=Object.fromEntries(WINS.map(w=>[w,mx&&wins[w]==='tiled'?'collapsed':wins[w]]))
  const grid=WINS.filter(w=>mode[w]!=='docked'&&mode[w]!=='floating'),fill=[...grid].reverse().find(w=>mode[w]==='tiled'||mode[w]==='max')
  const cols=grid.map(w=>mode[w]==='collapsed'?'24px':w===fill?'minmax(0,1fr)':WIDTH[w]).concat(fill?[]:['minmax(0,1fr)']).join(' ')
  return {mode,grid,cols,desk:!fill}
}
// Snap like a desktop OS: the pointer reaches the left/right workspace edge, or the window's left edge lands within 2d of its tiling slot.
export function shouldSnap(p,g,box,slotX,d=24){return p.x<d||p.x>box.w-d||Math.abs(g.x-slotX)<d*2}
// Windows may hang off-screen but keep `keep` px and the title bar reachable; 240×160 is the smallest usable pane.
export function clampGeo(g,box,keep=80){const w=Math.min(Math.max(240,g.w),box.w),h=Math.min(Math.max(160,g.h),box.h);return {w,h,x:Math.max(keep-w,Math.min(box.w-keep,g.x)),y:Math.max(0,Math.min(box.h-30,g.y))}}
export function loadWins(store=localStorage){
  try{const v=JSON.parse(store.getItem('mail.wins'));const wins=freshWins();for(const w of WINS)if(MODES.includes(v?.wins?.[w]))wins[w]=v.wins[w];return {wins,geo:v?.geo&&typeof v.geo==='object'?v.geo:{}}}catch{return {wins:freshWins(),geo:{}}}
}
export function saveWins(s,store=localStorage){try{store.setItem('mail.wins',JSON.stringify({wins:s.wins,geo:s.geo}))}catch{}}
