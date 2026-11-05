import {test} from 'node:test';
import assert from 'node:assert/strict';
import {hostLayout} from '../src/host-layout.mjs';

test('measured edge faces limit camera zoom and stay above the card',()=>{
  const l=hostLayout({width:720,height:1280,face_zone:[0,0,720,1280]},'full');
  assert.equal(l.zoom,1);
  assert.equal(l.face[1],0);
  assert.equal(l.face[1]+l.face[3],805);
});
test('split view reserves the keyword column for all camera positions',()=>{
  const l=hostLayout({width:720,height:1280,face_zone:[50,100,350,400]},'split');
  assert.ok(360+(l.face[0]-360)*l.zoom>235);
  assert.ok(l.face[1]+l.face[3]<805);
  assert.throws(()=>hostLayout({width:720,height:1280,face_zone:[-10,0,100,100]},'full'));
});
