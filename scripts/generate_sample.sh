#!/bin/bash
set -e

# Generate a 10-second test MP4 (color bars + tone)
echo "=== Generating sample MP4 ==="
ffmpeg -y -f lavfi -i "smptebars=duration=10:size=1280x720:rate=30" \
       -f lavfi -i "sine=frequency=440:duration=10" \
       -c:v libx264 -preset fast -crf 23 \
       -c:a aac -b:a 128k \
       -pix_fmt yuv420p \
       samples/test.mp4

echo "✓ samples/test.mp4 created"
ls -lh samples/test.mp4
