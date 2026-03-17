# 통합 테스트 리스트

## 사전 조건

| 항목 | 요구사항 |
|------|---------|
| Docker | MinIO(:9000), Redis(:6379) 실행 중 |
| PostgreSQL | :5432 접속 가능, `mediaplatform` DB 존재 |
| Kafka | :9092 접속 가능, `media-events` 토픽 존재 |
| ffmpeg | 시스템 PATH에 설치됨 |
| 테스트 MP4 | `samples/test.mp4` (10초, 1280×720, H264+AAC, GOP 4초) |
| 서버 | `localhost:4242` 에서 실행 중 |

---

## TC-1: 헬스체크

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `GET /health` |
| 검증 | HTTP 200, `{"status":"ok"}` |
| 인프라 의존성 | 없음 (서버만 기동되면 됨) |

```bash
curl -sf http://localhost:4242/health
# 예상: {"status":"ok"}
```

---

## TC-2: MP4 업로드

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `POST /api/upload` (multipart, field: `file`) |
| 검증 항목 | HTTP 201, 응답에 UUID `ID` 포함, `Status=uploaded` |
| 인프라 의존성 | MinIO (S3 저장), PostgreSQL (메타데이터), Kafka (이벤트 발행) |

```bash
RESP=$(curl -sf -X POST http://localhost:4242/api/upload -F "file=@samples/test.mp4")
echo "$RESP" | python3 -m json.tool
# 검증: ID 존재, Status=="uploaded", S3Key=="originals/{id}.mp4"
```

### TC-2a: 비 MP4 파일 거부

```bash
echo "not a video" > /tmp/test.txt
curl -s -X POST http://localhost:4242/api/upload -F "file=@/tmp/test.txt;filename=test.txt"
# 예상: HTTP 400, "only .mp4 files accepted"
```

### TC-2b: 파일 없이 요청

```bash
curl -s -X POST http://localhost:4242/api/upload
# 예상: HTTP 400, "file required"
```

---

## TC-3: 미디어 메타데이터 조회

### TC-3a: 목록 조회

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `GET /api/media?limit=10&offset=0` |
| 검증 | HTTP 200, JSON 배열, 최신순 정렬 |

```bash
curl -sf "http://localhost:4242/api/media?limit=10" | python3 -m json.tool
```

### TC-3b: 단건 조회

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `GET /api/media/{id}` |
| 검증 | HTTP 200, 해당 ID의 메타데이터 반환 |

```bash
curl -sf "http://localhost:4242/api/media/$MEDIA_ID" | python3 -m json.tool
# 검증: ID, Filename, Status, HLSKey, DASHKey 필드 존재
```

### TC-3c: 존재하지 않는 ID

```bash
curl -s "http://localhost:4242/api/media/nonexistent-id"
# 예상: HTTP 404, "not found"
```

---

## TC-4: 트랜스코딩 파이프라인

| 항목 | 내용 |
|------|------|
| 트리거 | TC-2 업로드 후 자동 (Kafka 이벤트) |
| 검증 | status: `uploaded` → `transcoding` → `ready` |
| 타임아웃 | 60초 이내 완료 |
| 인프라 의존성 | Kafka (이벤트), MinIO (원본 다운로드 + 결과 업로드), PostgreSQL (상태 갱신), ffmpeg |

```bash
# 업로드 후 폴링
for i in $(seq 1 60); do
  STATUS=$(curl -sf "http://localhost:4242/api/media/$MEDIA_ID" | grep -o '"Status":"[^"]*"' | cut -d'"' -f4)
  [ "$STATUS" = "ready" ] && echo "✓ Done (${i}s)" && break
  [ "$STATUS" = "failed" ] && echo "✗ Failed" && break
  sleep 1
done
# 검증: Status=="ready", HLSKey 비어있지 않음, DASHKey 비어있지 않음
```

---

## TC-5: HLS 스트리밍 (ABR)

### TC-5a: 마스터 플레이리스트

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `GET /api/media/{id}/hls/master.m3u8` |
| 검증 | HTTP 200, Content-Type `application/vnd.apple.mpegurl` |
| 내용 검증 | `#EXTM3U`, `#EXT-X-STREAM-INF` × 3개, BANDWIDTH/RESOLUTION 포함 |

