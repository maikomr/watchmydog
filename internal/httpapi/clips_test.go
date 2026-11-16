package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/app/clips"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// memRecorder keeps clip "videos" in memory.
type memRecorder struct{ files map[domain.ClipID][]byte }

func (m *memRecorder) Record(ctx context.Context, _ domain.Camera) error { <-ctx.Done(); return nil }
func (m *memRecorder) Cut(context.Context, domain.ClipID, domain.CameraID, time.Time, time.Time) (domain.Clip, error) {
	return domain.Clip{}, ports.ErrNothingRecorded
}
func (m *memRecorder) Open(id domain.ClipID) (ports.ClipFile, error) {
	data, ok := m.files[id]
	if !ok {
		return nil, ports.ErrClipNotFound
	}
	return readSeekNopCloser{bytes.NewReader(data)}, nil
}
func (m *memRecorder) Delete(id domain.ClipID) error { delete(m.files, id); return nil }
func (m *memRecorder) Stored() ([]domain.ClipID, error) {
	out := []domain.ClipID{}
	for id := range m.files {
		out = append(out, id)
	}
	return out, nil
}

type readSeekNopCloser struct{ *bytes.Reader }

func (readSeekNopCloser) Close() error { return nil }

const (
	clipOld = domain.ClipID("00000000000000000000000000000001")
	clipNew = domain.ClipID("00000000000000000000000000000002")
)

var clipRules = clips.Settings{Enabled: true, MaxBytes: 10 << 30, KeepFor: 30 * 24 * time.Hour}

// newClips returns a clips service holding two clips of the dog camera.
func newClips(t *testing.T, s clips.Settings) (*clips.Service, *memRecorder) {
	t.Helper()
	store := memory.New()
	rec := &memRecorder{files: map[domain.ClipID][]byte{}}
	at := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	for i, id := range []domain.ClipID{clipOld, clipNew} {
		video := []byte("0123456789 video " + string(id))
		rec.files[id] = video
		c := domain.Clip{
			ID: id, CameraID: "dog", StartedAt: at.Add(time.Duration(i) * time.Hour),
			Duration: 42340 * time.Millisecond, SizeBytes: int64(len(video)),
		}
		if err := store.SaveClip(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	return clips.New(rec, store, s, slog.New(slog.NewTextHandler(io.Discard, nil))), rec
}

func newClipsHandler(t *testing.T, s clips.Settings) (http.Handler, *memRecorder) {
	t.Helper()
	svc, rec := newClips(t, s)
	return NewHandler(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cameras: []domain.Camera{dog()},
		Clips:   svc,
	}), rec
}

func TestListClips(t *testing.T) {
	h, _ := newClipsHandler(t, clipRules)
	rr := do(h, http.MethodGet, "/api/clips?camera=dog")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var res clipListView
	decode(t, rr, &res)
	if !res.Enabled || res.MaxBytes != 10<<30 || res.KeepSeconds != 30*24*3600 || res.UsedBytes != 2*int64(len("0123456789 video ")+32) {
		t.Errorf("list = %+v", res)
	}
	if len(res.Clips) != 2 || res.Clips[0].ID != string(clipNew) {
		t.Fatalf("clips = %+v, want the newest first", res.Clips)
	}
	c := res.Clips[0]
	want := clipView{
		ID: string(clipNew), CameraID: "dog", StartedAt: time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC),
		DurationSeconds: 42.3, SizeBytes: c.SizeBytes, VideoURL: "/api/clips/" + string(clipNew) + "/video.mp4",
	}
	if c != want {
		t.Errorf("clip = %+v, want %+v", c, want)
	}

	decode(t, do(h, http.MethodGet, "/api/clips?limit=1"), &res)
	if len(res.Clips) != 1 {
		t.Errorf("limit=1 returned %d clips", len(res.Clips))
	}
	for path, status := range map[string]int{
		"/api/clips?camera=cat": http.StatusNotFound,
		"/api/clips?limit=0":    http.StatusBadRequest,
		"/api/clips?limit=abc":  http.StatusBadRequest,
		"/api/clips?limit=9999": http.StatusBadRequest,
	} {
		if rr := do(h, http.MethodGet, path); rr.Code != status {
			t.Errorf("%s: status %d, want %d", path, rr.Code, status)
		}
	}
}

func TestListClipsSaysWhenRecordingIsOff(t *testing.T) {
	s := clipRules
	s.Enabled = false
	h, _ := newClipsHandler(t, s)
	var res clipListView
	decode(t, do(h, http.MethodGet, "/api/clips"), &res)
	if res.Enabled || len(res.Clips) != 2 {
		t.Errorf("list = %+v, want enabled=false and the old clips still listed", res)
	}
}

func TestClipVideoSupportsRangesAndDownload(t *testing.T) {
	h, rec := newClipsHandler(t, clipRules)
	path := "/api/clips/" + string(clipOld) + "/video.mp4"

	rr := do(h, http.MethodGet, path)
	if rr.Code != http.StatusOK || rr.Body.String() != string(rec.files[clipOld]) {
		t.Fatalf("status %d, body %q", rr.Code, rr.Body)
	}
	hd := rr.Header()
	if hd.Get("Content-Type") != "video/mp4" || hd.Get("Accept-Ranges") != "bytes" || hd.Get("Content-Disposition") != "" {
		t.Errorf("headers = %v", hd)
	}
	if hd.Get("Cache-Control") != "private, max-age=86400, immutable" {
		t.Errorf("Cache-Control = %q", hd.Get("Cache-Control"))
	}

	// A player asking for a part of the file.
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=2-5")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "2345" {
		t.Errorf("range: status %d, body %q", rr.Code, rr.Body)
	}

	rr = do(h, http.MethodGet, path+"?download=1")
	if got := rr.Header().Get("Content-Disposition"); got != `attachment; filename="dog-20261116-143000.mp4"` {
		t.Errorf("Content-Disposition = %q", got)
	}

	for _, bad := range []string{"/api/clips/ffffffffffffffffffffffffffffffff/video.mp4", "/api/clips/nope/video.mp4"} {
		if rr := do(h, http.MethodGet, bad); rr.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", bad, rr.Code)
		}
	}
}

func TestDeleteClip(t *testing.T) {
	h, rec := newClipsHandler(t, clipRules)
	path := "/api/clips/" + string(clipOld)
	if rr := do(h, http.MethodDelete, path); rr.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if _, ok := rec.files[clipOld]; ok {
		t.Error("the video is still there")
	}
	var res clipListView
	decode(t, do(h, http.MethodGet, "/api/clips"), &res)
	if len(res.Clips) != 1 || res.Clips[0].ID != string(clipNew) {
		t.Errorf("clips = %+v", res.Clips)
	}
	if rr := do(h, http.MethodDelete, path); rr.Code != http.StatusNotFound {
		t.Errorf("second delete: status %d, want 404", rr.Code)
	}
	if rr := do(h, http.MethodGet, path+"/video.mp4"); rr.Code != http.StatusNotFound {
		t.Errorf("video after delete: status %d", rr.Code)
	}
}

func TestClipRoutesAreAbsentWithoutTheService(t *testing.T) {
	h := newTest(t, newEngine(), "")
	if rr := do(h, http.MethodGet, "/api/clips"); rr.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rr.Code)
	}
}
