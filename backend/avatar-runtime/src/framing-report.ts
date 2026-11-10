import {resolveRecipe,shotLayout,type Box} from './film-design.ts';
import {avatarFramingDecision,stableAvatarPlan,avatarWindowBox,type AvatarTrack} from './avatar-framing.ts';

type Material={need_id:string;kind:string;avatar_framing?:AvatarTrack};
type Shot={id:string;segment_id:string;start:number;end:number;primary:string;overlay?:string;card:string;layout:string;recipe?:string;primary_in?:number;overlay_in?:number;avatar_position?:string;avatar_shape?:string};
type Manifest={width:number;height:number;input:{style:string;avatar_position?:string};segments:{id:string;start:number}[];materials:Material[]};

export type AvatarLayer={role:'primary'|'overlay';box:Box;window:boolean;shape:string;material:Material;start:number};

// The talking-head layers a shot renders, with the exact boxes FilmShot uses.
export function avatarLayers(m:Manifest,shot:Shot):AvatarLayer[]{
 const segment=m.segments.find(s=>s.id===shot.segment_id);
 const primary=m.materials.find(s=>s.need_id===shot.primary),overlay=m.materials.find(s=>s.need_id===shot.overlay);
 if(!segment||!primary)return [];
 let recipe:string=resolveRecipe(shot,primary);
 if(primary.kind==='animation')recipe=overlay?'visual-pip':'visual-hero';
 if(recipe==='visual-pip'&&!overlay)recipe='visual-hero';
 if(/^(mg-|explain-)/.test(m.input.style)&&!overlay&&primary.kind!=='animation')recipe=primary.kind==='avatar'?'host-hero':'visual-hero';
 const side=shot.avatar_position||m.input.avatar_position||'right',shape=shot.avatar_shape||'rounded';
 const layout=shotLayout(m.width,m.height,recipe,side);
 if(recipe==='visual-pip'&&overlay?.kind==='avatar'&&layout.overlay){layout.primary={x:0,y:0,w:m.width,h:m.height};layout.overlay=avatarWindowBox(m.width,m.height,shape,side);}
 const offset=shot.start-segment.start,out:AvatarLayer[]=[];
 const framed=(b:Box)=>b.x>0||b.y>0;
 if(primary.kind==='avatar'&&framed(layout.primary))out.push({role:'primary',box:layout.primary,window:false,shape:'rectangle',material:primary,start:shot.primary_in??offset});
 const panel=layout.panel&&primary.kind!=='animation'?layout.panel:undefined;
 const overlayBox=layout.overlay||panel;
 if(overlay?.kind==='avatar'&&overlayBox)out.push({role:'overlay',box:overlayBox,window:true,shape,material:overlay,start:shot.overlay_in??offset});
 return out;
}

export function avatarFramingReport(m:Manifest,shots:Shot[]){
 return shots.flatMap(shot=>avatarLayers(m,shot).map(layer=>{
  const decision=avatarFramingDecision(layer.box,layer.material.avatar_framing,layer.start,layer.start+shot.end-shot.start,layer.shape);
   const stable=stableAvatarPlan(layer.box,layer.material.avatar_framing,layer.start,layer.start+shot.end-shot.start,layer.shape);
   return {shot_id:shot.id,layer:layer.role,requested_shape:layer.shape,actual_shape:layer.shape,mode:decision?.mode??'centered',face_height:decision?.faceHeight??null,crop_policy:stable?.version??'static-cover',crop_segments:stable?.segments.map(s=>({start:s.start,end:s.end,scale:s.crop.scale,scale_variation:0,translation_x:Math.max(...s.positions.map(p=>p.left))-Math.min(...s.positions.map(p=>p.left)),translation_y:Math.max(...s.positions.map(p=>p.top))-Math.min(...s.positions.map(p=>p.top))}))??[]};
 }));
}
