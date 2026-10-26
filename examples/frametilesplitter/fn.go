package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
)

const (
	maxPayload = 16 << 20
	headerSize = 52
)

type framedImage struct {
	id      uint64
	sentAt  uint64
	payload []byte
}

func init() {
	OnStreamConnected = splitFrames
}

func fn(body string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("frametilesplitter requires a stream connection")
}

func splitFrames(stream StreamConnection) error {
	targets := OutputTargetNames()
	if _, _, err := tileGrid(len(targets)); err != nil {
		return fmt.Errorf("frametilesplitter output targets: %w (%v)", err, targets)
	}

	for {
		input, err := readFrame(stream.Conn)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		img, err := jpeg.Decode(bytes.NewReader(input.payload))
		if err != nil {
			return fmt.Errorf("decode frame %d: %w", input.id, err)
		}
		tiles, err := makeTiles(img, len(targets))
		if err != nil {
			return fmt.Errorf("split frame %d: %w", input.id, err)
		}
		for i, tile := range tiles {
			payload, err := encodeJPEG(tile)
			if err != nil {
				return fmt.Errorf("encode tile %d of frame %d: %w", i, input.id, err)
			}
			var out bytes.Buffer
			if err := writeFrame(&out, framedImage{id: input.id, sentAt: input.sentAt, payload: payload}); err != nil {
				return err
			}
			if err := SendOutputBytesTo(targets[i], out.Bytes()); err != nil {
				return fmt.Errorf("send tile %d of frame %d: %w", i, input.id, err)
			}
		}
	}
}

func makeTiles(src image.Image, count int) ([]image.Image, error) {
	columns, rows, err := tileGrid(count)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < columns || h < rows || w%columns != 0 || h%rows != 0 {
		return nil, fmt.Errorf("image dimensions %dx%d are not evenly divisible by %dx%d tile grid", w, h, columns, rows)
	}
	tiles := make([]image.Image, 0, count)
	tileWidth, tileHeight := w/columns, h/rows
	for i := 0; i < count; i++ {
		column, row := i%columns, i/columns
		region := image.Rect(
			b.Min.X+column*tileWidth,
			b.Min.Y+row*tileHeight,
			b.Min.X+(column+1)*tileWidth,
			b.Min.Y+(row+1)*tileHeight,
		)
		tile := image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
		draw.Draw(tile, tile.Bounds(), src, region.Min, draw.Src)
		tiles = append(tiles, tile)
	}
	return tiles, nil
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

func encodeJPEG(img image.Image) ([]byte, error) {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readFrame(r io.Reader) (framedImage, error) {
	var h [headerSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return framedImage{}, err
	}
	n := uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
	if n == 0 || n > maxPayload {
		return framedImage{}, fmt.Errorf("invalid frame payload length %d", n)
	}
	payload := make([]byte, int(n))
	if _, err := io.ReadFull(r, payload); err != nil {
		return framedImage{}, fmt.Errorf("read frame payload: %w", err)
	}
	return framedImage{
		id: uint64FromBytes(h[4:12]), sentAt: uint64FromBytes(h[12:20]), payload: payload,
	}, nil
}

func writeFrame(w io.Writer, frame framedImage) error {
	if len(frame.payload) == 0 || len(frame.payload) > maxPayload {
		return fmt.Errorf("invalid output payload length %d", len(frame.payload))
	}
	var h [headerSize]byte
	h[0] = byte(len(frame.payload) >> 24)
	h[1] = byte(len(frame.payload) >> 16)
	h[2] = byte(len(frame.payload) >> 8)
	h[3] = byte(len(frame.payload))
	putUint64(h[4:12], frame.id)
	putUint64(h[12:20], frame.sentAt)
	return writeAll(w, h[:], frame.payload)
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
	for _, part := range parts {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n <= 0 || n > len(part) {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}
