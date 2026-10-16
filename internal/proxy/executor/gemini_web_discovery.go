package executor

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	json "encoding/json/v2"
)

// Model discovery. The site's registry is the only authority on which models a
// session may use, because the served set changes with the credential: an
// anonymous session gets one model, a signed-in one several.
//
// batchPayload runs one batchexecute RPC and returns its payload.
//
// The response is the same line protocol StreamGenerate uses, so only the one
// decoding path exists: read every line, and return the payload of the first
// wrb.fr envelope — the RPC id is asserted by the caller through the payload
// shape it then has to parse.
func (s *geminiWebSession) batchPayload(ctx context.Context, rpcID, payload string) (any, error) {
	tokens, err := s.tokens(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal([]any{[]any{[]any{rpcID, payload, nil, "generic"}}})
	if err != nil {
		return nil, fmt.Errorf("gemini-web: encode %s request: %w", rpcID, err)
	}
	query := url.Values{}
	query.Set("rpcids", rpcID)
	query.Set("source-path", "/app")
	query.Set("hl", "en")
	query.Set("rt", "c")
	query.Set("bl", tokens.bl)
	query.Set("f.sid", tokens.fSid)

	// The registry RPC is the call that decides which models a session may use,
	// so it carries the signed-in caller's signature when the credential can
	// produce one. Without it a configured cookie can still be answered as an
	// anonymous visitor, which is the difference between "Pro and Argon are
	// offered" and "Flash-Lite only".
	headers := map[string]string{}
	if auth := geminiWebSAPISIDHash(s.cookie, s.endpoints.origin); auth != "" {
		headers["authorization"] = auth
	}

	requestCtx, cancel := context.WithTimeout(ctx, geminiWebRPCTimeout)
	defer cancel()
	response, err := s.do(requestCtx, http.MethodPost,
		s.endpoints.batch+"?"+query.Encode(),
		strings.NewReader(geminiWebFormBody(tokens.at, string(body))),
		"application/x-www-form-urlencoded;charset=UTF-8", headers)
	if err != nil {
		return nil, fmt.Errorf("gemini-web: call %s: %w", rpcID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &geminiWebUpstreamStatus{Status: response.StatusCode, Operation: rpcID}
	}
	decoded, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("gemini-web: read %s response: %w", rpcID, err)
	}
	for line := range strings.Lines(string(decoded)) {
		parsed, err := parseGeminiWebLine(line)
		if err != nil {
			continue
		}
		if parsed == nil {
			continue
		}
		if parsed.RPCError != nil {
			return nil, parsed.RPCError
		}
	}
	payloadValue, found := geminiWebDecodeBatchPayload(string(decoded))
	if !found {
		return nil, fmt.Errorf("gemini-web: %s response carried no payload", rpcID)
	}
	return payloadValue, nil
}

// geminiWebDecodeBatchPayload pulls the JSON value out of the first wrb.fr line.
func geminiWebDecodeBatchPayload(body string) (any, bool) {
	for line := range strings.Lines(body) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		var outer []jsontext.Value
		if err := json.Unmarshal([]byte(trimmed), &outer); err != nil {
			continue
		}
		for _, entry := range outer {
			var part []jsontext.Value
			if err := json.Unmarshal(entry, &part); err != nil || len(part) < 3 {
				continue
			}
			var tag string
			if err := json.Unmarshal(part[0], &tag); err != nil || tag != "wrb.fr" {
				continue
			}
			var payload string
			if err := json.Unmarshal(part[2], &payload); err != nil || payload == "" {
				continue
			}
			var value any
			if err := json.Unmarshal([]byte(payload), &value); err != nil {
				continue
			}
			return value, true
		}
	}
	return nil, false
}

// availableModels asks the site which models this session can answer with.
//
// The registry is fetched from the live session rather than read from the
// catalog, because the catalog is static and the served set is not: the same
// request is answered by a different set once a cookie is configured.
//
// A model list is the only proof of a usable session, so its absence is what a
// refusal looks like. Nothing in the payload is read as an error code: the
// registry carries the constant 1016 that the site's own failure envelope also
// uses, and treating that value as a signal reported every working anonymous
// session as a refused one — found by running a real turn, not by a unit test.
func (s *geminiWebSession) availableModels(ctx context.Context) ([]geminiWebModel, error) {
	payload, err := s.batchPayload(ctx, "otAQ7b", "[]")
	if err != nil {
		return nil, err
	}
	models, _, _, ok := geminiWebParseModelRegistry(payload)
	if !ok || len(models) == 0 {
		// The cached page tokens may simply be stale, so they are re-read once
		// before the session is called unusable.
		s.invalidateTokens()
		payload, err = s.batchPayload(ctx, "otAQ7b", "[]")
		if err != nil {
			return nil, err
		}
		if models, _, _, ok = geminiWebParseModelRegistry(payload); !ok || len(models) == 0 {
			if s.cookie != "" {
				return nil, errGeminiWebSessionRefused
			}
			return nil, errors.New("gemini-web: gemini.google.com returned no model registry")
		}
	}
	return models, nil
}
