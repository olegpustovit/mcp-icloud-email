import assert from 'node:assert/strict'
import test from 'node:test'
import {sizeEmailFrame} from '../src/email-frame.js'

test('email sizing settles without repeated height writes and includes body margins',()=>{
  const previousObserver=globalThis.ResizeObserver,previousStyle=globalThis.getComputedStyle
  let resize,disconnected=false,height='',writes=0,bodyHeight=1200.25
  globalThis.ResizeObserver=class{
    constructor(callback){resize=callback}
    observe(){}
    disconnect(){disconnected=true}
  }
  globalThis.getComputedStyle=()=>({marginTop:'8px',marginBottom:'8px'})
  const frame={isConnected:true,clientHeight:480,contentDocument:{documentElement:{clientHeight:480},body:{get scrollHeight(){return Math.round(bodyHeight)},getBoundingClientRect:()=>({height:bodyHeight})}},contentWindow:{addEventListener(){}},style:{get height(){return height},set height(value){height=value;writes++}}}
  try{
    sizeEmailFrame(frame)
    assert.equal(height,'1217px')
    resize();resize()
    assert.equal(writes,1,'unchanged layout must not trigger more style mutations')
    bodyHeight=500.5;resize()
    assert.equal(height,'517px')
    assert.equal(writes,2)
    frame.contentDocument.documentElement.clientHeight=465;resize()
    assert.equal(height,'532px','wide original mail needs room for its horizontal scrollbar')
    assert.equal(writes,3)
    frame.isConnected=false;resize()
    assert.equal(disconnected,true)
    assert.equal(writes,3)
  }finally{globalThis.ResizeObserver=previousObserver;globalThis.getComputedStyle=previousStyle}
})
