import test from 'node:test'
import assert from 'node:assert/strict'
import {freshWins,winOp,unmax,layout,shouldSnap,clampGeo,loadWins,saveWins} from '../src/windows.js'

test('collapse, dock, maximize and restore transitions',()=>{
  let w=freshWins()
  assert.equal(layout(w).cols,'200px var(--wl,400px) minmax(0,1fr)')
  w=winOp(w,'side','collapse');assert.equal(layout(w).cols,'24px var(--wl,400px) minmax(0,1fr)')
  w=winOp(w,'side','tiled');w=winOp(w,'list','docked')
  assert.deepEqual(layout(w).grid,['side','reader']);assert.equal(layout(w).cols,'200px minmax(0,1fr)')
  w=winOp(w,'reader','max');assert.equal(layout(w).mode.side,'collapsed');assert.equal(layout(w).cols,'24px minmax(0,1fr)')
  assert.equal(winOp(w,'reader','max').reader,'tiled')
  assert.equal(winOp(w,'side','tiled').reader,'tiled')
  assert.equal(unmax(w).reader,'tiled');assert.equal(unmax(w).list,'docked')
})
test('desktop shows when nothing tiled and floating panes leave the grid',()=>{
  let w=freshWins();for(const k of ['side','list','reader'])w=winOp(w,k,'collapse')
  assert.equal(layout(w).desk,true);assert.equal(layout(w).cols,'24px 24px 24px minmax(0,1fr)')
  w=winOp(winOp(freshWins(),'reader','floating'),'list','tiled');assert.deepEqual(layout(w).grid,['side','list'])
  assert.equal(layout(w).desk,false)
})
test('snap near edges or slot, clamp, and persistence tolerate bad data',()=>{
  const box={w:1000,h:600}
  const g={x:300,y:200,w:300,h:200}
  assert.equal(shouldSnap({x:400,y:250},g,box,208),false)
  assert.equal(shouldSnap({x:10,y:250},g,box,208),true)
  assert.equal(shouldSnap({x:990,y:250},g,box,208),true)
  assert.equal(shouldSnap({x:400,y:250},{...g,x:230},box,208),true)
  assert.equal(shouldSnap({x:400,y:4},g,box,208),false)
  assert.deepEqual(clampGeo({x:-500,y:900,w:1200,h:100},box),{w:1000,h:160,x:-500,y:570})
  assert.deepEqual(clampGeo({x:-900,y:-20,w:300,h:200},box),{w:300,h:200,x:-220,y:0})
  assert.deepEqual(clampGeo({x:990,y:10,w:300,h:200},box),{w:300,h:200,x:920,y:10})
  const mem={v:null,getItem(){return this.v},setItem(k,v){this.v=v}}
  saveWins({wins:{...freshWins(),list:'docked'},geo:{reader:{x:1,y:2,w:3,h:4}}},mem)
  assert.equal(loadWins(mem).wins.list,'docked');assert.equal(loadWins(mem).geo.reader.w,3)
  mem.v='{bad';assert.deepEqual(loadWins(mem).wins,freshWins())
  mem.v='{"wins":{"side":"bogus"}}';assert.equal(loadWins(mem).wins.side,'tiled')
  assert.deepEqual(loadWins({getItem(){throw 1}}).wins,freshWins())
})
