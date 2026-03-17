package live

import (
	"context"
	"log"
	"strings"
	"sync"

	"github.com/media-service/media-platform/internal/telemetry"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"go.opentelemetry.io/otel/metric"
)

// Channel represents a live RTSP stream being published.
type Channel struct {
	Path   string
	server *Server
	mu     sync.RWMutex
	subs   map[uint64]func(*rtp.Packet) // subscriber callbacks
	next   uint64
}

func (c *Channel) addSubscriber(fn func(*rtp.Packet)) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.next
	c.next++
	c.subs[id] = fn
	if c.server != nil && c.server.viewerGauge != nil {
		c.server.viewerGauge.Add(context.Background(), 1)
	}
	return id
}

func (c *Channel) removeSubscriber(id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, id)
	if c.server != nil && c.server.viewerGauge != nil {
		c.server.viewerGauge.Add(context.Background(), -1)
	}
}

func (c *Channel) fanOut(pkt *rtp.Packet) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, fn := range c.subs {
		clone := &rtp.Packet{Header: pkt.Header.Clone(), Payload: make([]byte, len(pkt.Payload))}
		copy(clone.Payload, pkt.Payload)
		fn(clone)
	}
	if c.server != nil && c.server.pktCounter != nil {
		c.server.pktCounter.Add(context.Background(), int64(len(c.subs)))
	}
}

// Subscribe returns a channel of RTP packets and an unsubscribe function.
func (c *Channel) Subscribe() (<-chan *rtp.Packet, func()) {
	ch := make(chan *rtp.Packet, 128)
	id := c.addSubscriber(func(pkt *rtp.Packet) {
		select {
		case ch <- pkt:
		default: // drop if slow consumer
		}
	})
	return ch, func() {
		c.removeSubscriber(id)
		close(ch)
	}
}

// Server is an RTSP ingest server that accepts publishers and fans out
// H.264 RTP packets to WebRTC subscribers.
type Server struct {
	rtsp     *gortsplib.Server
	mu       sync.RWMutex
	channels map[string]*Channel   // path → channel
	streams  map[string]*gortsplib.ServerStream
	pubs     map[*gortsplib.ServerSession]string // session → path

	// OTel metrics
	channelGauge metric.Int64UpDownCounter
	viewerGauge  metric.Int64UpDownCounter
	pktCounter   metric.Int64Counter
}

func NewServer(addr string) *Server {
	s := &Server{
		channels: make(map[string]*Channel),
		streams:  make(map[string]*gortsplib.ServerStream),
		pubs:     make(map[*gortsplib.ServerSession]string),
	}

	// Register metrics (best-effort, ignore errors)
	if telemetry.Meter != nil {
		s.channelGauge, _ = telemetry.Meter.Int64UpDownCounter("live.channels", metric.WithDescription("Active live channels"))
		s.viewerGauge, _ = telemetry.Meter.Int64UpDownCounter("live.viewers", metric.WithDescription("Active WebRTC viewers"))
		s.pktCounter, _ = telemetry.Meter.Int64Counter("live.packets_relayed", metric.WithDescription("Total RTP packets relayed"))
	}

	s.rtsp = &gortsplib.Server{
		Handler:        s,
		RTSPAddress:    addr,
		UDPRTPAddress:  ":8000",
		UDPRTCPAddress: ":8001",
	}
	return s
}

func (s *Server) Start() error {
	log.Printf("✓ RTSP ingest server on %s", s.rtsp.RTSPAddress)
	return s.rtsp.Start()
}

func (s *Server) Close() { s.rtsp.Close() }

// Channels returns a snapshot of active channel paths.
func (s *Server) Channels() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.channels))
	for p := range s.channels {
		out = append(out, p)
	}
	return out
}

// GetChannel returns the channel for the given path, or nil.
func (s *Server) GetChannel(path string) *Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channels[path]
}

// --- gortsplib.ServerHandler implementation ---

func (s *Server) OnConnOpen(_ *gortsplib.ServerHandlerOnConnOpenCtx)   {}
func (s *Server) OnConnClose(_ *gortsplib.ServerHandlerOnConnCloseCtx) {}
func (s *Server) OnSessionOpen(_ *gortsplib.ServerHandlerOnSessionOpenCtx) {}

func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, ok := s.pubs[ctx.Session]
	if !ok {
		return
	}
	delete(s.pubs, ctx.Session)

	if st, ok := s.streams[path]; ok {
		st.Close()
		delete(s.streams, path)
	}
	delete(s.channels, path)
	log.Printf("[live] channel removed: %s", path)

	if s.channelGauge != nil {
		s.channelGauge.Add(context.Background(), -1)
	}
}

func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.streams[normPath(ctx.Path)]
	if !ok {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, st, nil
}

func (s *Server) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	path := normPath(ctx.Path)
	log.Printf("[live] ANNOUNCE %s", path)

	s.mu.Lock()
	defer s.mu.Unlock()

	// close existing publisher on same path
	if st, ok := s.streams[path]; ok {
		st.Close()
	}

	st := &gortsplib.ServerStream{
		Server: s.rtsp,
		Desc:   ctx.Description,
	}
	if err := st.Initialize(); err != nil {
		return &base.Response{StatusCode: base.StatusInternalServerError}, err
	}

	s.streams[path] = st
	s.channels[path] = &Channel{Path: path, server: s, subs: make(map[uint64]func(*rtp.Packet))}
	s.pubs[ctx.Session] = path

	if s.channelGauge != nil {
		s.channelGauge.Add(context.Background(), 1)
	}

	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if ctx.Session.State() == gortsplib.ServerSessionStatePreRecord {
		return &base.Response{StatusCode: base.StatusOK}, nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.streams[normPath(ctx.Path)]
	if !ok {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, st, nil
}

func (s *Server) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *Server) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	path := normPath(s.pubs[ctx.Session])
	log.Printf("[live] RECORD started: %s", path)

	ch := s.GetChannel(path)
	if ch == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil
	}

	// intercept H.264 RTP packets and fan-out to WebRTC subscribers
	ctx.Session.OnPacketRTPAny(func(medi *description.Media, forma format.Format, pkt *rtp.Packet) {
		// only relay video (H.264)
		if _, ok := forma.(*format.H264); ok {
			ch.fanOut(pkt)
		}
		// also write to ServerStream for potential RTSP readers
		if st := s.streams[path]; st != nil {
			_ = st.WritePacketRTP(medi, pkt)
		}
	})

	return &base.Response{StatusCode: base.StatusOK}, nil
}

func normPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	return strings.TrimSuffix(p, "/")
}
