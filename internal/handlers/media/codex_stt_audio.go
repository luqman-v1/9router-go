package media

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Audio conversion bounds for Codex transcription, ported from upstream
// open-sse/handlers/codexSttAudio.js: the upload is mono 24 kHz PCM16 WAV,
// and both the input size and the decoded duration are capped so a long
// recording cannot pin the 1-vCPU box running ffmpeg.
const (
	// codexMaxAudioBytes caps the upload the caller sends (25 MiB, the size
	// ChatGPT's own transcription endpoint documents).
	codexMaxAudioBytes = 25 * 1024 * 1024
	// codexMaxAudioSeconds caps the decoded audio (120 s).
	codexMaxAudioSeconds = 120
	// codexMaxPCMBytes is what codexMaxAudioSeconds of mono 24 kHz 16-bit
	// audio occupies.
	codexMaxPCMBytes = codexMaxAudioSeconds * 24000 * 2
	// codexSampleRate is the rate of both the decoded PCM and the WAV header.
	codexSampleRate = 24000
)

// codexSTTError carries the HTTP status a caller must answer with.
type codexSTTError struct {
	status  int
	message string
}

func (e *codexSTTError) Error() string { return e.message }

func codexSTTFailure(status int, message string) *codexSTTError {
	return &codexSTTError{status: status, message: message}
}

// buildWav wraps raw mono 24 kHz PCM16 samples in the 44-byte canonical WAV
// header the transcription endpoint expects.
func buildWav(pcm []byte) []byte {
	wav := make([]byte, 44+len(pcm))
	copy(wav[0:], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], codexSampleRate)
	binary.LittleEndian.PutUint32(wav[28:], codexSampleRate*2)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(pcm)))
	copy(wav[44:], pcm)
	return wav
}

// convertAudio decodes the uploaded file to mono 24 kHz PCM16 with ffmpeg and
// wraps it in a WAV container. The input is staged in a private temporary
// directory that is removed on both success and failure, the decoder runs
// single-threaded against a whitelisted set of demuxers, and a recording
// longer than codexMaxAudioSeconds is killed and refused rather than
// truncated.
func convertAudio(fileBytes []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "9router-codex-stt-")
	if err != nil {
		return nil, codexSTTFailure(502, "Audio conversion failed")
	}
	defer func() { _ = os.RemoveAll(dir) }()

	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, fileBytes, 0o600); err != nil {
		return nil, codexSTTFailure(502, "Audio conversion failed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "1",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", "wav,mp3,mov,matroska,webm,ogg,flac,aac",
		"-i", input, "-map", "0:a:0", "-vn", "-t", fmt.Sprintf("%.2f", float64(codexMaxAudioSeconds)+0.01),
		"-threads", "1", "-ac", "1", "-ar", fmt.Sprint(codexSampleRate), "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1",
	)
	cmd.Stderr = nil
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, codexSTTFailure(503, "ffmpeg is required for transcription")
	}
	if ctx.Err() != nil {
		return nil, codexSTTFailure(504, "Audio conversion timed out")
	}
	if err != nil {
		// exec.LookPath already failed earlier for a missing ffmpeg; anything
		// here is ffmpeg refusing the input itself.
		return nil, codexSTTFailure(400, "Invalid or unsupported audio")
	}
	if len(out) == 0 {
		return nil, codexSTTFailure(400, "Invalid or unsupported audio")
	}
	if len(out) > codexMaxPCMBytes {
		return nil, codexSTTFailure(400, fmt.Sprintf("Audio duration exceeds %d seconds", codexMaxAudioSeconds))
	}
	return buildWav(out), nil
}
