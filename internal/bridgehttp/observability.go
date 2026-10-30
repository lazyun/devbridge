package bridgehttp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
)

var localRequestSequence atomic.Uint64

type statusCapture struct {
	http.ResponseWriter
	status int
}

// Unwrap preserves ResponseController streaming and per-client deadlines.
func (w *statusCapture) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusCapture) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCapture) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(value)
}

func localRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started := time.Now()
		requestID := request.Header.Get("X-Request-ID")
		if !observability.ValidRequestID(requestID) {
			requestID = localRequestID()
		}
		ctx := observability.WithContext(request.Context(), observability.Correlation{RequestID: requestID})
		request = request.WithContext(ctx)
		observability.Info(ctx, "http", "local.request.requested", "local Console/API request received", map[string]any{"method": request.Method, "path": request.URL.Path})
		capture := &statusCapture{ResponseWriter: w}
		next.ServeHTTP(capture, request)
		status := capture.status
		if status == 0 {
			status = http.StatusOK
		}
		data := map[string]any{"method": request.Method, "path": request.URL.Path, "status": status, "durationMillis": time.Since(started).Milliseconds()}
		if status >= 500 {
			data["errorCode"] = "LOCAL_API_ERROR"
			observability.Error(ctx, "http", "local.request.failed", "local Console/API request failed", data)
		} else {
			observability.Info(ctx, "http", "local.request.completed", "local Console/API request completed", data)
		}
	})
}

func localRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "req_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("req_local_%d", localRequestSequence.Add(1))
}
