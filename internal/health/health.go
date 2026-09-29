// Package health serves the HTTP probes used by GKE and Cloud Run.
package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// State is the subscriber readiness shared with the HTTP handlers.
type State struct {
	mu      sync.RWMutex
	ready   bool
	problem string
}

// SetReady marks whether the StreamingPull loop is running.
func (s *State) SetReady(ready bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ready = ready
	s.mu.Unlock()
}

// SetError records a fatal subscriber error. Liveness starts failing so the
// platform restarts the container if the process has not exited yet.
func (s *State) SetError(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	s.problem = err.Error()
	s.ready = false
	s.mu.Unlock()
}

func (s *State) snapshot() (ready bool, problem string) {
	if s == nil {
		return false, ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready, s.problem
}

// Handler exposes /healthz, /readyz, and a one-line status on /.
func Handler(state *State) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, problem := state.snapshot()
		if problem != "" {
			write(w, http.StatusServiceUnavailable, "unhealthy: "+problem+"\n")
			return
		}
		write(w, http.StatusOK, "ok\n")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		ready, problem := state.snapshot()
		if problem != "" {
			write(w, http.StatusServiceUnavailable, "not ready: "+problem+"\n")
			return
		}
		if !ready {
			write(w, http.StatusServiceUnavailable, "not ready\n")
			return
		}
		write(w, http.StatusOK, "ready\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		ready, problem := state.snapshot()
		status := "starting"
		if problem != "" {
			status = "error"
		} else if ready {
			status = "ready"
		}
		write(w, http.StatusOK, "pubsub-printer "+status+"\n")
	})
	return mux
}

// Server is a small HTTP server for platform probes.
type Server struct {
	http *http.Server
}

// New listens on addr once Run is called.
func New(addr string, state *State) *Server {
	return &Server{
		http: &http.Server{
			Addr:              addr,
			Handler:           Handler(state),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
}

// Run serves until ctx is cancelled or Listen fails.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		err := s.http.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			errCh <- nil
			return
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("health server shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("health server: %w", err)
		}
		return nil
	}
}
