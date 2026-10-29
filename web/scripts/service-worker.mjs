import { readFile, readdir, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import assert from 'node:assert/strict'

const files=[]
async function walk(dir){for(const entry of await readdir('dist/'+dir,{withFileTypes:true})){const path=dir+entry.name;if(entry.isDirectory())await walk(path+'/');else if(path!=='sw.js')files.push('/'+path)}}
await walk('')
const version=createHash('sha256')
for(const path of files.sort())version.update(await readFile('dist'+path))
const original=await readFile('dist/sw.js','utf8')
assert(original.includes('__PRECACHE_MANIFEST__')&&original.includes('__BUILD_VERSION__'),'service worker placeholders missing')
const code=original.replace('__PRECACHE_MANIFEST__',JSON.stringify(files).replace(/'/g,"\\'")).replace('__BUILD_VERSION__',version.digest('hex').slice(0,12))
assert(files.includes('/index.html')&&files.includes('/manifest.webmanifest')&&files.some(path=>path.endsWith('.js')),'incomplete offline shell')
await writeFile('dist/sw.js',code)
console.log(`Service worker: precached ${files.length} app files`)
