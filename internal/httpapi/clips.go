package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

const (
	defaultClipLimit = 30
	maxClipLimit     = 500
)

// clipView is the API shape of a clip.
type clipView struct {
	ID        string    `json:"id"`
	CameraID  string    `json:"cameraId"`
	StartedAt time.Time `json:"startedAt"`
	// DurationSeconds is the length of the video.
	DurationSeconds float64 `json:"durationSeconds"`
	SizeBytes       int64   `json:"sizeBytes"`
	// VideoURL plays the clip; add "?download=1" to save it as a file.
	VideoURL string `json:"videoUrl"`
}

func viewClip(c domain.Clip) clipView {
	return clipView{
		ID: string(c.ID), CameraID: string(c.CameraID), StartedAt: c.StartedAt.UTC(),
		DurationSeconds: round1(c.Duration.Seconds()), SizeBytes: c.SizeBytes,
		VideoURL: "/api/clips/" + string(c.ID) + "/video.mp4",
	}
}

// clipListView is the answer of GET /api/clips.
type clipListView struct {
	// Enabled is false when recording is switched off in the server's
	// config; older clips can still be there.
	Enabled bool       `json:"enabled"`
	Clips   []clipView `json:"clips"`
	// UsedBytes is the disk space of all clips, MaxBytes the limit at
	// which the oldest are deleted.
	UsedBytes int64 `json:"usedBytes"`
	MaxBytes  int64 `json:"maxBytes"`
	// KeepSeconds is how long a clip is kept; 0 means until the disk
	// limit removes it.
	KeepSeconds int64 `json:"keepSeconds"`
}

// handleListClips serves GET /api/clips?camera=&limit=, newest first.
func (d Deps) handleListClips(w http.ResponseWriter, r *http.Request) {
	var cam domain.CameraID
	limit := defaultClipLimit
	query := r.URL.Query()
	if v := query.Get("camera"); v != "" {
		if _, ok := d.findCamera(v); !ok {
			writeError(w, http.StatusNotFound, "unknown camera")
			return
		}
		cam = domain.CameraID(v)
	}
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxClipLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be 1-%d", maxClipLimit))
			return
		}
		limit = n
	}
	clips, used, err := d.Clips.List(r.Context(), cam, limit)
	if err != nil {
		d.Logger.Error("list clips", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read clips")
		return
	}
	s := d.Clips.Settings()
	res := clipListView{
		Enabled: s.Enabled, Clips: make([]clipView, 0, len(clips)),
		UsedBytes: used, MaxBytes: s.MaxBytes, KeepSeconds: int64(s.KeepFor.Seconds()),
	}
	for _, c := range clips {
		res.Clips = append(res.Clips, viewClip(c))
	}
	writeJSON(w, http.StatusOK, res)
}

// handleClipVideo serves GET /api/clips/{id}/video.mp4.
//
// http.ServeContent answers range requests ("give me bytes 1000-1999"),
// which a browser's video player needs to jump around in the clip.
func (d Deps) handleClipVideo(w http.ResponseWriter, r *http.Request) {
	clip, file, err := d.Clips.Open(r.Context(), domain.ClipID(r.PathValue("id")))
	if errors.Is(err, ports.ErrClipNotFound) {
		writeError(w, http.StatusNotFound, "clip not found")
		return
	}
	if err != nil {
		d.Logger.Error("open clip", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read clip")
		return
	}
	defer file.Close()

	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	// A clip never changes, so the browser may keep it; "private" because
	// it is behind the login.
	h.Set("Cache-Control", "private, max-age=86400, immutable")
	if r.URL.Query().Get("download") != "" {
		name := fmt.Sprintf("%s-%s.mp4", clip.CameraID, clip.StartedAt.UTC().Format("20060102-150405"))
		h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	http.ServeContent(w, r, "", clip.EndedAt(), file)
}

// handleDeleteClip serves DELETE /api/clips/{id}.
func (d Deps) handleDeleteClip(w http.ResponseWriter, r *http.Request) {
	id := domain.ClipID(r.PathValue("id"))
	err := d.Clips.Delete(r.Context(), id)
	if errors.Is(err, ports.ErrClipNotFound) {
		writeError(w, http.StatusNotFound, "clip not found")
		return
	}
	if err != nil {
		d.Logger.Error("delete clip", "clip", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not delete clip")
		return
	}
	by := "anonymous"
	if p, ok := principalFrom(r.Context()); ok {
		by = p.Subject
	}
	d.Logger.Info("clip deleted", "clip", id, "by", by)
	w.WriteHeader(http.StatusNoContent)
}
