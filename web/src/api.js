let owner = ''
let account = ''
export function setOwner(value, address = '') { owner = value; account = address }

export async function request(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: { ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...(owner && path.startsWith('/api/mail/') ? { 'X-Mail-Owner': owner, 'X-Mail-Account': account } : {}), ...options.headers },
  })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const error = new Error(body.error || body.message || `Request failed (${response.status})`)
    error.status = response.status
    throw error
  }
  return response.status === 204 ? null : response.json()
}

export async function attachmentBlob(path) {
  const response=await fetch(path,{credentials:'same-origin',headers:{'X-Mail-Owner':owner,'X-Mail-Account':account}})
  if(!response.ok)throw new Error('Attachment download failed. Refresh the message and try again.')
  return response.blob()
}
export async function downloadAttachment(path, filename) {
  const url=URL.createObjectURL(await attachmentBlob(path))
  const link=document.createElement('a');link.href=url;link.download=filename;link.click()
  setTimeout(()=>URL.revokeObjectURL(url),30000)
}

export async function proxyImage(url){
 const response=await fetch('/api/mail/remote-image',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Mail-Owner':owner,'X-Mail-Account':account},body:JSON.stringify({url})})
 if(!response.ok)throw new Error('Remote image could not be loaded.')
 return response.blob()
}
