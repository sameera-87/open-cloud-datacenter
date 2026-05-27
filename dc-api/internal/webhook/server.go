package webhook

import (
	"io"
	"net/http"

	"github.com/rs/zerolog"
)

// Server is the HTTPS admission webhook server.
type Server struct {
	wrapper *AdmissionReviewWrapper
	log     zerolog.Logger
}

// NewServer constructs a Server from a wrapper and a logger.
func NewServer(wrapper *AdmissionReviewWrapper, log zerolog.Logger) *Server {
	return &Server{wrapper: wrapper, log: log}
}

// Handler returns the http.Handler for the /mutate and /healthz endpoints.
// The caller is responsible for wrapping it in TLS (net/http.ListenAndServeTLS).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", s.mutate)
	mux.HandleFunc("/healthz", s.healthz)
	return mux
}

// mutate is the HTTP handler for POST /mutate.
// It reads the raw AdmissionReview body, delegates to the wrapper, and writes
// the response.  On any error it logs and returns 500 — the apiserver will
// fall back to failurePolicy:Ignore and allow the VM through unchanged.
func (s *Server) mutate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.log.Error().Err(err).Msg("webhook: read body failed")
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}

	resp, err := s.wrapper.Review(r.Context(), body)
	if err != nil {
		s.log.Error().Err(err).Msg("webhook: review failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(resp); err != nil {
		s.log.Error().Err(err).Msg("webhook: write response failed")
	}
}

// healthz is a simple liveness probe endpoint used as a Kubernetes liveness probe.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
