import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
const root=process.cwd(),out=process.argv[2];
fs.mkdirSync(out,{recursive:true});
const styles=JSON.parse(fs.readFileSync('explainer-styles.json','utf8'));
for(const style of styles){
 const source=path.join(root,'catalog',style.id+'-reference.mp4');
 if(!fs.existsSync(source))continue;
 const meta=JSON.parse(execFileSync('ffprobe',['-v','error','-show_format','-of','json',source]));
 const duration=Number(meta.format.duration);
 execFileSync('ffmpeg',['-v','error','-y','-i',source,'-vf',`fps=8/${duration},scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2,tile=4x2`,'-frames:v','1','-threads','1',path.join(out,style.id+'.jpg')]);
 console.log(JSON.stringify({id:style.id,duration,sampleTimes:Array.from({length:8},(_,i)=>Math.round((i+.5)*duration/8))}));
}
