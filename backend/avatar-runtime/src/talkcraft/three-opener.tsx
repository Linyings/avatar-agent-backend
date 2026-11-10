import React from 'react';
import {AbsoluteFill, interpolate, useCurrentFrame, useVideoConfig} from 'remotion';
import {ThreeCanvas} from '@remotion/three';
import {useCamera} from './camera';
import * as THREE from 'three';

// Deterministic 3D scene driven entirely by useCurrentFrame().
// No requestAnimationFrame, no external assets, no random state.

function GlobeScene({progress}: {progress: number}) {
  const meshRef = React.useRef<THREE.Mesh>(null);
  const linesRef = React.useRef<THREE.LineSegments>(null);
  const ringRef = React.useRef<THREE.Mesh>(null);

  // slow Y-axis rotation driven by progress
  const rotY = progress * Math.PI * 0.6;

  React.useLayoutEffect(() => {
    if (meshRef.current) meshRef.current.rotation.y = rotY;
    if (linesRef.current) linesRef.current.rotation.y = rotY;
    if (ringRef.current) {
      ringRef.current.rotation.x = Math.PI * 0.42;
      ringRef.current.rotation.z = rotY * 0.4;
    }
  });

  const opacity = Math.min(1, progress * 3);

  return (
    <>
      <ambientLight intensity={0.35} />
      <pointLight position={[4, 3, 4]} intensity={120} color="#6ee7f7" />
      <pointLight position={[-4, -2, 2]} intensity={40} color="#a78bfa" />

      {/* Globe surface */}
      <mesh ref={meshRef}>
        <sphereGeometry args={[1.4, 48, 48]} />
        <meshStandardMaterial
          color="#0f172a"
          emissive="#1e3a5f"
          emissiveIntensity={0.4}
          transparent
          opacity={opacity}
          roughness={0.7}
          metalness={0.2}
        />
      </mesh>

      {/* Latitude / longitude wireframe */}
      <lineSegments ref={linesRef}>
        <edgesGeometry args={[new THREE.SphereGeometry(1.42, 24, 16)]} />
        <lineBasicMaterial color="#38bdf8" transparent opacity={opacity * 0.5} />
      </lineSegments>

      {/* Orbital ring */}
      <mesh ref={ringRef}>
        <torusGeometry args={[2.1, 0.012, 8, 120]} />
        <meshBasicMaterial color="#7dd3fc" transparent opacity={opacity * 0.7} />
      </mesh>

      {/* Orbit glow dot */}
      <mesh
        position={[
          2.1 * Math.cos(progress * Math.PI * 2),
          0.3 * Math.sin(progress * Math.PI * 4),
          2.1 * Math.sin(progress * Math.PI * 2),
        ]}
      >
        <sphereGeometry args={[0.07, 12, 12]} />
        <meshBasicMaterial color="#e0f2fe" />
      </mesh>
    </>
  );
}

function ParticleField({progress}: {progress: number}) {
  const count = 180;
  const positions = React.useMemo(() => {
    const arr = new Float32Array(count * 3);
    // deterministic pseudo-random using index — no Math.random()
    for (let i = 0; i < count; i++) {
      const t = i / count;
      const phi = Math.acos(1 - 2 * t);
      const theta = Math.sqrt(count * Math.PI) * phi;
      arr[i * 3] = 3.2 * Math.sin(phi) * Math.cos(theta);
      arr[i * 3 + 1] = 3.2 * Math.cos(phi);
      arr[i * 3 + 2] = 3.2 * Math.sin(phi) * Math.sin(theta);
    }
    return arr;
  }, []);

  return (
    <points>
      <bufferGeometry>
        <bufferAttribute attach="attributes-position" args={[positions, 3]} />
      </bufferGeometry>
      <pointsMaterial
        color="#bae6fd"
        size={0.025}
        transparent
        opacity={Math.min(1, progress * 2) * 0.6}
        sizeAttenuation
      />
    </points>
  );
}

export type OpenerProps = {
  title: string;
  subtitle?: string;
  accentColor?: string;
  durationSec: number;
};

export function ThreeOpener({title, subtitle, accentColor = '#38bdf8', durationSec}: OpenerProps) {
  const {width, height, fps} = useVideoConfig();
  const frame = useCurrentFrame();
  const sec = frame / fps;
  const progress = Math.min(1, sec / durationSec);

  // Fade in
  const fadeIn = interpolate(sec, [0, 0.6], [0, 1], {extrapolateRight: 'clamp'});
  // Fade out last 1.5s
  const fadeOut = interpolate(sec, [durationSec - 1.5, durationSec], [1, 0], {extrapolateLeft: 'clamp', extrapolateRight: 'clamp'});
  const opacity = Math.min(fadeIn, fadeOut);

  // Title slides up
  const titleY = interpolate(sec, [0.3, 1.0], [40, 0], {extrapolateLeft: 'clamp', extrapolateRight: 'clamp'});
  const titleOpacity = interpolate(sec, [0.3, 1.0], [0, 1], {extrapolateLeft: 'clamp', extrapolateRight: 'clamp'});

  const fontSize = Math.round(Math.min(width, height) * 0.055);
  const subSize = Math.round(Math.min(width, height) * 0.032);

  return (
    <AbsoluteFill style={{background: '#030712', opacity}}>
      {/* Three.js scene */}
      <ThreeCanvas width={width} height={height} style={{position: 'absolute', inset: 0}}>
        <GlobeScene progress={progress} />
        <ParticleField progress={progress} />
      </ThreeCanvas>

      {/* Vignette */}
      <AbsoluteFill
        style={{
          background: 'radial-gradient(ellipse at 50% 50%, transparent 40%, rgba(3,7,18,0.75) 100%)',
          pointerEvents: 'none',
        }}
      />

      {/* Title */}
      <AbsoluteFill
        style={{
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          gap: 16,
          transform: `translateY(${titleY}px)`,
          opacity: titleOpacity,
        }}
      >
        {subtitle && (
          <div
            style={{
              fontSize: subSize,
              color: accentColor,
              letterSpacing: '0.18em',
              fontWeight: 600,
              textTransform: 'uppercase',
              fontFamily: '"Noto Sans CJK SC", sans-serif',
            }}
          >
            {subtitle}
          </div>
        )}
        <div
          style={{
            fontSize,
            color: '#f8fafc',
            fontWeight: 800,
            textAlign: 'center',
            maxWidth: width * 0.82,
            lineHeight: 1.3,
            fontFamily: '"Noto Sans CJK SC", sans-serif',
            textShadow: `0 0 40px ${accentColor}66`,
          }}
        >
          {title}
        </div>
        {/* Accent line */}
        <div
          style={{
            width: interpolate(sec, [0.8, 1.6], [0, width * 0.22], {extrapolateLeft: 'clamp', extrapolateRight: 'clamp'}),
            height: 3,
            background: `linear-gradient(90deg, transparent, ${accentColor}, transparent)`,
            borderRadius: 2,
          }}
        />
      </AbsoluteFill>
    </AbsoluteFill>
  );
}
