import {test} from 'node:test'
import assert from 'node:assert/strict'
import {evictedBodies} from '../src/replica/body-cache.js'
test('body cache evicts least recently used bytes, including an oversized body',()=>{
  const rows=[{key:'new',at:20,bytes:60},{key:'old',at:1,bytes:50},{key:'middle',at:10,bytes:30}]
  assert.deepEqual(evictedBodies(rows,100),['old'])
  assert.deepEqual(evictedBodies([{key:'large',at:20,bytes:101}],100),['large'])
  assert.equal(rows[0].key,'new')
})
