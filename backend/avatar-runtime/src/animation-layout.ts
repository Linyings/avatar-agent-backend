// Layout in the animation's 1000-unit SVG coordinate system.
export function cardBandLayout(width:number,height:number){
 const scale=Math.min(width/960,height*.18/240);
 return {width:960,height:240,scale,left:(width-960*scale)/2,top:height*.73+(height*.18-240*scale)/2};
}

export function cardTextLayout(w:number,h:number,text:string,summary:boolean,wide:boolean){
 const inline=summary||h<150;
 const x=summary?85:inline?70:22,y=inline?12:55;
 const width=Math.max(1,w-x-22),height=Math.max(1,h-y-12);
 const characters=Math.max(1,[...text].length);
 let fontSize=wide?31:37;
 // Use full character width as a conservative estimate for Chinese copy.
 while(fontSize>1&&Math.ceil(characters*fontSize/width)*fontSize*1.5>height)fontSize--;
 return {x,y,width,height,fontSize};
}