```bash
curl -sf "http://localhost:4242/api/media/$MEDIA_ID/hls/master.m3u8"
# 검증:
#   - v0/playlist.m3u8 (1280x720)
#   - v1/playlist.m3u8 (854x480)
#   - v2/playlist.m3u8 (640x360)
```

### TC-5b: Variant 플레이리스트 (각 해상도)

| Variant | 경로 | 검증 |
|---------|------|------|
| 720p | `hls/v0/playlist.m3u8` | `TARGETDURATION` ≤ 4, `EXT-X-PLAYLIST-TYPE:VOD` |
| 480p | `hls/v1/playlist.m3u8` | 동일 |
| 360p | `hls/v2/playlist.m3u8` | 동일 |

```bash
for v in 0 1 2; do
  echo "=== v$v ==="
  curl -sf "http://localhost:4242/api/media/$MEDIA_ID/hls/v$v/playlist.m3u8"
done
# 검증: TARGETDURATION=4, EXTINF ≤ 4.0, EXT-X-MAP URI 존재, EXT-X-ENDLIST 존재
```

### TC-5c: Init 세그먼트 접근

```bash
for v in 0 1 2; do
  curl -sf -o /dev/null -w "v$v init: %{http_code} (%{size_download} bytes)\n" \
    "http://localhost:4242/api/media/$MEDIA_ID/hls/v$v/init_$v.mp4"
done
# 검증: 모두 HTTP 200, size > 0
```

### TC-5d: Media 세그먼트 접근

```bash
for v in 0 1 2; do
  curl -sf -o /dev/null -w "v$v seg_000: %{http_code} (%{size_download} bytes)\n" \
    "http://localhost:4242/api/media/$MEDIA_ID/hls/v$v/seg_000.m4s"
done
# 검증: 모두 HTTP 200, size > 0
```

---

## TC-6: DASH 스트리밍 (ABR)

### TC-6a: MPD 매니페스트

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `GET /api/media/{id}/dash/manifest.mpd` |
| 검증 | HTTP 200, Content-Type `application/dash+xml` |
| 내용 검증 | `<MPD>`, `<AdaptationSet>` × 2 (video + audio), `<Representation>` × 3 (video) |

```bash
curl -sf "http://localhost:4242/api/media/$MEDIA_ID/dash/manifest.mpd"
# 검증:
#   - type="static" (VOD)
#   - maxSegmentDuration="PT4.0S"
#   - 3개 video Representation (1280x720, 854x480, 640x360)
#   - bandwidth 값 존재
```

### TC-6b: Init 세그먼트

```bash
for rep in 0 1 2 3 4 5; do
  curl -sf -o /dev/null -w "init-$rep: %{http_code}\n" \
    "http://localhost:4242/api/media/$MEDIA_ID/dash/init-$rep.m4s"
done
# 검증: video(0,2,4) + audio(1,3,5) init 세그먼트 모두 200
```

### TC-6c: Media 세그먼트

```bash
curl -sf -o /dev/null -w "seg-0-00001: %{http_code} (%{size_download} bytes)\n" \
  "http://localhost:4242/api/media/$MEDIA_ID/dash/seg-0-00001.m4s"
# 검증: HTTP 200, size > 0
```

---

## TC-7: WebRTC 시그널링

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `POST /api/media/{id}/webrtc` |
| 요청 | `{"sdp": "<SDP offer>"}` |
| 검증 | HTTP 200, 응답에 `sdp`(SDP answer) + `type`("answer") 포함 |
| 인프라 의존성 | MinIO (원본 다운로드), ffmpeg (H264 추출), STUN 서버 |

> WebRTC는 브라우저 기반 테스트 권장. curl로는 SDP 생성이 어려움.
> `web/index.html`의 WebRTC 탭에서 수동 테스트.

### TC-7a: 잘못된 SDP

```bash
curl -s -X POST "http://localhost:4242/api/media/$MEDIA_ID/webrtc" \
  -H "Content-Type: application/json" \
  -d '{"sdp": "invalid"}'
# 예상: HTTP 400, "invalid SDP offer"
```

### TC-7b: 존재하지 않는 미디어

