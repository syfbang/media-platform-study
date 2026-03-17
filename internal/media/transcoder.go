package media

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/media-service/media-platform/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type Job struct {
	MediaID   string
	InputPath string
	OutputDir string
}

type Result struct {
	MediaID string
	HLSDir  string
	DASHDir string
	Err     error
}

type Transcoder struct {
	workers int
	jobs    chan Job
	results chan Result
	wg      sync.WaitGroup
}

func NewTranscoder(workers int) *Transcoder {
	return &Transcoder{
		workers: workers,
		jobs:    make(chan Job, 100),
		results: make(chan Result, 100),
	}
}

func (t *Transcoder) Start(ctx context.Context) {
	for i := 0; i < t.workers; i++ {
		t.wg.Add(1)
		go func(id int) {
			defer t.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-t.jobs:
					if !ok {
						return
					}
					log.Printf("[transcoder-%d] processing %s", id, job.MediaID)
					t.results <- t.process(ctx, job)
				}
			}
		}(i)
	}
}

func (t *Transcoder) Submit(job Job) { t.jobs <- job }
func (t *Transcoder) Results() <-chan Result { return t.results }

func (t *Transcoder) Stop() {
	close(t.jobs)
	t.wg.Wait()
	close(t.results)
}

// 3 variants: 720p@2500k, 480p@1200k, 360p@600k
var variants = []struct {
	width, height int
	vBitrate      string
	maxrate       string
	bufsize       string
	aBitrate      string
}{
	{1280, 720, "2500k", "2500k", "5000k", "128k"},
	{854, 480, "1200k", "1200k", "2400k", "128k"},
	{640, 360, "600k", "600k", "1200k", "96k"},
}

func (t *Transcoder) process(ctx context.Context, job Job) Result {
	ctx, span := telemetry.Tracer.Start(ctx, "transcoder.process")
	defer span.End()
	span.SetAttributes(attribute.String("media.id", job.MediaID))

	hlsDir := filepath.Join(job.OutputDir, "hls")
	dashDir := filepath.Join(job.OutputDir, "dash")

	for _, d := range []string{hlsDir, dashDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			span.SetStatus(codes.Error, err.Error())
			return Result{MediaID: job.MediaID, Err: fmt.Errorf("mkdir: %w", err)}
		}
	}

	if err := transcodeHLS(ctx, job.InputPath, hlsDir); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Result{MediaID: job.MediaID, Err: fmt.Errorf("hls: %w", err)}
	}
	if err := transcodeDASH(ctx, job.InputPath, dashDir); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Result{MediaID: job.MediaID, Err: fmt.Errorf("dash: %w", err)}
	}

	return Result{MediaID: job.MediaID, HLSDir: hlsDir, DASHDir: dashDir}
}

func transcodeHLS(ctx context.Context, input, outDir string) error {
	// Create subdirs for each stream
	for i := range variants {
		os.MkdirAll(filepath.Join(outDir, fmt.Sprintf("v%d", i)), 0755)
	}

	args := []string{"-y", "-loglevel", "warning", "-i", input}

	// filter_complex: split into 3 scaled outputs
	filter := fmt.Sprintf(
		"[0:v]split=3[v0][v1][v2];[v0]scale=%d:%d[o0];[v1]scale=%d:%d[o1];[v2]scale=%d:%d[o2]",
		variants[0].width, variants[0].height,
		variants[1].width, variants[1].height,
		variants[2].width, variants[2].height,
	)
	args = append(args, "-filter_complex", filter)

	// Map each variant
	for i, v := range variants {
		args = append(args,
			"-map", fmt.Sprintf("[o%d]", i), "-map", "0:a?",
			fmt.Sprintf("-c:v:%d", i), "libx264",
			fmt.Sprintf("-b:v:%d", i), v.vBitrate,
			fmt.Sprintf("-maxrate:v:%d", i), v.maxrate,
			fmt.Sprintf("-bufsize:v:%d", i), v.bufsize,
			"-preset", "fast",
			fmt.Sprintf("-c:a:%d", i), "aac",
			fmt.Sprintf("-b:a:%d", i), v.aBitrate,
		)
	}

	args = append(args,
		"-force_key_frames", "expr:gte(t,n_forced*4)",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_segment_type", "fmp4",
		"-hls_playlist_type", "vod",
		"-master_pl_name", "master.m3u8",
		"-var_stream_map", "v:0,a:0 v:1,a:1 v:2,a:2",
		"-hls_segment_filename", filepath.Join(outDir, "v%v/seg_%03d.m4s"),
		filepath.Join(outDir, "v%v/playlist.m3u8"),
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func transcodeDASH(ctx context.Context, input, outDir string) error {
	args := []string{"-y", "-loglevel", "warning", "-i", input}

	filter := fmt.Sprintf(
		"[0:v]split=3[v0][v1][v2];[v0]scale=%d:%d[o0];[v1]scale=%d:%d[o1];[v2]scale=%d:%d[o2]",
		variants[0].width, variants[0].height,
		variants[1].width, variants[1].height,
		variants[2].width, variants[2].height,
	)
	args = append(args, "-filter_complex", filter)

	for i, v := range variants {
		args = append(args,
			"-map", fmt.Sprintf("[o%d]", i), "-map", "0:a?",
			fmt.Sprintf("-c:v:%d", i), "libx264",
			fmt.Sprintf("-b:v:%d", i), v.vBitrate,
			fmt.Sprintf("-maxrate:v:%d", i), v.maxrate,
			fmt.Sprintf("-bufsize:v:%d", i), v.bufsize,
			"-preset", "fast",
			fmt.Sprintf("-c:a:%d", i), "aac",
			fmt.Sprintf("-b:a:%d", i), v.aBitrate,
		)
	}

	args = append(args,
		"-force_key_frames", "expr:gte(t,n_forced*4)",
		"-f", "dash",
		"-seg_duration", "4",
		"-adaptation_sets", "id=0,streams=v id=1,streams=a",
		"-init_seg_name", "init-$RepresentationID$.m4s",
		"-media_seg_name", "seg-$RepresentationID$-$Number%05d$.m4s",
		filepath.Join(outDir, "manifest.mpd"),
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
