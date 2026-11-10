import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {avatarFramingReport,avatarLayers} from '../src/framing-report.ts';
import {stableAvatarPlan,stableAvatarCrop} from '../src/avatar-framing.ts';

// Shots, materials and face tracks of the vertical run that showed a 9:16 host
// letterboxed inside the square explainer frame (run fbfc23d4).
const manifest=JSON.parse(fs.readFileSync(new URL('./fixtures/close-up-vertical.json',import.meta.url),'utf8'));

test('every talking-head layer of the regressed vertical run fills its window',()=>{
 const report=avatarFramingReport(manifest,manifest.shots);
 assert.ok(report.length>=5,'pip windows must be reported');
 for(const row of report){
  assert.notEqual(row.mode,'full-source');
  assert.ok(row.face_height>0,`${row.shot_id} must be face-cropped`);
   assert.equal(row.actual_shape,row.requested_shape,'a circle stays a circle');
   assert.equal(row.crop_policy,'stable-crop/1');
 }
 for(const shot of manifest.shots)for(const layer of avatarLayers(manifest,shot)){
   const plan=stableAvatarPlan(layer.box,layer.material.avatar_framing,layer.start,layer.start+shot.end-shot.start,layer.shape);
  for(const p of [0,.5,1]){
    const crop=stableAvatarCrop(plan,layer.start+p*(shot.end-shot.start)*.98);
   assert.ok(crop,'tracked time must crop');
   assert.ok(crop.left<=1e-6&&crop.top<=1e-6&&crop.left+crop.width>=layer.box.w-1e-6&&crop.top+crop.height>=layer.box.h-1e-6,`${shot.id} exposes margins`);
  }
 }
});
