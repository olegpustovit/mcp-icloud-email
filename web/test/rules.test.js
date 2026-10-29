import test from 'node:test'
import assert from 'node:assert/strict'
import {editableRule} from '../src/rules.js'
test('legacy rules edit in the multiple-condition format without changing saved preferences',()=>{
 const legacy={id:'rule-1',field:'from',match:'ends',value:'@example.com',action:'move',target:'Clients',enabled:true},copy=editableRule(legacy)
 assert.deepEqual(copy.conditions,[{field:'from',match:'ends',value:'@example.com'}]);assert.deepEqual(copy.actions,[{action:'move',target:'Clients'}]);copy.actions[0].target='Archive';assert.equal(legacy.target,'Clients')
 const modern={id:'rule-2',enabled:false,conditions:[{field:'subject',match:'contains',value:'invoice'}],actions:[{action:'flag',target:'green'},{action:'read'}]},edited=editableRule(modern);edited.conditions[0].value='paid';assert.equal(modern.conditions[0].value,'invoice');assert.equal(edited.enabled,false)
})
