export const bodyKey=message=>`${message.folder}|${message.uidValidity}|${message.id}`

export function prefetchMessages(rows){
  const messages=new Map()
  for(const row of rows){
    const message=row.threadMessages?.at(-1)||row
    if(row.pendingLocation||message.pendingLocation||message.size>8*1024*1024)continue
    const key=bodyKey(message)
    if(!messages.has(key))messages.set(key,message)
    if(messages.size===30)break
  }
  return [...messages.values()]
}
