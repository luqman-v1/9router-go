### fix(images): derive binary Content-Type from image bytes

- In media download and image handlers, derive binary `Content-Type` from image bytes via `http.DetectContentType` when upstream header is empty or generic `application/octet-stream` (porting upstream `decolua/9router` PR #5372).
- Ensures correct MIME type handling (PNG, JPEG, WebP) for downstream clients.
- Added unit tests in `internal/handlers/media/image_content_type_test.go`.
