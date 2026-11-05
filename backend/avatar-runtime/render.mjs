import {bundle} from '@remotion/bundler';
import {renderMedia, selectComposition} from '@remotion/renderer';
import {readFile, mkdir} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {validateManifest} from './timeline.mjs';

const root=path.dirname(fileURLToPath(import.meta.url));
const task=path.resolve(process.argv[2]);
const manifest=JSON.parse(await readFile(path.join(task,'manifest.json'),'utf8'));
validateManifest(manifest);
await mkdir(path.join(root,'.cache'),{recursive:true});
const serveUrl=await bundle({entryPoint:path.join(root,'src/index.tsx'),publicDir:task,outDir:path.join(root,'.cache','bundle')});
const inputProps={manifest};
const composition=await selectComposition({serveUrl,id:'AvatarMVP',inputProps});
await renderMedia({serveUrl,composition,inputProps,codec:'h264',audioCodec:'aac',outputLocation:path.join(task,'output.mp4'),concurrency:2,overwrite:true});
console.log('Rendered AvatarMVP with a single final narration track');
