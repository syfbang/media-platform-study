# Media Service Platform

Go 기반 미디어 스트리밍 플랫폼 데모. VOD(MP4 업로드 → 트랜스코딩 → HLS/DASH/WebRTC) + Live(RTSP 인제스트 → WebRTC 릴레이) 멀티 프로토콜 서빙.

## 아키텍처

```
Client (Browser)
  │
  ▼
HTTP Server (Go)
  ├── POST /api/upload      → MinIO(S3) 저장 + Kafka 이벤트 발행
  ├── GET  /api/media        → PostgreSQL 메타데이터 조회
  ├── GET  /api/media/{id}/hls/*   → HLS(fMP4) 스트리밍
  ├── GET  /api/media/{id}/dash/*  → DASH(fMP4) 스트리밍
  ├── POST /api/media/{id}/webrtc  → WebRTC 시그널링 (VOD)
  ├── GET  /api/live               → 라이브 채널 목록
  └── POST /api/live/{ch}/webrtc   → WebRTC 시그널링 (Live)
        │                                    ▲
        ▼                                    │ H.264 RTP relay
  ┌─────────────────────────────────────┐    │
  │  Kafka Consumer → ffmpeg Worker Pool │    │
  │  MP4 → HLS (m3u8+fMP4)             │    │
  │  MP4 → DASH (mpd+fMP4)             │    │
  └─────────────────────────────────────┘    │
        │                                    │
        ▼                              ┌─────┴──────────┐
  MinIO (S3) ← 원본 + 트랜스코딩 결과  │ RTSP Ingest    │
                                       │ Server (:8554) │
                                       └─────▲──────────┘
                                             │ RTSP (H.264)
                                       Vehicle Camera / ffmpeg
```

## 기술 스택

| 구분 | 기술 |
|------|------|
| 언어 | Go 1.23+ |
| 스토리지 | MinIO (S3 호환) |
| DB | PostgreSQL 16 |
| 메시징 | Apache Kafka (Confluent 7.6) |
| 캐시 | Redis 7 |
| 트랜스코딩 | ffmpeg (MP4→HLS/DASH) |
| WebRTC | pion/webrtc v4 |
| RTSP | gortsplib v5 (인제스트 서버) |
| 컨테이너 | Docker + Docker Compose |

### Observability

| 구분 | 기술 |
|------|------|
| 계측 | OpenTelemetry SDK (Go) |
| 수집 | OTel Collector |
| 트레이스 | Jaeger |
| 메트릭 | Prometheus |
| 대시보드 | Grafana |

## 빠른 시작

### 사전 요구사항

- Docker & Docker Compose
- Go 1.22+ (로컬 개발 시)
- ffmpeg (트랜스코딩 + 샘플 생성)

### 1. 인프라 기동

```bash
docker compose up -d
```

PostgreSQL, MinIO, Kafka, Redis가 올라옵니다.

### 2. 서버 실행

```bash
# 로컬 개발
go run ./cmd/server/

# 또는 빌드 후 실행
go build -o server ./cmd/server/
./server
```

서버: http://localhost:4242

### 3. 테스트 MP4 생성

```bash
bash scripts/generate_sample.sh
```

10초짜리 SMPTE 컬러바 + 440Hz 톤 MP4 생성.

### 4. 통합 테스트

```bash
bash scripts/integration_test.sh
```

업로드 → 트랜스코딩 대기 → HLS/DASH 엔드포인트 검증까지 자동 수행.

### 5. 라이브 스트리밍 테스트

```bash
# 터미널 2에서 차량 카메라 시뮬레이터 실행
bash scripts/rtsp_push.sh vehicle-001
```

서버 로그에 `[live] ANNOUNCE vehicle-001` 확인 후, 웹 플레이어의 Live Channels 섹션에서 WebRTC 재생.

### 6. 웹 플레이어

http://localhost:4242 접속 → 파일 업로드 → HLS/DASH/WebRTC 탭 전환 재생.

## API

| Method | Path | 설명 |
|--------|------|------|
| GET | `/health` | 헬스체크 |
| POST | `/api/upload` | MP4 업로드 (multipart/form-data, field: `file`) |
| GET | `/api/media` | 미디어 목록 |
| GET | `/api/media/{id}` | 미디어 상세 |
| GET | `/api/media/{id}/hls/{file...}` | HLS 스트리밍 |
| GET | `/api/media/{id}/dash/{file...}` | DASH 스트리밍 |
| POST | `/api/media/{id}/webrtc` | WebRTC SDP 시그널링 |
| GET | `/api/live` | 라이브 채널 목록 |
| POST | `/api/live/{channel}/webrtc` | 라이브 WebRTC 시그널링 |

## 프로젝트 구조

```
├── cmd/server/main.go           # 엔트리포인트
├── internal/
│   ├── config/config.go         # 환경변수 설정
│   ├── storage/minio.go         # S3(MinIO) 연동
│   ├── messaging/kafka.go       # Kafka Producer/Consumer
│   ├── repository/postgres.go   # PostgreSQL CRUD
│   ├── media/transcoder.go      # ffmpeg 워커 풀
│   ├── live/ingest.go           # RTSP 인제스트 서버
│   ├── telemetry/telemetry.go   # OpenTelemetry SDK 초기화
│   └── handler/
│       ├── handler.go           # REST API
│       ├── webrtc.go            # WebRTC 스트리밍 (VOD)
│       └── live.go              # WebRTC 스트리밍 (Live)
├── web/index.html               # 테스트 플레이어
├── docker-compose.yml           # 인프라 정의
├── Dockerfile                   # 멀티스테이지 빌드
└── scripts/
    ├── generate_sample.sh       # 테스트 MP4 생성
    ├── rtsp_push.sh             # 차량 카메라 시뮬레이터 (RTSP)
    └── integration_test.sh      # 통합 테스트
```

## 환경변수

`.env` 파일 또는 환경변수로 설정. 기본값이 있어 별도 설정 없이 `docker compose up` 후 바로 실행 가능.

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `SERVER_PORT` | `4242` | HTTP 서버 포트 |
| `DB_HOST` | `localhost` | PostgreSQL 호스트 |
| `DB_PORT` | `5432` | PostgreSQL 포트 |
| `MINIO_ENDPOINT` | `localhost:9000` | MinIO 엔드포인트 |
| `KAFKA_BROKERS` | `localhost:29092` | Kafka 브로커 |
| `REDIS_ADDR` | `localhost:6379` | Redis 주소 |

## 문서

- [ARCHITECTURE.md](ARCHITECTURE.md) — 시스템 구성도, 데이터 플로우, 컴포넌트 상세, ABR 파이프라인, 설계 결정
- [TESTING.md](TESTING.md) — 통합 테스트 시나리오 (TC-1~TC-12), curl 명령어, 검증 체크리스트
- [study.md](study.md) — 기술 스택 이론적 심층 분석 (12개 섹션, 스트리밍/코덱/Kafka/WebRTC/OTel 등)

## 정리

```bash
docker compose down -v   # 인프라 + 볼륨 삭제
```
