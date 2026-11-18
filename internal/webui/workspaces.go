package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"ngodingmcp/internal/workspace"
)

// Workspaces is what the workspace selector needs from the server. The app
// package implements it, because switching also touches the command runner
// and the process manager.
type Workspaces interface {
	List() (WorkspaceList, error)
	// Save creates a saved workspace (original == "") or updates the one
	// named original.
	Save(original string, in WorkspaceInput) (WorkspaceList, error)
	Delete(name string) (WorkspaceList, error)
	// Switch makes the saved workspace name active. When background
	// processes run and stopProcesses is false it returns a
	// *ProcessesRunningError and changes nothing.
	Switch(ctx context.Context, name string, stopProcesses bool) (SwitchResult, error)
}

// WorkspaceInput is the body of a create or update request. Nil or zero
// settings inherit the global defaults.
type WorkspaceInput struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	AllowSensitive *bool  `json:"allowSensitive"`
	MaxFileBytes   int64  `json:"maxFileBytes"`
}

// WorkspaceItem is one saved workspace in a listing.
type WorkspaceItem struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	AllowSensitive *bool  `json:"allowSensitive"`
	MaxFileBytes   int64  `json:"maxFileBytes"`
	Active         bool   `json:"active"`
	// Missing is true when the folder no longer exists.
	Missing bool `json:"missing"`
}

// WorkspaceDefaults are the global settings the server was started with.
type WorkspaceDefaults struct {
	AllowSensitive bool  `json:"allowSensitive"`
	MaxFileBytes   int64 `json:"maxFileBytes"`
}

// WorkspaceList is the selector state.
type WorkspaceList struct {
	// ActiveRoot is the folder in use now; ActiveName is its saved name, or
	// empty when that folder is not saved yet.
	ActiveRoot string `json:"activeRoot"`
	ActiveName string `json:"activeName"`
	// Effective are the settings in force for the active workspace.
	Effective  WorkspaceDefaults `json:"effective"`
	Defaults   WorkspaceDefaults `json:"defaults"`
	Workspaces []WorkspaceItem   `json:"workspaces"`
	// RunningProcesses names the background processes a switch would stop.
	RunningProcesses []string `json:"runningProcesses"`
}

// SwitchResult reports a completed switch.
type SwitchResult struct {
	Name             string            `json:"name"`
	Root             string            `json:"root"`
	Effective        WorkspaceDefaults `json:"effective"`
	StoppedProcesses []string          `json:"stoppedProcesses"`
}

// Errors returned by a Workspaces implementation.
var (
	ErrWorkspaceNotFound = errors.New("saved workspace not found")
	ErrWorkspaceExists   = errors.New("a saved workspace with this name already exists")
	ErrWorkspaceInvalid  = errors.New("invalid workspace")
)

// ProcessesRunningError blocks a switch until the user agrees to stop the
// background processes of the current workspace.
type ProcessesRunningError struct{ Names []string }

func (e *ProcessesRunningError) Error() string {
	return "background processes are still running: " + strings.Join(e.Names, ", ")
}

// SetWorkspaces attaches the workspace selector. Without it the selector
// endpoints are not registered.
func (u *UI) SetWorkspaces(w Workspaces) { u.workspaces = w }

func (u *UI) addWorkspaceRoutes(mux *http.ServeMux) {
	if u.workspaces == nil {
		return
	}
	mux.HandleFunc("GET /workspaces", u.handleWorkspaces)
	mux.HandleFunc("POST /workspaces", u.handleCreateWorkspace)
	mux.HandleFunc("PUT /workspaces/{name}", u.handleUpdateWorkspace)
	mux.HandleFunc("DELETE /workspaces/{name}", u.handleDeleteWorkspace)
	mux.HandleFunc("POST /workspaces/{name}/activate", u.handleActivateWorkspace)
}

// holdWorkspace keeps the workspace from being switched while a file request
// runs, so one request never mixes two workspace roots.
func (u *UI) holdWorkspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release := u.ws.Hold()
		defer release()
		next.ServeHTTP(w, r)
	})
}

func (u *UI) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := u.workspaces.List()
	if err != nil {
		writeWorkspaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (u *UI) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var in WorkspaceInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	list, err := u.workspaces.Save("", in)
	if err != nil {
		writeWorkspaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, list)
}

func (u *UI) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	var in WorkspaceInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	list, err := u.workspaces.Save(r.PathValue("name"), in)
	if err != nil {
		writeWorkspaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (u *UI) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	list, err := u.workspaces.Delete(r.PathValue("name"))
	if err != nil {
		writeWorkspaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type activateRequest struct {
	StopProcesses bool `json:"stopProcesses"`
}

func (u *UI) handleActivateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req activateRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := u.workspaces.Switch(r.Context(), r.PathValue("name"), req.StopProcesses)
	if err != nil {
		writeWorkspaceErr(w, err)
		return
	}
	if res.StoppedProcesses == nil {
		res.StoppedProcesses = []string{}
	}
	writeJSON(w, http.StatusOK, res)
}

func writeWorkspaceErr(w http.ResponseWriter, err error) {
	var running *ProcessesRunningError
	switch {
	case errors.As(err, &running):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":            err.Error(),
			"runningProcesses": running.Names,
		})
	case errors.Is(err, ErrWorkspaceNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrWorkspaceExists), errors.Is(err, workspace.ErrBusy):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeErr(w, err)
	}
}
