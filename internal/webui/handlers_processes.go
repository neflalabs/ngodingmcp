package webui

import (
	"net/http"
	"ngodingmcp/internal/procman"
	"time"
)

func (u *UI) handleProcesses(w http.ResponseWriter, r *http.Request) {
	procs := u.procs.List()
	if procs == nil {
		procs = []procman.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": procs})
}

type stopProcessRequest struct {
	Wait         *bool `json:"wait"`
	GraceSeconds int   `json:"graceSeconds"`
}

func (u *UI) handleStopProcess(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing process name"})
		return
	}
	var req stopProcessRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	grace := time.Duration(req.GraceSeconds) * time.Second
	wait := req.Wait != nil && *req.Wait
	info, err := u.procs.Stop(name, grace, wait)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleProcessLogs returns a process's output. Pass after=<nextSeq> from the
// previous response to receive only newer lines; tail limits the first read.
func (u *UI) handleProcessLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	q := r.URL.Query()
	tail, err := queryInt(q.Get("tail"), 200)
	if err != nil || tail < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tail must be a non-negative integer"})
		return
	}
	if tail > maxLogTail {
		tail = maxLogTail
	}
	after, err := queryInt(q.Get("after"), 0)
	if err != nil || after < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "after must be a non-negative integer"})
		return
	}
	info, logs, err := u.procs.Logs(name, tail, int64(after))
	if err != nil {
		writeErr(w, err)
		return
	}
	if logs.Lines == nil {
		logs.Lines = []procman.LogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"process": info, "logs": logs})
}

func (u *UI) handleRestartProcess(w http.ResponseWriter, r *http.Request) {
	var req stopProcessRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	info, err := u.procs.Restart(r.PathValue("name"), time.Duration(req.GraceSeconds)*time.Second)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (u *UI) handleStopAllProcesses(w http.ResponseWriter, r *http.Request) {
	var req stopProcessRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	grace := time.Duration(req.GraceSeconds) * time.Second
	procs := u.procs.StopAll(r.Context(), grace)
	if procs == nil {
		procs = []procman.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": procs})
}
