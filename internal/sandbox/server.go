package sandbox

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// maxRequestBytes caps a run request, arguments included.
const maxRequestBytes = 64 << 10

// Handler serves POST /run, sandboxd's only operation. The body must hold exactly the fields of
// Request; anything else, such as an image, a command or limits, is refused and logged.
func Handler(r *Runner, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, req *http.Request) {
		refused := func(msg string) {
			log.Warn("sandbox request refused", "reason", msg)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		}

		body := http.MaxBytesReader(w, req.Body, maxRequestBytes)
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		var run Request
		if err := dec.Decode(&run); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				refused("request too large")
				return
			}
			refused("invalid request: " + err.Error())
			return
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			refused("body must be a single JSON object")
			return
		}
		if run.Skill == "" || run.Script == "" {
			refused("skill and script are required")
			return
		}

		res, err := r.Run(req.Context(), run)
		switch {
		case errors.Is(err, ErrRefused):
			refused(err.Error())
		case err != nil:
			log.Error("sandbox run failed", "skill", run.Skill, "script", run.Script, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusOK, res)
		}
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
