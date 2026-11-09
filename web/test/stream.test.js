import test from 'node:test'
import assert from 'node:assert/strict'
import {readEvents} from '../src/replica/stream.js'

test('SSE parser resumes IDs and handles CRLF, comments, Unicode split across chunks and multiline data',async()=>{
  const text=': heartbeat\r\nid: 7\r\nevent: change\r\ndata: {"subject":"Привіт"}\r\n\r\nevent: reset\ndata: first\ndata: second\n\n'
  const bytes=new TextEncoder().encode(text)
  const stream=new ReadableStream({start(controller){for(let i=0;i<bytes.length;i+=3)controller.enqueue(bytes.slice(i,i+3));controller.close()}})
  const events=[];await readEvents(stream,event=>events.push(event))
  assert.deepEqual(events,[{event:'change',id:'7',data:'{"subject":"Привіт"}'},{event:'reset',id:'7',data:'first\nsecond'}])
})

test('a silent stream times out and cancels its reader so sync can reconnect',async()=>{
  let cancelled=false
  const stream=new ReadableStream({start(controller){controller.enqueue(new TextEncoder().encode(': connected\n\n'))},cancel(){cancelled=true}})
  await assert.rejects(readEvents(stream,()=>{},20),/heartbeat timed out/)
  assert.equal(cancelled,true)
  assert.equal(stream.locked,false)
})

test('heartbeat comments keep an otherwise idle stream alive',async()=>{
  const stream=new ReadableStream({async start(controller){for(let i=0;i<5;i++){controller.enqueue(new TextEncoder().encode(': heartbeat\n\n'));await new Promise(resolve=>setTimeout(resolve,10))}controller.close()}})
  await readEvents(stream,()=>assert.fail('comments must not become events'),40)
})
