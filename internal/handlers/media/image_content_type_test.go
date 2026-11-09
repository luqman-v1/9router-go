package media

import (
	"bytes"
	"mime/multipart"
	"net/textproto"
	"testing"
)

func TestFileToDataURI_DetectsContentType(t *testing.T) {
	tests := []struct {
		name         string
		contentType  string
		data         []byte
		expectedMime string
	}{
		{
			name:         "PNG with missing Content-Type",
			contentType:  "",
			data:         []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"),
			expectedMime: "image/png",
		},
		{
			name:         "PNG with application/octet-stream",
			contentType:  "application/octet-stream",
			data:         []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"),
			expectedMime: "image/png",
		},
		{
			name:         "JPEG with missing Content-Type",
			contentType:  "",
			data:         []byte("\xff\xd8\xff\xe0\x00\x10JFIF"),
			expectedMime: "image/jpeg",
		},
		{
			name:         "JPEG with application/octet-stream",
			contentType:  "application/octet-stream",
			data:         []byte("\xff\xd8\xff\xe0\x00\x10JFIF"),
			expectedMime: "image/jpeg",
		},
		{
			name:         "WebP with missing Content-Type",
			contentType:  "",
			data:         []byte("RIFF\x2a\x00\x00\x00WEBPVP8 "),
			expectedMime: "image/webp",
		},
		{
			name:         "WebP with application/octet-stream",
			contentType:  "application/octet-stream",
			data:         []byte("RIFF\x2a\x00\x00\x00WEBPVP8 "),
			expectedMime: "image/webp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			h := textproto.MIMEHeader{}
			if tc.contentType != "" {
				h.Set("Content-Type", tc.contentType)
			}
			h.Set("Content-Disposition", `form-data; name="image"; filename="test.img"`)
			part, err := mw.CreatePart(h)
			if err != nil {
				t.Fatalf("failed to create part: %v", err)
			}
			_, _ = part.Write(tc.data)
			_ = mw.Close()

			reader := multipart.NewReader(&buf, mw.Boundary())
			form, err := reader.ReadForm(32 << 20)
			if err != nil {
				t.Fatalf("failed to read form: %v", err)
			}
			defer form.RemoveAll()

			files := form.File["image"]
			if len(files) == 0 {
				t.Fatalf("expected image file in form")
			}

			uri, err := fileToDataURI(files[0])
			if err != nil {
				t.Fatalf("fileToDataURI failed: %v", err)
			}

			expectedPrefix := "data:" + tc.expectedMime + ";base64,"
			if !bytes.HasPrefix([]byte(uri), []byte(expectedPrefix)) {
				t.Errorf("expected data URI to start with %q, got %q", expectedPrefix, uri[:min(50, len(uri))])
			}
		})
	}
}
