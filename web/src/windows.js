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
// A released window snaps back when its left/right edge is within `d` of the workspace edge, or its left edge is within 2d of its tiling slot.
// Top/bottom edges only count for windows shorter than the workspace (a full-height pane always touches both).
export function shouldSnap(g,box,slotX,d=24){return g.x<d||g.x+g.w>box.w-d||Math.abs(g.x-slotX)<d*2||(g.h<box.h-d*2&&(g.y<d||g.y+g.h>box.h-d))}
export function clampGeo(g,box){const w=Math.min(g.w,box.w),h=Math.min(g.h,box.h);return {w,h,x:Math.max(0,Math.min(box.w-w,g.x)),y:Math.max(0,Math.min(box.h-h,g.y))}}
export function loadWins(store=localStorage){
  try{const v=JSON.parse(store.getItem('mail.wins'));const wins=freshWins();for(const w of WINS)if(MODES.includes(v?.wins?.[w]))wins[w]=v.wins[w];return {wins,geo:v?.geo&&typeof v.geo==='object'?v.geo:{}}}catch{return {wins:freshWins(),geo:{}}}
}
export function saveWins(s,store=localStorage){try{store.setItem('mail.wins',JSON.stringify({wins:s.wins,geo:s.geo}))}catch{}}
