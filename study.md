# 미디어 서비스 플랫폼 — 이론적 심층 분석

> 이 문서는 프로젝트에서 사용된 기술 스택의 이론적 배경, 설계 원리, 트레이드오프를 체계적으로 분석한다.
> 각 섹션은 "왜 이 기술을 선택했는가"에 대한 근거를 제공하며, 면접에서 깊이 있는 기술 토론이 가능하도록 구성했다.

---

## 목차

1. [스트리밍 프로토콜 이론](#1-스트리밍-프로토콜-이론)
2. [비디오 코덱 & 컨테이너 이론](#2-비디오-코덱--컨테이너-이론)
    - 2.7 H.264 슬라이스 구조와 멀티 슬라이스 인코딩
    - 2.8 Annex B vs AVCC 바이트스트림 포맷
3. [이벤트 드리븐 아키텍처 & 메시지 큐](#3-이벤트-드리븐-아키텍처--메시지-큐)
4. [오브젝트 스토리지 & CDN 전략](#4-오브젝트-스토리지--cdn-전략)
5. [데이터베이스 설계 & 상태 관리](#5-데이터베이스-설계--상태-관리)
6. [Go 동시성 패턴](#6-go-동시성-패턴)
7. [WebRTC 시그널링 & 미디어 전송](#7-webrtc-시그널링--미디어-전송)
    - 7.6 VOD WebRTC 파이프라인 상세
    - 7.7 ICE 내부망 최적화와 STUN
    - 7.8 실제 디버깅: VOD WebRTC 화면 깨짐 해결
8. [RTSP 인제스트 & 제로 트랜스코딩 릴레이](#8-rtsp-인제스트--제로-트랜스코딩-릴레이)
9. [트랜스코딩 파이프라인](#9-트랜스코딩-파이프라인)
10. [컨테이너화 & 인프라](#10-컨테이너화--인프라)
11. [자율주행 맥락에서의 설계 고려사항](#11-자율주행-맥락에서의-설계-고려사항)
12. [Observability & OpenTelemetry](#12-observability--opentelemetry)
    - 12.1 Observability의 세 기둥
    - 12.2 OpenTelemetry 아키텍처
    - 12.3 이 프로젝트의 계측 전략
    - 12.4 Collector를 사용하는 이유
    - 12.5 플릿 모니터링 연계
    - 12.6 분산 트레이싱 심화
    - 12.7 메트릭 타입 심화
    - 12.8 Grafana 프로비저닝 & 대시보드 설계
    - 12.9 프로덕션 Observability 패턴

---

## 1. 스트리밍 프로토콜 이론

이 프로젝트는 HLS, DASH, WebRTC, RTSP/RTP 네 가지 스트리밍 프로토콜을 모두 사용한다. 각 프로토콜은 서로 다른 문제를 해결하기 위해 설계되었으며, 미디어 서비스 플랫폼에서 이들을 조합하는 것은 지연시간-확장성-호환성의 트레이드오프를 최적화하기 위한 아키텍처적 결정이다.

### 1.1 HLS (HTTP Live Streaming)

#### 동작 원리

HLS는 Apple이 2009년에 발표한 HTTP 기반 적응형 스트리밍 프로토콜이다(RFC 8216). 핵심 아이디어는 단순하다: **연속적인 미디어 스트림을 고정 길이의 HTTP 리소스(세그먼트)로 분할하고, 매니페스트 파일로 재생 순서를 기술한다.**

```
[인코더] → 세그먼트 분할 → [오리진 서버]
                              │
                    master.m3u8 (마스터 플레이리스트)
                    ├── v0/playlist.m3u8 (720p)
                    │   ├── init.mp4 (초기화 세그먼트)
                    │   ├── seg_000.m4s (4초)
                    │   ├── seg_001.m4s (4초)
                    │   └── ...
                    ├── v1/playlist.m3u8 (480p)
                    └── v2/playlist.m3u8 (360p)
```

**마스터 플레이리스트**는 사용 가능한 variant(해상도/비트레이트 조합)를 나열하고, 각 variant의 미디어 플레이리스트는 실제 세그먼트 URL을 시간순으로 나열한다. 클라이언트(hls.js 등)는 네트워크 대역폭을 측정하여 적절한 variant를 선택하고, HTTP GET으로 세그먼트를 순차 다운로드한다.

#### 세그먼트 구조와 fMP4

전통적으로 HLS는 MPEG-TS(.ts) 컨테이너를 사용했으나, Apple은 HLS 7(2017)부터 **fMP4(Fragmented MP4)** 세그먼트를 지원한다. 이 프로젝트에서 fMP4를 선택한 이유:

| 특성 | MPEG-TS | fMP4 |
|------|---------|------|
| 오버헤드 | 188바이트 패킷 헤더 반복 | 박스 구조, 낮은 오버헤드 |
| DASH 호환 | 불가 | **CMAF으로 HLS/DASH 세그먼트 공유 가능** |
| DRM | 별도 암호화 | CENC (Common Encryption) 표준 |
| 코덱 지원 | H.264/AAC 중심 | H.265, VP9, AV1 등 확장 용이 |

fMP4 세그먼트는 `moof`(Movie Fragment) + `mdat`(Media Data) 박스로 구성된다. 초기화 세그먼트(`init.mp4`)에 `ftyp` + `moov` 박스가 있어 디코더 설정 정보(SPS/PPS 등)를 포함한다.

#### 지연시간 특성

일반 HLS의 end-to-end 지연시간은 **6~30초**다. 이는 구조적 한계에서 비롯된다:

```
지연 = 인코딩 지연 + (세그먼트 길이 × 버퍼 세그먼트 수) + 네트워크 지연

예: 4초 세그먼트 × 3개 버퍼 = 12초 + α
```

Apple의 **LL-HLS(Low-Latency HLS)**는 세그먼트를 더 작은 partial segment로 분할하고, `EXT-X-PRELOAD-HINT`로 다음 세그먼트를 미리 요청하여 **2~6초**까지 줄인다. 그러나 이는 서버 측 HTTP/2 push 또는 chunked transfer가 필요하다.

#### 프로젝트 적용

```go
// transcoder.go — HLS fMP4 생성
"-f", "hls",
"-hls_time", "4",                    // 4초 세그먼트
"-hls_segment_type", "fmp4",         // fMP4 (CMAF 호환)
"-hls_playlist_type", "vod",         // VOD 모드
"-master_pl_name", "master.m3u8",    // 마스터 플레이리스트
"-var_stream_map", "v:0,a:0 v:1,a:1 v:2,a:2",  // 3개 variant
```

VOD 파이프라인에서 HLS는 **가장 넓은 디바이스 호환성**을 제공한다. iOS Safari는 네이티브 HLS만 지원하므로, Apple 생태계 커버리지를 위해 필수다.

### 1.2 DASH (Dynamic Adaptive Streaming over HTTP)

#### 동작 원리

DASH는 MPEG가 표준화한 HTTP 기반 적응형 스트리밍 프로토콜이다(ISO/IEC 23009-1, 2012). HLS와 유사한 세그먼트 기반 접근이지만, **코덱 무관(codec-agnostic)** 설계가 핵심 차별점이다.

```xml
<!-- MPD (Media Presentation Description) 구조 -->
<MPD>
  <Period>
    <AdaptationSet id="0" contentType="video">
      <Representation id="0" width="1280" height="720" bandwidth="2500000">
        <SegmentTemplate initialization="init-$RepresentationID$.m4s"
                         media="seg-$RepresentationID$-$Number%05d$.m4s"/>
      </Representation>
      <Representation id="1" width="854" height="480" bandwidth="1200000"/>
      <Representation id="2" width="640" height="360" bandwidth="600000"/>
    </AdaptationSet>
    <AdaptationSet id="1" contentType="audio">
      ...
    </AdaptationSet>
  </Period>
</MPD>
```

DASH의 계층 구조: **MPD → Period → AdaptationSet → Representation → Segment**

- **Period**: 시간 구간 (광고 삽입 지점 등)
- **AdaptationSet**: 동일 콘텐츠의 다른 인코딩 그룹 (비디오/오디오 분리)
- **Representation**: 특정 해상도/비트레이트 조합
- **Segment**: 실제 미디어 데이터 단위

#### HLS와의 핵심 차이

| 측면 | HLS | DASH |
|------|-----|------|
| 표준화 | Apple 독점 → IETF RFC | MPEG/ISO 국제 표준 |
| 매니페스트 | m3u8 (텍스트) | MPD (XML) |
| 코덱 | H.264/H.265/AAC 중심 | **코덱 무관** (AV1, VP9 등) |
| DRM | FairPlay (Apple) | **Widevine + PlayReady** |
| 브라우저 | Safari 네이티브, 나머지 hls.js | dash.js (MSE 기반) |
| 광고 삽입 | 제한적 | Period 기반 유연한 삽입 |

#### CMAF (Common Media Application Format)

CMAF(ISO/IEC 23000-19)은 HLS와 DASH의 세그먼트 포맷을 통일하려는 시도다. **fMP4 + CENC** 조합으로, 동일한 세그먼트 파일을 HLS와 DASH 모두에서 사용할 수 있다.

```
                    ┌── master.m3u8 (HLS 매니페스트)
공유 세그먼트 ──────┤
(fMP4)              └── manifest.mpd (DASH 매니페스트)
```

이 프로젝트에서는 HLS와 DASH 모두 fMP4를 사용하므로, 향후 CMAF 통합으로 **스토리지 비용을 ~50% 절감**할 수 있는 확장 포인트가 있다.

#### 프로젝트 적용

```go
// transcoder.go — DASH 생성
"-f", "dash",
"-seg_duration", "4",
"-adaptation_sets", "id=0,streams=v id=1,streams=a",  // 비디오/오디오 분리
"-init_seg_name", "init-$RepresentationID$.m4s",
"-media_seg_name", "seg-$RepresentationID$-$Number%05d$.m4s",
```

DASH는 **Android/Chrome 생태계**와 **DRM(Widevine)** 지원을 위해 필수다. HLS + DASH 조합으로 사실상 모든 디바이스를 커버한다.

### 1.3 WebRTC (Web Real-Time Communication)

#### 동작 원리

WebRTC는 브라우저 간 실시간 미디어 통신을 위한 프레임워크다(W3C + IETF). HLS/DASH와 근본적으로 다른 점은 **HTTP가 아닌 UDP 기반 RTP로 미디어를 전송**한다는 것이다.

```
┌──────────────────────────────────────────────────────┐
│                    WebRTC 스택                         │
│                                                       │
│  Application ─── MediaStream API                      │
│       │                                               │
│  Signaling ───── SDP (Session Description Protocol)   │
│       │          offer/answer 모델                     │
│       │                                               │
│  Security ────── DTLS (키 교환)                        │
│       │          SRTP (미디어 암호화)                    │
│       │                                               │
│  Connectivity ── ICE (Interactive Connectivity Est.)   │
│       │          STUN (NAT 바인딩 발견)                 │
│       │          TURN (릴레이 폴백)                     │
│       │                                               │
│  Transport ───── RTP/RTCP (미디어 전송/피드백)          │
│       │          SCTP (데이터 채널)                     │
│       │                                               │
│  Network ─────── UDP (기본) / TCP (폴백)               │
└──────────────────────────────────────────────────────┘
```

#### SDP 시그널링 흐름

WebRTC 연결 수립은 **SDP(Session Description Protocol) offer/answer** 교환으로 시작된다:

```
Browser (Offerer)                    Server (Answerer)
    │                                      │
    │── createOffer() ────────────────────▶│
    │   SDP: 지원 코덱, ICE 후보,          │
    │        미디어 방향(recvonly)          │
    │                                      │── createAnswer()
    │                                      │   SDP: 선택된 코덱,
    │◀─────────────────── answer SDP ──────│   서버 ICE 후보
    │                                      │
    │── setRemoteDescription() ───────────▶│
    │                                      │
    │◀═══════ ICE Connectivity Check ═════▶│
    │         (STUN binding request)       │
    │                                      │
    │◀═══════ DTLS Handshake ═════════════▶│
    │         (키 교환, SRTP 설정)          │
    │                                      │
    │◀══════════ SRTP Media ══════════════▶│
    │         (H.264 RTP 패킷)             │
```

이 프로젝트에서는 **HTTP POST를 시그널링 채널로 사용**한다. 브라우저가 SDP offer를 POST하면, 서버가 SDP answer를 응답으로 반환하는 단순한 WHIP(WebRTC-HTTP Ingestion Protocol) 유사 패턴이다.

#### 지연시간 특성

WebRTC의 end-to-end 지연시간은 **100~500ms**로, HLS/DASH 대비 10~100배 낮다:

```
WebRTC: 캡처(~30ms) + 인코딩(~50ms) + 네트워크(~50ms) + 디코딩(~30ms) + 렌더링(~16ms)
       ≈ 150~300ms

HLS:   인코딩 + 세그먼트 생성(4s) + 버퍼링(8~12s) + 네트워크 + 디코딩
       ≈ 12~30s
```

이 차이는 **버퍼링 전략**에서 비롯된다. HLS는 안정적 재생을 위해 여러 세그먼트를 미리 버퍼링하지만, WebRTC는 실시간성을 위해 최소한의 jitter buffer만 유지한다.

#### 프로젝트 적용 (VOD + Live)

**VOD WebRTC** (`webrtc.go`):
```go
// ffmpeg으로 MP4 → H264 Annex B 추출 → NAL unit 파싱 → WriteSample()
cmd := exec.CommandContext(ctx, "ffmpeg", "-re", "-i", mp4Path,
    "-c:v", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "h264", "-an", "pipe:1")
```

**Live WebRTC** (`live.go`):
```go
// RTSP RTP 패킷을 그대로 WebRTC TrackLocalStaticRTP로 릴레이
track.WriteRTP(pkt)  // 제로 트랜스코딩
```

VOD에서는 "즉시 재생" 경험을, Live에서는 "초저지연 모니터링"을 제공한다.

### 1.4 RTSP/RTP (Real-Time Streaming Protocol / Real-time Transport Protocol)

#### RTSP — 제어 프로토콜

RTSP(RFC 7826)는 미디어 서버의 스트림을 제어하기 위한 **텍스트 기반 프로토콜**이다. HTTP와 유사한 요청-응답 구조를 가지지만, **상태를 유지(stateful)**한다는 점이 다르다.

```
RTSP 상태 머신:

  INIT ──ANNOUNCE──▶ READY ──RECORD──▶ PLAYING
                       │                  │
                       │◀──TEARDOWN──────│
                       │                  │
                       └──────────────────┘
```

이 프로젝트의 RTSP 인제스트 서버는 **ANNOUNCE + RECORD** 모드를 사용한다:

1. **ANNOUNCE**: 퍼블리셔(차량 카메라)가 스트림의 SDP를 서버에 등록
2. **SETUP**: 전송 파라미터 협상 (TCP interleaved / UDP)
3. **RECORD**: 미디어 데이터 전송 시작
4. **TEARDOWN**: 세션 종료

```go
// ingest.go — RTSP 상태 머신 구현
func (s *Server) OnAnnounce(ctx) { /* 채널 생성, SDP 등록 */ }
func (s *Server) OnRecord(ctx)   { /* RTP 패킷 인터셉트 시작 */ }
func (s *Server) OnSessionClose(ctx) { /* 채널 + 구독자 정리 */ }
```

#### RTP — 미디어 전송 프로토콜

RTP(RFC 3550)는 실시간 미디어 데이터를 전송하는 프로토콜이다. UDP 위에서 동작하며, 패킷 구조:

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|V=2|P|X|  CC   |M|     PT      |       Sequence Number         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                           Timestamp                           |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                             SSRC                              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                          Payload...                           |
```

- **PT (Payload Type)**: 코덱 식별 (96~127: 동적 할당, SDP에서 매핑)
- **Sequence Number**: 패킷 순서 (손실 감지)
- **Timestamp**: 미디어 타이밍 (H.264: 90kHz 클럭)
- **SSRC**: 소스 식별자
- **M (Marker)**: 프레임 경계 표시 (H.264에서 NAL unit의 마지막 패킷)

#### RTSP vs RTMP

| 특성 | RTSP/RTP | RTMP |
|------|----------|------|
| 설계 목적 | IP 카메라, 감시 시스템 | Flash 기반 라이브 스트리밍 |
| 전송 | UDP (기본) / TCP | TCP only |
| 표준화 | IETF RFC | Adobe 독점 (부분 공개) |
| 현재 상태 | **IP 카메라 업계 표준** | Flash 종료로 쇠퇴 |
| 차량 카메라 | ✅ 네이티브 지원 | ❌ 미지원 |

자율주행 차량 카메라는 RTSP를 네이티브로 지원할 가능성이 높다. ONVIF(IP 카메라 표준)가 RTSP를 기반으로 하기 때문이다.

### 1.5 프로토콜 비교 분석

| 특성 | HLS | DASH | WebRTC | RTSP/RTP |
|------|-----|------|--------|----------|
| **지연시간** | 6~30s | 6~30s | 100~500ms | 100~500ms |
| **전송 프로토콜** | HTTP/TCP | HTTP/TCP | UDP (SRTP) | UDP/TCP |
| **확장성** | ★★★★★ CDN 친화 | ★★★★★ CDN 친화 | ★★☆ SFU 필요 | ★★☆ 서버 부하 |
| **호환성** | iOS 네이티브 | Android/Chrome | 모든 모던 브라우저 | IP 카메라/NVR |
| **ABR** | 클라이언트 주도 | 클라이언트 주도 | 서버/클라이언트 | N/A |
| **암호화** | TLS | TLS | DTLS-SRTP (필수) | SRTP (선택) |
| **NAT 트래버설** | 불필요 (HTTP) | 불필요 (HTTP) | ICE/STUN/TURN | 복잡 (UDP) |
| **적용 시나리오** | VOD, 대규모 라이브 | VOD, DRM 콘텐츠 | 화상회의, 초저지연 | 카메라 인제스트 |

### 1.6 왜 4개 프로토콜 모두 필요한가

```
                    지연시간 ◀────────────────────────▶ 확장성
                    (낮음)                              (높음)

    WebRTC ◀──────────────────────────────────────────▶ HLS/DASH
    ~200ms          RTSP/RTP                            ~15s
                    ~300ms
                    (인제스트 전용)

    ┌─────────────────────────────────────────────────────────┐
    │                    프로젝트 프로토콜 맵                    │
    │                                                          │
    │  [차량 카메라] ──RTSP──▶ [서버] ──WebRTC──▶ [모니터링]    │
    │                           │                              │
    │  [업로드 MP4] ──HTTP──▶ [서버] ──HLS/DASH──▶ [대규모 배포]│
    │                           │                              │
    │                           └──WebRTC──▶ [즉시 미리보기]    │
    └─────────────────────────────────────────────────────────┘
```

- **RTSP**: 차량 카메라 → 서버 인제스트 (업계 표준, 카메라 네이티브 지원)
- **WebRTC**: 서버 → 브라우저 초저지연 릴레이 (라이브 모니터링, VOD 미리보기)
- **HLS**: 서버 → 대규모 시청자 배포 (iOS 호환, CDN 친화)
- **DASH**: 서버 → 대규모 시청자 배포 (Android/DRM, 국제 표준)

각 프로토콜은 **대체 불가능한 고유 역할**을 담당한다. RTSP를 WebRTC로 대체하면 카메라 호환성을 잃고, HLS를 WebRTC로 대체하면 CDN 확장성을 잃는다.

---


## 2. 비디오 코덱 & 컨테이너 이론

이 프로젝트에서 H.264는 VOD 트랜스코딩과 라이브 릴레이 모두의 핵심 코덱이다. 라이브 WebRTC 디버깅 과정에서 SPS/PPS 누락 문제를 직접 경험했으며, 이 섹션은 그 경험을 이론적 배경과 함께 정리한다.

### 2.1 H.264/AVC 개요

H.264(ITU-T H.264 / ISO/IEC 14496-10 AVC)는 2003년에 표준화된 비디오 코덱으로, 20년이 지난 현재도 **인터넷 비디오의 ~80%**를 차지한다. 후속 코덱(H.265/HEVC, AV1)이 더 높은 압축 효율을 제공하지만, H.264가 여전히 지배적인 이유:

- **하드웨어 디코더 보편성**: 모든 모바일/데스크톱/임베디드 디바이스에 하드웨어 디코더 탑재
- **특허 상황**: 기본 프로파일은 무료 사용 가능 (MPEG LA 라이선스)
- **WebRTC 필수 코덱**: RFC 7742에서 H.264 Constrained Baseline을 필수(MUST) 코덱으로 지정
- **IP 카메라 표준**: 대부분의 IP 카메라/차량 카메라가 H.264 하드웨어 인코더 내장

#### 프로파일 (Profile)

프로파일은 인코더가 사용할 수 있는 코딩 도구의 집합을 정의한다:

| 프로파일 | 주요 특성 | 용도 |
|---------|----------|------|
| **Baseline** | I/P 슬라이스만, CAVLC, 인루프 디블로킹 | 실시간 통신, 모바일, **WebRTC** |
| Constrained Baseline | Baseline의 부분집합 | WebRTC 필수 코덱 |
| Main | B 슬라이스, CABAC, 가중 예측 | 방송, SD 스트리밍 |
| **High** | 8×8 변환, 양자화 스케일링 매트릭스 | HD 방송, **VOD 스트리밍** |
| High 10 | 10비트 색심도 | HDR 콘텐츠 |

이 프로젝트에서의 선택:
- **라이브 인제스트**: Baseline (`-profile:v baseline`) — 인코딩 지연 최소화, WebRTC 호환 보장
- **VOD 트랜스코딩**: High 프로파일이 이상적이나, 현재 `fast` 프리셋으로 Baseline 수준 사용

#### 레벨 (Level)

레벨은 디코더가 처리해야 하는 최대 부하를 정의한다:

| 레벨 | 최대 해상도 | 최대 프레임레이트 | 최대 비트레이트 (High) |
|------|-----------|-----------------|---------------------|
| 3.0 | 720×480 | 30fps | 10 Mbps |
| **3.1** | **1280×720** | **30fps** | **14 Mbps** |
| 4.0 | 2048×1024 | 30fps | 20 Mbps |
| 4.1 | 2048×1024 | 30fps | 50 Mbps |
| 5.1 | 4096×2160 | 30fps | 300 Mbps |

프로젝트의 SDP에서 `profile-level-id=42001f`:
- `42` = Baseline profile (66 in decimal)
- `00` = constraint flags
- `1f` = Level 3.1 (31 in decimal) → 1280×720@30fps

```go
// live.go — WebRTC 트랙 코덱 파라미터
SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
```

### 2.2 NAL 유닛 구조

H.264 비트스트림은 **NAL(Network Abstraction Layer) 유닛**의 시퀀스로 구성된다. NAL은 비디오 데이터를 네트워크 전송에 적합한 단위로 패키징하는 계층이다.

#### NAL 유닛 헤더

```
+---------------+
|0|1|2|3|4|5|6|7|
+-+-+-+-+-+-+-+-+
|F|NRI|  Type   |
+---------------+

F:    금지 비트 (0이어야 함)
NRI:  참조 중요도 (00=disposable, 11=highest)
Type: NAL 유닛 타입 (0~31)
```

#### 핵심 NAL 유닛 타입

| Type | 이름 | 설명 | 중요도 |
|------|------|------|--------|
| 1 | Non-IDR Slice | P/B 프레임 데이터 | 일반 |
| 5 | **IDR Slice** | 즉시 디코더 리프레시 (키프레임) | **높음** |
| 6 | SEI | 보충 정보 (타이밍, 자막 등) | 낮음 |
| **7** | **SPS** | **시퀀스 파라미터 세트** | **필수** |
| **8** | **PPS** | **픽처 파라미터 세트** | **필수** |
| 9 | AUD | 액세스 유닛 구분자 | 선택 |
| 24 | STAP-A | 단일 시간 집합 패킷 (RTP) | - |
| 28 | **FU-A** | **단편화 유닛** (큰 NAL 분할, RTP) | - |

#### IDR 프레임의 의미

IDR(Instantaneous Decoder Refresh)은 단순한 I-프레임이 아니다. IDR은 **디코더의 참조 프레임 버퍼를 완전히 리셋**한다. IDR 이후의 프레임은 IDR 이전의 어떤 프레임도 참조하지 않는다.

```
시간 →
... P P P [IDR] P P P P P P [IDR] P P P ...
          ↑                  ↑
          여기서부터 독립적    여기서부터 독립적
          디코딩 가능          디코딩 가능
```

이것이 **랜덤 액세스 포인트(RAP)**이며, HLS/DASH 세그먼트 경계는 반드시 IDR에 정렬되어야 한다:

```go
// transcoder.go — 4초마다 키프레임 강제
"-force_key_frames", "expr:gte(t,n_forced*4)"
```

### 2.3 SPS/PPS 심층 분석 — 실제 디버깅 경험

#### SPS (Sequence Parameter Set, NAL Type 7)

SPS는 **비디오 시퀀스 전체에 적용되는 디코더 설정 정보**를 담는다:

```
SPS 주요 필드:
├── profile_idc          → 프로파일 (66=Baseline, 77=Main, 100=High)
├── level_idc            → 레벨 (31=3.1 → 720p@30fps)
├── seq_parameter_set_id → SPS 식별자 (0~31)
├── chroma_format_idc    → 색차 서브샘플링 (1=4:2:0)
├── pic_width_in_mbs     → 가로 매크로블록 수 (1280/16=80)
├── pic_height_in_map_units → 세로 매크로블록 수 (720/16=45)
├── frame_mbs_only_flag  → 프레임 전용 (1) vs 필드 (0)
├── max_num_ref_frames   → 최대 참조 프레임 수
├── vui_parameters       → 타이밍 정보, 색공간 등
│   ├── timing_info_present_flag
│   ├── num_units_in_tick
│   └── time_scale       → 프레임레이트 계산에 사용
└── ...
```

**SPS 없이는 디코더가 비디오의 해상도조차 알 수 없다.** 이것이 SPS가 "필수"인 이유다.

#### PPS (Picture Parameter Set, NAL Type 8)

PPS는 **개별 픽처(프레임)의 디코딩 파라미터**를 정의한다:

```
PPS 주요 필드:
├── pic_parameter_set_id     → PPS 식별자
├── seq_parameter_set_id     → 참조하는 SPS ID
├── entropy_coding_mode_flag → 0=CAVLC, 1=CABAC
├── num_slice_groups         → 슬라이스 그룹 수
├── num_ref_idx_l0_default   → L0 참조 인덱스 기본값
├── weighted_pred_flag       → 가중 예측 사용 여부
├── pic_init_qp              → 초기 양자화 파라미터
├── deblocking_filter_control → 디블로킹 필터 제어
└── ...
```

#### 실제 디버깅: 라이브 WebRTC에서 영상이 안 나온 이유

이 프로젝트에서 라이브 WebRTC 구현 시 겪은 문제와 해결 과정:

```
문제 상황:
1. ffmpeg이 RTSP로 H.264 스트림 전송 중
2. 브라우저가 WebRTC로 연결 → PeerConnection "connected" 상태
3. RTP 패킷이 정상적으로 릴레이됨 (30fps, 300pkts/10s)
4. 그러나 브라우저에 영상이 표시되지 않음
```

**원인 분석:**

```
시간축:
t=0   ffmpeg 시작 → SPS/PPS 전송 → IDR → P P P P P ...
t=10  브라우저 WebRTC 연결 (mid-stream join)
t=10+ 브라우저가 받는 패킷: P P P P P ... (SPS/PPS 없음!)
      → 디코더 초기화 불가 → 검은 화면
```

ffmpeg의 기본 동작은 **스트림 시작 시 한 번만 SPS/PPS를 전송**한다. 브라우저가 중간에 접속하면 SPS/PPS를 놓치고, 다음 IDR이 와도 SPS/PPS가 없으면 디코딩할 수 없다.

**해결:**

```bash
# rtsp_push.sh — repeat-headers 옵션 추가
-x264-params repeat-headers=1
```

`repeat-headers=1`은 x264 인코더에게 **매 IDR 프레임 앞에 SPS/PPS를 반복 삽입**하도록 지시한다:

```
변경 전: [SPS PPS IDR] P P P ... [IDR] P P P ... [IDR] P P P ...
변경 후: [SPS PPS IDR] P P P ... [SPS PPS IDR] P P P ... [SPS PPS IDR] ...
```

이제 브라우저가 언제 접속하든, 최대 1 GOP(1초, `-g 30` at 30fps) 내에 SPS/PPS + IDR을 수신하여 디코딩을 시작할 수 있다.

**프로덕션 레벨 해결책:**

인코더 설정에 의존하는 것은 취약하다. 서버 측에서 SPS/PPS를 캐싱하는 것이 올바른 접근:

```
개선된 아키텍처:
Channel {
    lastSPS *rtp.Packet  // 마지막 SPS 캐시
    lastPPS *rtp.Packet  // 마지막 PPS 캐시
}

Subscribe() 시:
1. 캐시된 SPS 전송
2. 캐시된 PPS 전송
3. 이후 실시간 패킷 릴레이 시작
→ 인코더 설정과 무관하게 즉시 디코딩 가능
```

### 2.4 RTP에서의 H.264 패킷화 (RFC 6184)

H.264 NAL 유닛을 RTP로 전송할 때, NAL 크기에 따라 세 가지 모드를 사용한다:

#### Single NAL Unit Mode

NAL 유닛이 MTU(~1200바이트) 이하일 때, RTP 페이로드에 그대로 담는다:

```
RTP Header (12 bytes) | NAL Header (1 byte) | NAL Data
```

SPS, PPS는 보통 수십~수백 바이트이므로 이 모드로 전송된다.

#### FU-A (Fragmentation Unit)

NAL 유닛이 MTU를 초과할 때(IDR 프레임 등), 여러 RTP 패킷으로 분할한다:

```
첫 번째 패킷:
RTP Header | FU Indicator | FU Header (S=1) | NAL Fragment
                                    ↑ Start bit

중간 패킷:
RTP Header | FU Indicator | FU Header (S=0,E=0) | NAL Fragment

마지막 패킷:
RTP Header | FU Indicator | FU Header (E=1) | NAL Fragment
                                    ↑ End bit
RTP Marker bit = 1 (프레임의 마지막 패킷)
```

#### STAP-A (Single-Time Aggregation Packet)

여러 작은 NAL 유닛을 하나의 RTP 패킷에 묶는다. SPS + PPS를 함께 전송할 때 주로 사용:

```
RTP Header | STAP-A Header | Size(2) | SPS NAL | Size(2) | PPS NAL
```

이 프로젝트의 제로 트랜스코딩 릴레이에서는 이 패킷화 형식을 **그대로 유지**한다. RTSP에서 받은 RTP 패킷의 페이로드를 수정 없이 WebRTC로 전달하므로, 패킷화/역패킷화 오버헤드가 없다.

### 2.5 fMP4 (Fragmented MP4) 구조

일반 MP4는 `moov` 박스(메타데이터)가 파일 끝에 위치하여 전체 파일을 다운로드해야 재생 가능하다. fMP4는 이를 해결한다:

```
일반 MP4:
┌──────┬──────────────────────────────┬──────┐
│ ftyp │         mdat (전체 미디어)     │ moov │ ← 끝에 위치
└──────┴──────────────────────────────┴──────┘
                                        ↑ 전체 다운로드 필요

fMP4 (Init Segment + Media Segments):
┌──────┬──────┐  ┌──────┬──────┐  ┌──────┬──────┐
│ ftyp │ moov │  │ moof │ mdat │  │ moof │ mdat │  ...
└──────┴──────┘  └──────┴──────┘  └──────┴──────┘
  Init Segment    Media Segment 1  Media Segment 2
  (디코더 설정)    (4초 분량)        (4초 분량)
```

#### 박스 구조 상세

**Init Segment** (`init.mp4`):
```
ftyp ── 파일 타입 (isom, iso5, dash 등)
moov ── 무비 메타데이터
├── mvhd ── 무비 헤더 (timescale, duration)
├── trak ── 트랙 (비디오/오디오 각각)
│   ├── tkhd ── 트랙 헤더 (width, height)
│   └── mdia
│       ├── mdhd ── 미디어 헤더 (timescale)
│       └── minf
│           └── stbl
│               ├── stsd ── 샘플 설명 (avc1 → SPS/PPS 포함)
│               └── ... (stts, stsc 등은 비어있음)
└── mvex ── 무비 확장 (fMP4임을 표시)
    └── trex ── 트랙 확장 기본값
```

**Media Segment** (`seg_000.m4s`):
```
moof ── 무비 프래그먼트
├── mfhd ── 프래그먼트 헤더 (sequence_number)
└── traf ── 트랙 프래그먼트
    ├── tfhd ── 트랙 프래그먼트 헤더
    ├── tfdt ── 트랙 프래그먼트 디코드 시간 (baseMediaDecodeTime)
    └── trun ── 트랙 런 (각 샘플의 duration, size, flags, offset)
mdat ── 미디어 데이터 (실제 H.264 NAL 유닛들)
```

### 2.6 ABR (Adaptive Bitrate Streaming)

ABR은 네트워크 상태에 따라 **실시간으로 비디오 품질을 전환**하는 기술이다. 클라이언트가 대역폭을 측정하고, 적절한 variant를 선택한다.

#### ABR 알고리즘 분류

**1. 처리량 기반 (Throughput-based)**
```
측정된 대역폭 = 세그먼트 크기 / 다운로드 시간
선택 variant = max(variant.bitrate ≤ 측정된 대역폭 × 안전 계수)
```
- 장점: 단순, 빠른 반응
- 단점: 대역폭 변동에 민감, 빈번한 품질 전환

**2. 버퍼 기반 (Buffer-based, BBA)**
```
if 버퍼 < 최소 임계값:
    최저 품질 선택 (버퍼 고갈 방지)
elif 버퍼 > 최대 임계값:
    최고 품질 선택
else:
    버퍼 수준에 비례하여 품질 선택
```
- 장점: 안정적, 품질 전환 최소화
- 단점: 초기 품질 상승이 느림

**3. 하이브리드 (hls.js 기본)**
```
hls.js의 AbrController:
1. 처리량 측정 (EWMA — 지수 가중 이동 평균)
2. 버퍼 수준 확인
3. 두 신호를 결합하여 variant 선택
4. 품질 하락은 즉시, 상승은 보수적으로
```

#### 프로젝트의 ABR 래더

```go
// transcoder.go — 3단계 ABR 래더
var variants = []struct {
    width, height int
    vBitrate      string
}{
    {1280, 720, "2500k"},   // v0: HD
    {854, 480, "1200k"},    // v1: SD
    {640, 360, "600k"},     // v2: 저화질
}
```

이 래더 설계의 근거:
- **720p/2.5Mbps**: 일반 Wi-Fi/LTE 환경의 기본 품질
- **480p/1.2Mbps**: 3G/불안정 네트워크 대응
- **360p/600kbps**: 극저대역폭 환경 (차량 터널 통과 등)
- **비트레이트 비율**: 각 단계가 약 2배 차이 → 부드러운 품질 전환

`maxrate`와 `bufsize`는 CBR(Constant Bitrate)에 가까운 인코딩을 유도하여, ABR 전환 시 대역폭 예측의 정확도를 높인다:

```go
"-maxrate:v:0", "2500k",  // 최대 비트레이트 = 목표와 동일
"-bufsize:v:0", "5000k",  // VBV 버퍼 = 목표의 2배
```

### 2.7 H.264 슬라이스 구조와 멀티 슬라이스 인코딩

#### 슬라이스란 무엇인가

H.264에서 하나의 **픽처(프레임)**는 하나 이상의 **슬라이스(slice)**로 구성된다. 슬라이스는 독립적으로 디코딩 가능한 매크로블록의 연속이다.

```
하나의 프레임 (1280×720):

┌──────────────────────────────────────┐
│          Slice 0 (type=5 IDR)        │  ← 상단 1/4
├──────────────────────────────────────┤
│          Slice 1 (type=5 IDR)        │  ← 상단 2/4
├──────────────────────────────────────┤
│          Slice 2 (type=5 IDR)        │  ← 하단 2/4
├──────────────────────────────────────┤
│          Slice 3 (type=5 IDR)        │  ← 하단 1/4
└──────────────────────────────────────┘

각 슬라이스는 별도의 NAL 유닛으로 전송된다.
같은 프레임의 슬라이스들은 동일한 RTP timestamp를 가져야 한다.
```

슬라이스 헤더의 핵심 필드:
- **`first_mb_in_slice`**: 이 슬라이스의 첫 매크로블록 번호. 0이면 프레임의 첫 슬라이스
- **`slice_type`**: I(2), P(0), B(1) 등
- **`frame_num`**: 프레임 순번. 같은 프레임의 슬라이스는 동일한 frame_num

#### x264의 스레딩 모델: frame-threads vs sliced-threads

x264 인코더는 두 가지 병렬화 전략을 제공한다:

| 모드 | 동작 | 지연시간 | 출력 |
|------|------|---------|------|
| **frame-threads** (기본) | 여러 프레임을 파이프라인 병렬 처리 | 프레임 수 × 지연 | 프레임당 NAL 1개 |
| **sliced-threads** | 하나의 프레임을 슬라이스로 분할, 각 스레드가 슬라이스 담당 | **최소** (슬라이스 완성 즉시 출력) | 프레임당 NAL N개 |

```
frame-threads (기본):
  Thread 0: [Frame 0 인코딩 완료] → 출력
  Thread 1:   [Frame 1 인코딩 완료] → 출력
  Thread 2:     [Frame 2 인코딩 완료] → 출력
  지연: N 프레임 (스레드 수만큼)

sliced-threads (-tune zerolatency):
  Frame 0:
    Thread 0: [Slice 0] → 즉시 출력
    Thread 1: [Slice 1] → 즉시 출력
    Thread 2: [Slice 2] → 즉시 출력
    Thread 3: [Slice 3] → 즉시 출력
  지연: 0 프레임 (슬라이스 완성 즉시)
```

#### `-tune zerolatency`의 내부 동작

`-tune zerolatency`는 x264에 다음 옵션을 일괄 설정한다:

```
bframes=0           → B 프레임 비활성화 (참조 대기 제거)
force-cfr=1         → 고정 프레임레이트 강제
no-mbtree=1         → 매크로블록 트리 비활성화 (lookahead 제거)
sync-lookahead=0    → 선행 분석 비활성화
rc-lookahead=0      → 레이트 컨트롤 선행 분석 비활성화
sliced-threads=1    → ★ 슬라이스 기반 스레딩 활성화
```

`sliced-threads=1`이 핵심이다. 이것이 활성화되면 `-threads N`에 따라 프레임이 N개 슬라이스로 분할된다.

#### 이 프로젝트에서 발생한 문제

```
ffmpeg 설정: -tune zerolatency -preset ultrafast
→ sliced-threads 활성화, 기본 스레드 수 = CPU 코어 수 (예: 8)
→ 프레임당 8개 슬라이스 생성

서버 로그:
  NAL type=7 size=23    ← SPS
  NAL type=8 size=4     ← PPS
  NAL type=5 size=1997  ← IDR 슬라이스 0 (first_mb=0)
  NAL type=5 size=1641  ← IDR 슬라이스 1
  NAL type=5 size=1561  ← IDR 슬라이스 2
  NAL type=5 size=1169  ← IDR 슬라이스 3
  NAL type=5 size=745   ← IDR 슬라이스 4
  ...

sendNALUnits 코드에서 각 NAL type=5마다 <-ticker.C 대기:
  슬라이스 0 → timestamp T+0ms
  슬라이스 1 → timestamp T+33ms   ← 다른 timestamp!
  슬라이스 2 → timestamp T+66ms
  ...

결과: 하나의 프레임이 여러 RTP timestamp에 걸쳐 전송
→ 디코더가 각 슬라이스를 별도 프레임으로 해석
→ 대각선 찢어짐 (diagonal tearing)
```

브라우저 스크린샷에서 SMPTE 컬러바가 대각선으로 기울어진 것은, 디코더가 프레임의 일부(슬라이스)만으로 화면을 렌더링하고 다음 "프레임"(실제로는 같은 프레임의 다음 슬라이스)으로 넘어가면서 발생한 전형적인 증상이다.

#### 해결: `-threads 1`

```go
// webrtc.go — ffmpeg 명령
"-tune", "zerolatency",
"-threads", "1",        // ← 추가: 인코딩 스레드 1개 = 슬라이스 1개
```

`-threads 1`이면 sliced-threads가 활성화되어 있어도 스레드가 1개이므로 슬라이스도 1개. 프레임 = NAL 1:1 매핑이 보장된다.

대안:
- `-x264-params sliced-threads=0`: sliced-threads 자체를 비활성화 → frame-threads로 전환. 하지만 frame-threads는 인코딩 지연이 증가한다.
- `-tune zerolatency` 제거: 모든 저지연 최적화가 사라짐. B 프레임 활성화 등 부작용.
- 코드에서 같은 프레임의 슬라이스를 묶어서 전송: `first_mb_in_slice` 파싱 필요. 복잡도 대비 이점 없음.

`-threads 1`이 가장 최소한의 수정이면서 문제를 정확히 해결한다. VOD 트랜스코딩은 실시간이 아니므로(ffmpeg이 파이프로 빠르게 출력, Go 코드가 ticker로 페이싱) 멀티스레드 인코딩 속도가 불필요하다.

### 2.8 Annex B vs AVCC 바이트스트림 포맷

H.264 비트스트림은 두 가지 포맷으로 패키징된다. 이 차이를 이해하지 못하면 NAL 파싱에서 버그가 발생한다.

#### Annex B (바이트스트림 포맷)

ITU-T H.264 Annex B에 정의된 포맷. **start code**로 NAL 유닛을 구분한다:

```
00 00 00 01 [NAL Header] [NAL Data...]  ← 4바이트 start code
00 00 01    [NAL Header] [NAL Data...]  ← 3바이트 start code (축약형)
```

```
Annex B 스트림 예시:

00 00 00 01 67 42 00 1f ...   ← SPS (NAL type 7, 0x67 & 0x1F = 7)
00 00 00 01 68 ce 38 80      ← PPS (NAL type 8)
00 00 00 01 65 88 80 40 ...   ← IDR (NAL type 5)
00 00 00 01 41 9a 24 ...      ← non-IDR (NAL type 1)
```

사용처:
- **라이브 스트리밍**: RTSP/RTP, 실시간 인코더 출력
- **ffmpeg `-f h264` 출력**: raw H.264 Annex B
- **MPEG-TS 컨테이너**: HLS의 전통적 세그먼트 포맷

#### AVCC (AVC Configuration Record)

ISO/IEC 14496-15에 정의된 포맷. **길이 프리픽스**로 NAL 유닛을 구분한다:

```
[4 bytes: NAL 길이] [NAL Header] [NAL Data...]
```

```
AVCC 스트림 예시:

00 00 00 17 67 42 00 1f ...   ← SPS (길이=23바이트)
00 00 00 04 68 ce 38 80      ← PPS (길이=4바이트)
00 00 07 cd 65 88 80 40 ...   ← IDR (길이=1997바이트)
```

SPS/PPS는 스트림 내에 인라인으로 포함되지 않고, **`avcC` 박스**(MP4 컨테이너의 디코더 설정 영역)에 별도 저장된다.

사용처:
- **MP4/fMP4 컨테이너**: HLS fMP4, DASH 세그먼트
- **WebRTC RTP 페이로드**: RFC 6184 (start code 없이 NAL 데이터만)

#### 변환: `h264_mp4toannexb` BSF

ffmpeg의 Bitstream Filter(BSF)로 AVCC → Annex B 변환:

```go
// webrtc.go — MP4(AVCC) → Annex B 변환
"-bsf:v", "h264_mp4toannexb",
"-f", "h264",  // 출력 포맷: raw Annex B
```

이 BSF가 수행하는 작업:
1. `avcC` 박스에서 SPS/PPS 추출 → Annex B start code 붙여서 스트림 앞에 삽입
2. 각 NAL의 길이 프리픽스 → start code(`00 00 00 01`)로 교체
3. 키프레임마다 SPS/PPS 반복 삽입 (옵션)

**주의**: `-c:v libx264`로 재인코딩하면 출력이 이미 Annex B이므로 이 BSF는 사실상 no-op이다. 하지만 `-c:v copy`(재인코딩 없이 복사)일 때는 필수다.

#### NAL 파싱 시 주의점 — 이 프로젝트의 실제 버그

초기 구현에서 byte-by-byte로 start code를 찾아 NAL을 분리하는 코드를 직접 작성했다. 이때 발생한 버그:

```go
// 버그 코드 (초기 구현)
nalData := buffer[startPos:endPos]  // start code 포함!
nalType := nalData[0] & 0x1F       // nalData[0] = 0x00 (start code 첫 바이트)
                                    // 0x00 & 0x1F = 0 → 유효하지 않은 NAL type
```

start code(`00 00 00 01`)를 strip하지 않고 NAL type을 체크했기 때문에, 모든 NAL의 type이 0으로 판정되어 switch문의 어떤 case에도 매칭되지 않았다. 결과: **NAL이 하나도 전송되지 않음 → 검은 화면**.

해결: pion의 `h264reader.NewReader()`를 사용. 이 라이브러리는 start code를 자동으로 strip하고, `nal.Data[0]`이 NAL 헤더 바이트를 가리키도록 보장한다.

```go
// 수정 코드 (h264reader 사용)
h264, _ := h264reader.NewReader(reader)
nal, _ := h264.NextNAL()
nalType := nal.Data[0] & 0x1F  // nal.Data[0]은 NAL 헤더 (start code 제거됨)
```

교훈:
1. **검증된 라이브러리 우선**: H.264 파싱은 엣지 케이스가 많다 (3바이트/4바이트 start code, emulation prevention byte 등). 직접 구현보다 검증된 파서를 사용하는 것이 안전하다.
2. **Annex B 데이터를 다룰 때 start code 존재 여부를 항상 확인**: 라이브러리마다 start code 포함/제거 여부가 다르다.
3. **디버그 로그 필수**: NAL type과 size를 로깅하지 않았다면 "type=0" 버그를 발견하기 어려웠을 것이다.

---


## 3. 이벤트 드리븐 아키텍처 & 메시지 큐

### 3.1 왜 이벤트 드리븐인가

#### 동기식 요청-응답의 한계

미디어 업로드 → 트랜스코딩 파이프라인을 동기식으로 구현한다면:

```
Client ── POST /upload ──▶ Handler ── transcode() ──▶ ffmpeg (3분 소요)
                                                         │
Client ◀── 201 Created ── Handler ◀── result ────────────┘
           (3분 후 응답)

문제:
1. HTTP 타임아웃 (보통 30~60초)
2. 클라이언트가 3분간 블로킹
3. 서버 스레드/goroutine 점유
4. 트랜스코딩 실패 시 재시도 불가 (요청 이미 종료)
5. 스케일링 불가 (업로드 서버 = 트랜스코딩 서버)
```

#### 이벤트 드리븐 아키텍처의 해결

```
Client ── POST /upload ──▶ Handler ── S3 저장 + DB 기록
                              │
Client ◀── 201 Created ──────┘  (즉시 응답, ~1초)
                              │
                              └── Kafka publish("media.uploaded")
                                        │
                                        ▼ (비동기)
                              Consumer ── ffmpeg transcode (3분)
                                        │
                                        └── DB update(status="ready")
```

핵심 이점:
- **시간적 디커플링**: 업로드와 트랜스코딩이 시간적으로 분리
- **공간적 디커플링**: 업로드 서버와 트랜스코딩 워커가 물리적으로 분리 가능
- **실패 격리**: 트랜스코딩 실패가 업로드에 영향 없음
- **재시도**: 메시지가 큐에 남아있으므로 실패 시 재처리 가능
- **독립 스케일링**: 업로드 트래픽과 트랜스코딩 부하를 독립적으로 스케일

#### 이벤트 드리븐 vs 이벤트 소싱

혼동하기 쉬운 두 개념:

| 특성 | 이벤트 드리븐 아키텍처 | 이벤트 소싱 |
|------|---------------------|-----------|
| 목적 | 컴포넌트 간 비동기 통신 | 상태 변경 이력 저장 |
| 이벤트 역할 | 알림/트리거 | **상태의 원천(source of truth)** |
| 상태 저장 | DB에 현재 상태 저장 | 이벤트 로그에서 상태 재구성 |
| 복잡도 | 낮음 | 높음 (스냅샷, 프로젝션 필요) |

이 프로젝트는 **이벤트 드리븐 아키텍처**를 사용한다. 이벤트는 트리거 역할이고, 상태의 원천은 PostgreSQL이다.

### 3.2 Apache Kafka 내부 구조

Kafka는 **분산 커밋 로그(distributed commit log)**다. 메시지 큐가 아니라 로그라는 점이 RabbitMQ 등과의 근본적 차이다.

#### 토픽과 파티션

```
Topic: "media-events"
┌─────────────────────────────────────────────────┐
│ Partition 0: [msg0][msg3][msg6][msg9] ...       │ → offset 0,1,2,3...
│ Partition 1: [msg1][msg4][msg7][msg10] ...      │ → offset 0,1,2,3...
│ Partition 2: [msg2][msg5][msg8][msg11] ...      │ → offset 0,1,2,3...
└─────────────────────────────────────────────────┘
```

- **토픽**: 메시지의 논리적 카테고리
- **파티션**: 토픽의 물리적 분할 단위. 각 파티션은 **순서가 보장되는 불변 로그**
- **오프셋**: 파티션 내 메시지의 고유 순번 (단조 증가)

파티션이 중요한 이유:
1. **병렬 처리**: 파티션 수 = 최대 병렬 소비자 수
2. **순서 보장**: 동일 파티션 내에서만 순서 보장
3. **수평 확장**: 파티션을 여러 브로커에 분산

#### 프로듀서 — 파티셔닝 전략

```go
// kafka.go — 프로듀서 설정
writer: &kafka.Writer{
    Addr:         kafka.TCP(cfg.Brokers...),
    Topic:        cfg.Topic,
    Balancer:     &kafka.LeastBytes{},    // 파티셔닝 전략
    BatchTimeout: 10 * time.Millisecond,  // 배치 타임아웃
    RequiredAcks: kafka.RequireOne,       // ACK 정책
}
```

**파티셔닝 전략:**

| 전략 | 동작 | 용도 |
|------|------|------|
| Round Robin | 순차적으로 파티션 분배 | 균등 분산 |
| Key Hash | `hash(key) % partitions` | **동일 키 → 동일 파티션** (순서 보장) |
| **LeastBytes** | 가장 적은 바이트를 받은 파티션 | **부하 균등화** |

프로젝트에서 `LeastBytes`를 사용하지만, 메시지 키로 `media_id`를 설정한다:

```go
// kafka.go — 메시지 발행
p.writer.WriteMessages(ctx, kafka.Message{
    Key:   []byte(evt.MediaID),  // 동일 미디어의 이벤트는 동일 파티션
    Value: data,
})
```

이렇게 하면 동일 미디어에 대한 `uploaded` → `transcode.completed` 이벤트가 **동일 파티션에서 순서대로** 처리된다.

#### ACK 정책

```
RequiredAcks 옵션:

RequireNone (acks=0):
  Producer ──▶ Broker (응답 안 기다림)
  → 최고 처리량, 메시지 유실 가능

RequireOne (acks=1):        ← 프로젝트 선택
  Producer ──▶ Leader Broker ──▶ ACK
  → 리더 기록 확인, 리더 장애 시 유실 가능

RequireAll (acks=-1):
  Producer ──▶ Leader ──▶ Followers 복제 ──▶ ACK
  → 최고 내구성, 지연시간 증가
```

프로젝트에서 `RequireOne`을 선택한 이유:
- 트랜스코딩 이벤트는 유실되어도 재업로드로 복구 가능
- `RequireAll` 대비 지연시간 50~100ms 절감
- 단일 브로커 환경에서는 `RequireOne` = `RequireAll`

#### 컨슈머 그룹

```
Consumer Group: "transcode-workers"

                    Topic: "media-events"
                    ┌──────────┬──────────┬──────────┐
                    │Partition0│Partition1│Partition2│
                    └────┬─────┴────┬─────┴────┬─────┘
                         │          │          │
                    ┌────▼────┐┌────▼────┐┌────▼────┐
                    │Consumer0││Consumer1││Consumer2│
                    └─────────┘└─────────┘└─────────┘
                    └──────── Consumer Group ────────┘
```

컨슈머 그룹의 핵심 규칙:
1. **하나의 파티션은 그룹 내 하나의 컨슈머에만 할당**
2. 컨슈머 수 > 파티션 수이면, 초과 컨슈머는 유휴 상태
3. 컨슈머 추가/제거 시 **리밸런싱** 발생

```go
// kafka.go — 컨슈머 설정
reader: kafka.NewReader(kafka.ReaderConfig{
    Brokers:        cfg.Brokers,
    Topic:          cfg.Topic,
    GroupID:        "transcode-workers",  // 컨슈머 그룹
    MinBytes:       1,                    // 최소 페치 크기
    MaxBytes:       10e6,                 // 최대 페치 크기 (10MB)
    CommitInterval: time.Second,          // 오프셋 커밋 주기
    StartOffset:    kafka.FirstOffset,    // 처음부터 읽기
})
```

#### 오프셋 관리

```
Partition 0: [0][1][2][3][4][5][6][7][8][9]
                          ↑              ↑
                    committed offset   latest offset
                    (여기까지 처리 완료)  (최신 메시지)

Consumer lag = latest offset - committed offset = 5
```

`CommitInterval: time.Second`는 1초마다 자동으로 오프셋을 커밋한다. 이는 **at-least-once** 시맨틱스를 의미한다:

- 메시지 처리 후 커밋 전에 크래시 → 재시작 시 같은 메시지 재처리
- 트랜스코딩은 **멱등(idempotent)** 연산이므로 재처리해도 안전

### 3.3 전달 보장 시맨틱스

| 시맨틱스 | 설명 | 구현 방법 | 프로젝트 적용 |
|---------|------|----------|-------------|
| At-most-once | 최대 1번 전달, 유실 가능 | 처리 전 커밋 | ❌ |
| **At-least-once** | 최소 1번 전달, 중복 가능 | **처리 후 커밋** | **✅ 현재** |
| Exactly-once | 정확히 1번 전달 | 트랜잭션 + 멱등 프로듀서 | 불필요 |

**At-least-once가 적합한 이유:**

```
시나리오: 트랜스코딩 중 서버 크래시

1. Consumer가 "media.uploaded" 메시지 수신
2. ffmpeg 트랜스코딩 시작 (3분 소요)
3. 2분 경과 시점에 서버 크래시
4. 오프셋 미커밋 → 재시작 시 같은 메시지 재수신
5. 트랜스코딩 재시작 (처음부터)

결과: 동일 미디어가 2번 트랜스코딩됨
      → S3에 같은 파일 덮어쓰기 (멱등)
      → DB status 같은 값으로 업데이트 (멱등)
      → 문제 없음
```

Exactly-once가 불필요한 이유: 트랜스코딩의 결과물(S3 파일, DB 상태)이 멱등이므로, 중복 처리의 부작용이 없다. Exactly-once는 Kafka 트랜잭션 오버헤드를 추가하므로 불필요한 복잡도다.

### 3.4 Kafka vs 대안 비교

| 특성 | Kafka | RabbitMQ | Redis Streams |
|------|-------|----------|---------------|
| 모델 | 분산 로그 | 메시지 브로커 | 인메모리 스트림 |
| 메시지 보존 | **영구 (설정 기간)** | 소비 후 삭제 | TTL 기반 |
| 처리량 | **100만+ msg/s** | 10만 msg/s | 50만 msg/s |
| 순서 보장 | 파티션 내 보장 | 큐 내 보장 | 스트림 내 보장 |
| 리플레이 | **가능 (오프셋 리셋)** | 불가 | 가능 |
| 컨슈머 그룹 | 네이티브 | 수동 구현 | 네이티브 |
| 운영 복잡도 | 높음 | 중간 | 낮음 |

프로젝트에서 Kafka를 선택한 이유:
1. **메시지 리플레이**: 트랜스코딩 실패 시 오프셋 리셋으로 재처리 가능
2. **영구 보존**: 이벤트 히스토리 유지 (감사 로그, 디버깅)
3. **확장성**: 차량 수 증가 시 파티션 추가로 수평 확장
4. **기술 스택 정합성**: 대규모 차량 텔레메트리 처리에 Kafka가 업계 표준

### 3.5 프로젝트의 이벤트 흐름

```
┌─────────────────────────────────────────────────────────────┐
│                    Kafka Topic: "media-events"               │
│                                                              │
│  ┌──────────────────┐     ┌──────────────────────────┐      │
│  │ media.uploaded    │     │ media.transcode.completed │      │
│  │ {                 │     │ {                         │      │
│  │   media_id: "abc" │     │   media_id: "abc"         │      │
│  │   key: "orig/abc" │     │   format: "hls"           │      │
│  │   timestamp: ...  │     │   timestamp: ...          │      │
│  │ }                 │     │ }                         │      │
│  └────────┬─────────┘     └──────────┬───────────────┘      │
│           │                          │                       │
└───────────┼──────────────────────────┼───────────────────────┘
            │                          │
            ▼                          ▼
   Consumer Group              (향후 확장 포인트)
   "transcode-workers"         - 알림 서비스
   → handleTranscodeEvent()    - 썸네일 생성
   → ffmpeg ABR 인코딩          - 메타데이터 추출
   → S3 업로드                  - CDN 캐시 워밍
   → DB 상태 업데이트
```

이벤트 스키마 설계 원칙:
- **이벤트 이름은 과거형**: "uploaded" (완료된 사실), "completed" (완료된 사실)
- **최소 필요 데이터만 포함**: 전체 미디어 객체가 아닌 ID + 키만
- **타임스탬프 필수**: 이벤트 순서 판단, 디버깅, 메트릭

---


## 4. 오브젝트 스토리지 & CDN 전략

### 4.1 오브젝트 스토리지 vs 파일시스템

미디어 파일 저장에 로컬 파일시스템 대신 오브젝트 스토리지를 사용하는 이유:

| 특성 | 로컬 파일시스템 | 오브젝트 스토리지 (S3) |
|------|---------------|---------------------|
| 확장성 | 단일 디스크/서버 한계 | **무제한 수평 확장** |
| 가용성 | 서버 장애 = 데이터 유실 | 자동 복제 (99.999999999% 내구성) |
| 접근 방식 | POSIX (open/read/write/seek) | **HTTP REST API** (PUT/GET/DELETE) |
| 메타데이터 | 파일명, 권한, 타임스탬프 | **커스텀 메타데이터** (Content-Type 등) |
| 비용 | 프로비저닝 기반 | **사용량 기반** |
| CDN 연동 | 별도 구성 필요 | **네이티브 통합** (CloudFront 등) |

미디어 서비스에서 특히 중요한 점:
- 원본 MP4 + HLS 세그먼트 + DASH 세그먼트 = 원본 대비 **3~5배 스토리지** 필요
- 트랜스코딩 결과물은 **write-once, read-many** 패턴 → 오브젝트 스토리지에 최적

### 4.2 S3 API와 MinIO

S3 API는 사실상 오브젝트 스토리지의 **표준 인터페이스**가 되었다. MinIO, GCS, Azure Blob 모두 S3 호환 API를 제공한다.

핵심 연산:

```
PutObject(bucket, key, data, metadata)  → 업로드
GetObject(bucket, key)                  → 다운로드
DeleteObject(bucket, key)               → 삭제
ListObjects(bucket, prefix)             → 목록 조회
PresignedGetObject(bucket, key, expiry) → 임시 URL 생성
```

프로젝트의 Storage 인터페이스:

```go
// storage.go — S3 구현체를 추상화
type Storage interface {
    Upload(ctx, key, reader, size, contentType) error
    Download(ctx, key) (io.ReadCloser, error)
    PresignedURL(ctx, key, expires) (*url.URL, error)
    Delete(ctx, key) error
}
```

이 인터페이스 설계의 의도:
- **MinIO → AWS S3 교체 시 코드 변경 제로**: 구현체만 교체
- **테스트 용이**: mock 구현으로 단위 테스트 가능
- **Go 관용구**: "Accept interfaces, return structs"

### 4.3 키 설계 (Key Naming)

```
media-files/                          ← 버킷
├── originals/
│   └── {uuid}.mp4                    ← 원본 파일
└── transcoded/
    └── {uuid}/
        ├── hls/
        │   ├── master.m3u8           ← HLS 마스터 플레이리스트
        │   ├── v0/playlist.m3u8      ← 720p variant
        │   ├── v0/init_0.mp4         ← init segment
        │   ├── v0/seg_000.m4s        ← media segment
        │   └── ...
        └── dash/
            ├── manifest.mpd          ← DASH MPD
            ├── init-0.m4s
            └── seg-0-00001.m4s
```

키 설계 원칙:
- **UUID 기반**: 충돌 없는 고유 식별
- **계층적 프리픽스**: `originals/` vs `transcoded/` → 라이프사이클 정책 분리 가능
- **포맷별 분리**: `hls/` vs `dash/` → 독립적 삭제/갱신

### 4.4 프리사인드 URL과 CDN 전략

현재 프로젝트는 **S3 프록시** 방식으로 미디어를 서빙한다:

```
Browser ──GET /api/media/{id}/hls/seg.m4s──▶ Go Server ──GetObject──▶ MinIO
Browser ◀──────── video data ──────────────── Go Server ◀──────────── MinIO
```

프로덕션 환경에서의 확장 옵션:

**옵션 1: Presigned URL 리다이렉트**
```
Browser ──GET /api/media/{id}/hls/seg.m4s──▶ Go Server
Browser ◀── 302 Redirect (presigned S3 URL) ── Go Server
Browser ──GET presigned URL──▶ S3/MinIO (직접 다운로드)
```
- 서버 대역폭 절감, 서버는 URL 생성만 담당
- 단점: CORS 설정 필요, URL 노출

**옵션 2: CDN (CloudFront) 연동**
```
Browser ──GET──▶ CloudFront ──cache miss──▶ S3 Origin
Browser ◀────── CloudFront ◀──────────────── S3
                    │
                    └── cache hit → 즉시 응답 (엣지 서버)
```
- 글로벌 엣지 캐싱, 최저 지연시간
- HLS/DASH 세그먼트는 **불변(immutable)** → 캐시 적중률 극대화
- `Cache-Control: public, max-age=31536000` (1년) 설정 가능

**옵션 3: 현재 방식 (S3 프록시)**
- 단순, 인증 통합 용이, 단일 엔드포인트
- 소규모/데모에 적합, 대규모 시 서버가 병목

### 4.5 미디어 서빙 최적화

프로젝트의 `serveMediaFile()`에서 적용된/적용 가능한 최적화:

```go
// handler.go — Content-Type 매핑
switch filepath.Ext(file) {
case ".m3u8": w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
case ".mpd":  w.Header().Set("Content-Type", "application/dash+xml")
case ".m4s":  w.Header().Set("Content-Type", "video/iso.segment")
}
w.Header().Set("Access-Control-Allow-Origin", "*")  // CORS
```

추가 최적화 포인트:
- **Byte-Range 요청**: 대용량 세그먼트의 부분 다운로드 (HTTP 206)
- **ETag/If-None-Match**: 조건부 요청으로 불필요한 전송 방지
- **Gzip**: m3u8/mpd 매니페스트는 텍스트 → 압축 효과 큼 (70~80% 절감)
- **Connection Keep-Alive**: HLS 세그먼트 연속 요청 시 TCP 핸드셰이크 절감

---


## 5. 데이터베이스 설계 & 상태 관리

### 5.1 스키마 설계

```sql
CREATE TABLE IF NOT EXISTS media (
    id           TEXT PRIMARY KEY,        -- UUID v4
    filename     TEXT NOT NULL,           -- 원본 파일명
    s3_key       TEXT NOT NULL,           -- S3 오브젝트 키
    content_type TEXT DEFAULT '',         -- MIME 타입
    size         BIGINT DEFAULT 0,        -- 바이트 단위
    status       TEXT DEFAULT 'uploaded', -- 상태 머신
    hls_key      TEXT DEFAULT '',         -- HLS 마스터 플레이리스트 키
    dash_key     TEXT DEFAULT '',         -- DASH MPD 키
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
);
```

설계 결정:
- **TEXT PK (UUID)**: auto-increment 대신 UUID → 분산 환경에서 충돌 없는 ID 생성, 클라이언트 측 생성 가능
- **status TEXT**: enum 대신 text → 마이그레이션 없이 상태 추가 가능 (트레이드오프: 타입 안전성 감소)
- **TIMESTAMPTZ**: timezone-aware 타임스탬프 → 글로벌 서비스에서 시간대 혼동 방지

### 5.2 상태 머신 패턴

미디어의 라이프사이클을 **유한 상태 머신(FSM)**으로 모델링:

```
                    ┌──────────┐
                    │ uploaded │ ← 초기 상태 (업로드 완료)
                    └────┬─────┘
                         │ Kafka 이벤트 수신
                         ▼
                    ┌──────────────┐
                    │ transcoding  │ ← 트랜스코딩 진행 중
                    └──┬────────┬──┘
                       │        │
              성공      │        │ 실패
                       ▼        ▼
                  ┌───────┐ ┌────────┐
                  │ ready │ │ failed │
                  └───────┘ └────────┘
```

상태 전이 규칙:
- `uploaded → transcoding`: Consumer가 이벤트 수신 시
- `transcoding → ready`: 트랜스코딩 + S3 업로드 성공 시
- `transcoding → failed`: 어느 단계든 에러 발생 시
- **역방향 전이 없음**: 한번 `ready`/`failed`면 되돌리지 않음

```go
// main.go — 상태 전이 구현
repo.UpdateStatus(ctx, evt.MediaID, "transcoding")  // uploaded → transcoding
// ... ffmpeg 실행 ...
repo.UpdateTranscodeResult(ctx, id, hlsKey, dashKey) // transcoding → ready
// 또는
repo.UpdateStatus(ctx, id, "failed")                 // transcoding → failed
```

프로덕션 개선 포인트:
- **상태 전이 검증**: `UPDATE ... WHERE status = 'transcoding'` 조건으로 잘못된 전이 방지
- **이력 테이블**: `media_status_history(media_id, from_status, to_status, timestamp)` 추가
- **재시도 카운터**: `retry_count` 컬럼으로 무한 재시도 방지

### 5.3 커넥션 풀링

```go
// postgres.go — 커넥션 풀 설정
db.SetMaxOpenConns(25)          // 최대 동시 연결
db.SetMaxIdleConns(5)           // 유휴 연결 유지
db.SetConnMaxLifetime(5 * time.Minute)  // 연결 최대 수명
```

이 설정의 근거:

| 파라미터 | 값 | 이유 |
|---------|---|------|
| MaxOpenConns | 25 | PostgreSQL 기본 max_connections=100, 여유분 확보 |
| MaxIdleConns | 5 | 평상시 트래픽에 충분, 메모리 절약 |
| ConnMaxLifetime | 5m | 오래된 연결 정리, DNS 변경 반영 |

커넥션 풀이 없으면:
```
요청마다: TCP 3-way handshake + TLS + PostgreSQL 인증 = ~50ms 오버헤드
풀 사용:  이미 열린 연결 재사용 = ~0ms 오버헤드
```

### 5.4 마이그레이션 전략

현재: **자동 마이그레이션** (`CREATE TABLE IF NOT EXISTS`)

```go
func (r *Repository) migrate(ctx context.Context) error {
    _, err := r.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS media (...)`)
    return err
}
```

장점: 별도 도구 불필요, 서버 시작 시 자동 실행
단점: 스키마 변경 이력 없음, 컬럼 추가/변경 불가, 롤백 불가

프로덕션 권장: **버전 기반 마이그레이션** (golang-migrate, goose 등)

```
migrations/
├── 001_create_media.up.sql
├── 001_create_media.down.sql
├── 002_add_thumbnail_key.up.sql
└── 002_add_thumbnail_key.down.sql
```

### 5.5 인덱싱 전략

현재 스키마에서 필요한 인덱스:

```sql
-- PK 인덱스 (자동 생성)
-- id TEXT PRIMARY KEY → B-tree 인덱스

-- 목록 조회 최적화 (ORDER BY created_at DESC)
CREATE INDEX idx_media_created_at ON media(created_at DESC);

-- 상태별 필터링 (트랜스코딩 대기 목록 등)
CREATE INDEX idx_media_status ON media(status) WHERE status != 'ready';
-- partial index: ready가 대부분이므로, 나머지만 인덱싱
```

B-tree vs Hash 인덱스:
- **B-tree**: 범위 검색, 정렬 지원 → `created_at`, `status` 적합
- **Hash**: 등가 검색만 → `id` 조회에 적합하나 PK가 이미 B-tree

### 5.6 트랜잭션 격리 수준

PostgreSQL 기본: **Read Committed**

이 프로젝트에서 충분한 이유:
- 단일 행 UPDATE가 대부분 (상태 변경)
- 동일 미디어를 동시에 트랜스코딩하는 경우 없음 (Kafka 파티션 키로 보장)
- Phantom read가 문제되는 쿼리 없음

더 높은 격리가 필요한 시나리오:
- **Repeatable Read**: 트랜스코딩 결과 업로드 중 다른 트랜잭션이 상태를 변경하면 안 되는 경우
- **Serializable**: 동시 업로드 시 중복 파일명 검사가 필요한 경우

---


## 6. Go 동시성 패턴

### 6.1 goroutine과 channel — Go 동시성의 기본 단위

Go의 동시성 모델은 CSP(Communicating Sequential Processes)에 기반한다. 핵심 철학: **"메모리를 공유하여 통신하지 말고, 통신하여 메모리를 공유하라."**

- **goroutine**: 경량 스레드 (~2KB 스택, OS 스레드 대비 1000배 가벼움)
- **channel**: goroutine 간 타입 안전한 통신 파이프

이 프로젝트에서 사용된 동시성 패턴 4가지를 분석한다.

### 6.2 Worker Pool 패턴

트랜스코딩은 CPU 집약적 작업이다. 무제한 goroutine 생성은 시스템을 과부하시킨다.

```go
// transcoder.go — Worker Pool 구현
type Transcoder struct {
    workers int
    jobs    chan Job     // 작업 큐 (buf:100)
    results chan Result  // 결과 큐 (buf:100)
    wg      sync.WaitGroup
}
```

구조:
```
Submit(Job) ──▶ [jobs chan, buf:100] ──▶ worker-0 ──▶ [results chan, buf:100]
                                        worker-1
                                        ↑
                                        ctx.Done() → 종료
```

설계 결정:
- **workers=2**: ffmpeg은 멀티코어 사용 → 2개 동시 트랜스코딩이 4코어 서버에 적합
- **buffered channel(100)**: 업로드 burst 흡수, 워커가 바쁠 때 큐잉
- **WaitGroup**: `Stop()` 시 모든 워커 완료 대기

버퍼 크기 선택 근거:
```
버퍼 0:   Submit()이 워커가 빌 때까지 블로킹 → 업로드 API 지연
버퍼 100: 100개 작업 큐잉 가능 → 업로드 API 즉시 응답
버퍼 ∞:   메모리 무한 증가 위험
```

### 6.3 Fan-out 패턴

라이브 스트리밍에서 하나의 RTSP 소스를 다수 WebRTC 시청자에게 분배:

```go
// ingest.go — Fan-out
func (c *Channel) fanOut(pkt *rtp.Packet) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    for _, fn := range c.subs {
        clone := &rtp.Packet{
            Header:  pkt.Header.Clone(),
            Payload: make([]byte, len(pkt.Payload)),
        }
        copy(clone.Payload, pkt.Payload)
        fn(clone)
    }
}
```

핵심 설계:
- **RWMutex**: 읽기(fan-out)는 동시 허용, 쓰기(구독자 추가/제거)만 배타적
- **Deep Copy**: gortsplib이 버퍼를 재사용하므로 반드시 페이로드 복사 필요
- **비동기 콜백**: `fn(clone)`이 채널에 send → 느린 소비자가 fan-out을 블로킹하지 않음

```go
// Subscribe의 콜백 — 128 버퍼 + drop 정책
ch := make(chan *rtp.Packet, 128)
id := c.addSubscriber(func(pkt *rtp.Packet) {
    select {
    case ch <- pkt:
    default: // 버퍼 풀 → 패킷 드롭 (실시간 우선)
    }
})
```

이 drop 정책은 **실시간 스트리밍의 핵심 트레이드오프**: 느린 시청자 한 명이 전체 스트림을 지연시키는 것보다, 해당 시청자의 프레임을 건너뛰는 것이 낫다.

### 6.4 Context 기반 취소

Go의 `context.Context`는 goroutine 트리 전체에 취소 신호를 전파한다:

```go
// main.go — context 계층
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

transcoder.Start(ctx)           // ctx 전달
go consumer.Consume(ctx, ...)   // ctx 전달

// 셧다운 시
cancel()  // → 모든 goroutine에 취소 전파
```

워커에서의 수신:
```go
// transcoder.go — context 감시
select {
case <-ctx.Done():
    return  // 셧다운 신호 → 워커 종료
case job, ok := <-t.jobs:
    if !ok { return }
    t.results <- t.process(ctx, job)
}
```

ffmpeg 프로세스도 context로 제어:
```go
cmd := exec.CommandContext(ctx, "ffmpeg", args...)
// ctx 취소 시 ffmpeg 프로세스에 SIGKILL 전송
```

### 6.5 Graceful Shutdown

서버 종료 시 진행 중인 작업을 안전하게 완료하는 패턴:

```go
// main.go — 시그널 핸들링
quit := make(chan os.Signal, 1)
signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
sig := <-quit  // 시그널 대기

cancel()  // 1. 컨슈머 + 트랜스코더 중지 신호

shutdownCtx, _ := context.WithTimeout(context.Background(), 15*time.Second)
srv.Shutdown(shutdownCtx)  // 2. HTTP 서버: 새 요청 거부, 진행 중 요청 완료 대기

transcoder.Stop()  // 3. 워커 풀: jobs 채널 닫기, WaitGroup 대기
```

종료 순서가 중요하다:
```
1. cancel()         → 새 작업 수신 중지
2. srv.Shutdown()   → 진행 중 HTTP 요청 완료 (최대 15초)
3. transcoder.Stop() → 진행 중 트랜스코딩 완료
4. defer repo.Close() → DB 연결 정리
5. defer producer.Close() → Kafka 프로듀서 플러시
```

**15초 타임아웃**: 트랜스코딩은 수 분 소요되므로, 셧다운 시 진행 중인 트랜스코딩은 중단된다. 이는 at-least-once 시맨틱스로 보완: 재시작 시 Kafka에서 미커밋 메시지를 재처리한다.

---


## 7. WebRTC 시그널링 & 미디어 전송

### 7.1 WebRTC 연결 수립 과정

WebRTC 연결은 4단계로 이루어진다: **시그널링 → ICE → DTLS → SRTP**

```
Browser                              Go Server (pion/webrtc)
   │                                       │
   │ 1. SDP Offer (HTTP POST)             │
   │──────────────────────────────────────▶│
   │                                       │── PeerConnection 생성
   │                                       │── Track 추가
   │                                       │── SetRemoteDescription(offer)
   │                                       │── CreateAnswer()
   │                                       │── ICE Gathering (STUN)
   │◀──────────────────────────────────────│
   │ 2. SDP Answer (HTTP Response)         │
   │                                       │
   │ 3. ICE Connectivity Check             │
   │◀═════════════════════════════════════▶│
   │    STUN Binding Request/Response      │
   │                                       │
   │ 4. DTLS Handshake                     │
   │◀═════════════════════════════════════▶│
   │    Certificate exchange, key derivation│
   │                                       │
   │ 5. SRTP Media Flow                    │
   │◀══════════════════════════════════════│
   │    H.264 RTP packets (encrypted)      │
```

### 7.2 SDP (Session Description Protocol)

SDP는 미디어 세션의 파라미터를 기술하는 텍스트 프로토콜(RFC 8866):

```
v=0                                    ← 프로토콜 버전
o=- 12345 2 IN IP4 0.0.0.0           ← 세션 식별
s=-                                    ← 세션 이름
t=0 0                                  ← 시간 (0=영구)
a=group:BUNDLE 0                       ← 미디어 번들링

m=video 9 UDP/TLS/RTP/SAVPF 96 97 ... ← 미디어 라인
a=mid:0                                ← 미디어 ID
a=rtpmap:96 VP8/90000                  ← PT 96 = VP8
a=rtpmap:103 H264/90000               ← PT 103 = H264
a=fmtp:103 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f
a=rtcp-fb:103 nack                     ← NACK 피드백 지원
a=rtcp-fb:103 nack pli                 ← PLI (Picture Loss Indication)
a=rtcp-fb:103 transport-cc             ← 전송 혼잡 제어
a=setup:active                         ← DTLS 역할
a=ice-ufrag:xxxx                       ← ICE 인증 정보
a=ice-pwd:yyyy
a=fingerprint:sha-256 AA:BB:CC:...     ← DTLS 인증서 핑거프린트
```

**Offer/Answer 모델:**
- Browser offer: 지원하는 모든 코덱 나열 (VP8, VP9, H264 여러 프로파일)
- Server answer: 하나의 코덱 선택 (H264 baseline) → **코덱 협상 완료**

프로젝트에서 서버가 H264만 지원하도록 트랙을 생성:
```go
track, _ := webrtc.NewTrackLocalStaticRTP(
    webrtc.RTPCodecCapability{
        MimeType:    webrtc.MimeTypeH264,
        ClockRate:   90000,
        SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
    }, "video", "live-"+channel,
)
```

### 7.3 ICE (Interactive Connectivity Establishment)

ICE(RFC 8445)는 NAT 뒤의 두 피어가 서로를 찾는 프로토콜이다.

**Candidate 타입:**

| 타입 | 설명 | 우선순위 |
|------|------|---------|
| host | 로컬 IP 주소 | 최고 |
| srflx | STUN으로 발견한 공인 IP | 중간 |
| relay | TURN 서버 경유 | 최저 |

**ICE Gathering 과정:**
```
1. 로컬 인터페이스 열거 → host candidate
2. STUN 서버에 Binding Request → srflx candidate (공인 IP 발견)
3. (필요시) TURN 서버에 Allocate → relay candidate
4. 모든 candidate를 SDP에 포함하여 상대방에게 전달
```

프로젝트에서 ICE gathering 완료를 기다리는 이유:
```go
gatherDone := webrtc.GatheringCompletePromise(pc)
pc.SetLocalDescription(answer)
<-gatherDone  // 모든 candidate 수집 완료 대기
```

Trickle ICE(candidate를 하나씩 전송)를 사용하지 않고 **full ICE**(모든 candidate를 SDP에 포함)를 사용한다. HTTP POST 한 번으로 시그널링을 완료하기 위함이다.

### 7.4 DTLS-SRTP

**DTLS (Datagram TLS)**: UDP 위의 TLS. WebRTC에서 키 교환에 사용.
**SRTP (Secure RTP)**: DTLS에서 파생된 키로 RTP 패킷을 암호화.

```
DTLS Handshake (UDP):
  ClientHello → ServerHello → Certificate → KeyExchange → Finished
  → 양쪽이 동일한 master secret 공유
  → SRTP 암호화 키 파생 (DTLS-SRTP key derivation)

이후 모든 RTP 패킷:
  RTP Header (평문) + Encrypted Payload + Authentication Tag
```

SDP의 `a=fingerprint`는 DTLS 인증서의 해시값이다. 시그널링 채널(HTTP)을 통해 교환된 fingerprint로 DTLS 인증서를 검증하여 MITM 공격을 방지한다.

### 7.5 VOD vs Live WebRTC 구현 차이

| 측면 | VOD (`webrtc.go`) | Live (`live.go`) |
|------|-------------------|------------------|
| 트랙 타입 | `TrackLocalStaticSample` | `TrackLocalStaticRTP` |
| 데이터 소스 | ffmpeg stdout → NAL 파싱 | RTSP RTP 패킷 직접 릴레이 |
| 전송 메서드 | `WriteSample(data, duration)` | `WriteRTP(packet)` |
| 타이밍 | 프레임 duration 명시 (33ms) | RTP timestamp 그대로 사용 |
| 수명 | 파일 재생 완료 시 종료 | 퍼블리셔 연결 해제 시 종료 |

`TrackLocalStaticSample`은 pion이 NAL → RTP 패킷화를 자동 처리한다.
`TrackLocalStaticRTP`는 이미 RTP 패킷이므로 그대로 전달 — **제로 오버헤드**.

### 7.6 VOD WebRTC 파이프라인 상세

VOD WebRTC는 HLS/DASH와 근본적으로 다른 전송 경로를 사용한다. 저장된 세그먼트를 HTTP로 서빙하는 것이 아니라, **실시간으로 MP4를 H.264 Annex B로 변환하여 RTP로 전송**한다.

#### 전체 파이프라인

```
S3 (MinIO)                    Go Server                         Browser
┌──────────┐    ┌──────────────────────────────────────┐    ┌──────────┐
│ 원본 MP4  │───▶│ tmpFile ──▶ ffmpeg ──▶ stdout (pipe) │    │ WebRTC   │
│ (AVCC)   │    │                          │            │    │ Player   │
└──────────┘    │                    h264reader          │    │          │
                │                    NextNAL()           │    │          │
                │                          │            │    │          │
                │                    sendNALUnits()     │    │          │
                │                    WriteSample()      │    │          │
                │                          │            │    │          │
                │                    pion Packetizer    │    │          │
                │                    NAL → RTP 패킷     │    │          │
                │                          │            │    │          │
                │                    SRTP 암호화        │───▶│ 디코딩    │
                │                                       │    │ 렌더링    │
                └──────────────────────────────────────┘    └──────────┘
```

#### WriteSample의 내부 동작

`TrackLocalStaticSample.WriteSample()`은 pion이 제공하는 고수준 API로, Annex B NAL 데이터를 받아서 RTP 패킷화까지 자동 처리한다:

```
WriteSample(data, duration)
  │
  ├─ 1. H264Payloader.Payload()
  │     NAL 크기에 따라:
  │     - ≤ MTU(1200B): Single NAL Unit → RTP 1개
  │     - > MTU: FU-A 분할 → RTP N개
  │     - 여러 작은 NAL: STAP-A 집합 → RTP 1개
  │
  ├─ 2. Packetizer.Packetize()
  │     - RTP 헤더 생성 (SSRC, PT, Sequence Number)
  │     - Timestamp 계산: += duration × clockRate
  │     - 마지막 패킷에 Marker bit 설정
  │
  └─ 3. WriteRTP() × N
        각 RTP 패킷을 DTLS-SRTP로 암호화하여 전송
```

**Duration 파라미터의 의미:**

```go
track.WriteSample(media.Sample{Data: data, Duration: 33 * time.Millisecond})
```

Duration은 RTP timestamp 증분을 결정한다:
```
RTP timestamp 증분 = duration × clockRate
                   = 0.033s × 90000Hz
                   = 2970

프레임 0: timestamp = 0
프레임 1: timestamp = 2970
프레임 2: timestamp = 5940
...
```

디코더는 이 timestamp로 프레임 표시 시점을 결정한다. Duration이 잘못되면 재생 속도가 비정상적이 된다.

#### 프레임 페이싱 (Ticker)

ffmpeg가 `-re` 없이 실행되면 인코딩 결과를 가능한 빨리 stdout에 출력한다. 10초 영상이 1~2초 만에 모두 출력될 수 있다. 이를 실시간 속도로 조절하는 것이 ticker의 역할:

```go
ticker := time.NewTicker(33 * time.Millisecond) // 30fps 페이싱

case 5: // IDR
    <-ticker.C  // 33ms 대기 → 실시간 속도 유지
    track.WriteSample(media.Sample{Data: data, Duration: frameDuration})
```

`-re` 옵션을 ffmpeg에 주는 대신 Go 코드에서 페이싱하는 이유:
- `-re`는 입력 파일의 타임스탬프 기준으로 읽기 속도를 제한 → 인코딩 파이프라인 전체가 느려짐
- Go ticker는 **출력 측에서만** 속도를 제한 → ffmpeg는 최대 속도로 인코딩, 버퍼에 축적
- 결과: 첫 프레임 출력까지의 지연이 줄어듦

#### SPS+PPS+IDR AU 묶기

H.264 디코더가 IDR 프레임을 디코딩하려면 SPS와 PPS가 먼저 필요하다. 이들을 하나의 **Access Unit(AU)**으로 묶어서 같은 RTP timestamp로 전송해야 한다:

```go
case 7, 8, 6: // SPS, PPS, SEI
    auBuf = append(auBuf, annexB...)  // 버퍼에 축적

case 5: // IDR
    data := append(auBuf, annexB...)  // SPS+PPS+IDR 합체
    track.WriteSample(...)            // 하나의 샘플로 전송
    auBuf = auBuf[:0]                // 버퍼 리셋
```

이렇게 하면 pion의 Packetizer가 SPS+PPS+IDR을 같은 timestamp의 RTP 패킷들로 변환한다. 디코더는 이 패킷들을 하나의 AU로 인식하여 정상 디코딩한다.

### 7.7 ICE 내부망 최적화와 STUN

#### STUN의 역할

STUN(Session Traversal Utilities for NAT, RFC 8489)은 클라이언트가 **자신의 공인 IP와 포트**를 발견하는 프로토콜이다:

```
Client (192.168.1.100:5000)          NAT (203.0.113.1)          STUN Server
        │                                  │                         │
        │── Binding Request ──────────────▶│── Binding Request ─────▶│
        │                                  │   src: 203.0.113.1:40000│
        │                                  │                         │
        │◀── Binding Response ────────────│◀── Binding Response ────│
        │    XOR-MAPPED-ADDRESS:           │    "너의 공인 주소는      │
        │    203.0.113.1:40000             │     203.0.113.1:40000"  │
```

이 정보로 **srflx(Server Reflexive) candidate**를 생성한다. 상대방이 이 공인 주소로 패킷을 보내면 NAT가 내부 클라이언트로 전달한다.

#### 내부망에서 STUN이 불필요한 경우

```
시나리오: 브라우저와 서버가 같은 머신 또는 같은 LAN

Browser (localhost)                    Go Server (localhost)
    │                                       │
    │ host candidate: 127.0.0.1:XXXXX      │ host candidate: 127.0.0.1:YYYYY
    │                                       │
    │◀═══════ ICE Connectivity Check ═════▶│
    │         (직접 연결 성공)               │
```

host candidate만으로 직접 연결이 가능하므로 STUN이 필요 없다. 그러나 **ICE 에이전트가 candidate를 하나도 수집하지 못하면 연결이 실패**할 수 있다. 일부 환경에서 host candidate 수집이 실패하는 경우를 대비하여 내부 STUN 서버를 운영한다.

#### 이 프로젝트의 내부 STUN 서버

외부 STUN 서버(예: `stun:stun.l.google.com:19302`)에 의존하면:
- 인터넷 연결 없는 내부망에서 동작 불가
- Google 서버 장애 시 ICE gathering 실패
- 기업 방화벽이 STUN 포트(3478/UDP)를 차단할 수 있음

해결: pion/turn 라이브러리로 내장 STUN 서버를 `:3478`에 구동:

```go
// internal/stun/stun.go
udpListener, _ := net.ListenPacket("udp4", ":3478")
server, _ := turn.NewServer(turn.ServerConfig{
    PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udpListener}},
})
```

서버 프로세스 하나에 HTTP(:4242) + RTSP(:8554) + STUN(:3478)이 모두 포함된다. 외부 의존성 제로.

#### 브라우저 Autoplay 정책

Chrome 66+부터 적용된 자동재생 정책:
- **음소거된 비디오**: 자동재생 허용
- **음소거되지 않은 비디오**: 사용자 상호작용(클릭 등) 후에만 재생 가능

```html
<!-- 자동재생을 위해 muted 필수 -->
<video id="player" autoplay muted playsinline></video>
```

`playsinline`은 iOS Safari에서 전체화면 전환 없이 인라인 재생을 허용한다. WebRTC 스트림은 `srcObject`로 설정되므로 `src` 속성과 다르게 동작하지만, `autoplay muted`는 여전히 필요하다.

### 7.8 실제 디버깅: VOD WebRTC 화면 깨짐 해결

이 프로젝트에서 VOD WebRTC를 구현하면서 겪은 3단계 디버깅 과정을 기록한다. 각 단계는 서로 다른 계층의 문제였다.

#### 1단계: 검은 화면 — NAL 파싱 버그

```
증상: ICE connected, 하지만 영상이 전혀 표시되지 않음
원인: byte-by-byte 파서가 start code를 strip하지 않아 NAL type=0으로 판정
      → switch문에서 모든 NAL이 무시됨 → WriteSample 호출 0건
해결: pion h264reader.NewReader()로 교체
      → start code 자동 strip, nal.Data[0]이 NAL 헤더 보장
```

#### 2단계: 영상 나오지만 대각선 찢어짐 — 멀티 슬라이스

```
증상: 컬러바가 대각선으로 기울어져 표시됨
원인: -tune zerolatency의 sliced-threads가 프레임당 6~8개 슬라이스 생성
      각 슬라이스를 별도 timestamp로 전송 → 디코더가 프레임 경계 오인식
진단: 서버 로그에서 NAL type=5가 연속 6~8개 출력되는 것을 확인
      → 하나의 IDR 프레임이 여러 슬라이스로 분할된 것
해결: ffmpeg에 -threads 1 추가 → 슬라이스 1개 보장
```

#### 디버깅 방법론

이 경험에서 얻은 WebRTC 미디어 디버깅 체크리스트:

```
1. ICE 연결 확인
   └─ PeerConnection state가 "connected"인가?
   └─ ICE candidate가 수집되었는가? (host/srflx)

2. RTP 전송 확인
   └─ WriteSample/WriteRTP 에러가 없는가?
   └─ NAL type과 size가 합리적인가? (로그 필수)

3. 코덱 협상 확인
   └─ SDP answer에서 H264가 선택되었는가?
   └─ profile-level-id가 매칭되는가?

4. NAL 구조 확인
   └─ SPS → PPS → IDR 순서가 보장되는가?
   └─ 프레임당 NAL 수가 1개인가? (멀티 슬라이스 여부)
   └─ start code가 올바르게 처리되었는가?

5. 타이밍 확인
   └─ Duration이 실제 프레임레이트와 일치하는가?
   └─ 같은 프레임의 NAL들이 같은 timestamp를 가지는가?
```

---


## 8. RTSP 인제스트 & 제로 트랜스코딩 릴레이

### 8.1 RTSP 상태 머신

프로젝트의 RTSP 서버는 **ANNOUNCE/RECORD 모드** (퍼블리셔가 서버에 스트림을 푸시):

```
Client (ffmpeg)                    Server (gortsplib)
    │                                    │
    │── ANNOUNCE (SDP) ─────────────────▶│ OnAnnounce()
    │   "나는 H.264 720p 스트림을 보낼 거야"  │ → Channel 생성
    │◀── 200 OK ─────────────────────────│   streams[path] 등록
    │                                    │
    │── SETUP (transport=TCP) ──────────▶│ OnSetup()
    │   "TCP interleaved로 보낼게"        │ → 전송 파라미터 설정
    │◀── 200 OK ─────────────────────────│
    │                                    │
    │── RECORD ─────────────────────────▶│ OnRecord()
    │   "지금부터 미디어 전송 시작"         │ → OnPacketRTPAny 콜백 등록
    │◀── 200 OK ─────────────────────────│
    │                                    │
    │══ RTP 패킷 (H.264) ══════════════▶│ → fanOut() → WebRTC
    │══ RTP 패킷 ══════════════════════▶│
    │══ ... (연속) ════════════════════▶│
    │                                    │
    │── TEARDOWN ───────────────────────▶│ OnSessionClose()
    │   (또는 TCP 연결 끊김)              │ → Channel 삭제, 구독자 정리
```

### 8.2 TCP Interleaved vs UDP

| 전송 모드 | 장점 | 단점 |
|----------|------|------|
| **TCP interleaved** | NAT 통과 용이, 패킷 손실 없음 | 지연시간 증가 (재전송) |
| UDP | 최저 지연시간 | NAT 문제, 패킷 손실 |

프로젝트에서 TCP를 선택한 이유:
- 차량 → 클라우드 통신은 NAT/방화벽 통과가 필수
- TCP의 재전송 지연(~RTT)은 H.264 디코딩에 큰 영향 없음
- ffmpeg 기본 설정: `-rtsp_transport tcp`

### 8.3 RTP 패킷 딥카피 문제

이 프로젝트에서 겪은 실제 버그:

```
문제: gortsplib의 OnPacketRTPAny 콜백에서 받은 *rtp.Packet의
      Payload 슬라이스가 내부 버퍼를 참조함.
      콜백 리턴 후 gortsplib이 버퍼를 재사용하므로,
      비동기로 WebRTC에 전달하면 이미 덮어쓰여진 데이터를 전송.
```

```go
// 버그 코드 (헤더만 복사)
clone := &rtp.Packet{Header: pkt.Header.Clone(), Payload: pkt.Payload}
// pkt.Payload는 gortsplib 내부 버퍼의 슬라이스 → 재사용됨!

// 수정 코드 (페이로드도 딥카피)
clone := &rtp.Packet{
    Header:  pkt.Header.Clone(),
    Payload: make([]byte, len(pkt.Payload)),
}
copy(clone.Payload, pkt.Payload)
```

이 문제는 Go의 슬라이스 시맨틱스에서 비롯된다:
- 슬라이스는 **underlying array에 대한 참조**
- `clone.Payload = pkt.Payload`는 같은 배열을 가리킴
- gortsplib이 배열을 재사용하면 clone의 데이터도 변경됨

교훈: **비동기 처리 시 공유 데이터는 반드시 딥카피**. 특히 라이브러리 내부 버퍼를 참조하는 슬라이스는 콜백 스코프를 벗어나면 안전하지 않다.

### 8.4 제로 트랜스코딩의 의미

```
일반 릴레이:
  RTSP(H.264) → 디코딩 → 리인코딩 → WebRTC(H.264)
  CPU: 높음, 지연: +100ms, 품질: 손실

제로 트랜스코딩 릴레이 (이 프로젝트):
  RTSP(H.264 RTP) → 패킷 복사 → WebRTC(H.264 RTP)
  CPU: 최소 (memcpy만), 지연: ~0ms 추가, 품질: 무손실
```

가능한 이유: RTSP와 WebRTC 모두 **RFC 6184 (RTP Payload Format for H.264)**를 사용한다. RTP 페이로드의 H.264 패킷화 형식이 동일하므로, 페이로드를 수정 없이 전달할 수 있다.

pion의 `TrackLocalStaticRTP.WriteRTP()`가 처리하는 것:
- SSRC 재작성 (WebRTC 세션의 SSRC로)
- Payload Type 재작성 (SDP 협상된 PT로)
- 페이로드는 **그대로 전달**

### 8.5 SPS/PPS 캐싱 — 프로덕션 해결책

§2에서 분석한 SPS/PPS 문제의 서버 측 해결 방안:

```
현재 (인코더 의존):
  ffmpeg -x264-params repeat-headers=1
  → 인코더가 매 IDR마다 SPS/PPS 반복

프로덕션 (서버 캐싱):
  Channel {
      lastSPS *rtp.Packet
      lastPPS *rtp.Packet
  }

  OnPacketRTP:
    NAL type 7 (SPS) → channel.lastSPS = clone
    NAL type 8 (PPS) → channel.lastPPS = clone
    기타 → fanOut()

  Subscribe():
    if lastSPS != nil { send(lastSPS) }
    if lastPPS != nil { send(lastPPS) }
    → 이후 실시간 릴레이
```

NAL 타입 추출 방법:
```
Single NAL:  payload[0] & 0x1F = NAL type
STAP-A:      payload[0] & 0x1F = 24, 내부 NAL 파싱 필요
FU-A:        payload[0] & 0x1F = 28, payload[1] & 0x1F = 원래 NAL type
```

---


## 9. 트랜스코딩 파이프라인

### 9.1 ffmpeg filter_complex — 멀티 출력 인코딩

하나의 입력에서 3개 해상도를 동시에 생성하는 핵심 기법:

```
입력 MP4 ──▶ [split=3] ──▶ [scale 1280x720] ──▶ libx264 2500k ──▶ v0
                          ├▶ [scale 854x480]  ──▶ libx264 1200k ──▶ v1
                          └▶ [scale 640x360]  ──▶ libx264 600k  ──▶ v2
```

```go
filter := "[0:v]split=3[v0][v1][v2];" +
    "[v0]scale=1280:720[o0];" +
    "[v1]scale=854:480[o1];" +
    "[v2]scale=640:360[o2]"
```

`split` 필터가 중요한 이유:
- 입력을 한 번만 디코딩하고 3개 스트림으로 복제
- 3번 디코딩하는 것 대비 **CPU ~60% 절감**
- 디코딩은 비용이 높고(역DCT, 모션 보상), 스케일링은 상대적으로 저렴

### 9.2 키프레임 정렬 (Keyframe Alignment)

ABR 스트리밍에서 variant 간 전환이 매끄러우려면, **모든 variant의 키프레임이 동일 시점에 위치**해야 한다:

```
v0 (720p): [IDR]----[IDR]----[IDR]----
v1 (480p): [IDR]----[IDR]----[IDR]----   ← 정렬됨
v2 (360p): [IDR]----[IDR]----[IDR]----
           t=0      t=4      t=8

전환 시: v0의 t=4 IDR → v1의 t=4 IDR로 즉시 전환 가능
```

```go
"-force_key_frames", "expr:gte(t,n_forced*4)"
```

`force_key_frames`는 인코더의 자체 키프레임 결정을 무시하고, **정확히 4초 간격**으로 IDR을 강제한다. 이것이 없으면:
- 각 variant가 독립적으로 키프레임 위치를 결정
- 세그먼트 경계가 불일치 → ABR 전환 시 버퍼링 발생

### 9.3 HLS fMP4 출력 구조

```go
"-f", "hls",
"-hls_time", "4",                    // 세그먼트 길이 4초
"-hls_segment_type", "fmp4",         // fMP4 (TS 대신)
"-hls_playlist_type", "vod",         // VOD 모드 (EXT-X-ENDLIST 포함)
"-master_pl_name", "master.m3u8",    // 마스터 플레이리스트 자동 생성
"-var_stream_map", "v:0,a:0 v:1,a:1 v:2,a:2",
```

생성되는 마스터 플레이리스트:
```
#EXTM3U
#EXT-X-VERSION:7
#EXT-X-STREAM-INF:BANDWIDTH=2628000,RESOLUTION=1280x720,CODECS="avc1.640028,mp4a.40.2"
v0/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1328000,RESOLUTION=854x480,CODECS="avc1.640028,mp4a.40.2"
v1/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=696000,RESOLUTION=640x360,CODECS="avc1.640028,mp4a.40.2"
v2/playlist.m3u8
```

variant 플레이리스트:
```
#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MAP:URI="init_0.mp4"          ← fMP4 init segment
#EXTINF:4.000000,
seg_000.m4s                           ← fMP4 media segment
#EXTINF:4.000000,
seg_001.m4s
...
#EXT-X-ENDLIST                        ← VOD 종료 표시
```

### 9.4 DASH MPD 출력 구조

```go
"-f", "dash",
"-seg_duration", "4",
"-adaptation_sets", "id=0,streams=v id=1,streams=a",
```

`adaptation_sets`의 의미:
- `id=0,streams=v`: 비디오 스트림들을 하나의 AdaptationSet으로 그룹화
- `id=1,streams=a`: 오디오 스트림들을 별도 AdaptationSet으로
- 클라이언트가 비디오/오디오를 **독립적으로** ABR 전환 가능

### 9.5 인코딩 파라미터 분석

```go
"-preset", "fast",        // 인코딩 속도 vs 압축 효율
"-maxrate:v:0", "2500k",  // VBV 최대 비트레이트
"-bufsize:v:0", "5000k",  // VBV 버퍼 크기
```

**프리셋 선택 근거:**

| 프리셋 | 속도 | 압축 효율 | 용도 |
|--------|------|----------|------|
| ultrafast | 10x | 낮음 | 실시간 인코딩 |
| **fast** | 5x | **중간** | **VOD 트랜스코딩** |
| medium | 1x (기준) | 높음 | 고품질 VOD |
| slow | 0.5x | 매우 높음 | 아카이브 |

`fast`는 속도와 품질의 균형점. VOD는 실시간이 아니므로 `ultrafast`까지 갈 필요 없고, `medium`은 트랜스코딩 시간이 2배 증가한다.

**VBV (Video Buffering Verifier):**
- `maxrate = bitrate`: CBR에 가까운 출력 → ABR 대역폭 예측 정확도 향상
- `bufsize = 2 × bitrate`: 순간적 비트레이트 변동 허용 범위

---


## 10. 컨테이너화 & 인프라

### 10.1 멀티스테이지 Docker 빌드

```dockerfile
# Stage 1: 빌드 (golang:1.23-alpine, ~300MB)
FROM golang:1.23-alpine AS builder
COPY . .
RUN go build -ldflags="-s -w" -o /server ./cmd/server/

# Stage 2: 런타임 (alpine:3.19, ~50MB)
FROM alpine:3.19
RUN apk add --no-cache ffmpeg ca-certificates
COPY --from=builder /server /server
COPY web/ /web/
EXPOSE 4242
CMD ["/server"]
```

멀티스테이지의 효과:
- 빌드 도구(Go 컴파일러, 소스코드)가 최종 이미지에 포함되지 않음
- **이미지 크기**: ~300MB (단일 스테이지) → ~80MB (멀티스테이지)
- **보안**: 공격 표면 최소화 (컴파일러, 소스코드 없음)

`-ldflags="-s -w"`:
- `-s`: 심볼 테이블 제거
- `-w`: DWARF 디버그 정보 제거
- 바이너리 크기 ~30% 감소

### 10.2 Docker Compose 서비스 오케스트레이션

```yaml
services:
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 5s
      timeout: 3s
      retries: 5
  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
```

헬스체크 설계:
- **interval=5s**: 5초마다 상태 확인
- **timeout=3s**: 3초 내 응답 없으면 실패
- **retries=5**: 5회 연속 실패 시 unhealthy
- 의존 서비스가 `healthy` 상태일 때만 앱 서버 시작 가능 (`depends_on.condition`)

### 10.3 12-Factor App 적용

| Factor | 프로젝트 적용 | 상태 |
|--------|-------------|------|
| 1. Codebase | Git 단일 저장소 | ✅ |
| 2. Dependencies | `go.mod` 명시적 선언 | ✅ |
| 3. Config | **환경변수** (`.env` + `os.Getenv`) | ✅ |
| 4. Backing Services | MinIO, PostgreSQL, Kafka를 URL로 연결 | ✅ |
| 5. Build/Release/Run | Docker 멀티스테이지 (빌드 분리) | ✅ |
| 6. Processes | 상태 없는 HTTP 서버 (상태는 DB/S3) | ✅ |
| 7. Port Binding | `SERVER_PORT=4242` 자체 바인딩 | ✅ |
| 8. Concurrency | Worker Pool로 수평 확장 | ✅ |
| 9. Disposability | Graceful Shutdown (15s timeout) | ✅ |
| 10. Dev/Prod Parity | Docker Compose로 동일 인프라 | ✅ |
| 11. Logs | `log.Printf` → stdout | ✅ |
| 12. Admin Processes | 자동 마이그레이션 (`CREATE TABLE IF NOT EXISTS`) | ✅ |

**Factor 3 (Config)** 심층:

```go
// config.go — 환경변수 우선, 기본값 폴백
func env(key, fallback string) string {
    if v := os.Getenv(key); v != "" { return v }
    return fallback
}
```

이 패턴의 장점:
- 개발: `.env` 파일로 로컬 설정
- 스테이징: Docker Compose `environment:` 섹션
- 프로덕션: Kubernetes ConfigMap/Secret
- **코드 변경 없이** 환경별 설정 전환

### 10.4 프로덕션 확장 고려사항

현재 단일 서버 구성에서 프로덕션으로 확장 시:

```
현재:
  [Go Server] ── [MinIO] ── [PostgreSQL] ── [Kafka]
  (모두 단일 인스턴스)

프로덕션:
  [LB] ──▶ [Go Server ×N] ──▶ [S3]
                │                │
                ▼                ▼
           [PostgreSQL RDS]  [MSK (Managed Kafka)]
           (Multi-AZ)        (3 broker)
```

스케일링 포인트:
- **HTTP 서버**: 상태 없음 → 수평 확장 (LB 뒤에 N개)
- **트랜스코딩 워커**: Kafka 파티션 수만큼 독립 스케일링
- **RTSP 인제스트**: 채널별 샤딩 (consistent hashing)
- **스토리지**: MinIO → AWS S3 (무제한 확장)

---


## 11. 자율주행 맥락에서의 설계 고려사항

### 11.1 차량-클라우드 미디어 파이프라인

자율주행 차량은 다수의 카메라(전방, 측방, 후방, 실내)를 탑재한다. 이 영상 데이터의 활용 시나리오:

```
┌─────────────────────────────────────────────────────┐
│                   자율주행 차량                        │
│  [전방 카메라] [측방 L/R] [후방] [실내] [LiDAR]       │
│       │            │        │      │                 │
│       └────────────┴────────┴──────┘                 │
│                    │                                  │
│            [엣지 컴퓨팅 유닛]                          │
│            H.264 인코딩 + RTSP 서버                   │
└────────────────────┬──────────────────────────────────┘
                     │ LTE/5G (불안정)
                     ▼
┌────────────────────────────────────────────────────────┐
│                   클라우드 플랫폼                        │
│                                                         │
│  실시간 모니터링 ← WebRTC (초저지연)                     │
│  사고 분석/학습 ← VOD (HLS/DASH, 녹화 영상)             │
│  원격 제어     ← WebRTC (양방향, 초저지연 필수)          │
│  플릿 관리     ← 대시보드 (다수 차량 동시 모니터링)       │
└────────────────────────────────────────────────────────┘
```

### 11.2 네트워크 불안정성 대응

차량은 이동 중이므로 네트워크 품질이 급변한다:

| 상황 | 대역폭 | 지연시간 | 패킷 손실 |
|------|--------|---------|----------|
| 5G 도심 | 100+ Mbps | 10ms | <0.1% |
| LTE 교외 | 10~50 Mbps | 30ms | 1~3% |
| 터널/지하 | 0 Mbps | ∞ | 100% |
| 핸드오버 | 변동 | 스파이크 | 5~10% |

대응 전략:

**SRT (Secure Reliable Transport)** — RTSP의 대안:
- Haivision이 개발, 오픈소스
- UDP 기반 + ARQ(자동 재전송 요청) → 패킷 손실 복구
- 적응형 지연 버퍼 → 네트워크 jitter 흡수
- 암호화 내장 (AES-128/256)
- **차량 → 클라우드 인제스트에 RTSP보다 적합**

```
현재:  차량 ──RTSP/TCP──▶ 클라우드 (TCP 재전송 지연)
개선:  차량 ──SRT/UDP───▶ 클라우드 (ARQ로 빠른 복구)
```

**QUIC 기반 전송** — HTTP/3:
- UDP 위의 멀티플렉싱, 0-RTT 연결
- 핸드오버 시 연결 유지 (Connection Migration)
- HLS/DASH 세그먼트 전송에 적합

### 11.3 대규모 플릿 관리

1000대 이상의 차량이 동시에 스트리밍할 때:

```
차량 1000대 × 카메라 4개 × 720p@30fps = 4000 스트림
각 스트림 ~2.5 Mbps → 총 인제스트 대역폭: ~10 Gbps
```

스케일링 아키텍처:

```
[차량 플릿]
    │
    ▼ (SRT/RTSP)
[인제스트 레이어] ── 지역별 엣지 서버 (consistent hashing by vehicle_id)
    │
    ▼ (Kafka)
[처리 레이어] ── 녹화, 이벤트 감지, AI 분석
    │
    ▼ (S3)
[스토리지 레이어] ── 원본 + 트랜스코딩 결과
    │
    ▼ (CDN + WebRTC SFU)
[배포 레이어] ── 대시보드, 모니터링, VOD
```

핵심 설계:
- **인제스트 샤딩**: `vehicle_id` 기반 consistent hashing → 특정 차량의 모든 카메라가 같은 서버로
- **Kafka 파티셔닝**: `vehicle_id`를 파티션 키 → 차량별 이벤트 순서 보장
- **SFU (Selective Forwarding Unit)**: 다수 시청자에게 WebRTC 릴레이 시 서버 측 미디어 라우팅

### 11.4 엣지 컴퓨팅

차량 내 엣지 컴퓨팅 유닛에서 수행할 수 있는 처리:

| 처리 | 위치 | 이유 |
|------|------|------|
| H.264 인코딩 | **엣지** | 원본 영상 전송은 대역폭 낭비 |
| 객체 감지 (YOLO) | **엣지** | 실시간 자율주행 판단에 필요 |
| 이벤트 클립 추출 | **엣지** | 사고/이상 감지 시 해당 구간만 업로드 |
| ABR 트랜스코딩 | **클라우드** | CPU 집약적, 엣지 리소스 부족 |
| 장기 저장 | **클라우드** | 엣지 스토리지 한계 |

이벤트 기반 업로드 전략:
```
평상시: 저해상도 스트림만 클라우드 전송 (360p, 600kbps)
이벤트 감지: 고해상도 클립 업로드 (720p, 이벤트 전후 30초)
수동 요청: 실시간 고해상도 WebRTC 스트림 시작
```

### 11.5 이 프로젝트와의 연결

| 프로젝트 기능 | 적용 시나리오 |
|-------------|-------------------|
| RTSP 인제스트 | 차량 카메라 → 클라우드 (→ SRT로 확장) |
| WebRTC 릴레이 | 원격 모니터링, 원격 제어 |
| HLS/DASH VOD | 사고 영상 리뷰, AI 학습 데이터 |
| Kafka 이벤트 | 차량 텔레메트리, 이벤트 트리거 |
| Worker Pool | 대규모 동시 트랜스코딩 |
| S3 스토리지 | 영상 아카이브, 학습 데이터셋 |

---


## 12. Observability & OpenTelemetry

### 12.1 Observability의 세 기둥

프로덕션 시스템을 운영하려면 "내부에서 무슨 일이 일어나는지" 외부에서 파악할 수 있어야 한다. 이를 위한 세 가지 신호:

| 신호 | 질문 | 예시 | 도구 |
|------|------|------|------|
| **Logs** | 무슨 일이 일어났는가? | `[upload] s3 error: connection refused` | ELK, Loki |
| **Metrics** | 얼마나 발생하는가? | `http_requests_total{status="500"} = 42` | Prometheus, Grafana |
| **Traces** | 어디서 시간이 소요되는가? | `upload → s3.put(120ms) → db.insert(5ms)` | Jaeger, Tempo |

세 신호의 관계:
```
Alert (메트릭 임계값 초과)
  → 대시보드에서 이상 패턴 확인 (Metrics)
    → 해당 시간대 트레이스 검색 (Traces)
      → 느린 span의 상세 로그 확인 (Logs)
```

### 12.2 OpenTelemetry 아키텍처

OTel은 CNCF 프로젝트로, 벤더 중립적인 계측 표준이다:

```
┌──────────────────────────────────────────────────┐
│                  Go Application                   │
│                                                    │
│  [OTel SDK]                                        │
│    TracerProvider ──┐                              │
│    MeterProvider  ──┼── OTLP gRPC ──▶ :4317       │
│    Propagator     ──┘                              │
└──────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────┐
│              OTel Collector                        │
│                                                    │
│  Receivers ──▶ Processors ──▶ Exporters           │
│  (OTLP)       (batch,        (Jaeger, Prometheus) │
│                memory_limiter)                     │
└──────────────────────────────────────────────────┘
          │                        │
          ▼                        ▼
   ┌──────────┐            ┌──────────────┐
   │  Jaeger   │            │  Prometheus   │
   │  :16686   │            │  :9090        │
   │ (traces)  │            │ (metrics)     │
   └──────────┘            └──────┬───────┘
                                   │
                                   ▼
                            ┌──────────────┐
                            │   Grafana     │
                            │   :3000       │
                            │ (dashboards)  │
                            └──────────────┘
```

핵심 개념:
- **Resource**: 텔레메트리를 생성하는 엔티티 식별 (`service.name`, `service.version`)
- **Span**: 하나의 작업 단위. 시작/종료 시간, 속성, 상태, 부모-자식 관계
- **Propagation**: 서비스 간 trace context 전달 (W3C TraceContext 헤더)
- **Collector**: 수집 → 처리 → 내보내기 파이프라인. 애플리케이션과 백엔드를 분리

### 12.3 이 프로젝트의 계측 전략

**Trace 계측 — 요청 흐름 추적:**

```
HTTP POST /api/upload
  └─ handler.upload (span)
       ├─ s3.upload (span) ─── MinIO PutObject
       ├─ db.create_media (span) ─── PostgreSQL INSERT
       └─ kafka.publish (span) ─── 이벤트 발행
                │
                │ trace context propagation (Kafka headers)
                ▼
           kafka.consume (span)
             └─ transcoder.process (span)
                  ├─ ffmpeg HLS 트랜스코딩
                  └─ ffmpeg DASH 트랜스코딩
```

업로드부터 트랜스코딩 완료까지 하나의 trace로 연결된다. Kafka 헤더에 W3C TraceContext를 주입/추출하여 비동기 경계를 넘어 trace가 이어진다.

**Metric 계측 — 실시간 상태 모니터링:**

| 메트릭 | 타입 | 설명 |
|--------|------|------|
| `http.server.request.duration` | Histogram | HTTP 요청 지연시간 (otelhttp 자동) |
| `http.server.request.size` | Histogram | 요청 바디 크기 (otelhttp 자동) |
| `live.channels` | UpDownCounter | 활성 라이브 채널 수 |
| `live.viewers` | UpDownCounter | WebRTC 시청자 수 |
| `live.packets_relayed` | Counter | 릴레이된 RTP 패킷 총 수 |

### 12.4 Collector를 사용하는 이유

애플리케이션에서 Jaeger/Prometheus로 직접 보내지 않고 Collector를 경유하는 이유:

1. **관심사 분리**: 애플리케이션은 OTLP만 알면 됨. 백엔드 교체 시 Collector 설정만 변경
2. **배치 처리**: 개별 span을 모아서 일괄 전송 → 네트워크 효율
3. **샘플링**: Collector에서 tail-based sampling 가능 (에러 트레이스만 100% 수집 등)
4. **버퍼링**: 백엔드 장애 시 Collector가 버퍼 역할

### 12.5 플릿 모니터링 연계

자율주행 차량 플릿에서의 Observability 확장:

```
[차량 1000대]
    │ OTel SDK (엣지)
    ▼
[리전별 OTel Collector] ── 샘플링 + 집계
    │
    ▼
[중앙 Collector] ── 전체 플릿 뷰
    ├─▶ Jaeger: 개별 차량 영상 파이프라인 트레이스
    ├─▶ Prometheus: 플릿 전체 메트릭 (스트림 수, 대역폭, 에러율)
    └─▶ Grafana: 실시간 대시보드
```

핵심 메트릭 예시:
- `vehicle.stream.active` — 차량별 활성 스트림 수
- `vehicle.upload.bandwidth_bps` — 차량별 업로드 대역폭
- `vehicle.connection.handover_count` — 네트워크 핸드오버 횟수
- `fleet.total_streams` — 전체 플릿 동시 스트림 수
- `transcode.queue.depth` — 트랜스코딩 대기열 깊이

SLO 설계:
- 라이브 스트림 가용성 ≥ 99.5% (30일 기준)
- 업로드 → 트랜스코딩 완료 P99 ≤ 60초
- WebRTC 시그널링 P99 ≤ 500ms

### 12.6 분산 트레이싱 심화

#### Span 모델

트레이스는 span의 DAG(Directed Acyclic Graph)다. 각 span은 하나의 작업 단위를 나타낸다:

```
Span {
    TraceID     [16 bytes]   ← 전체 트레이스 고유 식별자
    SpanID      [8 bytes]    ← 이 span 고유 식별자
    ParentID    [8 bytes]    ← 부모 span (root span은 없음)
    Name        string       ← operation 이름 (e.g., "s3.upload")
    StartTime   timestamp
    EndTime     timestamp
    Status      {OK, Error, Unset}
    Attributes  map[string]any   ← 키-값 메타데이터
    Events      []Event          ← 시점 이벤트 (로그와 유사)
    Links       []Link           ← 다른 트레이스와의 연결
}
```

span 간 관계:
- **Parent-Child**: 동기 호출. 부모 span이 끝나기 전에 자식이 끝남
- **FollowsFrom**: 비동기 호출. 부모가 먼저 끝날 수 있음 (Kafka consume 등)
- **Link**: 인과 관계는 있지만 부모-자식은 아닌 경우 (배치 처리 등)

#### Context Propagation 메커니즘

분산 시스템에서 트레이스를 연결하려면 span context를 서비스 간에 전달해야 한다:

```
Service A                    Network                    Service B
┌──────────┐                                          ┌──────────┐
│ Span A   │                                          │ Span B   │
│          │── Inject(ctx, carrier) ──▶                │          │
│          │   traceparent 헤더 삽입   HTTP/Kafka/gRPC │          │
│          │                          ──────────────▶  │          │
│          │                          Extract(carrier) │          │
│          │                          ctx 복원 ────────│          │
└──────────┘                                          └──────────┘
```

**W3C TraceContext 표준** (이 프로젝트에서 사용):
```
traceparent: 00-{traceID(32hex)}-{spanID(16hex)}-{flags(2hex)}
예: traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
```

전파 매체별 구현:
| 매체 | 전파 방식 | 이 프로젝트 |
|------|----------|------------|
| HTTP | `traceparent` 헤더 | otelhttp 미들웨어 (자동) |
| Kafka | 메시지 헤더 | `kafkaHeaderCarrier` (수동 구현) |
| gRPC | metadata | otelgrpc 인터셉터 (미사용) |

Kafka 전파가 중요한 이유: 업로드(HTTP) → 트랜스코딩(Kafka consumer)은 비동기 경계를 넘는다. 이 경계에서 trace context가 끊기면 "업로드는 됐는데 트랜스코딩이 왜 느린지" 추적이 불가능하다.

#### Sampling 전략

프로덕션에서 모든 트레이스를 100% 수집하면 비용과 성능 문제가 발생한다:

| 전략 | 설명 | 장점 | 단점 |
|------|------|------|------|
| **Always On** | 모든 트레이스 수집 | 완전한 가시성 | 비용 폭발, 성능 영향 |
| **Probabilistic** | 확률적 샘플링 (e.g., 10%) | 간단, 예측 가능한 비용 | 중요 트레이스 누락 가능 |
| **Rate Limiting** | 초당 N개 트레이스 | 비용 상한 보장 | 트래픽 급증 시 샘플링률 급감 |
| **Tail-based** | Collector에서 완성된 트레이스 보고 결정 | 에러/느린 트레이스 100% 수집 | Collector 메모리 사용, 복잡도 |

프로덕션 권장 조합:
```
Head-based: 기본 10% 샘플링 (SDK에서)
  +
Tail-based: 에러 트레이스 100% 수집 (Collector에서)
  +
Always: 특정 API (결제, 인증) 100% 수집
```

이 프로젝트에서는 데모 목적으로 Always On (100%) 사용.

### 12.7 메트릭 타입 심화

#### 4가지 기본 메트릭 타입

**1. Counter — 단조 증가 값**
```
live_packets_relayed_total = 1,234,567
```
- 절대값 자체는 의미 없음. `rate()`로 변화율을 봐야 함
- 예: `rate(live_packets_relayed_total[5m])` = 초당 릴레이 패킷 수
- 리셋 감지: Prometheus가 카운터 리셋(재시작)을 자동 처리

**2. Gauge — 현재 값 (증감 가능)**
```
live_channels = 3
live_viewers = 12
```
- 현재 상태를 나타냄. 스냅샷 값
- 예: 현재 활성 라이브 채널 수, 현재 시청자 수
- OTel에서는 `UpDownCounter`로 구현 (Add +1/-1)

**3. Histogram — 값의 분포**
```
http_server_request_duration_seconds_bucket{le="0.01"} = 100
http_server_request_duration_seconds_bucket{le="0.1"}  = 450
http_server_request_duration_seconds_bucket{le="1.0"}  = 498
http_server_request_duration_seconds_bucket{le="+Inf"} = 500
http_server_request_duration_seconds_sum   = 23.5
http_server_request_duration_seconds_count = 500
```
- 버킷 기반 분포 측정. 퍼센타일 계산 가능
- `histogram_quantile(0.95, rate(...[5m]))` = P95 응답시간
- 장점: 집계 가능 (여러 인스턴스의 히스토그램 합산)
- 단점: 버킷 경계에 따라 정확도 달라짐

**4. Summary — 클라이언트 측 퍼센타일 (Prometheus 전용)**
- 애플리케이션에서 직접 퍼센타일 계산
- 장점: 정확한 퍼센타일
- 단점: 집계 불가 (여러 인스턴스 합산 불가)
- OTel에서는 Histogram 사용 권장

#### Cardinality 문제

메트릭의 고유 시계열 수 = label 조합의 곱:

```
http_requests_total{method, path, status}

method: GET, POST, PUT, DELETE         → 4
path: /api/upload, /api/media, ...     → 10
status: 200, 201, 400, 404, 500       → 5

시계열 수 = 4 × 10 × 5 = 200 (관리 가능)
```

위험한 패턴:
```
http_requests_total{method, path, status, user_id}

user_id: 100만 명 → 시계열 수 = 4 × 10 × 5 × 1,000,000 = 2억 (폭발!)
```

규칙:
- label 값이 무한히 증가할 수 있는 필드(user_id, request_id, IP)는 label로 쓰지 않는다
- 이런 값은 trace의 attribute로 넣는다 (트레이스는 개별 이벤트, 메트릭은 집계)
- 이 프로젝트에서 `media.id`를 메트릭 label이 아닌 span attribute로 넣은 이유

#### USE / RED 메서드

시스템 모니터링의 두 가지 프레임워크:

**USE (리소스 관점) — Brendan Gregg:**
| 지표 | 의미 | 예시 |
|------|------|------|
| Utilization | 리소스 사용률 | CPU 80%, 메모리 70% |
| Saturation | 포화도 (대기열) | 트랜스코딩 큐 깊이 |
| Errors | 에러 수 | 디스크 I/O 에러 |

**RED (서비스 관점) — Tom Wilkie:**
| 지표 | 의미 | 이 프로젝트 메트릭 |
|------|------|-------------------|
| Rate | 초당 요청 수 | `rate(http_server_request_duration_seconds_count[1m])` |
| Errors | 에러 요청 수 | `rate(...{http_status_code=~"5.."}[1m])` |
| Duration | 응답 시간 | `histogram_quantile(0.95, rate(..._bucket[1m]))` |

RED는 마이크로서비스에 적합. 이 프로젝트의 Grafana 대시보드는 RED 메서드 기반으로 설계.

### 12.8 Grafana 프로비저닝 & 대시보드 설계

#### Infrastructure as Code 관점

Grafana 설정을 코드로 관리하면:
- 환경 재현 가능 (docker compose up → 대시보드 자동 생성)
- 버전 관리 (git으로 대시보드 변경 이력 추적)
- 리뷰 가능 (대시보드 변경도 PR로 리뷰)

```
grafana/
├── provisioning/
│   ├── datasources/
│   │   └── datasources.yml    # 데이터소스 자동 등록
│   └── dashboards/
│       └── dashboards.yml     # 대시보드 파일 경로 설정
└── dashboards/
    └── media-platform.json    # 대시보드 정의 (JSON)
```

프로비저닝 흐름:
```
Grafana 시작
  → /etc/grafana/provisioning/datasources/ 읽기
  → Prometheus, Jaeger 데이터소스 자동 등록
  → /etc/grafana/provisioning/dashboards/ 읽기
  → /var/lib/grafana/dashboards/*.json 로드
  → 대시보드 자동 생성
```

#### 대시보드 설계 원칙

**1. 계층적 구조 (Drill-down)**
```
Level 1: 서비스 개요 (전체 요청률, 에러율, P95)
  └─ Level 2: 엔드포인트별 상세 (각 API 경로별 메트릭)
       └─ Level 3: 개별 요청 트레이스 (Jaeger 링크)
```

**2. 시간 범위 일관성**
- 모든 패널이 동일 시간 범위 사용 (Grafana 글로벌 time picker)
- rate() 윈도우는 scrape interval의 4배 이상 권장 (15s scrape → 1m rate)

**3. 임계값 시각화**
- 정상: 녹색, 경고: 노란색, 위험: 빨간색
- SLO 기준선을 대시보드에 표시

**4. 변수(Variable) 활용**
- 데이터소스 변수: `${DS_PROMETHEUS}` — 환경별 데이터소스 전환
- 필터 변수: `${method}`, `${path}` — 드롭다운으로 필터링

### 12.9 프로덕션 Observability 패턴

#### SLI / SLO / SLA

```
SLA (Service Level Agreement)
  "99.9% 가용성 보장, 위반 시 크레딧 환불"
  └─ 비즈니스 계약. 법적 구속력.

SLO (Service Level Objective)
  "99.95% 가용성 목표" (SLA보다 엄격하게 설정)
  └─ 내부 엔지니어링 목표. SLA 위반 전에 대응하기 위한 버퍼.

SLI (Service Level Indicator)
  "성공 요청 수 / 전체 요청 수 × 100"
  └─ 실제 측정값. SLO 달성 여부를 판단하는 지표.
```

이 프로젝트의 SLI/SLO 예시:

| SLI | 측정 방법 | SLO |
|-----|----------|-----|
| 업로드 성공률 | `1 - rate(upload_errors) / rate(upload_total)` | ≥ 99.9% |
| 트랜스코딩 지연 | `histogram_quantile(0.99, transcode_duration)` | P99 ≤ 60s |
| HLS 서빙 가용성 | `rate(hls_2xx) / rate(hls_total)` | ≥ 99.95% |
| WebRTC 시그널링 지연 | `histogram_quantile(0.99, signaling_duration)` | P99 ≤ 500ms |
| 라이브 스트림 가용성 | `live_channels_up / live_channels_total` | ≥ 99.5% |

#### Error Budget

SLO 99.9% = 30일 중 43.2분 다운타임 허용.

```
Error Budget = 1 - SLO = 0.1%
30일 × 24시간 × 60분 × 0.001 = 43.2분

남은 budget > 0  → 새 기능 배포 가능
남은 budget ≤ 0  → 안정성 작업만 (배포 동결)
```

Error Budget 정책은 개발 속도와 안정성의 균형을 자동으로 조절한다.

#### Alert 설계 원칙

**좋은 알림의 조건:**
1. **Actionable** — 받으면 즉시 할 수 있는 행동이 있어야 함
2. **Symptom-based** — 원인이 아닌 증상에 알림 (CPU 높음 ✗ → 응답시간 느림 ✓)
3. **SLO-based** — Error budget 소진 속도 기반

**Multi-window Burn Rate Alert:**
```
빠른 소진 (1시간 윈도우):
  error_rate > 14.4 × (1 - SLO)  → 즉시 알림 (페이저)
  "이 속도면 1시간 내 월간 budget 소진"

느린 소진 (6시간 윈도우):
  error_rate > 6 × (1 - SLO)     → 티켓 생성
  "이 속도면 하루 내 월간 budget 소진"

매우 느린 소진 (3일 윈도우):
  error_rate > 1 × (1 - SLO)     → 대시보드 표시
  "이 속도면 월말에 budget 소진"
```

**Alert Fatigue 방지:**
- 알림이 너무 많으면 무시하게 됨 → 진짜 장애를 놓침
- 규칙: 알림을 받으면 반드시 행동해야 함. 행동할 게 없으면 알림 삭제
- 심각도 분류: P1(즉시 대응), P2(업무시간 내), P3(다음 스프린트)

#### 장애 대응 워크플로우

```
1. 감지 (Detection)
   Alert 발생 → Slack/PagerDuty 알림
   │
2. 분류 (Triage)
   Grafana 대시보드에서 영향 범위 파악
   "어떤 서비스? 몇 %의 사용자? 얼마나 심각?"
   │
3. 진단 (Diagnosis)
   Metrics → 이상 패턴 확인 (어떤 메트릭이 튀었나?)
   Traces  → 느린/실패 요청의 span 분석 (어디서 병목?)
   Logs    → 해당 span의 상세 에러 메시지
   │
4. 완화 (Mitigation)
   롤백, 스케일 아웃, 트래픽 차단 등 즉시 조치
   "원인 분석보다 서비스 복구가 먼저"
   │
5. 해결 (Resolution)
   근본 원인 수정 + 배포
   │
6. 사후 분석 (Post-mortem)
   타임라인 정리, 근본 원인, 재발 방지 액션 아이템
   "비난 없는 문화 (blameless)"
```

Observability 3 pillars가 장애 대응에서 어떻게 연결되는지:

```
Alert (메트릭 임계값 초과)
  │
  ▼
Dashboard (Grafana)
  "HTTP 5xx rate가 10분 전부터 급증"
  "영향: /api/upload 엔드포인트, 전체 트래픽의 30%"
  │
  ▼
Trace (Jaeger)
  "실패 요청의 트레이스를 보니 s3.upload span에서 timeout"
  "s3.upload 평균 120ms → 5000ms로 급증"
  │
  ▼
Log
  "MinIO connection refused — 디스크 full"
  │
  ▼
Action
  "MinIO 디스크 정리 + 오래된 원본 파일 아카이빙 정책 추가"
```

---

