package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/maikomr/watchmydog/internal/app/talk"
)

const (
	// maxTalkChunk bounds one voice chunk. A fifth of a second of Opus is a
	// kilobyte or two; this leaves room for a phone that fell behind.
	maxTalkChunk = 256 << 10
	// maxTalkSeq bounds the chunk number (hours of talking).
	maxTalkSeq = 1 << 20
)

// handleTalkStart serves POST /api/cameras/{id}/talk: it opens the
// loudspeaker and answers with the session the chunks belong to.
func (d Deps) handleTalkStart(w http.ResponseWriter, r *http.Request) {
	c, ok := d.findCamera(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown camera")
		return
	}
	id, err := d.Talk.Start(c.ID)
	switch {
	case errors.Is(err, talk.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		d.Logger.Error("talk start", "camera", c.ID, "err", err)
		writeError(w, http.StatusBadGateway, "could not open the loudspeaker")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"session": string(id)})
}

// handleTalkChunk serves PUT /api/talk/{session}/chunks/{seq}: the body is
// the next piece of the recording, seq its running number from 0.
func (d Deps) handleTalkChunk(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil || seq < 0 || seq > maxTalkSeq {
		writeError(w, http.StatusBadRequest, "chunk number must be a whole number from 0")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTalkChunk))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "chunk too large")
		return
	}
	err = d.Talk.Write(talk.SessionID(r.PathValue("session")), seq, data)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, talk.ErrNoSession):
		// 410 Gone: the client should stop sending and start again.
		writeError(w, http.StatusGone, err.Error())
	case errors.Is(err, talk.ErrTooFarAhead):
		writeError(w, http.StatusConflict, err.Error())
	default:
		d.Logger.Error("talk chunk", "err", err)
		writeError(w, http.StatusBadGateway, "the loudspeaker stopped")
	}
}

// handleTalkStop serves DELETE /api/talk/{session}.
func (d Deps) handleTalkStop(w http.ResponseWriter, r *http.Request) {
	d.Talk.Stop(talk.SessionID(r.PathValue("session")))
	w.WriteHeader(http.StatusNoContent)
}
