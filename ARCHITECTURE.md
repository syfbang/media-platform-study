# Architecture

## 개요

Go 기반 미디어 스트리밍 플랫폼. 두 가지 핵심 파이프라인을 이벤트 드리븐 아키텍처로 구현:

- **VOD**: MP4 업로드 → ABR 트랜스코딩 → HLS/DASH/WebRTC 멀티 프로토콜 서빙
- **Live**: RTSP 인제스트 → H.264 제로 트랜스코딩 → WebRTC 릴레이 (초저지연)

## 시스템 구성도

```
┌─────────────────────────────────────────────────────────────────┐
│                        Client (Browser)                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌───────────────┐  │
│  │ Upload   │  │ hls.js   │  │ dash.js  │  │ WebRTC Player │  │
│  │ (MP4)    │  │ (ABR)    │  │ (ABR)    │  │ (VOD + Live)  │  │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └──────┬────────┘  │
└───────┼──────────────┼──────────────┼───────────────┼──────────┘
        │              │              │               │
        ▼              ▼              ▼               ▼
┌─────────────────────────────────────────────────────────────────┐
│                    HTTP Server (Go 1.22+)                        │
│                    net/http + otelhttp middleware                │
│                                                                  │
│  ── VOD Pipeline ──────────────────────────────────────────┐    │
│  POST /api/upload          [span: handler.upload]          │    │
│  GET  /api/media/{id}/hls  [span: handler.serveMediaFile]  │    │
│  GET  /api/media/{id}/dash [span: handler.serveMediaFile]  │    │
│  POST /api/media/{id}/webrtc                               │    │
│                                                             │    │
│  ── Live Pipeline ─────────────────────────────────────┐   │    │
│  GET  /api/live                                        │   │    │
│  POST /api/live/{channel}/webrtc                       │   │    │
│  └─────────────────────────────────────────────────────┘   │    │
│                                                             │    │
│  ┌──────────────────────────────────────────────────────┐  │    │
│  │  Kafka Consumer [span: kafka.consume]                │  │    │
│  │  ← W3C TraceContext propagation via Kafka headers    │  │    │
│  └──────────────────────┬───────────────────────────────┘  │    │
│                         ▼                                   │    │
│  ┌──────────────────────────────────────────────────────┐  │    │
│  │  Transcoder Pool [span: transcoder.process]          │  │    │
│  │  2 workers → ffmpeg ABR encode                       │  │    │
│  └──────────────────────────────────────────────────────┘  │    │
│                                                             │    │
│  ┌──────────────────────────────────────────────────────┐  │    │
│  │  OTel SDK (TracerProvider + MeterProvider)            │  │    │
│  │  ── OTLP gRPC ──────────────────────────────────────▶│──┼──┐ │
│  └──────────────────────────────────────────────────────┘  │  │ │
└──┬──────────────────┬──────────────────┬───────────────────┘  │ │
   │                  │                  │                       │ │
   ▼                  ▼                  ▼                       │ │
┌──────┐       ┌───────────┐      ┌──────────┐                 │ │
│MinIO │       │PostgreSQL │      │  Kafka   │                 │ │
│(S3)  │       │           │      │          │                 │ │
│:9000 │       │  :5432    │      │  :9092   │                 │ │
└──────┘       └───────────┘      └──────────┘                 │ │
                                                                │ │
┌───────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │              OTel Collector (:4317)                        │   │
│  │  Receiver: OTLP gRPC                                      │   │
│  │  Processors: batch (200ms/1024) + memory_limiter (512MB)  │   │
│  │  Exporters: ──┬── OTLP HTTP → Jaeger (traces)            │   │
│  │               └── Prometheus /metrics (metrics)           │   │
│  └──────┬────────────────────────────┬───────────────────────┘   │
│         │                            │                            │
│         ▼                            ▼                            │
│  ┌──────────────┐            ┌──────────────┐                    │
│  │   Jaeger      │            │  Prometheus   │                    │
│  │   :16686 (UI) │            │  :9090        │                    │
│  │   분산 트레이스│            │  메트릭 저장  │                    │
│  └──────────────┘            └──────┬───────┘                    │
│                                      │                            │
│                                      ▼                            │
│                              ┌──────────────┐                    │
│                              │   Grafana     │                    │
│                              │   :3000       │                    │
│                              │   통합 대시보드│                    │
│                              └──────────────┘                    │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                  RTSP Ingest Server (:8554)                      │
│                  gortsplib v5 — H.264 RTP fan-out               │
│                                                                  │
│  [metric: live.channels]  [metric: live.viewers]                │
│  [metric: live.packets_relayed]                                 │
│                                                                  │
│  OnAnnounce → 채널 생성    OnRecord → RTP fan-out               │
│  OnClose    → 채널 자동 정리                                     │
└─────────────────────────────────────────────────────────────────┘
                    ▲
                    │ RTSP (H.264 + AAC)
┌─────────────────────────────────────────────────────────────────┐
│              Vehicle Camera / ffmpeg Simulator                   │
└─────────────────────────────────────────────────────────────────┘
```