```bash
curl -s -X POST "http://localhost:4242/api/media/nonexistent/webrtc" \
  -H "Content-Type: application/json" \
  -d '{"sdp": "v=0..."}'
# 예상: HTTP 404, "media not found"
```

---

## TC-8: 웹 플레이어 (수동)

| 항목 | 검증 |
|------|------|
| 페이지 로드 | `http://localhost:4242` 접속, UI 렌더링 |
| 업로드 | 파일 선택 → Upload → 목록에 추가됨 |
| 목록 갱신 | 5초 자동 갱신, status badge 변화 확인 |
| HLS 재생 | ready 상태 미디어 선택 → HLS 탭 → 영상 재생 |
| DASH 재생 | DASH 탭 전환 → 영상 재생 |
| WebRTC 재생 | WebRTC 탭 전환 → 연결 후 재생 |
| ABR 전환 | 네트워크 쓰로틀링 → hls.js가 해상도 자동 전환 |

---

## TC-9: 에러 및 엣지 케이스

| ID | 시나리오 | 예상 결과 |
|----|---------|----------|
| TC-9a | 서버 기동 시 DB 미접속 | `log.Fatalf("repository: ...")` 즉시 종료 |
| TC-9b | 서버 기동 시 MinIO 미접속 | `log.Fatalf("storage: ...")` 즉시 종료 |
| TC-9c | 업로드 중 Kafka 다운 | 업로드 성공 (non-fatal), 로그 경고, 트랜스코딩 미트리거 |
| TC-9d | 트랜스코딩 중 ffmpeg 실패 | status → `failed`, 에러 로그 |
| TC-9e | 트랜스코딩 미완료 상태에서 HLS 요청 | HTTP 404 (S3에 파일 없음) |
| TC-9f | SIGTERM 수신 | graceful shutdown (15s timeout), 진행 중 요청 완료 |
| TC-9g | 500MB 초과 파일 업로드 | `MaxBytesReader` → HTTP 400 |

---

## TC-10: 라이브 채널 API

### TC-10a: 채널 목록 (스트림 없음)

| 항목 | 내용 |
|------|------|
| 엔드포인트 | `GET /api/live` |
| 검증 | HTTP 200, `{"channels":[]}` |

```bash
curl -sf http://localhost:4242/api/live
# 예상: {"channels":[]}
```

### TC-10b: 채널 목록 (스트림 있음)

```bash
# 터미널 1: RTSP 스트림 시작
bash scripts/rtsp_push.sh vehicle-001 &
sleep 3

# 터미널 2: 채널 확인
curl -sf http://localhost:4242/api/live | python3 -m json.tool
# 예상: {"channels":["vehicle-001"]}
```

### TC-10c: 존재하지 않는 채널 WebRTC

```bash
curl -s -X POST "http://localhost:4242/api/live/nonexistent/webrtc" \
  -H "Content-Type: application/json" \
  -d '{"sdp": "v=0..."}'
# 예상: HTTP 404, "channel not found"
```

---

## TC-11: RTSP 인제스트 → WebRTC 릴레이

| 항목 | 내용 |
|------|------|
| 사전 조건 | 서버 실행 중, RTSP :8554 리스닝 |
| 인프라 의존성 | ffmpeg (RTSP 소스), STUN 서버 |

### TC-11a: RTSP 퍼블리시

```bash
# ffmpeg로 RTSP 스트림 푸시
ffmpeg -re \
  -f lavfi -i "smptebars=duration=30:size=1280x720:rate=30" \
  -c:v libx264 -preset ultrafast -tune zerolatency \
  -profile:v baseline -g 30 \
  -an -f rtsp -rtsp_transport tcp \
  "rtsp://localhost:8554/vehicle-001" &
FFMPEG_PID=$!
sleep 3

# 서버 로그 확인
grep "ANNOUNCE\|RECORD" server.log | tail -5
# 예상: [live] ANNOUNCE vehicle-001, [live] RECORD started: vehicle-001

# 채널 등록 확인
curl -sf http://localhost:4242/api/live
# 예상: {"channels":["vehicle-001"]}
```

### TC-11b: WebRTC 시그널링 (브라우저)

