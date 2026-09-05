# Public synthetic fixtures

These 32x32 images contain generated colors and gradients, not device captures.
The normal unit suite decodes them with the actual embedded WASI module.

Generation commands (FFmpeg with libx264):

```sh
ffmpeg -f lavfi -i 'color=c=red:size=32x32:rate=1' -frames:v 1 \
  -c:v libx264 -profile:v baseline -pix_fmt yuv420p \
  -x264-params 'keyint=1:threads=1' -f h264 red-baseline.h264
ffmpeg -f lavfi -i 'color=c=red:size=32x32:rate=1' -frames:v 1 \
  -c:v libx264 -profile:v high -pix_fmt yuv420p \
  -x264-params 'keyint=1:threads=1' -f h264 red-high.h264
ffmpeg -f lavfi -i 'nullsrc=s=32x32,geq=lum=X*4+Y*3:cb=128:cr=128' \
  -frames:v 1 -c:v libx264 -profile:v high -pix_fmt yuv420p \
  -x264-params 'keyint=1:threads=1' -f h264 gradient-high.h264
ffmpeg -i gradient-high.h264 -frames:v 1 gradient-high.png
```

The independent gradient PNG allows two levels per channel for RGB conversion
rounding. No test calls FFmpeg or another executable at runtime.


The 160x96 motion fixtures contain 12 generated `testsrc2` frames each. The
reference `.yuv` files are decoded by FFmpeg's native H.264 decoder, independently
of the embedded WASI runtime. Every plane must match exactly, including B-frame
reordering. Generation used FFmpeg 9.0.1 and libx264:

```sh
for kind in p b; do
  bframes=0
  if [ "$kind" = b ]; then bframes=2; fi
  ffmpeg -f lavfi -i testsrc2=size=160x96:rate=30 -frames:v 12 \
    -c:v libx264 -profile:v high -pix_fmt yuv420p \
    -x264-params "keyint=12:bframes=$bframes:aud=1" -f h264 "motion-$kind.h264"
  ffmpeg -i "motion-$kind.h264" -f rawvideo -pix_fmt yuv420p "motion-$kind.yuv"
done
```
