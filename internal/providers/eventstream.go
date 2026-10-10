package providers

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sort"
	"strconv"
)

// AWS EventStream framing bounds. These are protocol-level and shared by every AWS
// EventStream consumer — Bedrock's invoke-with-response-stream and Kiro's
// CodeWhisperer stream — so one bounds or CRC fix cannot land in only half of the
// codebase.
//
// Frame layout:
// [totalLength u32][headersLength u32][preludeCrc u32][headers][payload][messageCrc u32]
const (
	// MaxEventStreamMessageBytes caps a whole frame. AWS's own guidance is 24 MiB; the
	// older 10 MiB cap silently truncated large but legitimate model responses.
	MaxEventStreamMessageBytes = 24 * 1024 * 1024
	// MaxEventStreamHeadersBytes caps the header block, which bounds the per-header
	// walk before any of it is parsed.
	MaxEventStreamHeadersBytes = 128 * 1024

	// eventStreamPreludeLen is the fixed prelude; eventStreamTrailerLen is the trailing
	// message CRC.
	eventStreamPreludeLen = 12
	eventStreamTrailerLen = 4
	// eventStreamMinFrameLen is the smallest legal frame: a prelude, no headers, no
	// payload, and the trailing CRC.
	eventStreamMinFrameLen = eventStreamPreludeLen + eventStreamTrailerLen
)

// AWS EventStream header value types, per the wire format.
const (
	headerTypeBoolTrue  = 0
	headerTypeBoolFalse = 1
	headerTypeByte      = 2
	headerTypeShort     = 3
	headerTypeInt       = 4
	headerTypeLong      = 5
	headerTypeBytes     = 6
	headerTypeString    = 7
	headerTypeTimestamp = 8
	headerTypeUUID      = 9
)

// ErrEventStreamTruncated reports a stream that ended part-way through a frame.
var ErrEventStreamTruncated = errors.New("aws eventstream ended mid-frame")

// EventStreamFrame is a single parsed AWS EventStream frame.
type EventStreamFrame struct {
	Headers map[string]string
	Payload []byte
}

// EventStreamReader reads AWS EventStream binary frames from an io.Reader.
// Format: https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-streaming.html
type EventStreamReader struct {
	reader io.Reader
}

// NewEventStreamReader creates a reader for AWS EventStream.
func NewEventStreamReader(r io.Reader) *EventStreamReader {
	return &EventStreamReader{reader: r}
}

// ReadFrame reads and parses one EventStream frame.
// Returns nil, nil when the stream ends cleanly on a frame boundary.
//
// Every integrity failure is an error rather than a partial frame, because a silently
// truncated frame becomes a silently truncated model response.
func (r *EventStreamReader) ReadFrame() (*EventStreamFrame, error) {
	prelude := make([]byte, eventStreamPreludeLen)
	read, err := io.ReadFull(r.reader, prelude)
	if err != nil {
		switch {
		case errors.Is(err, io.EOF) && read == 0:
			// Clean end: the stream stopped exactly on a frame boundary.
			return nil, nil
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return nil, fmt.Errorf("%w: got %d of %d prelude bytes", ErrEventStreamTruncated, read, eventStreamPreludeLen)
		default:
			return nil, fmt.Errorf("read eventstream prelude: %w", err)
		}
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headerLen := binary.BigEndian.Uint32(prelude[4:8])

	if totalLen < eventStreamMinFrameLen || totalLen > MaxEventStreamMessageBytes {
		return nil, fmt.Errorf("invalid eventstream frame length %d (bounds %d..%d)",
			totalLen, eventStreamMinFrameLen, MaxEventStreamMessageBytes)
	}
	// headerLen must fit inside the frame with room left for the payload and the
	// trailing CRC; otherwise the slices below would read past the frame on a
	// malicious or corrupt upstream.
	if headerLen > MaxEventStreamHeadersBytes || uint64(headerLen) > uint64(totalLen)-eventStreamMinFrameLen {
		return nil, fmt.Errorf("invalid eventstream header length %d (frame total %d)", headerLen, totalLen)
	}

	// The prelude CRC guards the two length fields before they are used to size anything.
	if binary.BigEndian.Uint32(prelude[8:12]) != crc32.ChecksumIEEE(prelude[0:8]) {
		return nil, errors.New("eventstream prelude CRC mismatch")
	}

	frame := make([]byte, totalLen)
	copy(frame, prelude)
	if _, err := io.ReadFull(r.reader, frame[eventStreamPreludeLen:]); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEventStreamTruncated, err)
	}

	if binary.BigEndian.Uint32(frame[totalLen-4:]) != crc32.ChecksumIEEE(frame[:totalLen-4]) {
		return nil, errors.New("eventstream message CRC mismatch")
	}

	return parseEventFrame(frame[:totalLen], int(headerLen))
}

// parseEventFrame decodes one complete, already length- and CRC-checked frame.
func parseEventFrame(frame []byte, headerLen int) (*EventStreamFrame, error) {
	headers := make(map[string]string, 4)
	headerEnd := eventStreamPreludeLen + headerLen

	if err := decodeEventHeaders(frame, headerEnd, headers); err != nil {
		return nil, err
	}

	payloadStart := eventStreamPreludeLen + headerLen
	payloadEnd := len(frame) - eventStreamTrailerLen
	if payloadEnd <= payloadStart {
		return &EventStreamFrame{Headers: headers}, nil
	}
	return &EventStreamFrame{
		Headers: headers,
		Payload: frame[payloadStart:payloadEnd],
	}, nil
}