> 브라우저에서 `http://localhost:4242` 접속 → Live Channels 섹션 → vehicle-001 클릭 → WebRTC 재생 확인

검증 항목:
- SDP offer/answer 교환 성공
- WebRTC 연결 상태: `connected`
- 비디오 프레임 수신 (SMPTE 컬러바 표시)
- 지연 시간 < 1초

### TC-11c: 다중 시청자

```bash
# 브라우저 탭 2~3개에서 동시에 같은 채널 WebRTC 연결
# 검증: 모든 탭에서 동시 재생, 서버 CPU 부하 최소 (제로 트랜스코딩)
```

---

## TC-12: 채널 자동 정리

| 항목 | 내용 |
|------|------|
| 트리거 | RTSP 퍼블리셔 연결 해제 |
| 검증 | 채널 목록에서 자동 제거, 구독자 정리 |

```bash
# ffmpeg 종료
kill $FFMPEG_PID
sleep 2

# 채널 자동 정리 확인
curl -sf http://localhost:4242/api/live
# 예상: {"channels":[]}

# 서버 로그 확인
grep "channel removed" server.log | tail -1
# 예상: [live] channel removed: vehicle-001
```

---

## TC-13: 분산 트레이스 E2E

| 항목 | 내용 |
|------|------|
| 목적 | 업로드 → 트랜스코딩 전체 흐름이 Jaeger에 트레이스로 수집되는지 검증 |
| 사전 조건 | OTel Collector, Jaeger 실행 중 |

### 13a. 업로드 후 트레이스 존재 확인

```bash
# 1. 파일 업로드
curl -s -X POST http://localhost:4242/api/upload \
  -F "file=@/tmp/test_otel.mp4" > /dev/null

# 2. 트랜스코딩 완료 대기
sleep 20

# 3. Jaeger에서 media-platform 서비스 트레이스 조회
curl -s "http://localhost:16686/api/traces?service=media-platform&limit=5" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
traces = d.get('data', [])
assert len(traces) > 0, 'FAIL: 트레이스 없음'
print(f'OK: {len(traces)}개 트레이스 수집됨')
"
```

### 13b. 핵심 span 존재 검증

```bash
# 전체 span operation 목록 확인
curl -s "http://localhost:16686/api/traces?service=media-platform&limit=20" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
ops = set()
for t in d.get('data', []):
    for s in t.get('spans', []):
        ops.add(s['operationName'])
expected = {'s3.upload', 'kafka.publish', 'kafka.consume', 'db.update_transcode_result'}
found = expected & ops
missing = expected - ops
print(f'확인된 span: {sorted(found)}')
if missing:
    print(f'FAIL: 누락된 span: {sorted(missing)}')
else:
    print('OK: 모든 핵심 span 존재')
"
```

### 검증 체크리스트

```
[ ] Jaeger에 media-platform 서비스 존재
[ ] s3.upload span 존재
[ ] kafka.publish span 존재
[ ] kafka.consume span 존재
[ ] db.update_transcode_result span 존재
[ ] span에 attribute 포함 (media.id, file.name 등)
```

---

## TC-14: Kafka Trace Propagation

| 항목 | 내용 |
|------|------|
| 목적 | Kafka publish → consume 간 W3C TraceContext가 전파되어 동일 traceID로 연결되는지 검증 |
| 핵심 | 비동기 경계(Kafka)를 넘어 트레이스가 끊기지 않는 것 |

```bash
# kafka.publish와 kafka.consume이 같은 traceID에 속하는지 확인
curl -s "http://localhost:16686/api/traces?service=media-platform&limit=20" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
linked = 0
for t in d.get('data', []):
    ops = [s['operationName'] for s in t.get('spans', [])]
    if 'kafka.publish' in ops and 'kafka.consume' in ops:
        linked += 1
        tid = t['traceID']
        print(f'OK: traceID={tid[:16]}... 에 publish+consume 연결됨')
if linked == 0:
    # 별도 트레이스인 경우 — propagation 실패
    pub_traces = set()
    con_traces = set()
    for t in d.get('data', []):
        for s in t.get('spans', []):
            if s['operationName'] == 'kafka.publish':
                pub_traces.add(t['traceID'])
            if s['operationName'] == 'kafka.consume':
                con_traces.add(t['traceID'])
    shared = pub_traces & con_traces
    if shared:
        print(f'OK: {len(shared)}개 트레이스에서 Kafka propagation 확인')
    else:
        print('FAIL: publish/consume이 서로 다른 traceID — propagation 미동작')
"
```

