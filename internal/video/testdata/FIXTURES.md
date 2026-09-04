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
