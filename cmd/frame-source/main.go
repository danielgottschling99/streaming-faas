package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"streaming-faas/benchmark/framing"
)

type metric struct {
	Type             string `json:"type"`
	RunID            string `json:"run_id,omitempty"`
	InputDir         string `json:"input_dir"`
	Mode             string `json:"mode"`
	FrameCount       int    `json:"frame_count"`
	BytesSent        int64  `json:"bytes_sent"`
	FirstFrameSentAt string `json:"first_frame_sent_at,omitempty"`
	CompletionTime   string `json:"completion_time"`
	Interrupted      bool   `json:"interrupted,omitempty"`
	Error            string `json:"error,omitempty"`
}

func main() {
	listen := flag.String("listen", "0.0.0.0:9000", "address on which the source waits for streaming-faas")
	input := flag.String("input", "frame-input", "directory containing JPEG frames")
	frames := flag.Int("frames", 300, "number of frames to send")
	mode := flag.String("mode", "burst", "burst or steady")
	fps := flag.Float64("fps", 30, "steady sending rate in frames per second")
	runID := flag.String("run-id", "", "benchmark run identifier")
	out := flag.String("out", "frame-source-metrics.jsonl", "JSONL metrics path; empty disables file output")
	flag.Parse()
	if *frames < 1 {
		log.Fatal("-frames must be positive")
	}
	if *mode != "burst" && *mode != "steady" {
		log.Fatal("-mode must be burst or steady")
	}
	if *mode == "steady" && *fps <= 0 {
		log.Fatal("-fps must be positive")
	}

	files, err := jpegFiles(*input)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	log.Printf("frame source listening on %s, %d frames from %s", *listen, *frames, *input)
	conn, err := accept(ctx, ln)
	if err != nil {
		if ctx.Err() == nil {
			log.Fatal(err)
		}
		return
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	m := send(ctx, conn, files, *frames, *mode, *fps, *runID, *input)
	if err := writeMetric(*out, m); err != nil {
		log.Printf("write metrics: %v", err)
	}
}

func jpegFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read input directory: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".jpg" || ext == ".jpeg" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no JPEG files found in %s", dir)
	}
	return files, nil
}

func accept(ctx context.Context, ln net.Listener) (net.Conn, error) {
	ch := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		c, e := ln.Accept()
		ch <- struct {
			conn net.Conn
			err  error
		}{c, e}
	}()
	select {
	case <-ctx.Done():
		_ = ln.Close()
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

func send(ctx context.Context, conn net.Conn, files []string, count int, mode string, fps float64, runID, input string) metric {
	started := time.Now().UTC()
	m := metric{Type: "normalized_frame_source", RunID: runID, InputDir: input, Mode: mode, FrameCount: count, CompletionTime: started.Format(time.RFC3339Nano)}
	var ticker *time.Ticker
	if mode == "steady" {
		ticker = time.NewTicker(time.Duration(float64(time.Second) / fps))
		defer ticker.Stop()
	}
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			m.Interrupted = true
			m.Error = ctx.Err().Error()
			return finish(m)
		default:
		}
		if ticker != nil {
			select {
			case <-ctx.Done():
				m.Interrupted = true
				m.Error = ctx.Err().Error()
				return finish(m)
			case <-ticker.C:
			}
		}
		payload, err := os.ReadFile(files[i%len(files)])
		if err != nil {
			m.Error = err.Error()
			return finish(m)
		}
		sent := time.Now().UTC()
		if m.FirstFrameSentAt == "" {
			m.FirstFrameSentAt = sent.Format(time.RFC3339Nano)
		}
		if err := framing.Write(conn, framing.Frame{ID: uint64(i + 1), SentAtNS: uint64(sent.UnixNano()), Payload: payload}); err != nil {
			if ctx.Err() != nil {
				m.Interrupted = true
			} else {
				m.Error = err.Error()
			}
			return finish(m)
		}
		m.BytesSent += int64(framing.HeaderSize + len(payload))
	}
	return finish(m)
}

func finish(m metric) metric { m.CompletionTime = time.Now().UTC().Format(time.RFC3339Nano); return m }

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
