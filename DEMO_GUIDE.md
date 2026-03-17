# Media Platform — 브라우저 데모 가이드

> 이 문서는 로컬 환경에서 전체 서비스를 브라우저로 테스트하는 방법을 안내한다.
> VOD 파이프라인, 라이브 스트리밍, Observability 스택을 순서대로 검증한다.

---

## 사전 준비

### 인프라 기동

```bash
docker compose up -d
```

6개 서비스가 올라온다:

| 서비스 | 포트 | 용도 |
|--------|------|------|
| MinIO | :9000 / :9001 (콘솔) | 오브젝트 스토리지 |
| Redis | :6379 | 캐시 |
| OTel Collector | :4317 | 텔레메트리 수집 |
| Jaeger | :16686 | 분산 트레이스 UI |
| Prometheus | :9090 | 메트릭 수집/쿼리 |
| Grafana | :3000 | 대시보드 |

### 서버 실행

```bash
# 빌드
go build -o server ./cmd/server/

# .env 로드 후 실행
set -a; source .env; set +a
./server
```

서버가 `:4242` (HTTP) + `:8554` (RTSP)에서 리스닝한다.

### 테스트 MP4 생성 (없는 경우)

```bash
ffmpeg -y -f lavfi -i testsrc=duration=5:size=640x360:rate=30 \
  -f lavfi -i sine=frequency=440:duration=5 \
  -c:v libx264 -c:a aac -shortest /tmp/test.mp4
```

---

## 서비스 URL 맵

| URL | 서비스 | 로그인 |
|-----|--------|--------|
| http://localhost:4242 | 웹 플레이어 (메인) | 없음 |
| http://localhost:16686 | Jaeger (트레이스) | 없음 |
| http://localhost:9090 | Prometheus (메트릭) | 없음 |
| http://localhost:3000 | Grafana (대시보드) | admin / admin |
| http://localhost:9001 | MinIO Console (스토리지) | minioadmin / minioadmin |

---

## 1단계: VOD 파이프라인

### 1-1. MP4 업로드

1. **http://localhost:4242** 접속
2. 파일 선택 → 업로드 버튼 클릭
3. 업로드 완료 메시지 확인

내부 동작:
```
브라우저 → POST /api/upload → MinIO 저장 → Kafka 이벤트 발행
                                              → Consumer가 수신
                                              → ffmpeg ABR 트랜스코딩
                                              → 720p/480p/360p HLS + DASH 생성
                                              → MinIO에 결과 저장
                                              → PostgreSQL 상태 업데이트
```

### 1-2. 트랜스코딩 대기

- 미디어 목록에서 상태 변화 관찰: `pending` → `processing` → `completed`
- 5초짜리 영상 기준 약 10~20초 소요

### 1-3. HLS 재생

- 트랜스코딩 완료 후 미디어 항목 클릭
- **HLS** 탭 선택 → 재생
- ABR(Adaptive Bitrate): 네트워크 상태에 따라 720p/480p/360p 자동 전환

### 1-4. DASH 재생

- **DASH** 탭 선택 → 재생
- HLS와 동일한 ABR 동작, MPEG-DASH 프로토콜 사용

### 1-5. WebRTC 재생 (VOD)

- **WebRTC** 탭 선택 → 재생
- 서버에서 직접 RTP로 미디어 전송 → 초저지연
- SDP Offer/Answer 시그널링이 자동으로 수행됨

---

## 2단계: 라이브 스트리밍

### 2-1. RTSP 소스 시작

터미널에서:
```bash
bash scripts/rtsp_push.sh vehicle-001
```

서버 로그에 `[live] ANNOUNCE vehicle-001` 출력 확인.

### 2-2. 브라우저에서 라이브 시청

1. **http://localhost:4242** 의 **Live Channels** 섹션
2. `vehicle-001` 채널 표시됨
3. 클릭 → WebRTC로 실시간 재생 (제로 트랜스코딩, H.264 RTP 릴레이)

### 2-3. 다중 채널 테스트

```bash
# 터미널 2
bash scripts/rtsp_push.sh vehicle-002

# 터미널 3
bash scripts/rtsp_push.sh vehicle-003
```

Live Channels에 여러 채널이 동시에 표시된다.

---

## 3단계: Observability

### 3-1. Jaeger — 분산 트레이스

**http://localhost:16686**

1. **Service** 드롭다운 → `media-platform` 선택
2. **Find Traces** 클릭
3. 업로드 시 생성된 트레이스 목록이 표시됨

트레이스 클릭 시 span 체인 확인:

```
[HTTP] POST /api/upload
  ├── s3.upload          (MinIO에 원본 저장)
  ├── kafka.publish      (트랜스코딩 이벤트 발행)
  │
  ... Kafka 비동기 경계 (동일 traceID로 연결) ...
  │
  ├── kafka.consume      (이벤트 수신)
  │   └── transcoder.process  (ffmpeg 트랜스코딩)
  └── db.update_transcode_result  (상태 업데이트)
```

