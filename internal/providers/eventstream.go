package providers

import (
	"encoding/binary"
	"fmt"
	"io"
)

// EventStreamFrame is a single parsed AWS EventStream frame.
type EventStreamFrame struct {
	Headers map[string]string
	Payload []byte
}

// EventStreamReader reads AWS EventStream binary frames from an io.Reader.
// Format: https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-streaming.html
type EventStreamReader struct {
	reader io.Reader
	buf    []byte
}

// NewEventStreamReader creates a reader for AWS EventStream.
func NewEventStreamReader(r io.Reader) *EventStreamReader {
	return &EventStreamReader{reader: r}
}

// ReadFrame reads and parses one EventStream frame.
// Returns nil, nil when stream ends cleanly.
func (r *EventStreamReader) ReadFrame() (*EventStreamFrame, error) {
	// Read 12-byte prelude
	prelude := make([]byte, 12)
	if _, err := io.ReadFull(r.reader, prelude); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, nil
		}
		return nil, fmt.Errorf("read prelude: %w", err)
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headerLen := binary.BigEndian.Uint32(prelude[4:8])
	// preludeCrc at bytes 8-12, skip validation

	if totalLen < 12 || totalLen > 10*1024*1024 {
		return nil, fmt.Errorf("invalid frame total length: %d", totalLen)
	}

	// Read remaining bytes
	frame := make([]byte, totalLen)
	copy(frame, prelude)
	if _, err := io.ReadFull(r.reader, frame[12:]); err != nil {
		return nil, fmt.Errorf("read frame body: %w", err)
	}

	// headerLen is actual header bytes — no adjustment needed
	headerBytes := frame[12 : 12+int(headerLen)]

	headers := make(map[string]string)
	pos := 0
	for pos < len(headerBytes) {
		if pos+1 > len(headerBytes) {
			break
		}
		nameLen := int(headerBytes[pos])
		pos++
		if pos+nameLen > len(headerBytes) {
			break
		}
		name := string(headerBytes[pos : pos+nameLen])
		pos += nameLen

		if pos+3 > len(headerBytes) {
			break
		}
		valueType := headerBytes[pos] // 7 = string
		pos++
		valLen := int(binary.BigEndian.Uint16(headerBytes[pos : pos+2]))
		pos += 2
		if pos+valLen > len(headerBytes) {
			break
		}
		if valueType == 7 || valueType == 6 || valueType == 3 {
			headers[name] = string(headerBytes[pos : pos+valLen])
		}
		pos += valLen
	}

	payloadStart := 12 + int(headerLen)
	payloadEnd := int(totalLen) - 4 // exclude trailing CRC
	if payloadEnd > payloadStart {
		return &EventStreamFrame{
			Headers: headers,
			Payload: frame[payloadStart:payloadEnd],
		}, nil
	}

	return &EventStreamFrame{
		Headers: headers,
		Payload: nil,
	}, nil
}
