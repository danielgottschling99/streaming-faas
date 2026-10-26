package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"time"
)

//go:embed 3S.png
var watermarkPNG []byte

func init() { OnStreamConnected = processFrames }

func fn(body string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("watermark requires a stream connection")
}

func processFrames(stream StreamConnection) error {
	logo, err := png.Decode(bytes.NewReader(watermarkPNG))
	if err != nil {
		return fmt.Errorf("decode watermark: %w", err)
	}
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
		marked := addWatermark(src, logo)
		binary.BigEndian.PutUint64(header[36:44], uint64(time.Now().UnixNano()))
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, marked, &jpeg.Options{Quality: 90}); err != nil {
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

func addWatermark(src, logo image.Image) *image.RGBA {
	bounds := src.Bounds()
	out := image.NewRGBA(bounds)
	draw.Draw(out, bounds, src, bounds.Min, draw.Src)
	shortSide := min(bounds.Dx(), bounds.Dy())
	logoBounds := logo.Bounds()
	if shortSide == 0 || logoBounds.Empty() {
		return out
	}
	size := max(1, min(96, shortSide/5))
	longSide := max(logoBounds.Dx(), logoBounds.Dy())
	width := max(1, logoBounds.Dx()*size/longSide)
	height := max(1, logoBounds.Dy()*size/longSide)
	scaled := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			scaled.Set(x, y, logo.At(
				logoBounds.Min.X+x*logoBounds.Dx()/width,
				logoBounds.Min.Y+y*logoBounds.Dy()/height,
			))
		}
	}
	margin := min(max(2, size/4), (shortSide-size)/2)
	left, top := bounds.Max.X-margin-width, bounds.Max.Y-margin-height
	draw.Draw(out, image.Rect(left, top, left+width, top+height), scaled, image.Point{}, draw.Over)
	return out
}
