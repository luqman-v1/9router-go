package media

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// HandleImageEdits handles POST /v1/images/edits (multipart/form-data).
// It parses the form, validates required fields, converts image/mask files
// to base64 Data URIs, packages the request as JSON, and forwards it via
// the existing media forwarding pipeline.
func (h *MediaHandler) HandleImageEdits(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 32 << 20 // 32MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid multipart form: %v", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: prompt")
		return
	}

	imageFiles := r.MultipartForm.File["image"]
	if len(imageFiles) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: image (upload at least one image file)")
		return
	}

	primaryImage, err := fileToDataURI(imageFiles[0])
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to read image: %v", err))
		return
	}

	var images []string
	for i := 1; i < len(imageFiles); i++ {
		uri, err := fileToDataURI(imageFiles[i])
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to read image[%d]: %v", i, err))
			return
		}
		images = append(images, uri)
	}

	var mask string
	maskFiles := r.MultipartForm.File["mask"]
	if len(maskFiles) > 0 {
		maskURI, err := fileToDataURI(maskFiles[0])
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to read mask: %v", err))
			return
		}
		mask = maskURI
	}

	model := strings.TrimSpace(r.FormValue("model"))
	size := strings.TrimSpace(r.FormValue("size"))
	responseFormat := strings.TrimSpace(r.FormValue("response_format"))
	background := strings.TrimSpace(r.FormValue("background"))
	quality := strings.TrimSpace(r.FormValue("quality"))

	n := 0
	if nStr := strings.TrimSpace(r.FormValue("n")); nStr != "" {
		_, _ = fmt.Sscanf(nStr, "%d", &n)
	}

	reqBody := map[string]any{
		"prompt":          prompt,
		"image":           primaryImage,
		"model":           model,
		"size":            size,
		"response_format": responseFormat,
		"background":      background,
		"quality":         quality,
	}
	if len(images) > 0 {
		reqBody["images"] = images
	}
	if mask != "" {
		reqBody["mask"] = mask
	}
	if n > 0 {
		reqBody["n"] = n
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("marshal request: %v", err))
		return
	}

	h.forwardMediaRequest(w, r, jsonBody, "dall-e-2", "/v1/images/edits")
}

func fileToDataURI(fh *multipart.FileHeader) (string, error) {
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}

	mimeType := fh.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		exts, _ := mime.ExtensionsByType(mimeType)
		if len(exts) > 0 && strings.HasPrefix(exts[0], ".image") {
		} else {
			mimeType = "image/png"
		}
	}

	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
