import {test} from 'node:test'
import assert from 'node:assert/strict'
import {bodyKey,prefetchMessages} from '../src/replica/body-prefetch.js'

test('prefetch warms the opened end of a thread, deduplicates and bounds work',()=>{
  const old={folder:'INBOX',uidValidity:7,id:'1'},latest={folder:'Archive',uidValidity:8,id:'2'}
  const thread={...old,threadMessages:[old,latest]}
  const rows=prefetchMessages([thread,latest,{...old,pendingLocation:true},{...old,size:9*1024*1024},old])
  assert.deepEqual(rows,[latest,old])
  assert.equal(bodyKey(rows[0]),'Archive|8|2')
  assert.equal(prefetchMessages(Array.from({length:100},(_,id)=>({...old,id}))).length,30)
})
