package providers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return encoded
}

func TestEventStreamReaderDecodesAWellFormedFrame(t *testing.T) {
	payload := mustJSON(t, map[string]any{"type": "content_block_delta", "delta": map[string]string{"text": "hi"}})
	frame := EncodeEventFrame(map[string]string{
		":event-type":   "chunk",
		":message-type": "event",
		":content-type": "application/json",
	}, payload)

	reader := NewEventStreamReader(bytes.NewReader(frame))
	parsed, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if parsed == nil {
		t.Fatal("ReadFrame() = nil, want a frame")
	}
	if got, want := parsed.Headers[":event-type"], "chunk"; got != want {
		t.Errorf(":event-type = %q, want %q", got, want)
	}
	if got, want := parsed.Headers[":message-type"], "event"; got != want {
		t.Errorf(":message-type = %q, want %q", got, want)
	}
	if !bytes.Equal(parsed.Payload, payload) {
		t.Errorf("Payload = %q, want %q", parsed.Payload, payload)
	}
}

func TestEventStreamReaderReadsAWholeStream(t *testing.T) {
	var stream []byte
	want := []string{"first", "second", "third"}
	for _, text := range want {
		stream = append(stream, EncodeEventFrame(map[string]string{":event-type": "chunk"}, mustJSON(t, map[string]string{"text": text}))...)
	}

	reader := NewEventStreamReader(bytes.NewReader(stream))
	for i, expected := range want {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: ReadFrame() error = %v", i, err)
		}
		if frame == nil {
			t.Fatalf("frame %d: ReadFrame() = nil, want a frame", i)
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			t.Fatalf("frame %d: unmarshal: %v", i, err)
		}
		if payload.Text != expected {
			t.Errorf("frame %d text = %q, want %q", i, payload.Text, expected)
		}
	}

	final, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("final ReadFrame() error = %v", err)
	}
	if final != nil {
		t.Errorf("final ReadFrame() = %+v, want nil at a clean end of stream", final)
	}
}

// The reader hands back a sub-slice of one per-frame allocation. A caller that mutates
// the payload must not be able to corrupt a later frame read from the same buffer.
func TestEventStreamFramePayloadIsIndependentPerFrame(t *testing.T) {
	first := mustJSON(t, map[string]string{"text": "aaaa"})
	second := mustJSON(t, map[string]string{"text": "bbbb"})
	stream := append(
		EncodeEventFrame(map[string]string{":event-type": "chunk"}, first),
		EncodeEventFrame(map[string]string{":event-type": "chunk"}, second)...,
	)

	reader := NewEventStreamReader(bytes.NewReader(stream))
	frameA, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("first ReadFrame() error = %v", err)
	}
	copy(frameA.Payload, "zzzz")
	frameB, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("second ReadFrame() error = %v", err)
	}
	if !bytes.Equal(frameB.Payload, second) {
		t.Errorf("second payload = %q, want %q", frameB.Payload, second)
	}
}

func TestEventStreamReaderEmptyFrameHasNilPayload(t *testing.T) {
	frame := EncodeEventFrame(map[string]string{":event-type": "metadata"}, nil)

	parsed, err := NewEventStreamReader(bytes.NewReader(frame)).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if parsed == nil {
		t.Fatal("ReadFrame() = nil, want a frame")
	}
	if parsed.Payload != nil {
		t.Errorf("Payload = %q, want nil for a metadata-only frame", parsed.Payload)
	}
	if got, want := parsed.Headers[":event-type"], "metadata"; got != want {
		t.Errorf(":event-type = %q, want %q", got, want)
	}
}

