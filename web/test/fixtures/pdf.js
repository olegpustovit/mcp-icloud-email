export function pdfFixture(){
  const objects=[
    '<< /Type /Catalog /Pages 2 0 R >>',
    '<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>',
    '<< /Type /Page /Parent 2 0 R /MediaBox [0 0 500 600] /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>',
    '<< /Type /Page /Parent 2 0 R /MediaBox [0 0 500 600] /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R >>',
    '<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',
    ...['PDF preview fixture','Second fixture page'].map(text=>{const data=`BT /F1 24 Tf 50 500 Td (${text}) Tj ET`;return `<< /Length ${data.length} >>\nstream\n${data}\nendstream`})
  ]
  let file='%PDF-1.4\n';const offsets=[0]
  objects.forEach((body,i)=>{offsets.push(file.length);file+=`${i+1} 0 obj\n${body}\nendobj\n`})
  const xref=file.length
  file+=`xref\n0 ${offsets.length}\n0000000000 65535 f \n`+offsets.slice(1).map(offset=>String(offset).padStart(10,'0')+' 00000 n \n').join('')
  return file+`trailer\n<< /Root 1 0 R /Size ${offsets.length} >>\nstartxref\n${xref}\n%%EOF\n`
}
