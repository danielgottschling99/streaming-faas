package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"strconv"
	"time"
)

const (
	maxPayload = 16 << 20
	headerSize = 52
)

func init() { OnStreamConnected = processFrames }

func fn(body string, headers map[string]string) (string, error) {
	return "", fmt.Errorf("segmentation-demo requires a stream connection")
}

func processFrames(stream StreamConnection) error {
	for {
		input, err := readFrame(stream.Conn)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		started := uint64(nowNS())
		src, err := jpeg.Decode(bytes.NewReader(input.payload))
		if err != nil {
			return fmt.Errorf("decode frame %d: %w", input.id, err)
		}
		decoded := uint64(nowNS())
		mask := segment(src)
		transformed := uint64(nowNS())
		var payload bytes.Buffer
		if err := jpeg.Encode(&payload, mask, &jpeg.Options{Quality: 85}); err != nil {
			return fmt.Errorf("encode frame %d: %w", input.id, err)
		}
		finished := uint64(nowNS())

		var output bytes.Buffer
		if err := writeFrame(&output, input.id, input.sentAt, started, decoded, transformed, finished, payload.Bytes()); err != nil {
			return err
		}
		if err := SendOutputBytes(output.Bytes()); err != nil {
			return err
		}
	}
}

func segment(src image.Image) *image.Gray {
	bounds := src.Bounds()
	content := contentBounds(src, bounds)
	seed := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := content.Min.X; x < content.Max.X; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			r8, g8, b8 := uint8(r>>8), uint8(g>>8), uint8(b>>8)
			brightness := (uint16(r8) + uint16(g8) + uint16(b8)) / 3
			warm := int(r8) > int(b8)+10 && int(r8) > int(g8)+6
			dark := brightness < 105 && b8 <= r8+12
			if (warm && brightness > 55) || dark {
				seed.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}

	start, ok := nearestSeed(seed, bounds)
	mask := image.NewGray(bounds)
	if ok {
		floodFill(seed, mask, start)
		for i := 0; i < 2; i++ {
			mask = dilate(mask)
		}
		for i := 0; i < refinementPasses; i++ {
			mask = refine(mask)
		}
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < content.Min.X; x++ {
			mask.SetGray(x, y, color.Gray{})
		}
		for x := content.Max.X; x < bounds.Max.X; x++ {
			mask.SetGray(x, y, color.Gray{})
		}
	}
	return mask
}

var refinementPasses = configuredRefinementPasses()

func configuredRefinementPasses() int {
	passes, err := strconv.Atoi(os.Getenv("SEGMENTATION_REFINEMENT_PASSES"))
	if err != nil || passes < 0 {
		return 0
	}
	return passes
}

func contentBounds(src image.Image, bounds image.Rectangle) image.Rectangle {
	left := bounds.Min.X
	for left < bounds.Max.X && paddingColumn(src, bounds, left) {
		left++
	}
	right := bounds.Max.X
	for right > left && paddingColumn(src, bounds, right-1) {
		right--
	}
	return image.Rect(left, bounds.Min.Y, right, bounds.Max.Y)
}

func paddingColumn(src image.Image, bounds image.Rectangle, x int) bool {
	maxBrightness := uint8(0)
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 16 {
		r, g, b, _ := src.At(x, y).RGBA()
		brightness := uint8(((r >> 8) + (g >> 8) + (b >> 8)) / 3)
		if brightness > maxBrightness {
			maxBrightness = brightness
		}
	}
	return maxBrightness < 32
}

func nearestSeed(seed *image.Gray, bounds image.Rectangle) (image.Point, bool) {
	cx, cy := bounds.Min.X+bounds.Dx()/2, bounds.Min.Y+bounds.Dy()/2
	best, distance := image.Point{}, int(^uint(0)>>1)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if seed.GrayAt(x, y).Y == 0 {
				continue
			}
			d := (x-cx)*(x-cx) + (y-cy)*(y-cy)
			if d < distance {
				best, distance = image.Point{X: x, Y: y}, d
			}
		}
	}
	return best, distance != int(^uint(0)>>1)
}

func floodFill(seed, dst *image.Gray, start image.Point) {
	queue := []image.Point{start}
	seed.SetGray(start.X, start.Y, color.Gray{})
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		dst.SetGray(p.X, p.Y, color.Gray{Y: 255})
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				n := image.Point{X: p.X + dx, Y: p.Y + dy}
				if n.In(seed.Bounds()) && seed.GrayAt(n.X, n.Y).Y != 0 {
					seed.SetGray(n.X, n.Y, color.Gray{})
					queue = append(queue, n)
				}
			}
		}
	}
}

func dilate(src *image.Gray) *image.Gray {
	dst := image.NewGray(src.Bounds())
	for y := src.Bounds().Min.Y; y < src.Bounds().Max.Y; y++ {
		for x := src.Bounds().Min.X; x < src.Bounds().Max.X; x++ {
			if src.GrayAt(x, y).Y == 0 {
				continue
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					p := image.Point{X: x + dx, Y: y + dy}
					if p.In(dst.Bounds()) {
						dst.SetGray(p.X, p.Y, color.Gray{Y: 255})
					}
				}
			}
		}
	}
	return dst
}

func refine(src *image.Gray) *image.Gray {
	dst := image.NewGray(src.Bounds())
	bounds := src.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			foreground := 0
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					p := image.Point{X: x + dx, Y: y + dy}
					if p.In(bounds) && src.GrayAt(p.X, p.Y).Y != 0 {
						foreground++
					}
				}
			}
			if foreground >= 7 {
				dst.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	return dst
}

func readFrame(r io.Reader) (frame, error) {
	var h [headerSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return frame{}, err
	}
	n := binary.BigEndian.Uint32(h[:4])
	if n == 0 || n > maxPayload {
		return frame{}, fmt.Errorf("invalid payload length %d", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame{}, err
	}
	return frame{id: binary.BigEndian.Uint64(h[4:12]), sentAt: binary.BigEndian.Uint64(h[12:20]), payload: payload}, nil
}

func writeFrame(w io.Writer, id, sentAt, started, decoded, transformed, finished uint64, payload []byte) error {
	var h [headerSize]byte
	binary.BigEndian.PutUint32(h[:4], uint32(len(payload)))
	binary.BigEndian.PutUint64(h[4:12], id)
	binary.BigEndian.PutUint64(h[12:20], sentAt)
	binary.BigEndian.PutUint64(h[20:28], started)
	binary.BigEndian.PutUint64(h[28:36], decoded)
	binary.BigEndian.PutUint64(h[36:44], transformed)
	binary.BigEndian.PutUint64(h[44:52], finished)
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func nowNS() int64 { return time.Now().UnixNano() }

type frame struct {
	id, sentAt uint64
	payload    []byte
}
