package executor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// ForwardBedrock forwards to Amazon Bedrock.
//
// One executor serves both registry entries: "bedrock" speaks the Anthropic Messages
// wire and "bedrock-xai" speaks OpenAI Chat Completions, and the entry's Format decides
// which. The response then continues through the ordinary downstream translation path, so
// a /v1/messages client on the xAI entry gets the same treatment it would on any other
// OpenAI-wire provider.
func ForwardBedrock(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	resp, err := proxy.ForwardBedrock(ctx, req.Client, req.Config, BedrockProviderID(req.Config),
		req.APIKey, req.Body, req.IsStream, req.ConnData)
	if err != nil {
		return fmt.Errorf("ForwardBedrock: %w", err)
	}
	defer resp.Body.Close()

	forwardUpstreamResponseHeaders(w, resp.Header)

	if !req.IsStream {
		// /invoke already returns the complete Anthropic Messages (or Chat Completions)
		// body, so there is no framing to unwrap.
		if req.UpstreamClaude {
			return handleClaudeMessagesNonStream(w, req, resp.Body)
		}
		return jsonResponse(ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
	}

	// The unwrapped EventStream is ordinary Claude SSE for the Anthropic entry, so it
	// goes through the existing Claude handlers rather than a second translation path:
	// usage accounting, the /v1/responses bridge, terminal synthesis and the [DONE]
	// sentinel all live there already.
	if req.UpstreamClaude {
		return handleBedrockClaudeStream(w, req, resp.Body)
	}
	return handleBedrockOpenAIStream(w, req, resp.Body)
}

// bedrockStreamToSSE decodes AWS EventStream frames and re-emits them as ordinary SSE,
// so the Claude translation path downstream sees the stream it already knows how to
// read. A protocol failure becomes an in-band error event rather than a returned error,
// because HTTP 200 has already gone out by the time the first frame arrives.
func bedrockStreamToSSE(w io.Writer, flusher http.Flusher, req *Request, upstream io.Reader, namedEvents bool) {
	sawTerminal := false
	failed := false
	reader := providers.NewEventStreamReader(upstream)

	for {
		frame, err := reader.ReadFrame()
		// A client that disconnects mid-answer legitimately never reaches Bedrock's
		// terminal event, so the truncation check must not fire for it.
		clientGone := req.Ctx != nil && req.Ctx.Err() != nil
		if err != nil {
			if !clientGone {
				if errors.Is(err, providers.ErrEventStreamTruncated) {
					// Trailing bytes that never formed a frame: the answer is incomplete.
					writeBedrockSSEError(w, namedEvents, "api_error", "bedrock stream ended mid-frame")
				} else {
					writeBedrockSSEError(w, namedEvents, "api_error", err.Error())
				}
				failed = true
			}
			break
		}
		if frame == nil {
			break
		}

		messageType := frame.Headers[":message-type"]
		if messageType == "exception" || messageType == "error" {
			// Bedrock reports throttling and validation failures as in-band frames, not
			// HTTP status codes, so these must surface instead of looking like a clean
			// end of stream.
			writeBedrockSSEError(w, namedEvents, bedrockExceptionType(frame),
				bedrockExceptionMessage(frame, messageType))
			failed = true
			break
		}

		if frame.Headers[":event-type"] != providers.BedrockChunkEvent {
			// InvokeModelWithResponseStream defines no other event type today, so an
			// unknown one means AWS extended the protocol. Failing would break every
			// stream over what may be a harmless metadata event; skipping silently could
			// hide lost content, so it is logged.
			log.Warn("executor", "bedrock skipped unrecognised EventStream event type",
				"eventType", frame.Headers[":event-type"])
			continue
		}

		inner, err := decodeBedrockChunk(frame.Payload)
		if err != nil {
			if !clientGone {
				writeBedrockSSEError(w, namedEvents, "api_error", err.Error())
				failed = true
			}
			break
		}

		eventType := ""
		if namedEvents {
			// Anthropic's SSE names the event after the payload's own type.
			var event map[string]jsontext.Value
			if err := json.Unmarshal(inner, &event); err != nil {
				if !clientGone {
					writeBedrockSSEError(w, namedEvents, "api_error",
						"bedrock chunk decoded to a value that is not a JSON object")
					failed = true
				}
				break
			}
			if eventType = eventString(event["type"]); eventType == "" {
				if !clientGone {
					writeBedrockSSEError(w, namedEvents, "api_error",
						"bedrock chunk decoded to an event with no type")
					failed = true
				}
				break
			}
		}

		frame2 := append([]byte("data: "), inner...)
		frame2 = append(frame2, '\n', '\n')
		if namedEvents {
			frame2 = append([]byte("event: "+eventType+"\n"), frame2...)
		}
		if _, writeErr := w.Write(frame2); writeErr != nil {
			// The client went away mid-write. Not worth reporting: the answer is simply
			// no longer wanted.
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
		if namedEvents {
			sawTerminal = eventType == providers.BedrockTerminalEvent
		} else {
			// OpenAI chat.completion.chunk has no `type`: completion is a non-null
			// finish_reason on a choice rather than a terminal event.
			sawTerminal = bedrockChunkFinished(inner)
		}
	}

	if req.Ctx != nil && req.Ctx.Err() != nil {
		// The client hung up mid-answer. That is a normal disconnect, not a protocol
		// failure, and nothing is owed to it any more.
		return
	}

	if failed {
		return
	}
	if !sawTerminal {
		// An upstream that closes cleanly mid-answer looks like a complete response to
		// the client unless the terminal marker is tracked. BuildStreamErrorBytes
		// closes an OpenAI-wire stream with the [DONE] sentinel it expects.
		writeBedrockSSEError(w, namedEvents, "api_error", bedrockTruncationMessage(namedEvents))
		return
	}
	if !namedEvents {
		// OpenAI clients expect the sentinel that closes a Chat Completions stream;
		// Bedrock's framing has no equivalent, so it is synthesised here the way a real
		// upstream sends it.
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func bedrockTruncationMessage(namedEvents bool) string {
	if namedEvents {
		return "bedrock stream ended before " + providers.BedrockTerminalEvent
	}
	return "bedrock stream ended before any choice reported a finish_reason"
}

// bedrockChunkFinished reports whether an OpenAI-wire chunk carries a non-null
// finish_reason, the marker that ends a Chat Completions stream.
func bedrockChunkFinished(inner []byte) bool {
	var chunk struct {
		Choices []struct {
			FinishReason jsontext.Value `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(inner, &chunk); err != nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil && choice.FinishReason.String() != "null" {
			return true
		}
	}
	return false
}

// handleBedrockClaudeStream unwraps the EventStream and hands ordinary Claude SSE to
// the existing Claude stream handler, which owns usage accounting and the downstream
// translation to whatever the client speaks.
func handleBedrockClaudeStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		bedrockStreamToSSE(pw, nil, req, upstream, true)
	}()
	return handleClaudeMessagesStream(w, req, pr)
}

// handleBedrockOpenAIStream unwraps the EventStream and pipes the resulting Chat
// Completions SSE through the standard OpenAI stream path.
func handleBedrockOpenAIStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		bedrockStreamToSSE(pw, nil, req, upstream, false)
	}()
	stallReader := proxy.NewStallReaderWithContext(req.Ctx, pr, 0, "bedrock")
	return execSSEStream(w, stallReader, req)
}

// BedrockProviderID picks the registry entry the request is routed to. The chat handler
// resolves the provider before reaching here, and Bedrock has exactly two entries, so the
// config's own base URL is the reliable discriminator: the xAI entry declares no Format.
func BedrockProviderID(cfg *providers.ProviderConfig) string {
	if cfg != nil && cfg.Format == "claude" {
		return "bedrock"
	}
	return "bedrock-xai"
}

// handleBedrockStream unwraps AWS EventStream framing into SSE in the entry's wire shape:
// named Claude events, or bare OpenAI `data:` chunks terminated by [DONE].
//
// Each `chunk` frame carries {"bytes": "<base64>"} whose contents are one Anthropic
// streaming event, so the transform is decode frame → base64-decode → re-emit. Errors are
// reported in-band rather than as a returned error, because HTTP 200 has already been
// written by the time the first frame arrives and the status can no longer change.
// bedrockExceptionType names the in-band failure Bedrock reported.
func bedrockExceptionType(frame *providers.EventStreamFrame) string {
	if t := frame.Headers[":exception-type"]; t != "" {
		return t
	}
	if t := frame.Headers[":error-code"]; t != "" {
		return t
	}
	return "api_error"
}

// bedrockExceptionMessage pulls the human-readable reason out of an exception frame,
// which AWS puts in the :error-message header or in the payload.
func bedrockExceptionMessage(frame *providers.EventStreamFrame, messageType string) string {
	if m := frame.Headers[":error-message"]; m != "" {
		return m
	}
	var payload struct {
		Message string `json:"message"`
		Capital string `json:"Message"`
	}
	if _, err := providers.DecodeEventPayload(frame.Payload, &payload); err == nil {
		if payload.Message != "" {
			return payload.Message
		}
		if payload.Capital != "" {
			return payload.Capital
		}
	}
	return "bedrock returned an EventStream " + messageType
}

// writeBedrockSSEError emits one terminal error frame in the entry's wire shape. The
// HTTP status is already committed, so the failure is only reportable in-band.
func writeBedrockSSEError(w io.Writer, namedEvents bool, errType, message string) {
	format := proxy.SSEFormatOpenAI
	if namedEvents {
		format = proxy.SSEFormatClaude
	}
	frame := proxy.BuildStreamErrorBytes(http.StatusBadGateway, errType+": "+message, format)
	if _, err := w.Write(frame); err != nil {
		log.Warn("executor", "bedrock could not write the stream error frame", "error", err)
	}
}

// decodeBedrockChunk base64-decodes a chunk frame's `bytes` field.
func decodeBedrockChunk(payload []byte) ([]byte, error) {
	var wrapper struct {
		Bytes string `json:"bytes"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil || wrapper.Bytes == "" {
		// A CRC-valid chunk with no payload means the protocol changed under us. Dropping
		// it would silently lose content, so it is a failure.
		return nil, errors.New("bedrock chunk frame carried no payload bytes")
	}
	decoded, err := base64.StdEncoding.DecodeString(wrapper.Bytes)
	if err != nil {
		return nil, fmt.Errorf("bedrock chunk was not valid base64: %w", err)
	}
	return decoded, nil
}

// eventString reads a JSON string field, returning "" for anything else — including an
// absent field, which jsontext reports as a nil Value.
func eventString(raw jsontext.Value) string {
	if raw == nil {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}
