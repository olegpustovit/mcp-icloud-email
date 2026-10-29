let loading
export function readerFonts(){
  return loading||=Promise.all([['sans','font-6.ttf'],['mono','font-2.ttf']].map(async([name,file])=>{
    const response=await fetch('/fonts/'+file)
    if(!response.ok)throw new Error('Reading font could not be loaded')
    const bytes=new Uint8Array(await response.arrayBuffer())
    let binary='';for(const byte of bytes)binary+=String.fromCharCode(byte)
    return [name,'data:font/ttf;base64,'+btoa(binary)]
  })).then(Object.fromEntries).catch(err=>{loading=null;throw err})
}
