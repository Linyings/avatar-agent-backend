/* Original composition in its native aspect ratio. No extra card/title shell. */
(async()=>{
 const c=window.MG_CONFIG;
 document.documentElement.style.cssText=`margin:0;width:${c.width}px;height:${c.height}px;overflow:hidden;background:${c.style.background}`;
 document.body.style.cssText='margin:0;overflow:hidden';
 const w=c.style.preview_width||1920,h=c.style.preview_height||1080;
 const scale=Math.min(c.width/w,c.height/h);
 const iframe=document.createElement('iframe');iframe.width=w;iframe.height=h;
 iframe.style.cssText=`position:absolute;border:0;width:${w}px;height:${h}px;transform-origin:0 0;transform:scale(${scale});left:${(c.width-w*scale)/2}px;top:${(c.height-h*scale)/2}px`;
 iframe.src='/'+c.entry;document.body.append(iframe);
 window.MG_RENDER=async(t,source)=>{await iframe.contentWindow.renderAt(source);};
 const began=Date.now();while(!iframe.contentWindow?.__ready){
  if(iframe.contentWindow?.MG_ERROR||Date.now()-began>85000)throw new Error('Native demo did not initialize: '+(iframe.contentWindow?.MG_ERROR||'timeout'));
  await new Promise(resolve=>setTimeout(resolve,50));
 }
 await window.MG_RENDER(0,0);window.MG_READY=true;
})().catch(e=>{window.MG_ERROR=e.message;});
