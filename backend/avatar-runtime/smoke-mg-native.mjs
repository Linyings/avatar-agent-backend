import fs from 'node:fs';
import path from 'node:path';
import {nativeStyles,renderNative} from './mg-native.mjs';
const out=path.resolve(process.argv[2]||'/tmp/mg-smoke');fs.mkdirSync(out,{recursive:true});
for(const style of nativeStyles){
 const started=Date.now();
 const a={style:style.id,width:360,height:640,fps:25,duration:10,title:'让创意成为现实',text:'先确定表达的主题。让图形跟随声音变化。用动画讲清楚核心内容。',brief:'主题到声音再到画面的连续制作过程',words:[]};
 const r=await renderNative(a,path.join(out,style.id),{frameSamples:[.8,3.2,5.5,8.5,9.8]});
 console.log(JSON.stringify({style:style.id,seconds:(Date.now()-started)/1000,frames:5,source:r.source}));
}