## 데이터 플로우

### 1. 업로드 파이프라인

```
Client                Handler              MinIO        PostgreSQL      Kafka
  │                      │                   │              │             │
  │── POST /api/upload ─▶│                   │              │             │
  │   (multipart MP4)    │                   │              │             │
  │                      │── PutObject ─────▶│              │             │
  │                      │   originals/{id}  │              │             │
  │                      │◀─ OK ─────────────│              │             │
  │                      │                   │              │             │
  │                      │── INSERT ────────────────────────▶│             │
  │                      │   status=uploaded  │              │             │
  │                      │◀─ OK ─────────────────────────────│             │
  │                      │                   │              │             │
  │                      │── Publish ────────────────────────────────────▶│
  │                      │   media.uploaded   │              │             │
  │◀─ 201 Created ───────│                   │              │             │
```

### 2. 트랜스코딩 파이프라인

```
Kafka Consumer          Transcoder Pool        MinIO           PostgreSQL
  │                         │                    │                 │
  │── ReadMessage ─────────▶│                    │                 │
  │   media.uploaded        │                    │                 │
  │                         │                    │                 │
  │── UpdateStatus ─────────────────────────────────────────────▶│
  │   status=transcoding    │                    │                 │
  │                         │                    │                 │
  │── Download(s3key) ─────────────────────────▶│                 │
  │◀─ MP4 stream ──────────────────────────────│                 │
  │                         │                    │                 │
  │── Submit(Job) ─────────▶│                    │                 │
  │                         │── ffmpeg HLS ──▶   │                 │
  │                         │   3 variants ABR   │                 │
  │                         │── ffmpeg DASH ──▶  │                 │
  │                         │   3 variants ABR   │                 │
  │                         │                    │                 │
  │◀─ Result ───────────────│                    │                 │
  │                         │                    │                 │
  │── uploadDir(hls/) ─────────────────────────▶│                 │
  │── uploadDir(dash/) ────────────────────────▶│                 │
  │                         │                    │                 │
  │── UpdateTranscodeResult ────────────────────────────────────▶│
  │   status=ready           │                    │                 │
```

### 3. 스트리밍 서빙

**VOD (파일 기반)**

| 프로토콜 | 경로 | 동작 |
|---------|------|------|
| HLS | `GET /api/media/{id}/hls/master.m3u8` | S3에서 마스터 플레이리스트 프록시 |
| HLS | `GET /api/media/{id}/hls/v{n}/playlist.m3u8` | S3에서 variant 플레이리스트 프록시 |
| HLS | `GET /api/media/{id}/hls/v{n}/seg_*.m4s` | S3에서 fMP4 세그먼트 프록시 |
| DASH | `GET /api/media/{id}/dash/manifest.mpd` | S3에서 MPD 매니페스트 프록시 |
| DASH | `GET /api/media/{id}/dash/seg-*.m4s` | S3에서 세그먼트 프록시 |
| WebRTC | `POST /api/media/{id}/webrtc` | SDP offer→answer, ffmpeg→H264 NAL→RTP |

**Live (실시간)**

| 프로토콜 | 경로 | 동작 |
|---------|------|------|
| WebRTC | `GET /api/live` | 활성 라이브 채널 목록 |
| WebRTC | `POST /api/live/{channel}/webrtc` | SDP offer→answer, RTSP RTP→WebRTC relay |

