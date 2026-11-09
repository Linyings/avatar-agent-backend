import fs from 'node:fs';
import {validateMotionCode,renderDirectorMotion} from './director-motion.mjs';
import {compileMotion} from './content-motion.mjs';
import {localMotionScene,printMotionError,MotionValidationError} from './motion-checks.mjs';
try{
 const input=JSON.parse(fs.readFileSync(process.argv[2],'utf8'));
 let result={valid:true};
 if(input.mode==='timing'){try{result={scenes:compileMotion(input.animation).scenes.map(localMotionScene)}}catch(error){throw new MotionValidationError('motion_plan_invalid',error.message)}}
 else{
  validateMotionCode(input.code);
  if(input.animation){const a={...input.animation,motion_plan:{...input.animation.motion_plan,code:input.code}};await renderDirectorMotion(a,null,{sceneIndex:input.scene_index});}
 }
 if(process.argv[3])fs.writeFileSync(process.argv[3],JSON.stringify(result));
 else console.log(JSON.stringify(result));
}catch(error){printMotionError(error);process.exitCode=1;}
