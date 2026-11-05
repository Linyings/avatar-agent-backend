import {test} from 'node:test';
import assert from 'node:assert/strict';
import {validateManifest} from '../timeline.mjs';

test('fractional boundaries cover each output frame exactly once',()=>{
  const fps=25,edges=[0,4.137,9.346,15.125];
  const manifest={fps,duration:edges[3],shots:[0,1,2].map(i=>({source:`shot-${i+1}/source.mp4`,clip:{start:edges[i],end:edges[i+1],fps,face_zone:[0,0,1,1]}}))};
  const mapping=validateManifest(manifest);
  assert.equal(mapping[0].from,0);assert.equal(mapping[0].to,mapping[1].from);assert.equal(mapping[1].to,mapping[2].from);assert.equal(mapping[2].to,Math.ceil(15.125*25));
});
test('rejects repeated source timeline and missing face evidence',()=>{
  assert.throws(()=>validateManifest({fps:25,duration:15,shots:[0,1,2].map(()=>({source:'a.mp4',clip:{start:0,end:5,fps:25,face_zone:[0,0,1,1]}}))}));
});