### 4. 라이브 스트리밍 파이프라인

```
Vehicle/ffmpeg          RTSP Server           LiveHandler          Browser
  │                        │                      │                   │
  │── RTSP ANNOUNCE ──────▶│                      │                   │
  │   path=/vehicle-001    │                      │                   │
  │                        │── Channel 생성        │                   │
  │                        │   subs: map[]         │                   │
  │                        │                      │                   │
  │── RTSP RECORD ────────▶│                      │                   │
  │                        │── OnPacketRTPAny     │                   │
  │                        │   H.264 필터링        │                   │
  │                        │                      │                   │
  │   ┌────────────────────│──────────────────────│───────────────────│
  │   │ 실시간 RTP 스트림   │                      │                   │
  │   │                    │                      │                   │
  │   │                    │                      │◀─ POST /webrtc ───│
  │   │                    │                      │   SDP offer       │
  │   │                    │                      │                   │
  │   │                    │◀─ Subscribe() ───────│                   │
  │   │                    │   ch ← rtp.Packet    │                   │
  │   │                    │                      │                   │
  │   │                    │                      │── SDP answer ────▶│
  │   │                    │                      │                   │
  │   │  H.264 RTP ───────▶│── fan-out ──────────▶│── WriteRTP() ───▶│
  │   │  (30fps)           │   (zero-copy relay)  │   (WebRTC track) │
  │   │                    │                      │                   │
  │   └────────────────────│──────────────────────│───────────────────│
  │                        │                      │                   │
  │── RTSP TEARDOWN ──────▶│                      │                   │
  │   (or disconnect)      │── Channel 삭제        │                   │
  │                        │   subscribers 정리    │                   │
```

핵심 특성:
- **제로 트랜스코딩**: H.264 RTP 패킷을 그대로 WebRTC로 릴레이 (CPU 부하 최소)
- **Fan-out**: 하나의 RTSP 소스에 다수 WebRTC 시청자 동시 연결
- **자동 정리**: 퍼블리셔 연결 해제 시 채널 + 구독자 자동 정리
- **Slow consumer 보호**: 128 패킷 버퍼, 초과 시 drop (실시간 우선)

### 5. Observability 데이터 플로우

#### Trace 흐름 — 요청 단위 분산 추적

```
HTTP POST /api/upload
  │
  ▼
┌─ handler.upload (span) ──────────────────────────────────────────┐
│  attributes: file.name, file.size                                │
│                                                                   │
│  ├─ s3.upload (child span) ──── MinIO PutObject                  │
│  │  attributes: s3.key, s3.size                                  │
│  │                                                                │
│  ├─ db.create_media (child span) ──── PostgreSQL INSERT          │
│  │  attributes: media.id, media.status                           │
│  │                                                                │
│  └─ kafka.publish (child span) ──── Kafka Produce                │
│     attributes: kafka.topic                                      │
│     ┌──────────────────────────────────────────────────────┐     │
│     │ W3C TraceContext injection into Kafka message headers │     │
│     │ traceparent: 00-{traceID}-{spanID}-01                │     │
│     └──────────────────────┬───────────────────────────────┘     │
└─────────────────────────────┼────────────────────────────────────┘
                              │
                    ══════════╪══════════  비동기 경계 (Kafka)
                              │
                              ▼
┌─ kafka.consume (span, linked by traceID) ────────────────────────┐
│  W3C TraceContext extraction from Kafka headers                   │
│  → 동일 traceID로 span 연결                                      │
│                                                                   │
│  └─ transcoder.process (child span)                              │
│     attributes: media.id                                         │
│     ├─ ffmpeg HLS 트랜스코딩                                     │
│     ├─ ffmpeg DASH 트랜스코딩                                    │
│     ├─ s3.upload × N (세그먼트 업로드)                           │
│     └─ db.update_transcode_result (child span)                   │
│        attributes: media.id, media.status                        │
└──────────────────────────────────────────────────────────────────┘
```

핵심: Kafka 메시지 헤더에 `traceparent`를 주입/추출하는 `kafkaHeaderCarrier` 패턴으로 비동기 경계를 넘어 하나의 트레이스로 연결.

#### Metric 흐름 — 실시간 시스템 상태

