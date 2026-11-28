### Fixed

- **Atomic logo upload staging, stream cap, and SVG CSP sandbox**: `HandleUploadLogo` now bounds multipart wire reads with `http.MaxBytesReader`, stages uploads in a temp file before saving settings and swapping into place so errors preserve existing assets, and `HandleServeUploads` attaches a strict `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox` header to served SVGs.
