#!/bin/bash
set -e

API="http://localhost:4242"
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $1${NC}"; }
fail() { echo -e "${RED}✗ $1${NC}"; exit 1; }

echo "=== Media Platform Integration Test ==="

# 1. Health check
echo -n "Health check... "
curl -sf "$API/health" | grep -q "ok" && pass "Server healthy" || fail "Server not responding"

# 2. Generate sample if not exists
if [ ! -f samples/test.mp4 ]; then
  echo "Generating sample MP4..."
  bash scripts/generate_sample.sh
fi

# 3. Upload
echo -n "Uploading MP4... "
UPLOAD_RESP=$(curl -sf -X POST "$API/api/upload" -F "file=@samples/test.mp4")
MEDIA_ID=$(echo "$UPLOAD_RESP" | grep -o '"ID":"[^"]*"' | cut -d'"' -f4)
[ -n "$MEDIA_ID" ] && pass "Uploaded: $MEDIA_ID" || fail "Upload failed: $UPLOAD_RESP"

# 4. Check media exists
echo -n "Checking metadata... "
curl -sf "$API/api/media/$MEDIA_ID" | grep -q "$MEDIA_ID" && pass "Metadata OK" || fail "Metadata not found"

# 5. Wait for transcoding (max 60s)
echo -n "Waiting for transcoding"
for i in $(seq 1 60); do
  STATUS=$(curl -sf "$API/api/media/$MEDIA_ID" | grep -o '"Status":"[^"]*"' | cut -d'"' -f4)
  if [ "$STATUS" = "ready" ]; then
    echo ""
    pass "Transcoding complete"
    break
  elif [ "$STATUS" = "failed" ]; then
    echo ""
    fail "Transcoding failed"
  fi
  echo -n "."
  sleep 1
done
[ "$STATUS" != "ready" ] && echo "" && fail "Transcoding timeout"

# 6. Test HLS endpoint
echo -n "Testing HLS... "
HLS_STATUS=$(curl -sf -o /dev/null -w "%{http_code}" "$API/api/media/$MEDIA_ID/hls/playlist.m3u8")
[ "$HLS_STATUS" = "200" ] && pass "HLS playlist accessible" || fail "HLS returned $HLS_STATUS"

# 7. Test DASH endpoint
echo -n "Testing DASH... "
DASH_STATUS=$(curl -sf -o /dev/null -w "%{http_code}" "$API/api/media/$MEDIA_ID/dash/manifest.mpd")
[ "$DASH_STATUS" = "200" ] && pass "DASH manifest accessible" || fail "DASH returned $DASH_STATUS"

# 8. Test media list
echo -n "Testing list API... "
curl -sf "$API/api/media" | grep -q "$MEDIA_ID" && pass "List API OK" || fail "List API failed"

echo ""
echo "=== All tests passed! ==="
echo "Web player: $API"
echo "Media ID: $MEDIA_ID"
echo "HLS: $API/api/media/$MEDIA_ID/hls/playlist.m3u8"
echo "DASH: $API/api/media/$MEDIA_ID/dash/manifest.mpd"