func TestEventStreamReaderRejectsCorruptFrames(t *testing.T) {
	valid := EncodeEventFrame(map[string]string{":event-type": "chunk"}, mustJSON(t, map[string]string{"bytes": "aGk="}))

	corruptPreludeCRC := bytes.Clone(valid)
	binary.BigEndian.PutUint32(corruptPreludeCRC[8:12], 0xDEADBEEF)

	corruptMessageCRC := bytes.Clone(valid)
	corruptMessageCRC[len(corruptMessageCRC)-1] ^= 0xFF

	corruptPayload := bytes.Clone(valid)
	// Flip a payload byte, leaving both CRC fields stale — the case the trailing CRC
	// exists for.
	corruptPayload[len(corruptPayload)-6] ^= 0xFF

	tooShort := make([]byte, eventStreamPreludeLen)
	binary.BigEndian.PutUint32(tooShort[0:4], 8)
	binary.BigEndian.PutUint32(tooShort[4:8], 0)
	binary.BigEndian.PutUint32(tooShort[8:12], crc32.ChecksumIEEE(tooShort[0:8]))

	oversized := make([]byte, 12)
	binary.BigEndian.PutUint32(oversized[0:4], MaxEventStreamMessageBytes+1)
	binary.BigEndian.PutUint32(oversized[4:8], 0)
	binary.BigEndian.PutUint32(oversized[8:12], crc32.ChecksumIEEE(oversized[0:8]))

	overHeaders := make([]byte, 12)
	binary.BigEndian.PutUint32(overHeaders[0:4], eventStreamMinFrameLen)
	binary.BigEndian.PutUint32(overHeaders[4:8], 4)
	binary.BigEndian.PutUint32(overHeaders[8:12], crc32.ChecksumIEEE(overHeaders[0:8]))

	overHeadersCap := make([]byte, 12)
	binary.BigEndian.PutUint32(overHeadersCap[0:4], eventStreamMinFrameLen)
	binary.BigEndian.PutUint32(overHeadersCap[4:8], MaxEventStreamHeadersBytes+1)
	binary.BigEndian.PutUint32(overHeadersCap[8:12], crc32.ChecksumIEEE(overHeadersCap[0:8]))

	tests := []struct {
		name    string
		frame   []byte
		wantErr string
	}{
		{
			name:    "prelude CRC",
			frame:   corruptPreludeCRC,
			wantErr: "prelude CRC mismatch",
		},
		{
			name:    "message CRC",
			frame:   corruptMessageCRC,
			wantErr: "message CRC mismatch",
		},
		{
			name:    "payload byte flipped under a stale CRC",
			frame:   corruptPayload,
			wantErr: "message CRC mismatch",
		},
		{
			name:    "total length below the minimum frame size",
			frame:   tooShort,
			wantErr: "invalid eventstream frame length",
		},
		{
			name:    "total length beyond the protocol cap",
			frame:   oversized,
			wantErr: "invalid eventstream frame length",
		},
		{
			name:    "header length exceeding the frame",
			frame:   overHeaders,
			wantErr: "invalid eventstream header length",
		},
		{
			name:    "header length beyond the protocol cap",
			frame:   overHeadersCap,
			wantErr: "invalid eventstream header length",
		},
		{
			name:    "stream cut inside the prelude",
			frame:   valid[:6],
			wantErr: "mid-frame",
		},
		{
			name:    "stream cut inside the frame body",
			frame:   valid[:len(valid)-3],
			wantErr: "mid-frame",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := NewEventStreamReader(bytes.NewReader(tt.frame)).ReadFrame()
			if err == nil {
				t.Fatalf("ReadFrame() = %+v, want error containing %q", frame, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestEventStreamReaderTruncationIsIdentifiable(t *testing.T) {
	frame := EncodeEventFrame(map[string]string{":event-type": "chunk"}, []byte(`{"bytes":"aGk="}`))
	_, err := NewEventStreamReader(bytes.NewReader(frame[:len(frame)-2])).ReadFrame()
	if err == nil {
		t.Fatal("ReadFrame() succeeded, want a truncation error")
	}
	// The Bedrock executor distinguishes "the upstream hung up mid-frame" from a
	// protocol failure, so the sentinel has to survive wrapping.
	if !errors.Is(err, ErrEventStreamTruncated) {
		t.Errorf("error = %v, want it to wrap ErrEventStreamTruncated", err)
	}
}

// The old decoder used a bare `break` on a malformed header, which made a corrupt frame
// look like a shorter one. Every malformed-header shape must now be an error.
func TestEventStreamReaderRejectsMalformedHeaders(t *testing.T) {
	buildFrame := func(headerBlock []byte, declaredHeaderLen uint32) []byte {
		totalLen := eventStreamMinFrameLen + len(headerBlock)
		frame := make([]byte, totalLen)
		binary.BigEndian.PutUint32(frame[0:4], uint32(totalLen))
		binary.BigEndian.PutUint32(frame[4:8], declaredHeaderLen)
		binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[0:8]))
		copy(frame[eventStreamPreludeLen:], headerBlock)
		binary.BigEndian.PutUint32(frame[totalLen-4:], crc32.ChecksumIEEE(frame[:totalLen-4]))
		return frame
	}

	tests := []struct {
		name    string
		frame   []byte
		wantErr string
	}{
		{
			// 15 bytes in the block, but the name claims 40 bytes of it.
			name: "name length runs past the header block",
			frame: buildFrame([]byte{
				40, ':', 'e', 'v', 'e', 'n', 't', '-', 't', 'y', 'p', 'e', headerTypeString, 0, 1,
			}, 15),
			wantErr: "exceeds its declared bounds",
		},
		{
			// The name is ":a:b:c" (6 bytes); the string value it declares is 65535
			// bytes inside a 12-byte header block.
			name: "string value length runs past the header block",
			frame: buildFrame([]byte{
				6, ':', 'a', ':', 'b', ':', 'c', headerTypeString, 0xFF, 0xFF, 'x', 'y',
			}, 12),
			wantErr: "exceeds header bounds",
		},
		{
			name: "duplicate header name",
			frame: buildFrame([]byte{
				1, 'a', headerTypeString, 0, 1, 'x',
				1, 'a', headerTypeString, 0, 1, 'y',
			}, 12),
			wantErr: "duplicate header",
		},
		{
			name: "unknown value type",
			frame: buildFrame([]byte{
				1, 'a', 99,
			}, 3),
			wantErr: "unknown header value type",
		},
		{
			// The name fits, but the value type byte that must follow it does not.
			name: "missing value type byte",
			frame: buildFrame([]byte{
				1, 'a',
			}, 2),
			wantErr: "exceeds its declared bounds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := NewEventStreamReader(bytes.NewReader(tt.frame)).ReadFrame()
			if err == nil {
				t.Fatalf("ReadFrame() = %+v, want error containing %q", frame, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// Bedrock and Kiro only ever read type-7 string headers, but a frame carrying a boolean
// or numeric header must still parse rather than break the walk for the headers after it.
func TestEventStreamReaderDecodesNonStringHeaderTypes(t *testing.T) {
	block := []byte{
		8, ':', 'e', 'n', 'a', 'b', 'l', 'e', 'd', headerTypeBoolTrue,
		8, ':', 'f', 'l', 'a', 'g', 'g', 'e', 'd', headerTypeString, 0, 1, 'y',
		8, ':', 'n', 'u', 'm', 'b', 'r', ':', '0', headerTypeInt, 0xFF, 0xFF, 0xFF, 0xFF,
	}
	totalLen := eventStreamMinFrameLen + len(block)
	frame := make([]byte, totalLen)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(block)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[0:8]))
	copy(frame[eventStreamPreludeLen:], block)
	binary.BigEndian.PutUint32(frame[totalLen-4:], crc32.ChecksumIEEE(frame[:totalLen-4]))

	parsed, err := NewEventStreamReader(bytes.NewReader(frame)).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	want := map[string]string{
		":enabled": "true",
		":flagged": "y",
		":numbr:0": "-1",
	}
	for name, value := range want {
		if got := parsed.Headers[name]; got != value {
			t.Errorf("header %q = %q, want %q", name, got, value)
		}
	}
}

func TestEncodeEventFrameIsDeterministic(t *testing.T) {
	headers := map[string]string{
		":event-type": "chunk", ":message-type": "event",
		":content-type": "application/json", ":extra": "value",
	}
	first := EncodeEventFrame(headers, []byte(`{"bytes":"aGk="}`))
	for range 20 {
		if !bytes.Equal(first, EncodeEventFrame(headers, []byte(`{"bytes":"aGk="}`))) {
			t.Fatal("EncodeEventFrame produced different bytes for the same input")
		}
	}
}

func TestDecodeEventPayload(t *testing.T) {
	tests := []struct {
		name      string
		payload   []byte
		wantOK    bool
		wantErr   bool
		wantField string
	}{
		{name: "absent payload", payload: nil, wantOK: false},
		{name: "valid object", payload: []byte(`{"bytes":"aGk="}`), wantOK: true, wantField: "aGk="},
		{name: "invalid JSON", payload: []byte(`{"bytes":`), wantOK: false, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst struct {
				Bytes string `json:"bytes"`
			}
			ok, err := DecodeEventPayload(tt.payload, &dst)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("DecodeEventPayload() ok, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeEventPayload() error = %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("DecodeEventPayload() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && dst.Bytes != tt.wantField {
				t.Errorf("bytes = %q, want %q", dst.Bytes, tt.wantField)
			}
		})
	}
}
