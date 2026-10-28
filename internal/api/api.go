// Package api implements the optional local integration API. It is intended
// for desktop applications that want progress events and job state over HTTP;
// the CLI JSON protocol remains the primary language-agnostic interface.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/engine"
	"github.com/yxxbc/wmlUpdate/internal/event"
	"github.com/yxxbc/wmlUpdate/internal/platform"
)

// Options configures the API server.
type Options struct {
	Token  string
	Logger *slog.Logger
}

// Server is the local updater API.
type Server struct {
	cfg      *config.Config
	platform platform.Platform
	token    string
	logger   *slog.Logger
	jobs     *jobManager
	http     *http.Server
}

// New builds an API server.
func New(cfg *config.Config, p platform.Platform, opts Options) (*Server, error) {
	if opts.Token == "" {
		return nil, errors.New("API token is required")
	}
	if len(opts.Token) < 16 {
		return nil, errors.New("API token must be at least 16 characters")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		cfg:      cfg,
		platform: p,
		token:    opts.Token,
		logger:   opts.Logger,
		jobs:     newJobManager(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/info", s.handleInfo)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("POST /v1/check", s.handleCheck)
	mux.HandleFunc("POST /v1/update", s.handleUpdate)
	mux.HandleFunc("POST /v1/fetch", s.handleFetch)
	mux.HandleFunc("POST /v1/rollback", s.handleRollback)
	mux.HandleFunc("POST /v1/mark", s.handleMark)
	mux.HandleFunc("GET /v1/jobs", s.handleJobs)
	mux.HandleFunc("GET /v1/jobs/{id}", s.handleJob)
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", s.handleCancel)
	mux.HandleFunc("GET /v1/jobs/{id}/events", s.handleEvents)
	s.http = &http.Server{
		Handler:           s.auth(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	return s, nil
}

// Handler returns the authenticated HTTP handler.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Serve serves until the listener is closed.
func (s *Server) Serve(l net.Listener) error {
	err := s.http.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the HTTP server and cancels running jobs.
func (s *Server) Shutdown(ctx context.Context) error {
	s.jobs.cancelAll()
	return s.http.Shutdown(ctx)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			token = r.Header.Get("X-WML-Token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing API token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app_id":   s.cfg.AppID,
		"channel":  s.cfg.Channel,
		"platform": s.platform,
		"source":   s.cfg.Source.Type,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	eng, err := engine.New(s.cfg, s.platform, event.NopSink{})
	if err != nil {
		writeEngineError(w, err)
		return
	}
	defer eng.Close()
	st, err := eng.Status()
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type checkRequest struct {
	CurrentVersion string `json:"current_version"`
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if err := decodeOptional(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	eng, err := engine.New(s.cfg, s.platform, event.NopSink{})
	if err != nil {
		writeEngineError(w, err)
		return
	}
	defer eng.Close()
	res, err := eng.Check(r.Context(), req.CurrentVersion)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type updateRequest struct {
	CurrentVersion string `json:"current_version"`
	Force          bool   `json:"force"`
	Defer          bool   `json:"defer"`
	WaitPIDs       []int  `json:"wait_pids"`
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if err := decodeOptional(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	job, err := s.jobs.start("update", true, func(ctx context.Context, sink event.Sink) (*engine.Result, error) {
		eng, err := engine.New(s.cfg, s.platform, sink)
		if err != nil {
			return nil, err
		}
		defer eng.Close()
		return eng.Update(ctx, engine.UpdateOptions{
			CurrentVersion: req.CurrentVersion,
			Force:          req.Force,
			Defer:          req.Defer,
			WaitPIDs:       req.WaitPIDs,
		})
	})
	if err != nil {
		writeError(w, http.StatusConflict, "job_conflict", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job.snapshot(false))
}

type fetchRequest struct {
	CurrentVersion string `json:"current_version"`
	Output         string `json:"output"`
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	var req fetchRequest
	if err := decodeOptional(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	job, err := s.jobs.start("fetch", true, func(ctx context.Context, sink event.Sink) (*engine.Result, error) {
		eng, err := engine.New(s.cfg, s.platform, sink)
		if err != nil {
			return nil, err
		}
		defer eng.Close()
		return eng.Fetch(ctx, engine.FetchOptions{CurrentVersion: req.CurrentVersion, Output: req.Output})
	})
	if err != nil {
		writeError(w, http.StatusConflict, "job_conflict", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job.snapshot(false))
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	job, err := s.jobs.start("rollback", true, func(ctx context.Context, sink event.Sink) (*engine.Result, error) {
		eng, err := engine.New(s.cfg, s.platform, sink)
		if err != nil {
			return nil, err
		}
		defer eng.Close()
		return eng.Rollback(ctx)
	})
	if err != nil {
		writeError(w, http.StatusConflict, "job_conflict", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job.snapshot(false))
}

type markRequest struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func (s *Server) handleMark(w http.ResponseWriter, r *http.Request) {
	var req markRequest
	if err := decodeOptional(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	eng, err := engine.New(s.cfg, s.platform, event.NopSink{})
	if err != nil {
		writeEngineError(w, err)
		return
	}
	defer eng.Close()
	res, err := eng.MarkInstalled(r.Context(), req.Version, req.SHA256)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.list()})
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.get(r.PathValue("id"))
	if job == nil {
		writeError(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	writeJSON(w, http.StatusOK, job.snapshot(true))
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.get(r.PathValue("id"))
	if job == nil {
		writeError(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	job.cancel()
	writeJSON(w, http.StatusOK, job.snapshot(false))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.get(r.PathValue("id"))
	if job == nil {
		writeError(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unsupported", "streaming is unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	last := 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		last, _ = strconv.Atoi(v)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		events := job.events.Events()
		for last < len(events) {
			data, err := json.Marshal(events[last])
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", last+1, data)
			last++
		}
		flusher.Flush()
		if job.done() {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func decodeOptional(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values in request body")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func writeEngineError(w http.ResponseWriter, err error) {
	code := engine.CodeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case "config", "integrity":
		status = http.StatusBadRequest
	case "no_release", "no_asset":
		status = http.StatusNotFound
	case "source", "network":
		status = http.StatusBadGateway
	case "locked":
		status = http.StatusConflict
	case "cancelled":
		status = http.StatusRequestTimeout
	}
	writeError(w, status, code, err.Error())
}

type jobStatus string

const (
	jobRunning   jobStatus = "running"
	jobSucceeded jobStatus = "succeeded"
	jobFailed    jobStatus = "failed"
	jobCancelled jobStatus = "cancelled"
)

type Job struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Status     jobStatus      `json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Result     *engine.Result `json:"result,omitempty"`
	Error      *jobError      `json:"error,omitempty"`

	events   *event.Buffer
	cancelFn context.CancelFunc
	finished chan struct{}
	mu       sync.Mutex
}

type jobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type jobSnapshot struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Status     jobStatus      `json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Result     *engine.Result `json:"result,omitempty"`
	Error      *jobError      `json:"error,omitempty"`
	Events     []event.Event  `json:"events,omitempty"`
}

func (j *Job) snapshot(includeEvents bool) jobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := jobSnapshot{
		ID: j.ID, Kind: j.Kind, Status: j.Status, CreatedAt: j.CreatedAt,
		FinishedAt: j.FinishedAt, Result: j.Result, Error: j.Error,
	}
	if includeEvents {
		s.Events = j.events.Events()
	}
	return s
}

func (j *Job) cancel() {
	j.mu.Lock()
	cancel := j.cancelFn
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (j *Job) done() bool {
	select {
	case <-j.finished:
		return true
	default:
		return false
	}
}

type jobManager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	running int
}

func newJobManager() *jobManager {
	return &jobManager{jobs: make(map[string]*Job)}
}

func (m *jobManager) start(kind string, mutating bool, fn func(context.Context, event.Sink) (*engine.Result, error)) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mutating && m.running > 0 {
		return nil, errors.New("another update job is already running")
	}
	m.pruneLocked()
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		ID:        id,
		Kind:      kind,
		Status:    jobRunning,
		CreatedAt: time.Now().UTC(),
		events:    event.NewBuffer(512),
		cancelFn:  cancel,
		finished:  make(chan struct{}),
	}
	m.jobs[id] = j
	if mutating {
		m.running++
	}
	go func() {
		defer close(j.finished)
		defer func() {
			if mutating {
				m.mu.Lock()
				m.running--
				m.mu.Unlock()
			}
		}()
		defer func() {
			if r := recover(); r != nil {
				j.mu.Lock()
				now := time.Now().UTC()
				j.FinishedAt = &now
				j.Status = jobFailed
				j.Error = &jobError{Code: "internal", Message: fmt.Sprintf("panic: %v", r)}
				j.mu.Unlock()
				j.events.Emit(event.Event{Type: event.TypeError, Error: &event.Error{Code: "internal", Message: fmt.Sprintf("panic: %v", r)}})
			}
		}()
		result, err := fn(ctx, j.events)
		cancel()
		j.mu.Lock()
		now := time.Now().UTC()
		j.FinishedAt = &now
		var terminal event.Event
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				j.Status = jobCancelled
			} else {
				j.Status = jobFailed
			}
			j.Error = &jobError{Code: engine.CodeOf(err), Message: err.Error()}
			terminal = event.Event{Type: event.TypeError, Error: &event.Error{Code: j.Error.Code, Message: j.Error.Message}}
		} else {
			j.Status = jobSucceeded
			j.Result = result
			terminal = event.Event{Type: event.TypeResult, Data: result}
		}
		j.mu.Unlock()
		j.events.Emit(terminal)
	}()
	return j, nil
}

func (m *jobManager) get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *jobManager) list() []jobSnapshot {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	out := make([]jobSnapshot, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.snapshot(false))
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	return out
}

func (m *jobManager) cancelAll() {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		j.cancel()
	}
}

func (m *jobManager) pruneLocked() {
	cutoff := time.Now().Add(-2 * time.Hour)
	for id, j := range m.jobs {
		if j.CreatedAt.Before(cutoff) && j.done() {
			delete(m.jobs, id)
		}
	}
}

func randomID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
