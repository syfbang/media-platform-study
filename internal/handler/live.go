package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/media-service/media-platform/internal/live"
	"github.com/pion/webrtc/v4"
)

// LiveHandler handles live streaming API endpoints.
type LiveHandler struct {
	ls *live.Server
}

func NewLiveHandler(ls *live.Server) *LiveHandler {
	return &LiveHandler{ls: ls}
}

func (h *LiveHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/live", h.listChannels)
	mux.HandleFunc("POST /api/live/{channel}/webrtc", h.webrtcSignal)
}

func (h *LiveHandler) listChannels(w http.ResponseWriter, _ *http.Request) {
	channels := h.ls.Channels()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"channels": channels})
}

func (h *LiveHandler) webrtcSignal(w http.ResponseWriter, r *http.Request) {
	channel := r.PathValue("channel")
	// support nested paths like "vehicle-001/cam-front" via wildcard workaround
	if rest := r.URL.Path; strings.Contains(rest, "/webrtc") {
		parts := strings.SplitN(strings.TrimPrefix(rest, "/api/live/"), "/webrtc", 2)
		if len(parts) > 0 && parts[0] != "" {
			channel = parts[0]
		}
	}

	ch := h.ls.GetChannel(channel)
	if ch == nil {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}

	var req struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SDP == "" {
		http.Error(w, "invalid SDP offer", http.StatusBadRequest)
		return
	}

	// Create PeerConnection
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:localhost:3478"}}},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Add H.264 video track (baseline profile, packetization-mode=1 matching ffmpeg)
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		},
		"video", "live-"+channel,
	)
	if err != nil {
		pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := pc.AddTrack(track); err != nil {
		pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Subscribe to live channel RTP packets
	rtpCh, unsub := ch.Subscribe()

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[live-webrtc] %s: %s", channel, state)
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateClosed ||
			state == webrtc.PeerConnectionStateDisconnected {
			unsub()
			pc.Close()
		}
	})

	// Relay RTP packets from RTSP to WebRTC
	go func() {
		var count uint64
		for pkt := range rtpCh {
			if err := track.WriteRTP(pkt); err != nil {
				log.Printf("[live-webrtc] %s: writeRTP error after %d pkts: %v", channel, count, err)
				return
			}
			count++
			if count == 1 || count%300 == 0 {
				log.Printf("[live-webrtc] %s: relayed %d pkts (PT=%d seq=%d)", channel, count, pkt.PayloadType, pkt.SequenceNumber)
			}
		}
	}()

	// SDP exchange
	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.SDP}
	if err := pc.SetRemoteDescription(offer); err != nil {
		unsub()
		pc.Close()
		http.Error(w, "invalid SDP: "+err.Error(), http.StatusBadRequest)
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		unsub()
		pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		unsub()
		pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	<-gatherDone

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"type": "answer",
		"sdp":  pc.LocalDescription().SDP,
	})
}
