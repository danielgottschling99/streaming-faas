package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxPayload = 16 << 20
	headerSize = 52
)

type frame struct {
	id, sentAt                              uint64
	started, decoded, transformed, finished uint64
	payload                                 []byte
}

type tileEvent struct {
	index int
	frame frame
	err   error
	done  bool
}

type fuserSession struct {
	mu       sync.Mutex
	cond     *sync.Cond
	expected []string
	conns    map[string]net.Conn
	started  bool
	startErr error
	done     chan struct{}
}

var session *fuserSession

func init() {
	OnInit = func() {
		names := expectedInputNames()
		session = &fuserSession{expected: names, conns: make(map[string]net.Conn, len(names))}
		session.cond = sync.NewCond(&session.mu)
	}
	OnStreamConnected = handleFuserStream
}

func fn(body string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("frametilefuser requires a stream connection")
}

func handleFuserStream(stream StreamConnection) error {
	if session == nil {
		return fmt.Errorf("frametilefuser session not initialized")
	}
	name := streamName(stream)
	session.mu.Lock()
	if indexOf(session.expected, name) < 0 {
		session.mu.Unlock()
		return fmt.Errorf("unexpected tile input %q; expected %v", name, session.expected)
	}
	if _, exists := session.conns[name]; exists {
		session.mu.Unlock()
		return fmt.Errorf("duplicate tile input %q", name)
	}
	session.conns[name] = stream.Conn
	if len(session.conns) == len(session.expected) && !session.started {
		session.started = true
		session.done = make(chan struct{})
		conns := make([]net.Conn, len(session.expected))
		for i, expected := range session.expected {
			conns[i] = session.conns[expected]
		}
		go session.run(conns)
		session.cond.Broadcast()
	} else {
		for !session.started && session.startErr == nil {
			session.cond.Wait()
		}
	}
	startErr, done := session.startErr, session.done
	session.mu.Unlock()
	if startErr != nil {
		return startErr
	}
	<-done
	return nil
}

func (s *fuserSession) run(conns []net.Conn) {
	events := make(chan tileEvent, len(conns)*2)
	var wg sync.WaitGroup
	for i, conn := range conns {
		wg.Add(1)
		go func(index int, conn net.Conn) {
			defer wg.Done()
			for {
				f, err := readFrame(conn)
				if err != nil {
					if err != io.EOF {
						events <- tileEvent{index: index, err: err}
					}
					events <- tileEvent{index: index, done: true}
					return
				}
				events <- tileEvent{index: index, frame: f}
			}
		}(i, conn)
	}

	pending := make(map[uint64][]frame)
	ready := make(map[uint64][]frame)
	nextID := uint64(1)
	closed := 0
	for closed < len(conns) {
		event := <-events
		if event.err != nil {
			s.setError(event.err)
			return
		}
		if event.done {
			closed++
			continue
		}
		parts := pending[event.frame.id]
		if len(parts) == 0 {
			parts = make([]frame, len(conns))
		}
		if parts[event.index].payload != nil {
			s.setError(fmt.Errorf("duplicate tile %d for frame %d", event.index, event.frame.id))
			return
		}
		parts[event.index] = event.frame
		pending[event.frame.id] = parts
		if complete(parts) {
			delete(pending, event.frame.id)
			ready[event.frame.id] = parts
			for {
				parts, ok := ready[nextID]
				if !ok {
					break
				}
				if err := emitFused(parts); err != nil {
					s.setError(err)
					return
				}
				delete(ready, nextID)
				nextID++
			}
		}
	}
	if len(pending) != 0 || len(ready) != 0 {
		s.setError(fmt.Errorf("incomplete or out-of-order tile set: pending=%d ready=%d next_frame=%d", len(pending), len(ready), nextID))
		return
	}
	s.finish()
	wg.Wait()
}

func emitFused(parts []frame) error {
	images := make([]image.Image, len(parts))
	for i, part := range parts {
		img, err := jpeg.Decode(bytes.NewReader(part.payload))
		if err != nil {
			return fmt.Errorf("decode tile %d of frame %d: %w", i, part.id, err)
		}
		images[i] = img
	}
	full, err := compose(images)
	if err != nil {
		return fmt.Errorf("compose frame %d: %w", parts[0].id, err)
	}
	var payload bytes.Buffer
	if err := jpeg.Encode(&payload, full, &jpeg.Options{Quality: 85}); err != nil {
		return fmt.Errorf("encode frame %d: %w", parts[0].id, err)
	}
	started, decoded, transformed := parts[0].started, parts[0].decoded, parts[0].transformed
	for _, part := range parts[1:] {
		if part.started < started || started == 0 {
			started = part.started
		}
		if part.decoded > decoded {
			decoded = part.decoded
		}
		if part.transformed > transformed {
			transformed = part.transformed
		}
	}
	output := frame{id: parts[0].id, sentAt: parts[0].sentAt, started: started, decoded: decoded, transformed: transformed, finished: uint64(time.Now().UnixNano()), payload: payload.Bytes()}
	var encoded bytes.Buffer
	if err := writeFrame(&encoded, output); err != nil {
		return err
	}
	return SendOutputBytes(encoded.Bytes())
}

