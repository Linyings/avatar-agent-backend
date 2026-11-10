import React,{useLayoutEffect,useRef,useState} from 'react';
import {continueRender,delayRender} from 'remotion';
// The installed Three.js runtime is also used by the style reference viewer.
import * as THREE from 'three';

type ObjectSpec={kind:string;x:number;y:number;size:number;progress:number};
type Props={width:number;height:number;viewHeight:number;objects:ObjectSpec[];color:string;secondary:string;paper:string;time:number;isometric:boolean};
// Deterministic 3D objects are built from the current semantic elements, never
// loaded from a demo model or video. Labels remain a crisp SVG layer above.
export function MotionObjects(p:Props){
 const ref=useRef<HTMLCanvasElement>(null);const [handle]=useState(()=>delayRender('渲染本段三维动效'));
 useLayoutEffect(()=>{
  const canvas=ref.current;if(!canvas)return;
  const renderer=new THREE.WebGLRenderer({canvas,alpha:true,antialias:true,preserveDrawingBuffer:true});
  renderer.setSize(p.width,p.height,false);renderer.setPixelRatio(1);renderer.setClearColor(0x000000,0);
  renderer.outputColorSpace=THREE.SRGBColorSpace;renderer.toneMapping=THREE.ACESFilmicToneMapping;renderer.toneMappingExposure=1.05;
  const scene=new THREE.Scene(),camera=new THREE.OrthographicCamera(-500,500,p.viewHeight/2,-p.viewHeight/2,.1,5000);camera.position.z=1800;
  scene.add(new THREE.HemisphereLight(0xffffff,0x626680,1.8));
  const key=new THREE.DirectionalLight(0xffffff,2.2);key.position.set(-400,600,1000);scene.add(key);
  const rim=new THREE.DirectionalLight(p.secondary,2.4);rim.position.set(500,-300,400);scene.add(rim);
  const materials=[p.color,p.secondary,p.paper].map(color=>new THREE.MeshPhysicalMaterial({color,roughness:p.isometric?.65:.24,metalness:p.isometric?0:.14,clearcoat:p.isometric?0:.9,clearcoatRoughness:.16}));
  const mesh=(group:any,geo:any,mi=0,x=0,y=0,z=0)=>{const m=new THREE.Mesh(geo,materials[mi]);m.position.set(x,y,z);group.add(m);return m};
  const ball=(g:any,r:number,mi=0,x=0,y=0,z=0)=>mesh(g,new THREE.SphereGeometry(r,24,18),mi,x,y,z);
  const cube=(g:any,w:number,h:number,d:number,mi=0,x=0,y=0,z=0)=>mesh(g,new THREE.BoxGeometry(w,h,d,2,2,2),mi,x,y,z);
  for(const o of p.objects){
   if(o.progress<=0)continue;
   const g=new THREE.Group(),s=o.size*.95;scene.add(g);g.position.set(o.x+o.size/2-500,p.viewHeight/2-o.y-o.size/2,0);
   g.rotation.set(p.isometric?.52:-.24,p.isometric?-.62:-.35+Math.sin(p.time*.6)*.06,0);g.scale.setScalar(Math.max(.01,o.progress));
   if(o.kind==='coin'){
    for(let i=0;i<4;i++){const m=mesh(g,new THREE.CylinderGeometry(s*.38,s*.38,s*.13,48),i%2,0,-s*.32+i*s*.17,0);m.rotation.x=.12;}
    const mark=mesh(g,new THREE.TorusGeometry(s*.23,s*.024,10,40),2,0,s*.28,0);mark.rotation.x=-Math.PI/2;
   }else if(o.kind==='chip'||o.kind==='code'){
    cube(g,s*.72,s*.72,s*.18,0);cube(g,s*.43,s*.43,s*.10,1,0,0,s*.14);
    for(let i=-2;i<=2;i++){cube(g,s*.13,s*.035,s*.055,2,-s*.42,i*s*.12);cube(g,s*.13,s*.035,s*.055,2,s*.42,i*s*.12);cube(g,s*.035,s*.13,s*.055,2,i*s*.12,s*.42);cube(g,s*.035,s*.13,s*.055,2,i*s*.12,-s*.42);}
   }else if(o.kind==='people'){
    for(let i=-1;i<=1;i++){const f=i===0?1:.72;ball(g,s*.14*f,i===0?0:1,i*s*.3,s*.20);mesh(g,new THREE.CapsuleGeometry(s*.13*f,s*.22*f,5,18),i===0?0:1,i*s*.3,-s*.15);}
   }else if(o.kind==='cloud'){
    for(const [x,y,r] of [[-.28,-.1,.21],[-.08,.03,.3],[.22,-.03,.24]])ball(g,s*r,0,s*x,s*y);
    cube(g,s*.6,s*.24,s*.28,0,0,-s*.17);
   }else if(o.kind==='globe'||o.kind==='clock'){
    ball(g,s*.40,0);for(let i=0;i<3;i++){const ring=mesh(g,new THREE.TorusGeometry(s*.412,s*.012,8,60),2);ring.rotation.y=i*Math.PI/3;}
    if(o.kind==='clock'){cube(g,s*.025,s*.24,s*.02,2,0,s*.10,s*.41);cube(g,s*.2,s*.025,s*.02,2,s*.08,0,s*.41);}
   }else if(o.kind==='leaf'){
    const shape=new THREE.Shape();shape.moveTo(-s*.3,-s*.3);shape.bezierCurveTo(-s*.65,s*.3,s*.1,s*.46,s*.34,s*.34);shape.bezierCurveTo(s*.49,0,s*.23,-s*.5,-s*.3,-s*.3);
    mesh(g,new THREE.ExtrudeGeometry(shape,{depth:s*.08,bevelEnabled:true,bevelThickness:s*.025,bevelSize:s*.025,bevelSegments:4,steps:1}),0);
    const stem=cube(g,s*.025,s*.64,s*.035,2,0,0,s*.12);stem.rotation.z=-.75;
   }else if(o.kind==='document'||o.kind==='shield'){
    cube(g,s*.63,s*.81,s*.12,0);for(let i=0;i<3;i++)cube(g,s*(i===2?.23:.43),s*.035,s*.03,2,0,s*(.20-i*.16),s*.08);
   }else if(o.kind==='link'){
    for(let i=-1;i<=1;i+=2){const ring=mesh(g,new THREE.TorusGeometry(s*.24,s*.065,16,40),i<0?0:1,i*s*.16,0,0);ring.rotation.y=i*.5;}
   }else if(o.kind==='spark'){
    mesh(g,new THREE.OctahedronGeometry(s*.44),0);ball(g,s*.10,1,s*.40,s*.34,s*.05);
   }else if(o.kind==='chart'){
    // Equal columns are a diagram symbol, not fabricated quantitative data.
    for(let i=-1;i<=1;i++)cube(g,s*.20,s*.65,s*.28,(i+2)%3,i*s*.27,0);
   }else{cube(g,s*.65,s*.65,s*.65,0);cube(g,s*.68,s*.08,s*.68,2,0,s*.10);}
  }
  renderer.render(scene,camera);continueRender(handle);
  return()=>{scene.traverse((obj:any)=>{if(obj.geometry)obj.geometry.dispose()});materials.forEach(m=>m.dispose());renderer.dispose()};
 },[p.width,p.height,p.viewHeight,p.time,p.color,p.secondary,p.paper,p.isometric,JSON.stringify(p.objects),handle]);
 return <canvas ref={ref} width={p.width} height={p.height} style={{position:'absolute',inset:0,width:'100%',height:'100%'}}/>;
}