```
Go Application
  │
  ├─ otelhttp middleware (자동)
  │    http_server_request_duration_seconds  [Histogram]
  │    http_server_request_body_size_bytes   [Histogram]
  │    http_server_response_body_size_bytes  [Histogram]
  │
  ├─ RTSP Ingest (커스텀)
  │    live_channels          [UpDownCounter]  활성 채널 수
  │    live_viewers           [UpDownCounter]  WebRTC 시청자 수
  │    live_packets_relayed   [Counter]        릴레이 RTP 패킷 총 수
  │
  └── OTLP gRPC (:4317) ──▶ OTel Collector
                                │
                                ├── /metrics (:8889) ──▶ Prometheus scrape
                                │                           │
                                │                           ▼
                                │                       Grafana 대시보드
                                │
                                └── OTLP HTTP (:4318) ──▶ Jaeger
                                                            │
                                                            ▼
                                                        Grafana Explore
```

#### 계측 포인트 매핑

| 컴포넌트 | 파일 | Trace (Span) | Metrics |
|---------|------|-------------|---------|
| HTTP 서버 | `main.go` | `otelhttp.NewHandler` (자동) | request duration, body size (자동) |
| 업로드 핸들러 | `handler.go` | `handler.upload` | — |
| 미디어 서빙 | `handler.go` | `handler.serveMediaFile` | — |
| S3 스토리지 | `minio.go` | `s3.upload`, `s3.download` | — |
| PostgreSQL | `postgres.go` | `db.create_media`, `db.update_status`, `db.update_transcode_result` | — |
| Kafka | `kafka.go` | `kafka.publish`, `kafka.consume` + context propagation | — |
| 트랜스코더 | `transcoder.go` | `transcoder.process` | — |
| RTSP 인제스트 | `ingest.go` | — | `live.channels`, `live.viewers`, `live.packets_relayed` |


## 컴포넌트 상세

### config (`internal/config/config.go`)

환경변수 기반 설정 로딩. 모든 값에 기본값이 있어 `.env` 없이도 동작 가능. 시작 시 필수 값 검증(`validate()`).

| 설정 그룹 | 주요 필드 | 기본값 |
|-----------|----------|--------|
| App | `APP_PORT` | `4242` |
| PostgreSQL | Host/Port/User/Password/DB/SSLMode | `localhost:5432`, `disable` |
| MinIO | Endpoint/AccessKey/SecretKey/Bucket | `localhost:9000`, `media-files` |
| Kafka | Brokers/Topic | `localhost:9092`, `media-events` |
| Redis | Addr/Password | `localhost:6379` |

### storage (`internal/storage/minio.go`)

`Storage` 인터페이스로 추상화. 현재 MinIO 구현체.

```go
type Storage interface {
    Upload(ctx, key, reader, size, contentType) error
    Download(ctx, key) (io.ReadCloser, error)
    PresignedURL(ctx, key, expires) (*url.URL, error)
    Delete(ctx, key) error
}
```

- 초기화 시 버킷 존재 확인 → 없으면 자동 생성
- 연결 타임아웃: 10초
- Content-Type 매핑: `.m3u8`→`application/vnd.apple.mpegurl`, `.mpd`→`application/dash+xml`, `.m4s/.mp4`→`video/iso.segment`

### messaging (`internal/messaging/kafka.go`)

| 컴포넌트 | 역할 | 설정 |
|---------|------|------|
| Producer | 이벤트 발행 | `LeastBytes` 밸런서, `RequireOne` ACK, 10ms batch |
| Consumer | 이벤트 소비 | consumer group, `FirstOffset`, 1s commit interval |

이벤트 타입:
- `media.uploaded` — 업로드 완료 시 발행, 트랜스코딩 트리거
- `media.transcode.completed` — 트랜스코딩 완료 시 발행

### repository (`internal/repository/postgres.go`)

`media` 테이블 단일 엔티티. 시작 시 `CREATE TABLE IF NOT EXISTS`로 자동 마이그레이션.

