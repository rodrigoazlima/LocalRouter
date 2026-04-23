// internal/server/sse.go
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/reqid"
)

type completionRequest struct {
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

type completionResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []choice       `json:"choices"`
	Usage   provider.Usage `json:"usage"`
}

type choice struct {
	Index   int              `json:"index"`
	Message provider.Message `json:"message"`
}

type streamChoice struct {
	Index int `json:"index"`
	Delta struct {
		Content string `json:"content"`
	} `json:"delta"`
}

type streamEvent struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Choices []streamChoice `json:"choices"`
}

type sseError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	var req completionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	id := reqid.New()
	clientIP := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		clientIP = xff
	}
	log.Printf("[%s] IN from=%s model=%q stream=%v", id, clientIP, req.Model, req.Stream)

	ctx := reqid.With(r.Context(), id)
	provReq := &provider.Request{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   req.Stream,
	}

	if !req.Stream {
		s.handleComplete(w, r.WithContext(ctx), provReq)
		return
	}
	s.handleStream(w, r.WithContext(ctx), provReq)
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request, req *provider.Request) {
	resp, err := s.router.Route(r.Context(), req)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q,"type":"router_error"}}`, err.Error()), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(completionResponse{
		ID:     resp.ID,
		Object: "chat.completion",
		Model:  resp.Model,
		Choices: []choice{{
			Index:   0,
			Message: provider.Message{Role: "assistant", Content: resp.Content},
		}},
		Usage: resp.Usage,
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, req *provider.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	s.metrics.StreamsStarted.Add(1)
	start := time.Now()
	disconnected := false
	defer func() {
		if disconnected {
			s.metrics.StreamsDisconnected.Add(1)
		} else {
			s.metrics.StreamsCompleted.Add(1)
		}
		s.metrics.StreamDuration.Add(time.Since(start).Milliseconds())
	}()

	ch, err := s.router.Stream(r.Context(), req)
	if err != nil {
		writeSSEError(w, err.Error())
		flusher.Flush()
		return
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			disconnected = true
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case chunk, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			if chunk.Err != nil {
				writeSSEError(w, chunk.Err.Error())
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(streamEvent{
				Object: "chat.completion.chunk",
				Choices: []streamChoice{{
					Delta: struct {
						Content string `json:"content"`
					}{Content: chunk.Delta},
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func writeSSEError(w http.ResponseWriter, msg string) {
	var e sseError
	e.Error.Message = msg
	e.Error.Type = "stream_error"
	data, _ := json.Marshal(e)
	fmt.Fprintf(w, "data: %s\n\n", data)
}
