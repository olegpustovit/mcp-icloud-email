import test from 'node:test'
import assert from 'node:assert/strict'
import {listLayout,listWindow,revealRow} from '../src/list-window.js'

test('large lists preserve day spacing, density, scroll extent and keyboard reach',()=>{
  const rows=Array.from({length:50000},(_,i)=>({day:i<10?'Today':'Yesterday'}))
  for(const density of ['compact','default','comfy']){
    const state={prefs:{density},listScroll:0,listHeight:800,cur:49999}
    const layout=listLayout(rows,density),first=listWindow(rows,state)
    assert.equal(layout.items.filter(item=>item.day).length,2)
    assert(first.items.length<30)
    assert.equal(first.before+first.items.reduce((sum,item)=>sum+item.height,0)+first.after,layout.total)
    revealRow(rows,state)
    const last=listWindow(rows,state)
    assert(last.items.some(item=>item.index===49999))
    assert.equal(last.after,0)
    state.cur=0;revealRow(rows,state);assert.equal(state.listScroll,0)
  }
  const phone={prefs:{},rowHeight:64,listHeight:700,listScroll:0,cur:49999}
  revealRow(rows,phone);assert(listWindow(rows,phone).items.some(item=>item.index===49999))
})
