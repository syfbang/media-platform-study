#!/usr/bin/env bash
# Vehicle camera simulator — pushes H.264 RTSP stream to ingest server.
# Usage: bash scripts/rtsp_push.sh [channel_name] [duration_sec]
#
# Example:
#   bash scripts/rtsp_push.sh vehicle-001        # 5 min stream
#   bash scripts/rtsp_push.sh vehicle-002 30     # 30 sec stream

set -euo pipefail

CHANNEL="${1:-vehicle-001}"
DURATION="${2:-300}"
RTSP_URL="rtsp://localhost:8554/${CHANNEL}"

echo "=== Vehicle Camera Simulator ==="
echo "Channel : ${CHANNEL}"
echo "RTSP URL: ${RTSP_URL}"
echo "Duration: ${DURATION}s"
echo "================================"

ffmpeg -re \
  -f lavfi -i "smptebars=duration=${DURATION}:size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=440:duration=${DURATION}" \
  -c:v libx264 -preset ultrafast -tune zerolatency \
  -profile:v baseline -g 30 -keyint_min 30 \
  -x264-params repeat-headers=1 \
  -c:a aac -ar 44100 -ac 1 \
  -f rtsp -rtsp_transport tcp \
  "${RTSP_URL}"