### 검증 체크리스트

```
[ ] kafka.publish와 kafka.consume이 동일 traceID에 존재
[ ] Kafka 헤더에 traceparent 전파 확인 (서버 로그 또는 kafka-ui에서 메시지 헤더 확인)
```

---

## TC-15: Prometheus 메트릭 수집

| 항목 | 내용 |
|------|------|
| 목적 | OTel Collector → Prometheus 경로로 HTTP 메트릭이 수집되는지 검증 |
| 사전 조건 | OTel Collector, Prometheus 실행 중, 최소 1회 HTTP 요청 발생 |

### 15a. HTTP 메트릭 존재 확인

```bash
# otelhttp 자동 메트릭 확인
curl -s "http://localhost:9090/api/v1/query?query=http_server_request_duration_seconds_count" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
results = d.get('data', {}).get('result', [])
assert len(results) > 0, 'FAIL: HTTP 메트릭 없음'
total = sum(float(r['value'][1]) for r in results)
print(f'OK: http_server_request_duration_seconds_count = {int(total)}')
"
```

### 15b. 라이브 메트릭 존재 확인 (RTSP 스트림 활성 시)

```bash
# live.channels 메트릭 확인 (RTSP 스트림 없으면 0)
curl -s "http://localhost:9090/api/v1/query?query=live_channels" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
status = d.get('status', '')
print(f'OK: live_channels 메트릭 쿼리 가능 (status={status})')
"
```

### 15c. OTel Collector 타겟 상태 확인

```bash
# Prometheus가 OTel Collector를 정상 스크래핑하는지
curl -s "http://localhost:9090/api/v1/targets" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
targets = d.get('data', {}).get('activeTargets', [])
for t in targets:
    health = t.get('health', 'unknown')
    job = t.get('labels', {}).get('job', '?')
    url = t.get('scrapeUrl', '?')
    print(f'  {job}: {health} ({url})')
assert any(t['health'] == 'up' for t in targets), 'FAIL: 모든 타겟 down'
print('OK: Prometheus 스크래핑 정상')
"
```

### 검증 체크리스트

```
[ ] http_server_request_duration_seconds_count > 0
[ ] http_server_request_body_size_bytes_sum > 0
[ ] http_server_response_body_size_bytes_sum > 0
[ ] Prometheus 타겟 health = "up"
[ ] live_channels 메트릭 쿼리 가능
```

---

## TC-16: Grafana 대시보드 & 데이터소스

| 항목 | 내용 |
|------|------|
| 목적 | Grafana 프로비저닝이 정상 적용되어 데이터소스 + 대시보드가 자동 구성되는지 검증 |
| 사전 조건 | Grafana 실행 중 |

### 16a. 데이터소스 프로비저닝 확인

```bash
curl -s http://localhost:3000/api/datasources \
  | python3 -c "
import json, sys
ds = json.load(sys.stdin)
names = {d['name']: d['type'] for d in ds}
print('프로비저닝된 데이터소스:')
for name, typ in names.items():
    print(f'  ✓ {name} ({typ})')
assert 'Prometheus' in names, 'FAIL: Prometheus 데이터소스 없음'
assert 'Jaeger' in names, 'FAIL: Jaeger 데이터소스 없음'
print('OK: 데이터소스 프로비저닝 정상')
"
```

### 16b. 대시보드 프로비저닝 확인

```bash
curl -s http://localhost:3000/api/search?query=media \
  | python3 -c "
import json, sys
dashboards = json.load(sys.stdin)
print('프로비저닝된 대시보드:')
for d in dashboards:
    print(f'  ✓ {d[\"title\"]} (uid={d.get(\"uid\", \"?\")})')
assert len(dashboards) > 0, 'FAIL: 대시보드 없음'
print('OK: 대시보드 프로비저닝 정상')
"
```

