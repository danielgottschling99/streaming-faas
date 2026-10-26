// Package framing implements the normalized JPEG benchmark wire protocol.
package framing

import (
	"encoding/binary"
	"fmt"
	"io"
)

const MaxPayload = 16 << 20
const HeaderSize = 52

type Frame struct {
	ID                         uint64
	SentAtNS                   uint64
	ProcessingStartedAtNS      uint64
	DecodeFinishedAtNS         uint64
	TransformationFinishedAtNS uint64
	ProcessingFinishedAtNS     uint64
	Payload                    []byte
}

func Read(r io.Reader) (Frame, error) {
	var header [HeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(header[:4])
	if length == 0 {
		return Frame{}, fmt.Errorf("invalid frame payload length: 0")
	}
	if length > MaxPayload {
		return Frame{}, fmt.Errorf("frame payload length %d exceeds maximum %d", length, MaxPayload)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, fmt.Errorf("read frame payload (%d bytes): %w", length, err)
	}
	return Frame{
		ID:                         binary.BigEndian.Uint64(header[4:12]),
		SentAtNS:                   binary.BigEndian.Uint64(header[12:20]),
		ProcessingStartedAtNS:      binary.BigEndian.Uint64(header[20:28]),
		DecodeFinishedAtNS:         binary.BigEndian.Uint64(header[28:36]),
		TransformationFinishedAtNS: binary.BigEndian.Uint64(header[36:44]),
		ProcessingFinishedAtNS:     binary.BigEndian.Uint64(header[44:52]),
		Payload:                    payload,
	}, nil
}

func Write(w io.Writer, frame Frame) error {
	if len(frame.Payload) == 0 || len(frame.Payload) > MaxPayload {
		return fmt.Errorf("invalid frame payload length: %d", len(frame.Payload))
	}
	var header [HeaderSize]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(frame.Payload)))
	binary.BigEndian.PutUint64(header[4:12], frame.ID)
	binary.BigEndian.PutUint64(header[12:20], frame.SentAtNS)
	binary.BigEndian.PutUint64(header[20:28], frame.ProcessingStartedAtNS)
	binary.BigEndian.PutUint64(header[28:36], frame.DecodeFinishedAtNS)
	binary.BigEndian.PutUint64(header[36:44], frame.TransformationFinishedAtNS)
	binary.BigEndian.PutUint64(header[44:52], frame.ProcessingFinishedAtNS)
	if err := writeFull(w, header[:]); err != nil {
		return err
	}
	return writeFull(w, frame.Payload)
}

func writeFull(w io.Writer, p []byte) error {
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
	return nil
}
