import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {buildCardPreview} from '../catalog-previews.mjs';

const digest=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
test('card demos are small silent H264 files, fast-start, cached, and preserve their original media',()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'card-preview-'));
 try{
  const source=path.join(dir,'source.mp4'),poster=path.join(dir,'source.jpg'),output=path.join(dir,'sample-card.mp4'),thumbnail=path.join(dir,'sample-card.jpg');
  execFileSync('ffmpeg',['-v','error','-y','-f','lavfi','-i','testsrc2=size=1920x1080:rate=30','-f','lavfi','-i','sine=frequency=440','-t','2','-c:v','libx264','-preset','ultrafast','-crf','18','-c:a','aac',source]);
  execFileSync('ffmpeg',['-v','error','-y','-i',source,'-frames:v','1',poster]);
  const original=digest(source),originalPoster=digest(poster);
  const result=buildCardPreview({source,poster,output,thumbnail});
  const info=JSON.parse(execFileSync('ffprobe',['-v','error','-show_streams','-show_format','-of','json',output]));
  assert.equal(info.streams.length,1);assert.equal(info.streams[0].codec_name,'h264');
  assert.ok(info.streams[0].width<=480&&info.streams[0].height<=480);
  assert.equal(info.streams[0].avg_frame_rate,'24/1');
  assert.ok(Math.abs(Number(info.format.duration)-2)<.1);
  assert.ok(result.bytes<fs.statSync(source).size/4,'Expected a substantial reduction for thumbnail playback');
  const bytes=fs.readFileSync(output);assert.ok(bytes.indexOf('moov')<bytes.indexOf('mdat'),'MP4 metadata must precede video frames');
  assert.equal(digest(source),original);assert.equal(digest(poster),originalPoster);
  const mtime=fs.statSync(output).mtimeMs;
  assert.equal(buildCardPreview({source,poster,output,thumbnail}).cached,true);
  assert.equal(fs.statSync(output).mtimeMs,mtime);
  fs.unlinkSync(thumbnail);
  assert.equal(buildCardPreview({source,poster,output,thumbnail}).cached,false);
  assert.ok(fs.statSync(thumbnail).size<fs.statSync(poster).size);
  assert.equal(buildCardPreview({source,poster,output,thumbnail,start:.75,seconds:.5}).cached,false);
  const trimmed=JSON.parse(execFileSync('ffprobe',['-v','error','-show_format','-of','json',output]));
  assert.ok(Math.abs(Number(trimmed.format.duration)-.5)<.1);
  const firstSegment=digest(output);
  assert.equal(buildCardPreview({source,poster,output,thumbnail,start:.75,seconds:.5}).cached,true);
  assert.equal(buildCardPreview({source,poster,output,thumbnail,start:1.25,seconds:.5}).cached,false);
  assert.notEqual(digest(output),firstSegment,'Changing the start must select a different animation segment');
  assert.equal(digest(source),original);
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
});
