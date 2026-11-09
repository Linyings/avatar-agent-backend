import fs from 'node:fs';
import {captionPages} from './src/film-design.ts';
const {scenes,budget}=JSON.parse(fs.readFileSync(0,'utf8'));
process.stdout.write(JSON.stringify(scenes.flatMap(scene=>captionPages(scene.chars,budget))));
