package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"streaming-faas/benchmark/framing"
)

type metric struct {
	Type                         string    `json:"type"`
	RunID                        string    `json:"run_id,omitempty"`
	ExpectedFrames               int       `json:"expected_frames"`
	ReceivedFrames               int       `json:"received_frames"`
	PerFrameLatencyMeasured      bool      `json:"per_frame_latency_measured"`
	FirstOutputFrameAt           string    `json:"first_output_frame_at,omitempty"`
	LastOutputFrameAt            string    `json:"last_output_frame_at,omitempty"`
	CompletionTime               string    `json:"completion_time"`
	EffectiveFPS                 float64   `json:"effective_fps"`
	InputBytes                   int64     `json:"input_bytes"`
	OutputBytes                  int64     `json:"output_bytes"`
	PerFrameLatencyMS            []float64 `json:"per_frame_latency_ms,omitempty"`
	PerFrameProcessingStartedAt  []string  `json:"per_frame_processing_started_at,omitempty"`
	PerFrameProcessingFinishedAt []string  `json:"per_frame_processing_finished_at,omitempty"`
	PerFrameProcessingMS         []float64 `json:"per_frame_processing_ms,omitempty"`
	PerFrameDecodeMS             []float64 `json:"per_frame_decode_ms,omitempty"`
	PerFrameTransformationMS     []float64 `json:"per_frame_transformation_ms,omitempty"`
	PerFrameEncodeMS             []float64 `json:"per_frame_encode_ms,omitempty"`
	Missing                      int       `json:"missing"`
	Duplicate                    int       `json:"duplicate"`
	Malformed                    int       `json:"malformed"`
	DecodeErrors                 int       `json:"decode_errors"`
	Success                      bool      `json:"success"`
	Error                        string    `json:"error,omitempty"`
}

