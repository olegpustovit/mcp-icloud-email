import {getDocument,GlobalWorkerOptions,AnnotationMode} from 'pdfjs-dist/build/pdf.mjs'
import workerURL from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

GlobalWorkerOptions.workerSrc=workerURL
export async function openPDF(blob){
  const task=getDocument({data:new Uint8Array(await blob.arrayBuffer()),isEvalSupported:false,useWasm:false,enableXfa:false,maxImageSize:40_000_000,disableFontFace:true})
  try{
    const pdf=await task.promise
    return {
      pages:pdf.numPages,
      destroy:()=>task.destroy(),
      async render(number){
        const page=await pdf.getPage(number),size=page.getViewport({scale:1})
        const viewport=page.getViewport({scale:Math.min(1.5,1600/size.width,2000/size.height)}),canvas=document.createElement('canvas')
        canvas.width=Math.max(1,Math.ceil(viewport.width));canvas.height=Math.max(1,Math.ceil(viewport.height))
        await page.render({canvas,viewport,annotationMode:AnnotationMode.DISABLE}).promise
        const text=await page.getTextContent()
        return {url:canvas.toDataURL('image/png'),text:text.items.map(item=>item.str||'').join(' ')}
      }
    }
  }catch(err){await task.destroy();throw err}
}
