// internal/server/sse.go
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/reqid"
)

// incomingMessage handles both string and array content from clients like Cline.
type incomingMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (m incomingMessage) toProviderMessage() provider.Message {
	// Try plain string first.
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return provider.Message{Role: m.Role, Content: s}
	}
	// Array of content blocks — extract text parts.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return provider.Message{Role: m.Role, Content: strings.Join(parts, "\n")}
	}
	return provider.Message{Role: m.Role, Content: string(m.Content)}
}

type completionRequest struct {
	Model       string            `json:"model"`
	Messages    []incomingMessage `json:"messages"`
	Stream      bool              `json:"stream"`
	Temperature *float64          `json:"temperature,omitempty"`
	TopP        *float64          `json:"top_p,omitempty"`
	MaxTokens   *int              `json:"max_tokens,omitempty"`
	Seed        *int              `json:"seed,omitempty"`
}

type completionResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []choice       `json:"choices"`
	Usage   provider.Usage `json:"usage"`
}

type choice struct {
	Index        int              `json:"index"`
	Message      provider.Message `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type streamChoice struct {
	Index int `json:"index"`
	Delta struct {
		Role    string `json:"role,omitempty"`
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type streamEvent struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[REQ] decode error: %v", err)
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		log.Printf("[REQ] no messages in request")
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

	msgs := make([]provider.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = m.toProviderMessage()
	}

	provReq := &provider.Request{
		Model:       req.Model,
		Messages:    msgs,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Seed:        req.Seed,
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
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []choice{{
			Index:        0,
			Message:      provider.Message{Role: "assistant", Content: resp.Content},
			FinishReason: "stop",
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

	resolvedModel, ch, err := s.router.Stream(r.Context(), req)
	if err != nil {
		writeSSEError(w, err.Error())
		flusher.Flush()
		return
	}

	created := time.Now().Unix()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	stopReason := "stop"
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
				// Send final chunk with finish_reason before [DONE].
				data, _ := json.Marshal(streamEvent{
					ID:      "",
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   resolvedModel,
					Choices: []streamChoice{{
						FinishReason: &stopReason,
					}},
				})
				fmt.Fprintf(w, "data: %s\n\n", data)
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
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   resolvedModel,
				Choices: []streamChoice{{
					Delta: struct {
						Role    string `json:"role,omitempty"`
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
