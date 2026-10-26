package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"time"
)

func init() { OnStreamConnected = processFrames }

func fn(body string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("colorfilter requires a stream connection")
}

func processFrames(stream StreamConnection) error {
	for {
		var header [52]byte
		if _, err := io.ReadFull(stream.Conn, header[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		length := binary.BigEndian.Uint32(header[:4])
		if length == 0 || length > 16<<20 {
			return fmt.Errorf("invalid frame payload length %d", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(stream.Conn, payload); err != nil {
			return err
		}

		binary.BigEndian.PutUint64(header[20:28], uint64(time.Now().UnixNano()))
		src, err := jpeg.Decode(bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("decode frame: %w", err)
		}
		binary.BigEndian.PutUint64(header[28:36], uint64(time.Now().UnixNano()))
		filtered := sepia(src)
		binary.BigEndian.PutUint64(header[36:44], uint64(time.Now().UnixNano()))
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, filtered, &jpeg.Options{Quality: 90}); err != nil {
			return err
		}
		if encoded.Len() > 16<<20 {
			return fmt.Errorf("output frame exceeds 16 MiB")
		}
		binary.BigEndian.PutUint64(header[44:52], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint32(header[:4], uint32(encoded.Len()))
		// Keep the original frame ID and source timestamp in header[4:20].
		if err := SendOutputBytes(append(header[:], encoded.Bytes()...)); err != nil {
			return err
		}
	}
}

func sepia(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	out := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			r, g, b = r>>8, g>>8, b>>8
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(min(255, (393*r+769*g+189*b)/1000)),
				G: uint8(min(255, (349*r+686*g+168*b)/1000)),
				B: uint8(min(255, (272*r+534*g+131*b)/1000)),
				A: 255,
			})
		}
	}
	return out
}
