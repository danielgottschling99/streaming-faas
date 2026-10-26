// Command frame-player displays normalized JPEG frames using ffplay.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"streaming-faas/benchmark/framing"
)

func main() {
	listen := flag.String("listen", "0.0.0.0:9100", "address on which to listen")
	ffplay := flag.String("ffplay", "ffplay", "ffplay executable")
	fps := flag.Float64("fps", 30, "playback rate in frames per second")
	flag.Parse()
	if *fps <= 0 {
		log.Fatal("-fps must be positive")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	log.Printf("frame player listening on %s", *listen)

	conn, err := accept(ctx, ln)
	if err != nil {
		if ctx.Err() == nil {
			log.Fatal(err)
		}
		return
	}
	defer conn.Close()

	cmd := exec.CommandContext(ctx, *ffplay,
		"-hide_banner", "-loglevel", "warning", "-window_title", "streaming-faas segmentation", "-fflags", "nobuffer", "-f", "mjpeg", "-framerate", formatFPS(*fps), "-i", "pipe:0")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}

	frameInterval := time.Duration(float64(time.Second) / *fps)
	nextFrameAt := time.Now()
	for {
		frame, err := framing.Read(conn)
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				log.Printf("read frame: %v", err)
			}
			break
		}
		if err := waitUntil(ctx, nextFrameAt); err != nil {
			break
		}
		if _, err := stdin.Write(frame.Payload); err != nil {
			log.Printf("write ffplay: %v", err)
			break
		}
		nextFrameAt = nextFrameAt.Add(frameInterval)
		if nextFrameAt.Before(time.Now()) {
			nextFrameAt = time.Now()
		}
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		log.Printf("ffplay: %v", err)
	}
}

func waitUntil(ctx context.Context, deadline time.Time) error {
	delay := time.Until(deadline)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func formatFPS(fps float64) string {
	return strconv.FormatFloat(fps, 'f', -1, 64)
}

func accept(ctx context.Context, ln net.Listener) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		accepted <- result{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = ln.Close()
		return nil, ctx.Err()
	case result := <-accepted:
		return result.conn, result.err
	}
}
