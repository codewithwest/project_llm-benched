package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"llm-benchmarker/internal/api"
	"llm-benchmarker/internal/db"
)

const maxCapturedBodyBytes = 10 << 20

type TransparentProxy struct {
	TargetURL    *url.URL
	DB           *db.Database
	ReverseProxy *httputil.ReverseProxy
}

func NewTransparentProxy(targetURL string, database *db.Database) (*TransparentProxy, error) {
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(parsedURL)

	return &TransparentProxy{
		TargetURL:    parsedURL,
		DB:           database,
		ReverseProxy: proxy,
	}, nil
}

type trackingResponseWriter struct {
	http.ResponseWriter
	startTime         time.Time
	firstTokenTime    time.Time
	streamTokenCount  int
	wordTokenCount    int
	responseBytes     int
	isInterceptTarget bool
	responseBody      bytes.Buffer
	statusCode        int
	errorMessage      string
}

func (w *trackingResponseWriter) tokenCount() int {
	if w.streamTokenCount > w.wordTokenCount {
		return w.streamTokenCount
	}
	return w.wordTokenCount
}

func (w *trackingResponseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.isInterceptTarget && w.firstTokenTime.IsZero() {
		w.firstTokenTime = time.Now()
	}

	if w.isInterceptTarget {
		w.streamTokenCount += bytes.Count(b, []byte("\n"))
		w.wordTokenCount += bytes.Count(b, []byte(" "))
		w.responseBytes += len(b)
		capture := b
		if w.responseBody.Len() < maxCapturedBodyBytes {
			remaining := maxCapturedBodyBytes - w.responseBody.Len()
			if len(capture) > remaining {
				capture = capture[:remaining]
			}
			w.responseBody.Write(capture)
		}
	}

	return w.ResponseWriter.Write(b)
}

func (w *trackingResponseWriter) WriteHeader(code int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackingResponseWriter) Flush() {
	if w.statusCode == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (p *TransparentProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	targetHost := p.TargetURL
	if customTarget := r.Header.Get("X-Target-Provider"); customTarget != "" {
		if parsed, err := url.Parse(customTarget); err == nil {
			targetHost = parsed
		}
	}

	isTarget := strings.Contains(r.URL.Path, "/generate") || strings.Contains(r.URL.Path, "/chat") || strings.Contains(r.URL.Path, "/completions") || strings.Contains(r.URL.Path, "/embeddings")

	var prompt string
	var promptLength int
	var modelName string
	var isStream bool
	var rawRequestBody string
	if isTarget && r.Body != nil {
		if r.ContentLength > maxCapturedBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err == nil {
			rawRequestBody = string(body)
			prompt = extractPrompt(body)
			promptLength = extractPromptLength(body)
			modelName = extractModel(body)
			isStream = extractStream(body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
	}

	log.Printf("→ %s %s (model: %s, stream: %v, prompt: %d chars)", r.Method, r.URL.Path, modelName, isStream, promptLength)

	rp := httputil.NewSingleHostReverseProxy(targetHost)
	rp.FlushInterval = 50 * time.Millisecond

	tracker := &trackingResponseWriter{
		ResponseWriter:    w,
		startTime:         time.Now(),
		isInterceptTarget: isTarget,
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		tracker.errorMessage = err.Error()
		http.Error(w, "upstream request failed", http.StatusBadGateway)
	}

	rp.ServeHTTP(tracker, r)

	if isTarget {
		endTime := time.Now()
		elapsed := endTime.Sub(tracker.startTime)

		var ttftNs int64
		if !tracker.firstTokenTime.IsZero() {
			ttftNs = tracker.firstTokenTime.Sub(tracker.startTime).Nanoseconds()
		}

		tps := 0.0
		if elapsed.Seconds() > 0 {
			tps = float64(tracker.tokenCount()) / elapsed.Seconds()
		}
		if tracker.statusCode == 0 {
			tracker.statusCode = http.StatusOK
		}
		if tracker.statusCode >= http.StatusBadRequest && tracker.errorMessage == "" {
			tracker.errorMessage = http.StatusText(tracker.statusCode)
		}
		tokenSource := "estimate"
		if isStream {
			tokenSource = "stream-chunks"
		}

		clientIP := r.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				clientIP = host
			} else {
				clientIP = r.RemoteAddr
			}
		}

		log.Printf("← %s | %d tokens | %.2f TPS | TTFT: %dms | model: %s | IP: %s | %dms",
			r.URL.Path, tracker.tokenCount(), tps, ttftNs/1_000_000, modelName, clientIP, elapsed.Milliseconds())

		err := p.DB.SaveBenchmarkWithMetadata(
			prompt,
			r.URL.Path,
			modelName,
			targetHost.String(),
			clientIP,
			tracker.statusCode,
			tracker.errorMessage,
			tokenSource,
			tps,
			ttftNs,
			0,
			elapsed.Milliseconds(),
			tracker.tokenCount(),
			promptLength,
			tracker.responseBytes,
			rawRequestBody,
			tracker.responseBody.String(),
		)
		if err != nil {
			log.Printf("Failed to save intercepted telemetry: %v", err)
		}

		api.GlobalMetrics.Record(modelName, tracker.tokenCount(), tps, ttftNs, elapsed.Milliseconds())
	}
}

func extractPrompt(body []byte) string {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "Intercepted Request"
	}
	if req.Prompt == "" {
		return "Intercepted Request"
	}
	if len(req.Prompt) > 200 {
		return req.Prompt[:200] + "..."
	}
	return req.Prompt
}

func extractPromptLength(body []byte) int {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0
	}
	return len(req.Prompt)
}

func extractModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "unknown"
	}
	if req.Model == "" {
		return "unknown"
	}
	return req.Model
}

func extractStream(body []byte) bool {
	var req struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return true
	}
	return req.Stream
}