func compose(tiles []image.Image) (*image.RGBA, error) {
	columns, rows, err := tileGrid(len(tiles))
	if err != nil {
		return nil, err
	}
	first := tiles[0].Bounds()
	for i, tile := range tiles[1:] {
		if tile.Bounds().Dx() != first.Dx() || tile.Bounds().Dy() != first.Dy() {
			return nil, fmt.Errorf("tile %d dimensions do not match tile 0", i+1)
		}
	}
	tileWidth, tileHeight := first.Dx(), first.Dy()
	out := image.NewRGBA(image.Rect(0, 0, columns*tileWidth, rows*tileHeight))
	for i, tile := range tiles {
		column, row := i%columns, i/columns
		dst := image.Rect(column*tileWidth, row*tileHeight, (column+1)*tileWidth, (row+1)*tileHeight)
		draw.Draw(out, dst, tile, tile.Bounds().Min, draw.Src)
	}
	return out, nil
}

func complete(parts []frame) bool {
	for _, p := range parts {
		if p.payload == nil {
			return false
		}
	}
	return true
}
func indexOf(values []string, value string) int {
	for i, v := range values {
		if v == value {
			return i
		}
	}
	return -1
}
func streamName(stream StreamConnection) string {
	if stream.Headers != nil {
		if v := strings.TrimSpace(stream.Headers["X-Faas-Upstream-Function"]); v != "" {
			return v
		}
	}
	return strings.TrimSpace(stream.Name)
}
func expectedInputNames() []string {
	raw := strings.TrimSpace(os.Getenv("FUSER_INPUT_NAMES"))
	if raw == "" {
		count := 4
		if configured := strings.TrimSpace(os.Getenv("FRAME_TILE_WORKERS")); configured != "" {
			if parsed, err := strconv.Atoi(configured); err == nil {
				count = parsed
			}
		}
		names := make([]string, count)
		for i := range names {
			names[i] = fmt.Sprintf("frame-tile%d", i)
		}
		return names
	}
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func tileGrid(count int) (columns, rows int, err error) {
	if count < 1 || count&(count-1) != 0 {
		return 0, 0, fmt.Errorf("tile worker count must be a positive power of two, got %d", count)
	}
	columns = 1
	for columns*columns < count {
		columns *= 2
	}
	return columns, count / columns, nil
}
func (s *fuserSession) setError(err error) { s.mu.Lock(); s.startErr = err; s.mu.Unlock(); s.finish() }
func (s *fuserSession) finish() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

func readFrame(r io.Reader) (frame, error) {
	var h [headerSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return frame{}, err
	}
	n := uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
	if n == 0 || n > maxPayload {
		return frame{}, fmt.Errorf("invalid frame payload length %d", n)
	}
	p := make([]byte, int(n))
	if _, err := io.ReadFull(r, p); err != nil {
		return frame{}, err
	}
	return frame{id: uint64FromBytes(h[4:12]), sentAt: uint64FromBytes(h[12:20]), started: uint64FromBytes(h[20:28]), decoded: uint64FromBytes(h[28:36]), transformed: uint64FromBytes(h[36:44]), finished: uint64FromBytes(h[44:52]), payload: p}, nil
}
func writeFrame(w io.Writer, f frame) error {
	var h [headerSize]byte
	h[0] = byte(len(f.payload) >> 24)
	h[1] = byte(len(f.payload) >> 16)
	h[2] = byte(len(f.payload) >> 8)
	h[3] = byte(len(f.payload))
	putUint64(h[4:12], f.id)
	putUint64(h[12:20], f.sentAt)
	putUint64(h[20:28], f.started)
	putUint64(h[28:36], f.decoded)
	putUint64(h[36:44], f.transformed)
	putUint64(h[44:52], f.finished)
	return writeAll(w, h[:], f.payload)
}
func uint64FromBytes(p []byte) uint64 {
	var n uint64
	for _, b := range p {
		n = n<<8 | uint64(b)
	}
	return n
}
func putUint64(p []byte, n uint64) {
	for i := len(p) - 1; i >= 0; i-- {
		p[i] = byte(n)
		n >>= 8
	}
}
func writeAll(w io.Writer, parts ...[]byte) error {
	for _, p := range parts {
		for len(p) > 0 {
			n, err := w.Write(p)
			if err != nil {
				return err
			}
			if n <= 0 || n > len(p) {
				return io.ErrShortWrite
			}
			p = p[n:]
		}
	}
	return nil
}
