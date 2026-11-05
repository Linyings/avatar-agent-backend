// Map the measured source face into the contained portrait viewport. Limit
// camera zoom so the entire safety box stays away from text and card regions.
export function hostLayout(clip, layout) {
  const face = clip.face_zone;
  if (!Array.isArray(face) || face.length !== 4 || !face.every(Number.isFinite) || !Number.isFinite(clip.width) || !Number.isFinite(clip.height) || clip.width <= 0 || clip.height <= 0 || face[0] < 0 || face[1] < 0 || face[2] <= 0 || face[3] <= 0 || face[0]+face[2] > clip.width || face[1]+face[3] > clip.height) throw new Error('Invalid measured face safe zone');
  const left = layout === 'split' ? 260 : 0;
  const width = 720-left;
  const height = 805;
  const scale = Math.min(width/clip.width, height/clip.height);
  const x = left+(width-clip.width*scale)/2+face[0]*scale;
  const y = (height-clip.height*scale)/2+face[1]*scale;
  const right = x+face[2]*scale;
  const bottom = y+face[3]*scale;
  let zoom = 1.025;
  for (const [point, center, min, max] of [[x,360,left ? 235 : 0,720], [right,360,left ? 235 : 0,720], [y,height/2,0,height], [bottom,height/2,0,height]]) {
    if (point < center) zoom = Math.min(zoom, (center-min)/(center-point));
    if (point > center) zoom = Math.min(zoom, (max-center)/(point-center));
  }
  return {left,width,height,zoom:Math.max(1,zoom),face:[x,y,right-x,bottom-y]};
}
