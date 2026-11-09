package dashboard

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"9router/proxy/internal/config"
	"9router/proxy/internal/handlerutil"
)

// The white-label logo is the one dashboard asset an operator supplies as a
// file rather than as a URL. It lands under DATA_DIR/uploads and is served
// back from /uploads, so a gateway with no public host still gets a branded
// sidebar: the <img> points at the gateway itself.
const (
	// logoUploadField is the multipart field name the dashboard posts.
	logoUploadField = "file"
	// logoMaxBytes caps the stored asset. A logo is a small image; anything
	// near this size is not a logo, and an unbounded write into the operator's
	// data dir is what the cap exists to prevent.
	logoMaxBytes = 2 << 20
	// logoUploadMemory is the in-memory portion ParseMultipartForm buffers
	// before spilling to disk. The logo itself is far smaller; this only
	// bounds the form scaffolding.
	logoUploadMemory = 1 << 20
	// uploadsDirName is the subdirectory of DATA_DIR holding operator-supplied
	// dashboard assets.
	uploadsDirName = "uploads"
)

// logoExtensions maps the accepted file extensions to the media type served
// for them. The set is deliberately small: these are the image formats a
// browser renders in an <img>, and an SVG is included because it is the format
// a logo is usually authored in.
var logoExtensions = map[string]string{
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
}

// logoDir returns the directory holding the uploaded logo, creating it on
// first use. It lives beside db/ and run/ under the same resolved data dir, so
// a deployment that moves DATA_DIR moves its logo with it.
func logoDir() (string, error) {
	dir := filepath.Join(config.ResolveDataDir(), uploadsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// logoFileName is the single stored name for the logo, derived from the
// extension the upload actually carries. A fixed name means replacing the logo
// overwrites the previous asset instead of leaving a stale file behind, and it
// means appLogoUrl never accumulates a new value per upload.
func logoFileName(ext string) string {
	return "logo" + ext
}

// HandleUploadLogo handles POST /api/settings/logo.
//
// The body is multipart/form-data with a single "file" field. On success the
// stored appLogoUrl is rewritten to the path this gateway serves the file from
// (/uploads/logo.<ext>), so the sidebar picks the new logo up on its next
// settings read without the operator copying a URL anywhere.
//
// The extension is taken from the uploaded filename, but the stored name is
// not: the client's filename is never used as a path component, so a filename
// carrying "../" cannot escape the uploads directory.
func (h *DashboardHandler) HandleUploadLogo(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(logoUploadMemory); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile(logoUploadField)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing '"+logoUploadField+"' field in multipart form")
		return
	}
	defer func() { _ = file.Close() }()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	mediaType, ok := logoExtensions[ext]
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "logo must be a PNG, SVG, WebP or JPEG image")
		return
	}
	if header.Size > logoMaxBytes {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "logo must be 2 MB or smaller")
		return
	}

	dir, err := logoDir()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to prepare the upload directory: "+err.Error())
		return
	}
	// A previous upload may have left a different extension behind, so the old
	// asset is removed before the new one is written: the slot holds one logo,
	// and a leftover logo.svg next to a new logo.png would be served by a URL
	// the operator no longer chose.
	for staleExt := range logoExtensions {
		if staleExt == ext {
			continue
		}
		_ = os.Remove(filepath.Join(dir, logoFileName(staleExt)))
	}

	// The size cap is enforced on the bytes actually read rather than on the
	// client's declared Content-Length: a request that understates its size
	// must not be able to write past the cap.
	limited := io.LimitReader(file, logoMaxBytes+1)
	dst, err := os.Create(filepath.Join(dir, logoFileName(ext)))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to create the logo file: "+err.Error())
		return
	}
	written, copyErr := func() (int64, error) {
		defer func() { _ = dst.Close() }()
		return io.Copy(dst, limited)
	}()
	if copyErr != nil {
		_ = os.Remove(filepath.Join(dir, logoFileName(ext)))
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to write the logo file: "+copyErr.Error())
		return
	}
	if written > logoMaxBytes {
		_ = os.Remove(filepath.Join(dir, logoFileName(ext)))
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "logo must be 2 MB or smaller")
		return
	}

	logoURL := "/" + uploadsDirName + "/" + logoFileName(ext)
	updates := map[string]any{appLogoUrlSettingKey: logoURL}
	if err := h.Repo.UpdateSettingsRaw(updates); err != nil {
		_ = os.Remove(filepath.Join(dir, logoFileName(ext)))
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save the logo setting: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"appLogoUrl": logoURL,
		"mediaType":  mediaType,
		"bytes":      written,
	})
}

