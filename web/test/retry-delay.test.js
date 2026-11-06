import test from 'node:test'
import assert from 'node:assert/strict'
import {syncRetryDelay} from '../src/replica/retry-delay.js'

test('sync retries always back off, add jitter, and cap at 30 seconds',()=>{
  assert.equal(syncRetryDelay(500,()=>0),500)
  assert.equal(syncRetryDelay(500,()=>1),650)
  assert.equal(syncRetryDelay(30000,()=>1),30000)
})
