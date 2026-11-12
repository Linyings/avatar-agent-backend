import hashlib
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
import workflow


def run_probe(folder, path, kind):
    workflow.write(folder/'probe-input.json', {'path': str(path), 'kind': kind})
    return workflow.command_probe(SimpleNamespace(task=folder, ffmpeg='ffmpeg', ffprobe='ffprobe'))


class ProbeAnyFileTests(unittest.TestCase):
    def test_attachment_is_stored_without_decoding(self):
        with tempfile.TemporaryDirectory() as d:
            folder = Path(d); p = folder/'brief.pdf'; p.write_bytes(b'%PDF-1.7 not decoded')
            out = run_probe(folder, p, 'file')
            self.assertEqual(out['sha256'], hashlib.sha256(p.read_bytes()).hexdigest())
            self.assertEqual(out['duration'], 0)

    def test_empty_attachment_is_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            folder = Path(d); p = folder/'empty.bin'; p.write_bytes(b'')
            with self.assertRaises(ValueError):
                run_probe(folder, p, 'file')

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'requires FFmpeg')
    def test_webp_image_is_converted_to_jpeg_in_place(self):
        with tempfile.TemporaryDirectory() as d:
            folder = Path(d); p = folder/'portrait'
            subprocess.run(['ffmpeg', '-y', '-v', 'error', '-f', 'lavfi', '-i', 'color=c=red:s=200x240', '-frames:v', '1', '-f', 'webp', str(p)], check=True)
            out = run_probe(folder, p, 'photo')
            info = workflow.probe(SimpleNamespace(ffprobe='ffprobe'), p)
            stream = next(s for s in info['streams'] if s['codec_type'] == 'video')
            self.assertEqual(stream['codec_name'], 'mjpeg')
            self.assertEqual(out['sha256'], hashlib.sha256(p.read_bytes()).hexdigest())
            self.assertTrue(out.get('converted'))

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'requires FFmpeg')
    def test_long_voice_reference_is_trimmed_to_a_sample(self):
        with tempfile.TemporaryDirectory() as d:
            folder = Path(d); p = folder/'voice.mp3'
            subprocess.run(['ffmpeg', '-y', '-v', 'error', '-f', 'lavfi', '-i', 'sine=frequency=300:duration=90', '-b:a', '64k', str(p)], check=True)
            out = run_probe(folder, p, 'audio')
            self.assertTrue(2 <= out['duration'] <= 30, out['duration'])

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'requires FFmpeg')
    def test_large_photo_is_recompressed_not_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            folder = Path(d); p = folder/'huge.png'
            # Noise does not compress: a 3000x3000 noise PNG is far over 8MB.
            subprocess.run(['ffmpeg', '-y', '-v', 'error', '-f', 'lavfi', '-i', 'nullsrc=s=3000x3000,geq=random(1)*255:128:128', '-frames:v', '1', str(p)], check=True)
            self.assertGreater(p.stat().st_size, 8 << 20)
            out = run_probe(folder, p, 'photo')
            self.assertLessEqual(p.stat().st_size, 8 << 20)
            self.assertTrue(out['converted'])

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'requires FFmpeg')
    def test_png_image_is_kept_unchanged(self):
        with tempfile.TemporaryDirectory() as d:
            folder = Path(d); p = folder/'portrait.png'
            subprocess.run(['ffmpeg', '-y', '-v', 'error', '-f', 'lavfi', '-i', 'color=c=blue:s=200x240', '-frames:v', '1', str(p)], check=True)
            before = p.read_bytes()
            out = run_probe(folder, p, 'image')
            self.assertEqual(p.read_bytes(), before)
            self.assertFalse(out.get('converted'))


if __name__ == '__main__':
    unittest.main()