확인 포인트:
- 각 span의 소요시간 (duration)
- span attributes: `media.id`, `s3.bucket`, `kafka.topic` 등
- 에러 발생 시 span status가 `ERROR`로 표시

### 3-2. Prometheus — 메트릭 쿼리

**http://localhost:9090**

상단 Expression 입력창에 다음 쿼리를 입력하고 Execute:

| 쿼리 | 의미 |
|------|------|
| `rate(http_server_request_duration_seconds_count[5m])` | 초당 HTTP 요청 수 |
| `histogram_quantile(0.95, rate(http_server_request_duration_seconds_bucket[5m]))` | P95 응답시간 |
| `http_server_request_body_size_bytes_sum` | 업로드된 총 바이트 |
| `live_channels` | 현재 활성 라이브 채널 수 |
| `live_viewers` | 현재 라이브 시청자 수 |

**Graph** 탭으로 전환하면 시계열 그래프로 볼 수 있다.

팁:
- 업로드를 여러 번 하면서 요청 수 변화 관찰
- 라이브 채널 시작/종료하면서 `live_channels` 변화 관찰

### 3-3. Grafana — 대시보드

**http://localhost:3000** (로그인: `admin` / `admin`, 비밀번호 변경은 Skip)

1. 왼쪽 메뉴 → **Dashboards**
2. **Media Platform** 대시보드 선택

6개 패널:

| 패널 | 내용 | 데이터 변화 트리거 |
|------|------|-------------------|
| HTTP Request Rate | 초당 요청 수 (method별) | API 호출 |
| HTTP Latency P95 | 95번째 백분위 응답시간 | API 호출 |
| Total Requests | 누적 요청 수 | API 호출 |
| Upload Bytes | 업로드된 총 바이트 | 파일 업로드 |
| Live Channels | 활성 라이브 채널 수 | RTSP 푸시 시작/종료 |
| Live Viewers | 라이브 시청자 수 | 브라우저에서 라이브 재생 |

데이터소스 확인:
- 왼쪽 메뉴 → **Connections** → **Data sources**
- Prometheus, Jaeger 두 개가 자동 프로비저닝되어 있음

### 3-4. MinIO Console — 스토리지

**http://localhost:9001** (로그인: `minioadmin` / `minioadmin`)

1. **Object Browser** → `media` 버킷 선택
2. 업로드된 파일 구조 확인:

```
media/
├── originals/
│   └── {media-id}.mp4          ← 원본
├── hls/
│   └── {media-id}/
│       ├── master.m3u8         ← ABR 마스터 플레이리스트
│       ├── 720p/               ← 720p 세그먼트
│       ├── 480p/               ← 480p 세그먼트
│       └── 360p/               ← 360p 세그먼트
└── dash/
    └── {media-id}/
        ├── manifest.mpd        ← DASH 매니페스트
        └── ...                 ← fMP4 세그먼트
```

---

## 추천 데모 시나리오

전체 파이프라인을 한 번에 보여주는 순서:

```
① localhost:4242 접속 → MP4 업로드
② localhost:16686 (Jaeger) → 방금 업로드의 트레이스 확인
③ localhost:4242 → 트랜스코딩 완료 후 HLS 재생
④ localhost:4242 → DASH 탭 전환 재생
⑤ localhost:4242 → WebRTC 탭 전환 재생
⑥ localhost:9090 (Prometheus) → rate 쿼리로 요청 메트릭 확인
⑦ localhost:3000 (Grafana) → 대시보드에서 패널 변화 관찰
⑧ 터미널에서 rtsp_push.sh vehicle-001 실행
⑨ localhost:4242 → Live Channels에서 WebRTC 실시간 재생
⑩ Grafana → live_channels, live_viewers 패널 변화 확인
⑪ localhost:9001 (MinIO) → 버킷에 저장된 파일 구조 확인
```

---

## 트러블슈팅

| 증상 | 원인 | 해결 |
|------|------|------|
| 업로드 실패 (500) | MinIO 인증 오류 | `set -a; source .env; set +a` 후 서버 재시작 |
| 트랜스코딩 안 됨 | Kafka 미연결 | `docker compose ps`로 Kafka 상태 확인 |
| HLS 재생 안 됨 | 트랜스코딩 미완료 | 미디어 상태가 `completed`인지 확인 |
| Jaeger에 트레이스 없음 | OTel Collector 미실행 | `docker compose up -d otel-collector` |
| Grafana 대시보드 비어있음 | 데이터 없음 | 업로드/재생 후 1~2분 대기 (scrape interval) |
| 라이브 채널 안 보임 | RTSP 푸시 미실행 | `bash scripts/rtsp_push.sh vehicle-001` 실행 |
| WebRTC 재생 안 됨 | 브라우저 호환성 | Chrome/Edge 최신 버전 사용 |
