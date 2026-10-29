import test from 'node:test'
import assert from 'node:assert/strict'
import {keyBindings,keyStroke,keyParts,bindingConflict} from '../src/keyboard.js'

test('keyboard presets and overrides share canonical keys',()=>{
 assert.equal(keyBindings({keyPreset:'gmail'}).snooze,'h')
 assert.equal(keyBindings({keyPreset:'gmail'})['thread-next'],'n')
 assert.equal(keyBindings({keyPreset:'gmail'})['thread-prev'],'p')
 assert.equal(keyBindings({keyPreset:'apple'}).reply,'Mod+r')
 assert.equal(keyBindings({keyPreset:'custom',keymap:{snooze:'t'}}).snooze,'t')
 assert.equal(keyStroke({key:'S',shiftKey:true}),'Shift+s')
 assert.equal(keyStroke({key:'I'}),'Shift+i')
 assert.equal(keyStroke({key:'U'}),'Shift+u')
 assert.equal(keyStroke({key:'#',shiftKey:true}),'#')
 assert.equal(keyStroke({key:'Enter',ctrlKey:true}),'Mod+Enter')
 assert.equal(keyStroke({key:'f',metaKey:true,shiftKey:true}),'Mod+Shift+f')
 assert.equal(keyStroke({key:'Dead'}),'')
 assert.deepEqual(keyParts('Mod+Shift+s'),['⌘','⇧','s'])
 assert.deepEqual(keyParts('g i'),['g','i'])
})
test('rebinding rejects collisions and browser controls',()=>{
 const bindings=keyBindings({keyPreset:'gmail'})
 assert.match(bindingConflict('archive','v',bindings),/Move already/)
 assert.match(bindingConflict('archive','g',bindings),/chord/)
 assert.match(bindingConflict('archive','Mod+w',bindings),/browser/)
 assert.equal(bindingConflict('snooze','t',bindings),'')
})