func main() {
	listen := flag.String("listen", "0.0.0.0:9100", "address on which to listen")
	frames := flag.Int("frames", 300, "expected frame count")
	runID := flag.String("run-id", "", "run identifier")
	out := flag.String("out", "frame-sink-metrics.jsonl", "JSONL metrics path; empty disables file output")
	saveDir := flag.String("save-dir", "", "directory in which to save received JPEG frames; empty disables saving")
	measurePerFrameLatency := flag.Bool("measure-per-frame-latency", false, "collect per-frame end-to-end and processing timings")
	flag.Parse()
	if *saveDir != "" {
		if err := os.MkdirAll(*saveDir, 0755); err != nil {
			log.Fatalf("create frame save directory: %v", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	log.Printf("frame sink listening on %s", *listen)
	conn, err := accept(ctx, ln)
	if err != nil {
		if ctx.Err() == nil {
			log.Fatal(err)
		}
		return
	}
	defer conn.Close()
	m := receive(conn, *frames, *runID, *saveDir, *measurePerFrameLatency)
	if err := writeMetric(*out, m); err != nil {
		log.Printf("write metrics: %v", err)
	}
}

func accept(ctx context.Context, ln net.Listener) (net.Conn, error) {
	ch := make(chan struct {
		c net.Conn
		e error
	}, 1)
	go func() {
		c, e := ln.Accept()
		ch <- struct {
			c net.Conn
			e error
		}{c, e}
	}()
	select {
	case <-ctx.Done():
		_ = ln.Close()
		return nil, ctx.Err()
	case r := <-ch:
		return r.c, r.e
	}
}

func receive(conn net.Conn, expected int, runID, saveDir string, measurePerFrameLatency bool) metric {
	start := time.Now().UTC()
	m := metric{
		Type:                    "normalized_frame_sink",
		RunID:                   runID,
		ExpectedFrames:          expected,
		CompletionTime:          start.Format(time.RFC3339Nano),
		PerFrameLatencyMeasured: measurePerFrameLatency,
	}
	var received atomic.Int64
	var malformed atomic.Int64
	var duplicate atomic.Int64
	var decodeErrors atomic.Int64
	progressTicker := time.NewTicker(60 * time.Second)
	defer progressTicker.Stop()
	progressDone := make(chan struct{})
	progressStopped := make(chan struct{})
	defer func() {
		close(progressDone)
		<-progressStopped
	}()
	go func() {
		defer close(progressStopped)
		for {
			select {
			case <-progressTicker.C:
				log.Printf(
					"progress: received=%d/%d malformed=%d duplicate=%d decode_errors=%d elapsed=%s",
					received.Load(), expected, malformed.Load(), duplicate.Load(), decodeErrors.Load(), time.Since(start).Round(time.Second),
				)
			case <-progressDone:
				return
			}
		}
	}()
	seen := make(map[uint64]bool)
	next := uint64(1)
	for expected <= 0 || m.ReceivedFrames < expected {
		f, err := framing.Read(conn)
		if err != nil {
			if err != io.EOF {
				m.Malformed++
				malformed.Store(int64(m.Malformed))
				m.Error = err.Error()
			}
			break
		}
		now := time.Now().UTC()
		m.InputBytes += int64(framing.HeaderSize) + int64(len(f.Payload))
		m.OutputBytes += int64(len(f.Payload))
		m.ReceivedFrames++
		received.Store(int64(m.ReceivedFrames))
		if m.FirstOutputFrameAt == "" {
			m.FirstOutputFrameAt = now.Format(time.RFC3339Nano)
		}
		m.LastOutputFrameAt = now.Format(time.RFC3339Nano)
		if seen[f.ID] {
			m.Duplicate++
			duplicate.Store(int64(m.Duplicate))
		}
		seen[f.ID] = true
		if f.ID != next {
			m.Malformed++ // IDs must be strictly sequential.
			malformed.Store(int64(m.Malformed))
		}
		next++
		if _, err := jpeg.DecodeConfig(bytes.NewReader(f.Payload)); err != nil {
			m.DecodeErrors++
			decodeErrors.Store(int64(m.DecodeErrors))
		} else if saveDir != "" {
			if err := saveFrame(saveDir, f.ID, f.Payload); err != nil {
				m.Malformed++
				malformed.Store(int64(m.Malformed))
				m.Error = err.Error()
				break
			}
		}
		if measurePerFrameLatency {
			if f.SentAtNS > 0 {
				m.PerFrameLatencyMS = append(m.PerFrameLatencyMS, float64(time.Now().UnixNano()-int64(f.SentAtNS))/1e6)
			}
			if f.ProcessingStartedAtNS > 0 && f.ProcessingFinishedAtNS >= f.ProcessingStartedAtNS {
				m.PerFrameProcessingStartedAt = append(m.PerFrameProcessingStartedAt, formatTimestampNS(f.ProcessingStartedAtNS))
				m.PerFrameProcessingFinishedAt = append(m.PerFrameProcessingFinishedAt, formatTimestampNS(f.ProcessingFinishedAtNS))
				m.PerFrameProcessingMS = append(m.PerFrameProcessingMS, float64(f.ProcessingFinishedAtNS-f.ProcessingStartedAtNS)/1e6)
				if f.DecodeFinishedAtNS >= f.ProcessingStartedAtNS && f.TransformationFinishedAtNS >= f.DecodeFinishedAtNS && f.ProcessingFinishedAtNS >= f.TransformationFinishedAtNS {
					m.PerFrameDecodeMS = append(m.PerFrameDecodeMS, float64(f.DecodeFinishedAtNS-f.ProcessingStartedAtNS)/1e6)
					m.PerFrameTransformationMS = append(m.PerFrameTransformationMS, float64(f.TransformationFinishedAtNS-f.DecodeFinishedAtNS)/1e6)
					m.PerFrameEncodeMS = append(m.PerFrameEncodeMS, float64(f.ProcessingFinishedAtNS-f.TransformationFinishedAtNS)/1e6)
				}
			}
		}
	}
	if expected > 0 && m.ReceivedFrames < expected {
		for id := 1; id <= expected; id++ {
			if !seen[uint64(id)] {
				m.Missing++
			}
		}
	}
	done := time.Now().UTC()
	m.CompletionTime = done.Format(time.RFC3339Nano)
	if !start.IsZero() {
		m.EffectiveFPS = float64(m.ReceivedFrames) / done.Sub(start).Seconds()
	}
	m.Success = (expected <= 0 || m.ReceivedFrames == expected) && m.Malformed == 0 && m.DecodeErrors == 0 && m.Duplicate == 0 && m.Missing == 0
	log.Printf("sink finished: received=%d/%d malformed=%d duplicate=%d decode_errors=%d success=%t", m.ReceivedFrames, expected, m.Malformed, m.Duplicate, m.DecodeErrors, m.Success)
	return m
}

func saveFrame(dir string, id uint64, payload []byte) error {
	path := filepath.Join(dir, "frame-"+strconv.FormatUint(id, 10)+".jpg")
	return os.WriteFile(path, payload, 0644)
}

func formatTimestampNS(value uint64) string {
	return time.Unix(0, int64(value)).UTC().Format(time.RFC3339Nano)
}

func writeMetric(path string, m metric) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}