// decodeEventHeaders walks the header block with every read bounds-checked against
// headerEnd. The previous decoder used a bare `break` on a malformed header, which made
// a corrupt frame look like a shorter one and left the remaining bytes unexamined.
func decodeEventHeaders(frame []byte, headerEnd int, headers map[string]string) error {
	offset := eventStreamPreludeLen

	need := func(count int) bool { return offset+count <= headerEnd }

	for offset < headerEnd {
		if !need(1) {
			return headerBoundsError(offset, headerEnd, 1)
		}
		nameLen := int(frame[offset])
		offset++

		if !need(nameLen + 1) {
			return headerBoundsError(offset, headerEnd, nameLen+1)
		}
		name := string(frame[offset : offset+nameLen])
		offset += nameLen
		if _, exists := headers[name]; exists {
			return fmt.Errorf("eventstream contains duplicate header %q", name)
		}

		valueType := frame[offset]
		offset++

		value, consumed, err := decodeEventHeaderValue(frame, offset, headerEnd, valueType)
		if err != nil {
			return fmt.Errorf("eventstream header %q: %w", name, err)
		}
		offset += consumed
		headers[name] = value
	}
	return nil
}

func headerBoundsError(offset, headerEnd, count int) error {
	return fmt.Errorf("eventstream header exceeds its declared bounds (need %d byte(s) at offset %d, block ends at %d)",
		count, offset, headerEnd)
}

// decodeEventHeaderValue reads one header value, returning its string form and how many
// bytes it occupied. Types with no useful textual form are rendered numerically so a
// frame using them still parses instead of breaking the walk.
func decodeEventHeaderValue(frame []byte, offset, headerEnd int, valueType byte) (string, int, error) {
	need := func(count int) error {
		if offset+count > headerEnd {
			return fmt.Errorf("value of type %d exceeds header bounds", valueType)
		}
		return nil
	}

	switch valueType {
	case headerTypeBoolTrue, headerTypeBoolFalse:
		return strconv.FormatBool(valueType == headerTypeBoolTrue), 0, nil
	case headerTypeByte:
		if err := need(1); err != nil {
			return "", 0, err
		}
		return strconv.Itoa(int(int8(frame[offset]))), 1, nil
	case headerTypeShort:
		if err := need(2); err != nil {
			return "", 0, err
		}
		return strconv.Itoa(int(int16(binary.BigEndian.Uint16(frame[offset:])))), 2, nil
	case headerTypeInt:
		if err := need(4); err != nil {
			return "", 0, err
		}
		return strconv.FormatInt(int64(int32(binary.BigEndian.Uint32(frame[offset:]))), 10), 4, nil
	case headerTypeLong, headerTypeTimestamp:
		if err := need(8); err != nil {
			return "", 0, err
		}
		return strconv.FormatUint(binary.BigEndian.Uint64(frame[offset:]), 10), 8, nil
	case headerTypeBytes, headerTypeString:
		if err := need(2); err != nil {
			return "", 0, err
		}
		valueLen := int(binary.BigEndian.Uint16(frame[offset:]))
		offset += 2
		if err := need(valueLen); err != nil {
			return "", 0, err
		}
		return string(frame[offset : offset+valueLen]), 2 + valueLen, nil
	case headerTypeUUID:
		if err := need(16); err != nil {
			return "", 0, err
		}
		return hexUUID(frame[offset : offset+16]), 16, nil
	default:
		return "", 0, fmt.Errorf("unknown header value type %d", valueType)
	}
}

func hexUUID(b []byte) string {
	const hexDigits = "0123456789abcdef"
	buf := make([]byte, 36)
	pos := 0
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			buf[pos] = '-'
			pos++
		}
		buf[pos] = hexDigits[c>>4]
		buf[pos+1] = hexDigits[c&0x0f]
		pos += 2
	}
	return string(buf)
}

// EncodeEventFrame builds one complete frame with correct CRCs. Nothing in the request
// path emits EventStream; it lives here so tests and fixtures stay in step with the
// decoder above.
func EncodeEventFrame(headers map[string]string, payload []byte) []byte {
	// Map iteration order is random, so sort: the same input must always encode to the
	// same bytes for a fixture to be reproducible.
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	headerBlock := encodeEventHeaders(headers, names)
	totalLen := eventStreamMinFrameLen + len(headerBlock) + len(payload)
	frame := make([]byte, totalLen)

	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headerBlock)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[0:8]))
	copy(frame[eventStreamPreludeLen:], headerBlock)
	copy(frame[eventStreamPreludeLen+len(headerBlock):], payload)
	binary.BigEndian.PutUint32(frame[totalLen-4:], crc32.ChecksumIEEE(frame[:totalLen-4]))
	return frame
}

func encodeEventHeaders(headers map[string]string, names []string) []byte {
	size := 0
	for _, name := range names {
		// 1 name length + name + 1 type + 2 value length + value
		size += 1 + len(name) + 1 + 2 + len(headers[name])
	}
	block := make([]byte, 0, size)
	for _, name := range names {
		value := headers[name]
		block = append(block, byte(len(name)))
		block = append(block, name...)
		block = append(block, headerTypeString)
		var length [2]byte
		binary.BigEndian.PutUint16(length[:], uint16(len(value)))
		block = append(block, length[:]...)
		block = append(block, value...)
	}
	return block
}

// DecodeEventPayload unmarshals an EventStream payload into dst. It reports false when
// the payload is absent, which is how AWS writes a metadata-only frame.
func DecodeEventPayload(payload []byte, dst any) (bool, error) {
	if len(payload) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return false, fmt.Errorf("eventstream payload is not valid JSON: %w", err)
	}
	return true, nil
}
