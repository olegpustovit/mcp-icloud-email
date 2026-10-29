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
