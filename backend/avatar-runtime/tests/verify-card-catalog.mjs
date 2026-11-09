// Run against the deployed runtime to inspect every real gallery encode.
import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
import assert from 'node:assert/strict';
const root=process.argv[2]||'/app/avatar-runtime';
const styles=JSON.parse(fs.readFileSync(path.join(root,'native-styles.json'),'utf8'));
const entries=styles.map(style=>{
 const base=path.join(root,'catalog',style.id),file=base+'-card.mp4';
 const info=JSON.parse(execFileSync('ffprobe',['-v','error','-show_streams','-show_format','-of','json',file]));
 const video=info.streams[0];assert.equal(info.streams.length,1);assert.equal(video.codec_name,'h264');
 assert.ok(video.width<=480&&video.height<=480);assert.equal(video.avg_frame_rate,'24/1');
 const content=fs.readFileSync(file);assert.ok(content.indexOf('moov')<content.indexOf('mdat'));
 const originalBytes=fs.statSync(base+'.mp4').size,bytes=content.length;
 assert.ok(bytes<originalBytes,'Compressed file is larger than original: '+style.id);
 assert.ok(fs.statSync(base+'-card.jpg').size<fs.statSync(base+'.jpg').size);
 return {id:style.id,width:video.width,height:video.height,fps:24,duration:Number(info.format.duration),originalBytes,bytes};
});
const originalBytes=entries.reduce((sum,x)=>sum+x.originalBytes,0),bytes=entries.reduce((sum,x)=>sum+x.bytes,0);
console.log(JSON.stringify({styles:entries.length,originalBytes,bytes,reductionPercent:Number(((1-bytes/originalBytes)*100).toFixed(2)),entries},null,2));