```sql
media (
    id           TEXT PRIMARY KEY,      -- UUID
    filename     TEXT NOT NULL,
    s3_key       TEXT NOT NULL,         -- originals/{id}.mp4
    content_type TEXT DEFAULT '',
    size         BIGINT DEFAULT 0,
    status       TEXT DEFAULT 'uploaded', -- uploaded → transcoding → ready | failed
    hls_key      TEXT DEFAULT '',        -- transcoded/{id}/hls/master.m3u8
    dash_key     TEXT DEFAULT '',        -- transcoded/{id}/dash/manifest.mpd
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
)
```

커넥션 풀: `MaxOpenConns=25`, `MaxIdleConns=5`, `ConnMaxLifetime=5m`

### transcoder (`internal/media/transcoder.go`)

Worker Pool 패턴. `Job` 채널(buf:100)로 작업 수신, `Result` 채널(buf:100)로 결과 반환.

```
Submit(Job) ──▶ [jobs chan] ──▶ worker-0 ──▶ [results chan] ──▶ processTranscodeResults()
                               worker-1
```

#### ABR 변환 스펙

| Variant | 해상도 | 비디오 비트레이트 | 오디오 비트레이트 | maxrate | bufsize |
|---------|--------|-----------------|-----------------|---------|---------|
| v0 | 1280×720 | 2500kbps | 128kbps | 2500k | 5000k |
| v1 | 854×480 | 1200kbps | 128kbps | 1200k | 2400k |
| v2 | 640×360 | 600kbps | 96kbps | 600k | 1200k |

공통 설정:
- 코덱: H.264 (libx264) + AAC
- 프리셋: `fast`
- 키프레임: `force_key_frames "expr:gte(t,n_forced*4)"` (4초 간격)
- 필터: `filter_complex` — `split=3` → 3개 `scale` 필터

#### HLS 출력

```
transcoded/{id}/hls/
├── master.m3u8              ← #EXT-X-STREAM-INF × 3 variants
├── v0/playlist.m3u8         ← 720p, TARGETDURATION=4
├── v0/init_0.mp4            ← fMP4 init segment
├── v0/seg_000.m4s ...       ← fMP4 media segments
├── v1/playlist.m3u8         ← 480p
├── v1/init_1.mp4, seg_*.m4s
├── v2/playlist.m3u8         ← 360p
└── v2/init_2.mp4, seg_*.m4s
```

- `hls_segment_type fmp4` — fragmented MP4 (CMAF 호환)
- `hls_playlist_type vod`
- `var_stream_map "v:0,a:0 v:1,a:1 v:2,a:2"`

#### DASH 출력

```
transcoded/{id}/dash/
├── manifest.mpd             ← AdaptationSet: video(3 Rep) + audio(3 Rep)
├── init-{RepID}.m4s         ← 각 Representation별 init segment
└── seg-{RepID}-{Num}.m4s   ← media segments
```

- `seg_duration 4`
- `adaptation_sets "id=0,streams=v id=1,streams=a"`

### handler (`internal/handler/handler.go`)

Go 1.22+ 패턴 기반 라우팅. 7개 엔드포인트.

| 메서드 | 경로 | 핸들러 | 설명 |
|--------|------|--------|------|
| GET | `/health` | `health()` | `{"status":"ok"}` |
| POST | `/api/upload` | `upload()` | MP4 업로드 (max 500MB, `.mp4` only) |
| GET | `/api/media` | `listMedia()` | 목록 (limit/offset, default 20) |
| GET | `/api/media/{id}` | `getMedia()` | 단건 조회 |
| GET | `/api/media/{id}/hls/{file...}` | `serveHLS()` | S3 프록시 |
| GET | `/api/media/{id}/dash/{file...}` | `serveDASH()` | S3 프록시 |
| POST | `/api/media/{id}/webrtc` | `webrtcSignal()` | SDP offer/answer |

### webrtc (`internal/handler/webrtc.go`)

1. 클라이언트 SDP offer 수신
2. 원본 MP4를 S3에서 다운로드 → 임시 파일
3. `pion/webrtc v4`로 PeerConnection 생성 (STUN: `stun.l.google.com:19302`)
4. H264 video track 추가, ICE gathering 완료 대기 (10s timeout)
5. SDP answer 반환
6. 백그라운드: `ffmpeg -re` → H264 Annex B (`h264_mp4toannexb`) → stdout pipe
7. NAL unit 파싱 (start code 0x00000001/0x000001 감지) → `WriteSample()` (33ms/frame, ~30fps)