// logoClearedByUpdate reports whether this settings write clears appLogoUrl,
// and if so which stored path is being replaced.
//
// The previous value is read from the stored row rather than from the payload:
// the payload carries the new value only, and the file to remove is the one the
// old value pointed at. A write that does not touch appLogoUrl — or that sets it
// to a non-empty value, as the upload handler does — clears nothing.
func logoClearedByUpdate(h *DashboardHandler, updates map[string]any) (string, bool) {
	value, ok := updates[appLogoUrlSettingKey]
	if !ok {
		return "", false
	}
	logo, _ := value.(string)
	if strings.TrimSpace(logo) != "" {
		return "", false
	}
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		return "", false
	}
	previous, _ := raw[appLogoUrlSettingKey].(string)
	if !isUploadedLogoPath(previous) {
		return "", false
	}
	return previous, true
}

// isUploadedLogoPath reports whether a stored appLogoUrl names an asset this
// gateway uploaded, as opposed to a remote URL or a path the operator serves
// themselves. Only an uploaded asset is removed: an operator who pointed the
// setting at their own file keeps that file when they clear the field.
func isUploadedLogoPath(value string) bool {
	return strings.HasPrefix(value, "/"+uploadsDirName+"/")
}

// removeUploadedLogo deletes the stored logo file for an uploaded path.
//
// The name is rebuilt from the extension rather than taken from the stored
// value, so a path that survived a hand edit cannot address a file outside the
// uploads directory. A missing file is not an error: the setting and the file
// are allowed to disagree, and the write has already succeeded.
func removeUploadedLogo(logoURL string) {
	if !isUploadedLogoPath(logoURL) {
		return
	}
	ext := strings.ToLower(filepath.Ext(logoURL))
	if _, ok := logoExtensions[ext]; !ok {
		return
	}
	dir, err := logoDir()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(dir, logoFileName(ext)))
}

// HandleServeUploads handles GET /uploads/*.
//
// It serves operator-supplied dashboard assets from DATA_DIR/uploads. The
// route is mounted behind the same dashboard gate as /dashboard/*, so an
// install that requires a login does not expose its branding assets to an
// anonymous caller.
//
// Only the fixed logo name is served. A request for any other path is answered
// 404 rather than being resolved against the directory: the upload slot holds
// one asset, and a wildcard serve would turn this into a general file reader
// for whatever else an operator happens to keep under DATA_DIR.
func (h *DashboardHandler) HandleServeUploads(w http.ResponseWriter, r *http.Request) {
	ext := strings.ToLower(filepath.Ext(r.URL.Path))
	mediaType, ok := logoExtensions[ext]
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "not found")
		return
	}
	name := logoFileName(ext)
	if filepath.Base(r.URL.Path) != name {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "not found")
		return
	}

	dir, err := logoDir()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "not found")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "not found")
		return
	}

	w.Header().Set("Content-Type", mediaType)
	// The logo is replaced in place under a name that never changes, so a
	// cached copy would outlive the asset it was fetched for. A short
	// must-revalidate keeps the sidebar's logo honest without forcing a
	// re-fetch on every navigation.
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, stat.ModTime(), f)
}
