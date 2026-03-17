package handler

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
)

type webrtcRequest struct {
	SDP string `json:"sdp"`
}

type webrtcResponse struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

// RegisterWebRTC adds WebRTC signaling endpoint.
func (h *Handler) RegisterWebRTC(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/media/{id}/webrtc", h.webrtcSignal)
}

func (h *Handler) webrtcSignal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	m, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "media not found"})
		return
	}

	var req webrtcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	// Download original MP4 to temp file
	obj, err := h.store.Download(r.Context(), m.S3Key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "download failed"})
		return
	}

	tmpFile, err := os.CreateTemp("", "webrtc-*.mp4")
	if err != nil {
		obj.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "temp file failed"})
		return
	}
	io.Copy(tmpFile, obj)
	obj.Close()
	tmpFile.Close()

	// Create PeerConnection
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:localhost:3478"}}},
	})
	if err != nil {
		os.Remove(tmpFile.Name())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "peer connection failed"})
		return
	}

	// Add video track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		"video", "media-platform",
	)
	if err != nil {
		pc.Close()
		os.Remove(tmpFile.Name())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "track creation failed"})
		return
	}

	if _, err = pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		os.Remove(tmpFile.Name())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "add track failed"})
		return
	}

	// Set remote SDP
	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.SDP}
	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		os.Remove(tmpFile.Name())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid SDP offer"})
		return
	}

	// Create answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		os.Remove(tmpFile.Name())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create answer failed"})
		return
	}

	// Wait for ICE gathering
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		os.Remove(tmpFile.Name())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "set local desc failed"})
		return
	}

	select {
	case <-gatherDone:
	case <-time.After(10 * time.Second):
		pc.Close()
		os.Remove(tmpFile.Name())
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "ICE gathering timeout"})
		return
	}

	// Start streaming in background
	go streamMP4ToWebRTC(pc, videoTrack, tmpFile.Name())

	localDesc := pc.LocalDescription()
	writeJSON(w, http.StatusOK, webrtcResponse{
		SDP:  localDesc.SDP,
		Type: localDesc.Type.String(),
	})
}

// streamMP4ToWebRTC uses ffmpeg to extract H264 NAL units and sends them via WebRTC.
func streamMP4ToWebRTC(pc *webrtc.PeerConnection, track *webrtc.TrackLocalStaticSample, mp4Path string) {
	defer os.Remove(mp4Path)
	defer pc.Close()

	// Wait for connection
	connected := make(chan struct{})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[webrtc] connection state: %s", state)
		switch state {
		case webrtc.PeerConnectionStateConnected:
			close(connected)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed,
			webrtc.PeerConnectionStateDisconnected:
			return
		}
	})

	select {
	case <-connected:
	case <-time.After(30 * time.Second):
		log.Println("[webrtc] connection timeout")
		return
	}

	// ffmpeg: MP4 → raw H264 Annex B on stdout
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", mp4Path,
		"-c:v", "libx264",
		"-profile:v", "baseline",
		"-level", "3.1",
		"-pix_fmt", "yuv420p",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-threads", "1",
		"-bsf:v", "h264_mp4toannexb",
		"-f", "h264",
		"-an",
		"pipe:1",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[webrtc] ffmpeg pipe error: %v", err)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[webrtc] ffmpeg start error: %v", err)
		return
	}

	// Parse H264 Annex B NAL units and send as samples
	sendNALUnits(stdout, track)
	cmd.Wait()
}

// sendNALUnits reads Annex B H264 stream and writes NAL units to the WebRTC track.
// SPS/PPS/SEI are sent immediately (duration 0), VCL NALs are paced by ticker.
func sendNALUnits(reader io.Reader, track *webrtc.TrackLocalStaticSample) {
	const frameDuration = time.Millisecond * 33 // ~30fps
	startCode := []byte{0x00, 0x00, 0x00, 0x01}

	h264, err := h264reader.NewReader(reader)
	if err != nil {
		log.Printf("[webrtc] h264reader: %v", err)
		return
	}

	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	var auBuf []byte // SPS+PPS+SEI buffer → IDR과 합쳐서 전송

	for {
		nal, err := h264.NextNAL()
		if err != nil {
			log.Printf("[webrtc] stream end: %v", err)
			return
		}

		nalType := nal.Data[0] & 0x1F
		annexB := append(startCode, nal.Data...)
		log.Printf("[webrtc] NAL type=%d size=%d", nalType, len(nal.Data))

		switch nalType {
		case 7, 8, 6: // SPS, PPS, SEI → AU 버퍼에 축적
			auBuf = append(auBuf, annexB...)
		case 5: // IDR → SPS+PPS와 합쳐서 전송
			<-ticker.C
			data := append(auBuf, annexB...)
			if err := track.WriteSample(media.Sample{Data: data, Duration: frameDuration}); err != nil {
				log.Printf("[webrtc] write: %v", err)
				return
			}
			auBuf = auBuf[:0]
		case 1: // non-IDR
			<-ticker.C
			if err := track.WriteSample(media.Sample{Data: annexB, Duration: frameDuration}); err != nil {
				log.Printf("[webrtc] write: %v", err)
				return
			}
		}
	}
}
