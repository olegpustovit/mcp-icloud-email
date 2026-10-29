import {open,read,all} from './replica/storage.js'

export async function startupWindow(account,folder){
  const db=await open('terminal-mail:'+account.email+':'+account.icloud_email)
  try{
    const [snapshot,folders,prefs,sync,transactions]=await Promise.all([
      read(db,'snapshots',folder),all(db,'folders'),read(db,'settings','preferences'),read(db,'settings','sync'),all(db,'transactions')
    ])
    return snapshot?.msgs?.length?{snapshot,folders,prefs,sync,transactions}:null
  }finally{db.close()}
}