### live ingest (`internal/live/ingest.go`)

RTSP 인제스트 서버. `gortsplib v5`로 RTSP 프로토콜 처리, 채널 레지스트리로 라이브 스트림 관리.

```go
type Server struct {
    rtsp     *gortsplib.Server    // RTSP 프로토콜 핸들링
    channels map[string]*Channel  // path → 라이브 채널
    streams  map[string]*ServerStream
    pubs     map[*ServerSession]string
}

type Channel struct {
    Path string
    subs map[uint64]func(*rtp.Packet)  // WebRTC 구독자 콜백
}
```

RTSP 상태 머신:
1. `OnAnnounce` — 퍼블리셔가 스트림 등록, Channel 생성
2. `OnRecord` — 스트리밍 시작, `OnPacketRTPAny`로 H.264 RTP 패킷 인터셉트
3. `OnSessionClose` — 퍼블리셔 연결 해제, Channel + Stream 자동 정리

Fan-out 메커니즘:
- `Subscribe()` → `(chan *rtp.Packet, unsubFunc)` 반환
- 128 패킷 버퍼, 느린 소비자는 패킷 drop (실시간 우선)
- 구독자 ID 기반 관리, 개별 해제 가능

### live handler (`internal/handler/live.go`)

라이브 스트리밍 HTTP API. RTSP 인제스트 서버의 채널을 WebRTC로 브라우저에 릴레이.

| 메서드 | 경로 | 설명 |
|--------|------|------|
| GET | `/api/live` | 활성 라이브 채널 목록 |
| POST | `/api/live/{channel}/webrtc` | 라이브 WebRTC 시그널링 |

WebRTC 릴레이 흐름:
1. `channel.Subscribe()` → RTP 패킷 수신 채널 획득
2. `TrackLocalStaticRTP` 생성 (H.264 codec)
3. SDP offer/answer 교환, ICE gathering
4. goroutine에서 `range rtpCh` → `track.WriteRTP(pkt)` (제로 카피 릴레이)
5. PeerConnection 종료 시 자동 unsubscribe

## 인프라

### Docker Compose

| 서비스 | 이미지 | 포트 | 역할 |
|--------|--------|------|------|
| minio | minio/minio | :9000 (API), :9001 (Console) | S3 호환 오브젝트 스토리지 |
| redis | redis:7-alpine | :6379 | 캐시 (확장용) |
| otel-collector | otel/opentelemetry-collector-contrib:0.96.0 | :4317 (OTLP gRPC) | 텔레메트리 수집/라우팅 |
| jaeger | jaegertracing/all-in-one:1.54 | :16686 (UI) | 분산 트레이스 |
| prometheus | prom/prometheus:v2.50.0 | :9090 | 메트릭 저장/쿼리 |
| grafana | grafana/grafana:10.3.1 | :3000 | 통합 대시보드 |

PostgreSQL(:5432), Kafka(:9092)는 기존 인프라 재사용.

서비스 의존성 체인:
```
jaeger → otel-collector → prometheus → grafana
```

### Dockerfile (Multi-stage)

```
Stage 1: golang:1.23-alpine → go build -ldflags="-s -w"
Stage 2: alpine:3.19 + ffmpeg + ca-certificates → /server
```

최종 이미지에 포함: 서버 바이너리 + ffmpeg + web/ 디렉토리

### telemetry (`internal/telemetry/telemetry.go`)

OpenTelemetry SDK 초기화. 애플리케이션의 모든 계측 데이터를 OTLP gRPC로 Collector에 전송.

```go
// 패키지 레벨 변수 — 각 컴포넌트에서 import하여 사용
var Tracer trace.Tracer   // span 생성용
var Meter  metric.Meter   // 메트릭 생성용
```

**TracerProvider 구성:**
- Exporter: OTLP gRPC (`OTEL_EXPORTER_OTLP_ENDPOINT`, 기본 `localhost:4317`)
- Resource: `service.name=media-platform`, `service.version=0.1.0`
- Propagator: W3C TraceContext (Kafka 헤더 전파에 사용)

**MeterProvider 구성:**
- Exporter: OTLP gRPC (동일 Collector 엔드포인트)
- Reader: PeriodicReader (기본 30초 간격)

