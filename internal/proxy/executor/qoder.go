package executor

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
)

const qoderRSAPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

func parseQoderPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(qoderRSAPublicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not RSA public key")
	}
	return rsaPub, nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - (len(data) % blockSize)
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

func aesEncryptCBCBase64(plaintext []byte, key []byte) (string, error) {
	if len(key) != 16 {
		return "", fmt.Errorf("aes key must be 16 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, key)
	mode.CryptBlocks(ciphertext, padded)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func rsaEncryptBase64(data []byte) (string, error) {
	pub, err := parseQoderPublicKey()
	if err != nil {
		return "", err
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub, data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func md5Hex(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}

func buildQoderCosyHeaders(body []byte, requestURL string, userID string, token string) (map[string]string, error) {
	if userID == "" {
		userID = "user-" + uuid.New().String()[:8]
	}
	if token == "" {
		token = "dt-" + uuid.New().String()
	}

	aesKeyStr := uuid.New().String()[:16]
	aesKey := []byte(aesKeyStr)

	userInfoJSON, err := json.Marshal(map[string]string{
		"uid":                  userID,
		"security_oauth_token": token,
		"name":                 "",
		"aid":                  "",
		"email":                "",
	})
	if err != nil {
		return nil, err
	}

	infoB64, err := aesEncryptCBCBase64(userInfoJSON, aesKey)
	if err != nil {
		return nil, err
	}

	cosyKeyB64, err := rsaEncryptBase64(aesKey)
	if err != nil {
		return nil, err
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	reqID := uuid.New().String()

	payloadJSON, _ := json.Marshal(map[string]string{
		"version":     "v1",
		"requestId":   reqID,
		"info":        infoB64,
		"cosyVersion": "1.0.0",
		"ideVersion":  "",
	})
	payloadB64 := base64.StdEncoding.EncodeToString(payloadJSON)

	sigPath := "/api/v2/service/pro/sse/agent_chat_generation"
	sigInput := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", payloadB64, cosyKeyB64, timestamp, string(body), sigPath)
	sig := md5Hex([]byte(sigInput))

	machineID := uuid.New().String()
	bodyHash := md5Hex(body)
	bodyLength := fmt.Sprintf("%d", len(body))

	headers := map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + sig,
		"Cosy-Key":               cosyKeyB64,
		"Cosy-User":              userID,
		"Cosy-Date":              timestamp,
		"Cosy-Version":           "1.0.0",
		"Cosy-Machineid":         machineID,
		"Cosy-Machinetoken":      machineID,
		"Cosy-Machinetype":       "5",
		"Cosy-Machineos":         "x86_64_windows",
		"Cosy-Clienttype":        "5",
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          bodyHash,
		"Cosy-Bodylength":        bodyLength,
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       "disagree",
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          "v2",
		"X-Request-Id":           uuid.New().String(),
	}

	return headers, nil
}

// ForwardQoder handles requests for Qoder using COSY signing.
func ForwardQoder(w http.ResponseWriter, req *Request) error {
	headers, err := buildQoderCosyHeaders(req.Body, "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation", "", req.APIKey)
	if err != nil {
		return fmt.Errorf("build Qoder COSY headers: %w", err)
	}

	targetURL := "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common"
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.DoRequest(ctx, req.Client, "POST", targetURL, headers, req.Body)
	if err != nil {
		return fmt.Errorf("ForwardQoder: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}
