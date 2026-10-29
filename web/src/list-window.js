export function listLayout(rows, density='default',rowHeight) {
  const height=rowHeight||({compact:56,default:60,comfy:74})[density]||60
  let top=0,last=''
  const items=rows.map((row,index)=>{
    const day=row.day!==last;last=row.day
    const item={index,day,top,height:height+(day?27.5:0)}
    top+=item.height
    return item
  })
  return {items,total:top}
}
export function listWindow(rows, state) {
  const layout=listLayout(rows,state.prefs?.density,state.rowHeight)
  const top=state.listScroll||0,height=state.listHeight||900,overscan=300
  const items=layout.items.filter(item=>item.top+item.height>=top-overscan&&item.top<=top+height+overscan)
  return {items,before:items[0]?.top||0,after:Math.max(0,layout.total-(items.at(-1)?.top||0)-(items.at(-1)?.height||0))}
}
export function revealRow(rows,state) {
  const item=listLayout(rows,state.prefs?.density,state.rowHeight).items[state.cur]
  if(!item)return
  const top=state.listScroll||0,height=state.listHeight||600
  if(item.top<top)state.listScroll=item.top
  else if(item.top+item.height>top+height)state.listScroll=item.top+item.height-height
}
