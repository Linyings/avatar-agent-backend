export function validateManifest(manifest) {
  if (!Number.isFinite(manifest.fps) || manifest.fps<=0 || manifest.shots.length!==3) throw new Error('Invalid render manifest');
  let end=0;
  for (const shot of manifest.shots) {
    const c=shot.clip;
    if (Math.abs(c.start-end)>.001 || c.end<=c.start || !shot.source || shot.source.includes('..') || Math.abs(c.fps-manifest.fps)>.001) throw new Error('Invalid source/time mapping');
    if (!Array.isArray(c.face_zone) || c.face_zone.length!==4) throw new Error('Missing measured face safe zone');
    end=c.end;
  }
  if (Math.abs(end-manifest.duration)>.001) throw new Error('Missing or repeated narration');
  return manifest.shots.map((s,i)=>({from:Math.round(s.clip.start*manifest.fps),to:i===2?Math.ceil(manifest.duration*manifest.fps):Math.round(s.clip.end*manifest.fps)}));
}
