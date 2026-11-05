import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
import {renderNative} from '../mg-native.mjs';
const output=process.argv[2]||'/tmp/mg-encoded';fs.mkdirSync(output,{recursive:true});const reports=[];
for(const [style,width,height] of [['mg-flat-vector',360,640],['mg-hanazi',480,270],['mg-3d-render',360,360],['mg-liquid',480,270]]){
 const began=Date.now();const dir=path.join(output,style);fs.mkdirSync(dir,{recursive:true});const target=path.join(dir,'material.mp4');
 const animation={style,width,height,fps:25,duration:1.6,title:'原版模板验证',text:'直接使用原模板。',words:[],render_mode:'semantic'};
 if(style==='mg-flat-vector'){
  fs.writeFileSync(path.join(dir,'animation-props.json'),JSON.stringify({animation}));
  execFileSync(process.execPath,[new URL('../workflow-render.mjs',import.meta.url).pathname,'animation',dir,target],{stdio:'inherit'});
 }else await renderNative(animation,target);
 const info=JSON.parse(execFileSync('ffprobe',['-v','error','-show_streams','-of','json',target]));const video=info.streams.find(s=>s.codec_type==='video');
 if(video.width!==width||video.height!==height||Number(video.nb_frames)!==40||info.streams.some(s=>s.codec_type==='audio'))throw new Error('Invalid encoded material: '+style);
 if(style==='mg-flat-vector'&&JSON.parse(fs.readFileSync(path.join(dir,'animation-native-report.json'))).mode!=='native-source')throw new Error('MG material fell back to semantic rendering');
 execFileSync('ffmpeg',['-v','error','-i',target,'-f','null','-']);
 reports.push({style,width,height,frames:40,seconds:1.6,muted:true,decode:'passed',elapsed_ms:Date.now()-began});console.log(JSON.stringify(reports.at(-1)));
}
fs.writeFileSync(path.join(output,'encoded-report.json'),JSON.stringify(reports,null,2));
