import argparse
import os
import sys
import subprocess
import shutil

def to_abs(p):
    return os.path.abspath(p)

def convert_with_pillow(inp, outp, quality, lossless, method, loop):
    try:
        from PIL import Image, ImageSequence, features
        if not features.check('webp'):
            return False
        im = Image.open(inp)
        frames = []
        durations = []
        for frame in ImageSequence.Iterator(im):
            frames.append(frame.convert('RGBA'))
            durations.append(frame.info.get('duration', im.info.get('duration', 100)))
        if not frames:
            return False
        save_kwargs = {
            'save_all': True,
            'append_images': frames[1:],
            'duration': durations,
            'loop': loop,
            'method': method,
            'quality': quality,
        }
        if lossless:
            save_kwargs['lossless'] = True
        frames[0].save(outp, **save_kwargs)
        return True
    except Exception:
        return False

def convert_with_ffmpeg(inp, outp, quality, lossless, loop):
    ffmpeg = shutil.which('ffmpeg')
    if not ffmpeg:
        return False
    cmd = [ffmpeg, '-y', '-i', inp, '-vcodec', 'libwebp']
    if lossless:
        cmd += ['-lossless', '1']
    else:
        cmd += ['-q:v', str(quality)]
    cmd += ['-loop', str(loop), '-an', '-sn', outp]
    try:
        r = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return r.returncode == 0 and os.path.exists(outp)
    except Exception:
        return False

def convert_with_magick(inp, outp, quality, lossless, loop):
    magick = shutil.which('magick') or shutil.which('convert')
    if not magick:
        return False
    defines = []
    defines.append(f'-define webp:loop={loop}')
    if lossless:
        defines.append('-define webp:lossless=true')
    else:
        defines.append(f'-define webp:quality={quality}')
    cmd = [magick, inp] + defines + [outp]
    try:
        r = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return r.returncode == 0 and os.path.exists(outp)
    except Exception:
        return False

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--input', default=os.path.join('static', 'gifs', 'success.gif'))
    parser.add_argument('--output', default=os.path.join('static', 'gifs', 'success.webp'))
    parser.add_argument('--quality', type=int, default=80)
    parser.add_argument('--lossless', action='store_true')
    parser.add_argument('--method', type=int, default=6)
    parser.add_argument('--loop', type=int, default=0)
    args = parser.parse_args()

    inp = to_abs(args.input)
    outp = to_abs(args.output)
    os.makedirs(os.path.dirname(outp), exist_ok=True)

    if not os.path.exists(inp):
        print('input not found: ' + inp)
        sys.exit(1)

    if convert_with_pillow(inp, outp, args.quality, args.lossless, args.method, args.loop):
        print('converted with pillow: ' + outp)
        sys.exit(0)

    if convert_with_ffmpeg(inp, outp, args.quality, args.lossless, args.loop):
        print('converted with ffmpeg: ' + outp)
        sys.exit(0)

    if convert_with_magick(inp, outp, args.quality, args.lossless, args.loop):
        print('converted with imagemagick: ' + outp)
        sys.exit(0)

    print('conversion failed')
    sys.exit(2)

if __name__ == '__main__':
    main()