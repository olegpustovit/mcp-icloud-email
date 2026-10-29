export function editableRule(rule={}){
 return {id:rule.id||crypto.randomUUID(),enabled:rule.enabled!==false,conditions:(rule.conditions?.length?rule.conditions:[{field:rule.field||'from',match:rule.match||'contains',value:rule.value||''}]).map(condition=>({...condition})),actions:(rule.actions?.length?rule.actions:[{action:rule.action||'move',target:rule.target||''}]).map(action=>({...action}))}
}
