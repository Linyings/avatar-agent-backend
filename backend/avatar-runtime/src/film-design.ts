// Application layouts informed by talkcraft shot-design/design-language; upstream
// sources remain unchanged. Timing and talking-footage source offsets are external.
import styles from '../native-styles.json' with {type:'json'};
export const PRESENTATION_VERSION='film-design/3';
export const RECIPES=['host-hero','host-explain','visual-hero','visual-pip','compare','steps','evidence','summary'] as const;
export type Recipe=typeof RECIPES[number];
export type Box={x:number;y:number;w:number;h:number};
export type FilmTheme={canvas:string;surface:string;ink:string;muted:string;accent:string;line:string;radius:number;paper:boolean;motion:number};
const themes:Record<string,FilmTheme>={
 'mg-flat-vector':{canvas:'#F5F4FF',surface:'#FFFFFF',ink:'#222047',muted:'#625E7A',accent:'#5146CE',line:'#DAD6EE',radius:24,paper:false,motion:.35},
 'mg-line-art':{canvas:'#0B1320',surface:'#142135',ink:'#F3E8D0',muted:'#BCB39F',accent:'#E8C877',line:'#435064',radius:4,paper:false,motion:.5},
 'mg-morph':{canvas:'#F4EEE3',surface:'#FFFCF7',ink:'#282323',muted:'#726860',accent:'#AC482C',line:'#D8CABB',radius:36,paper:false,motion:.45},
 'mg-bauhaus':{canvas:'#F1E9DA',surface:'#FCF8EF',ink:'#23211E',muted:'#6D6256',accent:'#B93928',line:'#C9BFAE',radius:0,paper:false,motion:.3},
 'mg-collage':{canvas:'#F2ECDF',surface:'#FFFCF3',ink:'#29251F',muted:'#746A5B',accent:'#AC3C30',line:'#CFC2AD',radius:2,paper:true,motion:.4},
};
export function filmTheme(style:string):FilmTheme{
 const design=styles.find(s=>s.id===style)?.design;
 if(design){const [canvas,surface,ink,accent,line]=design.palette;return {canvas,surface,ink,accent,line,muted:line,radius:['geometric','pixel','collage'].includes(design.treatment)?2:24,paper:['paper','collage','sketch'].includes(design.treatment),motion:design.motion==='glide'?.7:.4};}
 return themes[style]||themes[style==='line-art'?'mg-line-art':'mg-flat-vector'];
}
export function semanticItems(text:string):string[]{return text.split(/[。；！？\n，、]/).map(s=>s.trim()).filter(Boolean).slice(0,4);}
export function captionPages<T extends {ch:string;t:number;e:number}>(chars:T[],budget:number):T[][]{
 const pages:T[][]=[];let page:T[]=[],units=0;
 const text=chars.map(c=>c.ch).join('');
 const boundaries=new Map<number,string>();
 for(const part of new Intl.Segmenter('zh',{granularity:'word'}).segment(text))boundaries.set(part.index,part.segment);
 let offset=0;
 for(let i=0;i<chars.length;){
  const rest=text.slice(offset);
  const amount=rest.match(/^\d+(?:[.,]\d+)*(?:\s*(?:万亿|亿|万|千|百)?(?:美元|人民币|欧元|港元|日元|元|%|％|倍|人|个|秒|分钟|年|月|天))?/);
  const raw=amount?.[0]||boundaries.get(offset)||chars[i].ch;
  const token:T[]=[];let length=0;
  while(i<chars.length&&length<raw.length){length+=chars[i].ch.length;token.push(chars[i++]);}
  offset+=length;
  const weight=token.reduce((sum,c)=>sum+(/[\x00-\x7f]/.test(c.ch)?.55:1),0);
  if(page.length&&units+weight>budget){pages.push(page);page=[];units=0;}
  page.push(...token);units+=weight;
  if(/[，。！？；]/.test(token.at(-1)!.ch)&&units>budget*.5){pages.push(page);page=[];units=0;}
 }
 if(page.length)pages.push(page);
 return pages;
}
export function captionPage<T extends {ch:string;t:number;e:number}>(chars:T[],time:number,budget:number):T[]{
 const pages=captionPages(chars,budget);
 return [...pages].reverse().find(p=>p[0].t<=time)||pages[0]||[];
}
export function overlaps(a:Box,b:Box){return a.x<b.x+b.w&&b.x<a.x+a.w&&a.y<b.y+b.h&&b.y<a.y+a.h;}
export function resolveRecipe(shot:{recipe?:string;layout?:string;card?:string},primary:{kind:string}):Recipe{
 if(RECIPES.includes(shot.recipe as Recipe))return shot.recipe as Recipe;
 if(primary.kind!=='avatar')return shot.layout==='pip'?'visual-pip':shot.card==='data'?'evidence':'visual-hero';
 if(shot.card==='chapter-title-card'||shot.card==='summary')return 'summary';
 if(shot.card==='steps')return 'steps';if(shot.card==='compare')return 'compare';
 if(shot.card==='data')return 'evidence';
 return shot.layout==='split'?'host-explain':'host-hero';
}
export function shotLayout(width:number,height:number,recipe:string,side='right'){
 const wide=width>height,margin=width*.045,gap=width*.025;
 const caption:Box={x:margin,y:height*.91,w:width-2*margin,h:height*.08};
 let primary:Box={x:0,y:0,w:width,h:height*.90};let overlay:Box|undefined;let panel:Box|undefined;
 if(['host-explain','compare','steps','summary','evidence'].includes(recipe)){
  if(wide){const pw=width*.52;primary={x:side==='left'?margin:width-margin-pw,y:height*.045,w:pw,h:height*.82};panel={x:side==='left'?margin+pw+gap:margin,y:height*.075,w:width-2*margin-pw-gap,h:height*.76};}
  else {primary={x:margin,y:height*.025,w:width-2*margin,h:height*.43};panel={x:margin,y:height*.48,w:width-2*margin,h:height*.40};}
 }else if(recipe==='visual-pip'){
  if(wide){primary={x:margin,y:height*.06,w:width*.665,h:height*.79};overlay={x:side==='left'?margin:width*.755,y:height*.30,w:width*.20,h:height*.49};if(side==='left')primary.x=width*.29;}
  else{primary={x:margin,y:height*.035,w:width-2*margin,h:height*.57};overlay={x:side==='left'?margin:width*.555,y:height*.64,w:width*.40,h:height*.245};}
 }
 return {primary,overlay,panel,caption};
}
export function mediaPlacement(box:Box,source:{width?:number;height?:number;face_zone?:number[]},kind:string){
 const sw=source.width||box.w,sh=source.height||box.h;
 const face=source.face_zone?.length===4?source.face_zone:[sw*.25,sh*.08,sw*.5,sh*.55];
 let scale=Math.max(box.w/sw,box.h/sh);let dx=(box.w-sw*scale)/2,dy=(box.h-sh*scale)/2;
 const safe=face[0]*scale+dx>=0&&face[1]*scale+dy>=0&&(face[0]+face[2])*scale+dx<=box.w&&(face[1]+face[3])*scale+dy<=box.h;
 const fit=kind==='avatar'&&source.face_zone?.length===4&&safe?'cover':'contain';
 if(fit==='contain'){scale=Math.min(box.w/sw,box.h/sh);dx=(box.w-sw*scale)/2;dy=(box.h-sh*scale)/2;}
 return {fit:fit as 'cover'|'contain',face:{x:box.x+face[0]*scale+dx,y:box.y+face[1]*scale+dy,w:face[2]*scale,h:face[3]*scale}};
}