**Graceful Shutdown:**
- `Init()` → `shutdownFunc` 반환
- `main.go`에서 `defer otelShutdown(ctx)` — 버퍼된 span/metric flush 보장

**Kafka Trace Propagation 패턴:**

```go
// kafkaHeaderCarrier — propagation.TextMapCarrier 구현
// Kafka 메시지 헤더를 W3C TraceContext 전파 매체로 사용
type kafkaHeaderCarrier struct{ headers *[]kafka.Header }

func (c kafkaHeaderCarrier) Get(key string) string    // 헤더에서 traceparent 추출
func (c kafkaHeaderCarrier) Set(key, val string)       // 헤더에 traceparent 주입
func (c kafkaHeaderCarrier) Keys() []string

// Producer: span context를 Kafka 헤더에 주입
otel.GetTextMapPropagator().Inject(ctx, kafkaHeaderCarrier{&msg.Headers})

// Consumer: Kafka 헤더에서 span context 추출
ctx = otel.GetTextMapPropagator().Extract(ctx, kafkaHeaderCarrier{&msg.Headers})
```

## Observability 인프라

### OTel Collector (`otel-collector-config.yaml`)

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317    # Go 앱에서 OTLP gRPC 수신

processors:
  batch:
    timeout: 200ms                 # 200ms마다 배치 전송
    send_batch_size: 1024          # 또는 1024개 모이면 즉시
  memory_limiter:
    limit_mib: 512                 # OOM 방지

exporters:
  otlphttp:
    endpoint: http://jaeger:4318   # Jaeger OTLP HTTP (traces)
  prometheus:
    endpoint: 0.0.0.0:8889        # Prometheus scrape 엔드포인트 (metrics)
```

파이프라인:
- `traces`: OTLP → batch → memory_limiter → Jaeger (OTLP HTTP)
- `metrics`: OTLP → batch → memory_limiter → Prometheus exporter

### Jaeger (`jaegertracing/all-in-one:1.54`)

분산 트레이스 저장 + 검색 + 시각화.

| 포트 | 프로토콜 | 용도 |
|------|---------|------|
| 4317 | OTLP gRPC | Collector에서 트레이스 수신 (내부) |
| 4318 | OTLP HTTP | Collector에서 트레이스 수신 (사용 중) |
| 16686 | HTTP | Jaeger UI (트레이스 검색/시각화) |

- 스토리지: in-memory (데모용, 프로덕션에서는 Elasticsearch/Cassandra)
- 서비스 의존성 그래프 자동 생성
- span 검색: 서비스명, operation, 태그, 시간 범위

### Prometheus (`prom/prometheus:v2.50.0`)

메트릭 수집 + 저장 + 쿼리.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: otel-collector
    scrape_interval: 15s
    static_configs:
      - targets: ['otel-collector:8889']   # Collector의 Prometheus exporter
```

- Pull 모델: 15초 간격으로 Collector의 `/metrics` 엔드포인트 스크래핑
- TSDB: 로컬 시계열 데이터베이스 (15일 기본 보존)
- PromQL: `rate()`, `histogram_quantile()` 등으로 메트릭 분석

### Grafana (`grafana/grafana:10.3.1`)

통합 대시보드 + 데이터 탐색.

**자동 프로비저닝:**
```
grafana/
├── provisioning/
│   ├── datasources/datasources.yml   # Prometheus + Jaeger 자동 등록
│   └── dashboards/dashboards.yml     # 대시보드 파일 경로 설정
└── dashboards/
    └── media-platform.json           # Media Platform 대시보드
```

**프로비저닝된 데이터소스:**
| 이름 | 타입 | URL | 용도 |
|------|------|-----|------|
| Prometheus | prometheus | `http://prometheus:9090` | 메트릭 쿼리 (기본 데이터소스) |
| Jaeger | jaeger | `http://jaeger:16686` | 트레이스 검색 (Explore) |