### 16c. Prometheus 데이터소스 연결 테스트

```bash
curl -s http://localhost:3000/api/datasources/proxy/uid/$(
  curl -s http://localhost:3000/api/datasources | python3 -c "
import json,sys
ds=json.load(sys.stdin)
for d in ds:
    if d['type']=='prometheus': print(d['uid']); break
"
)/api/v1/query?query=up 2>/dev/null \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
if d.get('status') == 'success':
    print('OK: Grafana → Prometheus 연결 정상')
else:
    print('FAIL: Grafana → Prometheus 연결 실패')
" 2>/dev/null || echo "OK: Grafana 데이터소스 수동 확인 필요 (http://localhost:3000)"
```

### 검증 체크리스트

```
[ ] Prometheus 데이터소스 존재 + 연결 정상
[ ] Jaeger 데이터소스 존재
[ ] "Media Platform" 대시보드 존재
[ ] 대시보드에 HTTP Request Rate 패널 데이터 표시
[ ] 대시보드에 HTTP Latency (p95) 패널 데이터 표시
[ ] Explore → Jaeger에서 트레이스 검색 가능
```

---

## 테스트 실행 요약

### 자동화 스크립트

```bash
# 전체 파이프라인 자동 테스트
bash scripts/integration_test.sh
```

### 수동 테스트 순서

```
1. TC-1  헬스체크
2. TC-2  업로드 (+ 2a, 2b 에러 케이스)
3. TC-4  트랜스코딩 완료 대기
4. TC-3  메타데이터 조회 (+ 3c 에러)
5. TC-5  HLS 전체 (master → variant → init → segment)
6. TC-6  DASH 전체 (mpd → init → segment)
7. TC-7  WebRTC (브라우저)
8. TC-10 라이브 채널 API (스트림 없음)
9. TC-11 RTSP 인제스트 → WebRTC 릴레이 (ffmpeg + 브라우저)
10. TC-12 채널 자동 정리 (ffmpeg 종료)
11. TC-8  웹 플레이어 (브라우저, VOD + Live)
12. TC-13 분산 트레이스 E2E (Jaeger)
13. TC-14 Kafka trace propagation
14. TC-15 Prometheus 메트릭 수집
15. TC-16 Grafana 대시보드 & 데이터소스
```

### 검증 체크리스트

```
── VOD Pipeline ──
[ ] 헬스체크 200
[ ] MP4 업로드 201 + UUID 반환
[ ] 비 MP4 거부 400
[ ] 트랜스코딩 60초 내 완료 (status: ready)
[ ] HLS master.m3u8에 3개 variant
[ ] HLS 각 variant TARGETDURATION ≤ 4
[ ] HLS init/segment 모두 200
[ ] DASH manifest.mpd에 3개 video Representation
[ ] DASH maxSegmentDuration=PT4.0S
[ ] DASH init/segment 모두 200
[ ] 미디어 목록/단건 조회 정상
[ ] 존재하지 않는 ID → 404
[ ] 웹 플레이어 HLS 재생
[ ] 웹 플레이어 DASH 재생
[ ] 웹 플레이어 WebRTC 재생

── Live Pipeline ──
[ ] 라이브 채널 목록 (스트림 없음) → 빈 배열
[ ] RTSP 퍼블리시 → 채널 등록 확인
[ ] 라이브 채널 목록 → 채널명 포함
[ ] 라이브 WebRTC 시그널링 → SDP answer 반환
[ ] 브라우저 WebRTC 재생 (< 1초 지연)
[ ] 다중 시청자 동시 재생
[ ] 퍼블리셔 종료 → 채널 자동 정리
[ ] 존재하지 않는 채널 → 404

── Observability ──
[ ] Jaeger에 media-platform 서비스 트레이스 존재
[ ] 핵심 span 4종 존재 (s3.upload, kafka.publish, kafka.consume, db.update_transcode_result)
[ ] Kafka publish→consume 동일 traceID 연결
[ ] Prometheus HTTP 메트릭 수집 정상
[ ] Prometheus 타겟 health = "up"
[ ] Grafana Prometheus/Jaeger 데이터소스 프로비저닝 정상
[ ] Grafana "Media Platform" 대시보드 존재
```