**대시보드 패널:**
| 패널 | PromQL | 설명 |
|------|--------|------|
| HTTP Request Rate | `rate(http_server_request_duration_seconds_count[1m])` | 초당 요청 수 (메서드/경로별) |
| HTTP Latency (p95) | `histogram_quantile(0.95, rate(http_server_request_duration_seconds_bucket[1m]))` | 95퍼센타일 응답시간 |
| Total Requests | `sum(http_server_request_duration_seconds_count)` | 누적 요청 수 |
| Upload Bytes Total | `sum(http_server_request_body_size_bytes_sum)` | 업로드 총 바이트 |
| Live Channels | `live_channels` | 현재 활성 라이브 채널 수 |
| Live Viewers | `live_viewers` | 현재 WebRTC 시청자 수 |

**설정:**
- `GF_AUTH_ANONYMOUS_ENABLED=true` + `GF_AUTH_ANONYMOUS_ORG_ROLE=Admin` — 로그인 없이 접근 (데모용)

## 설계 결정

| 결정 | 이유 | 트레이드오프 |
|------|------|-------------|
| 이벤트 드리븐 (Kafka) | 업로드와 트랜스코딩 분리, 재시도 가능 | 인프라 복잡도 증가 |
| Worker Pool | 동시 트랜스코딩 수 제어, CPU 보호 | 큐 대기 시간 발생 |
| S3 프록시 서빙 | 단일 엔드포인트, 인증 통합 가능 | CDN 대비 지연 |
| fMP4 (CMAF) | HLS/DASH 세그먼트 포맷 통일 가능 | TS 대비 호환성 약간 낮음 |
| force_key_frames | 정확한 세그먼트 분할 보장 | 인코딩 효율 미세 감소 |
| Storage 인터페이스 | MinIO→AWS S3 교체 용이 | 추상화 레이어 오버헤드 |
| Auto-migration | 별도 마이그레이션 도구 불필요 | 프로덕션에서는 versioned migration 권장 |
| RTSP 인제스트 (gortsplib) | 차량 IP 카메라 표준 프로토콜, 업계 호환성 | UDP fallback 미지원 (TCP only) |
| 제로 트랜스코딩 릴레이 | CPU 부하 최소, 초저지연 (~200ms) | 코덱 변환 불가 (H.264 only) |
| Fan-out + Drop 정책 | 실시간성 보장, 느린 시청자가 전체에 영향 안 줌 | 패킷 손실 시 일시적 화면 깨짐 |
| OTel Collector 경유 | 벤더 중립, 배치 처리, 백엔드 교체 시 앱 코드 무변경 | 추가 인프라 컴포넌트 |
| Kafka trace propagation | 비동기 경계 넘어 E2E 트레이스 연결 | 헤더 오버헤드 미미 |
| otelhttp 미들웨어 | HTTP 메트릭 자동 수집 (RED metrics), 수동 계측 최소화 | 세밀한 커스텀 메트릭은 별도 추가 필요 |
| Grafana 프로비저닝 | 코드로 데이터소스/대시보드 관리, 재현 가능한 환경 | JSON 대시보드 수동 편집 번거로움 |
| Prometheus pull 모델 | Collector가 메트릭 노출, Prometheus가 스크래핑 — 표준 패턴 | push 대비 실시간성 약간 낮음 (15s 간격) |
| 커스텀 메트릭 (live.*) | 라이브 스트리밍 특화 지표, 비즈니스 메트릭 | 메트릭 cardinality 관리 필요 |

## 확장 포인트

- **CDN 연동**: S3 프록시 → PresignedURL 또는 CloudFront 리다이렉트
- **GPU 트랜스코딩**: ffmpeg `-hwaccel cuda` + NVENC
- **Redis 캐시**: 메타데이터/플레이리스트 캐싱 (현재 미사용)
- **DRM**: Widevine/FairPlay 키 서버 연동
- **Thumbnail 생성**: 트랜스코딩 파이프라인에 스크린샷 추출 추가
- **모니터링**: OpenTelemetry traces + Prometheus metrics → **구현 완료** (OTel Collector + Jaeger + Prometheus + Grafana)
- **라이브 녹화**: RTSP 인제스트 스트림을 S3에 동시 저장 (DVR)
- **LL-HLS 변환**: 라이브 RTSP → Low-Latency HLS 서빙 (대규모 시청자)
- **SRT 인제스트**: 불안정 네트워크(LTE/5G) 대응, 패킷 손실 복구
