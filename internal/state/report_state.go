package state

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/provider"
)

// ProviderStatus represents the current operational status of a provider
type ProviderStatus int

const (
	StatusHealthy ProviderStatus = iota
	StatusDegraded
	StatusBlocked
	StatusUnreachable
	StatusMisconfigured
)

func (s ProviderStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusDegraded:
		return "degraded"
	case StatusBlocked:
		return "blocked"
	case StatusUnreachable:
		return "unreachable"
	case StatusMisconfigured:
		return "misconfigured"
	default:
		return "unknown"
	}
}

// ErrorType represents the type of error encountered
type ErrorType int

const (
	ErrorTypeNone ErrorType = iota
	ErrorTypeErrorRateLimit
	ErrorTypeErrorDeprecatedModel
	ErrorTypeErrorEmptyResponse
	ErrorTypeErrorTimeout
	ErrorTypeErrorInvalidEndpoint
	ErrorTypeErrorUnknown
)

func (e ErrorType) String() string {
	switch e {
	case ErrorTypeErrorRateLimit:
		return "rate_limit"
	case ErrorTypeErrorDeprecatedModel:
		return "deprecated_model"
	case ErrorTypeErrorEmptyResponse:
		return "empty_response"
	case ErrorTypeErrorTimeout:
		return "timeout"
	case ErrorTypeErrorInvalidEndpoint:
		return "invalid_endpoint"
	case ErrorTypeErrorUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// HTTPErrorInfo captures HTTP-level error details
type HTTPErrorInfo struct {
	Type     ErrorType `json:"type"`
	Message  string    `json:"message"`
	HTTPCode *int      `json:"http_code,omitempty"`
	RawBody  string    `json:"-"`
}

// ProbeResult captures the result of a health probe
type ProbeResult struct {
	Success     bool           `json:"success"`
	LatencyMs   int64          `json:"latency_ms"`
	LastChecked time.Time      `json:"last_checked"`
	Error       *HTTPErrorInfo `json:"error,omitempty"`
}

// RequestOutcome captures the result of a request attempt
type RequestOutcome struct {
	LastSuccess         *time.Time     `json:"last_success,omitempty"`
	LastFailure         *time.Time     `json:"last_failure,omitempty"`
	LastError           *HTTPErrorInfo `json:"last_error,omitempty"`
	ConsecutiveFailures int            `json:"consecutive_failures"`
}

// RateLimitState captures rate limiting information
type RateLimitState struct {
	Limited        bool       `json:"limited"`
	Last429        *time.Time `json:"last_429,omitempty"`
	RecoveryWindow *int       `json:"recovery_window,omitempty"` // in seconds
}

// ModelsInfo captures model discovery state
type ModelsInfo struct {
	Discovered  int       `json:"discovered"`
	LastUpdated time.Time `json:"last_updated"`
}

// MetricsInfo captures request metrics
type MetricsInfo struct {
	TotalRequests       int64 `json:"total_requests"`
	TotalFailures       int64 `json:"total_failures"`
	ConsecutiveFailures int   `json:"consecutive_failures"`
}

// LimitWindowSave captures the state of one rate-limit window for persistence.
type LimitWindowSave struct {
	Count   int       `json:"count"`
	ResetAt time.Time `json:"reset_at"`
}


// ProviderState represents the complete state of a provider for reporting
type ProviderState struct {
	Name          string           `json:"name"`
	Status        ProviderStatus   `json:"status"`
	Probe         ProbeResult      `json:"probe"`
	Request       RequestOutcome   `json:"request"`
	Metrics       MetricsInfo      `json:"metrics"`
	RateLimit     RateLimitState   `json:"rate_limit"`
	Models        ModelsInfo       `json:"models"`
	BlockedUntil   *time.Time        `json:"blocked_until,omitempty"`
	ExhaustedUntil *time.Time        `json:"exhausted_until,omitempty"`
	LimitWindows   []LimitWindowSave `json:"limit_windows,omitempty"`
}

// GlobalState represents the overall system state
type GlobalState struct {
	TotalRequests    int64     `json:"total_requests"`
	TotalFailures    int64     `json:"total_failures"`
	ActiveProviders  int       `json:"active_providers"`
	BlockedProviders int       `json:"blocked_providers"`
	GeneratedAt      time.Time `json:"generated_at"`
}

// ReportData contains all data needed for the report
type ReportData struct {
	Global    GlobalState     `json:"global"`
	Providers []ProviderState `json:"providers"`
}

// StateManager extends the base state manager with reporting capabilities
type StateManager struct {
	mu              sync.RWMutex
	blocked         map[string]time.Time // provider id - blocked until
	exhausted       map[string]time.Time // provider id - exhausted until
	health          HealthReader
	probeResults    map[string]*ProbeResult
	requestOutcomes map[string]*RequestOutcome
	rateLimitState  map[string]*RateLimitState
	modelsInfo      map[string]*ModelsInfo
	metrics         map[string]*MetricsInfo
}

// NewStateManager creates a new StateManager with extended reporting capabilities
func NewStateManager(h HealthReader) *StateManager {
	sm := &StateManager{
		blocked:         make(map[string]time.Time),
		exhausted:       make(map[string]time.Time),
		health:          h,
		probeResults:    make(map[string]*ProbeResult),
		requestOutcomes: make(map[string]*RequestOutcome),
		rateLimitState:  make(map[string]*RateLimitState),
		modelsInfo:      make(map[string]*ModelsInfo),
		metrics:         make(map[string]*MetricsInfo),
	}
	return sm
}

// RecordProbeResult records the result of a health probe
func (sm *StateManager) RecordProbeResult(id string, success bool, latencyMs int64, err error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var errorInfo *HTTPErrorInfo
	if err != nil {
		errorType := ErrorTypeErrorUnknown
		httpCode := 0

		// Try to extract HTTP error info
		if herr, ok := err.(*provider.HTTPError); ok && herr != nil {
			httpCode = herr.StatusCode
			switch httpCode {
			case 429:
				errorType = ErrorTypeErrorRateLimit
			case 404:
				errorType = ErrorTypeErrorInvalidEndpoint
			}
		}

		errorInfo = &HTTPErrorInfo{
			Type:     errorType,
			Message:  err.Error(),
			HTTPCode: &httpCode,
		}
	}

	sm.probeResults[id] = &ProbeResult{
		Success:     success,
		LatencyMs:   latencyMs,
		LastChecked: time.Now(),
		Error:       errorInfo,
	}
}

// RecordRequestSuccess records a successful request
func (sm *StateManager) RecordRequestSuccess(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.metrics[id]; !ok {
		sm.metrics[id] = &MetricsInfo{}
	}
	sm.metrics[id].TotalRequests++

	outcome, _ := sm.requestOutcomes[id]
	if outcome != nil {
		now := time.Now()
		outcome.LastSuccess = &now
		outcome.ConsecutiveFailures = 0
	} else {
		now := time.Now()
		sm.requestOutcomes[id] = &RequestOutcome{
			LastSuccess:         &now,
			ConsecutiveFailures: 0,
		}
	}
}

// RecordRequestFailure records a failed request with error classification
func (sm *StateManager) RecordRequestFailure(id string, err error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.metrics[id]; !ok {
		sm.metrics[id] = &MetricsInfo{}
	}
	sm.metrics[id].TotalFailures++

	var errorType ErrorType
	var httpCode int

	if herr, ok := err.(*provider.HTTPError); ok && herr != nil {
		httpCode = herr.StatusCode
		switch httpCode {
		case 429:
			errorType = ErrorTypeErrorRateLimit
		case 404:
			errorType = ErrorTypeErrorDeprecatedModel // or invalid endpoint
		case 405:
			errorType = ErrorTypeErrorInvalidEndpoint
		default:
			errorType = ErrorTypeErrorUnknown
		}
	} else {
		if isTimeout(err) {
			errorType = ErrorTypeErrorTimeout
		} else {
			errorType = ErrorTypeErrorEmptyResponse // assume empty stream if no HTTP error
		}
	}

	now := time.Now()
	outcome, _ := sm.requestOutcomes[id]
	if outcome != nil {
		outcome.LastFailure = &now
		outcome.LastError = &HTTPErrorInfo{Type: errorType, Message: err.Error(), HTTPCode: &httpCode}
		outcome.ConsecutiveFailures++
	} else {
		sm.requestOutcomes[id] = &RequestOutcome{
			LastFailure:         &now,
			LastError:           &HTTPErrorInfo{Type: errorType, Message: err.Error(), HTTPCode: &httpCode},
			ConsecutiveFailures: 1,
		}
	}

	// Update rate limit state
	if errorType == ErrorTypeErrorRateLimit {
		sm.rateLimitState[id] = &RateLimitState{
			Limited:        true,
			Last429:        &now,
			RecoveryWindow: func() *int { v := 300; return &v }(), // default 5 min
		}
	} else {
		sm.rateLimitState[id] = &RateLimitState{Limited: false}
	}
}

// RecordModelsDiscovered records discovered models for a provider
func (sm *StateManager) RecordModelsDiscovered(id string, count int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.modelsInfo[id]; !ok {
		sm.modelsInfo[id] = &ModelsInfo{}
	}
	sm.modelsInfo[id].Discovered = count
	sm.modelsInfo[id].LastUpdated = time.Now()
}

// Block extends the base manager's block with state updates
func (sm *StateManager) Block(id string, d time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.blocked[id] = time.Now().Add(d)
}

// SetExhausted extends the base manager's exhausted setting
func (sm *StateManager) SetExhausted(id string, resetAt time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.exhausted[id] = resetAt
}

// GetProviderState returns the complete state for a single provider
func (sm *StateManager) GetProviderState(id string) ProviderState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	probe := sm.probeResults[id]
	outcome := sm.requestOutcomes[id]
	rateLimit := sm.rateLimitState[id]
	models := sm.modelsInfo[id]
	metrics := sm.metrics[id]

	if probe == nil {
		probe = &ProbeResult{}
	}
	if outcome == nil {
		outcome = &RequestOutcome{ConsecutiveFailures: 0}
	}
	if rateLimit == nil {
		rateLimit = &RateLimitState{}
	}
	if models == nil {
		models = &ModelsInfo{}
	}
	if metrics == nil {
		metrics = &MetricsInfo{}
	}

	status := sm.determineStatus(id, probe, outcome)

	return ProviderState{
		Name:   id,
		Status: status,
		Probe: ProbeResult{
			Success:     probe.Success,
			LatencyMs:   probe.LatencyMs,
			LastChecked: probe.LastChecked,
			Error:       probe.Error,
		},
		Request: RequestOutcome{
			LastSuccess:         outcome.LastSuccess,
			LastFailure:         outcome.LastFailure,
			LastError:           outcome.LastError,
			ConsecutiveFailures: outcome.ConsecutiveFailures,
		},
		Metrics: MetricsInfo{
			TotalRequests:       int64(metrics.TotalRequests),
			TotalFailures:       int64(metrics.TotalFailures),
			ConsecutiveFailures: outcome.ConsecutiveFailures,
		},
		RateLimit: RateLimitState{
			Limited:        rateLimit.Limited,
			Last429:        rateLimit.Last429,
			RecoveryWindow: rateLimit.RecoveryWindow,
		},
		Models: ModelsInfo{
			Discovered:  models.Discovered,
			LastUpdated: models.LastUpdated,
		},
	}
}

// determineStatus calculates the provider's status based on all available data
func (sm *StateManager) determineStatus(id string, probe *ProbeResult, outcome *RequestOutcome) ProviderStatus {
	now := time.Now()

	sm.mu.RLock()
	bu := sm.blocked[id]
	eu := sm.exhausted[id]
	sm.mu.RUnlock()

	// Check if blocked due to rate limit or exhaustion
	if now.Before(bu) || now.Before(eu) {
		return StatusBlocked
	}

	// Check probe status
	if !probe.Success || (probe.Error != nil && isUnreachableError(probe.Error)) {
		return StatusUnreachable
	}

	// Check misconfiguration errors
	if outcome.LastError != nil {
		switch outcome.LastError.Type {
		case ErrorTypeErrorDeprecatedModel, ErrorTypeErrorInvalidEndpoint:
			return StatusMisconfigured
		case ErrorTypeErrorRateLimit:
			return StatusBlocked
		default:
			return StatusDegraded
		}
	}

	// Check for transient failures
	if outcome.ConsecutiveFailures > 0 && outcome.LastError != nil &&
		(outcome.LastError.Type == ErrorTypeErrorEmptyResponse || outcome.LastError.Type == ErrorTypeErrorTimeout) {
		return StatusDegraded
	}

	// Default to healthy if probe is OK and no failures
	return StatusHealthy
}

// GetGlobalState returns the global system state
func (sm *StateManager) GetGlobalState() GlobalState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var totalRequests, totalFailures int64
	activeProviders := 0
	blockedProviders := 0

	for id := range sm.metrics {
		m := sm.metrics[id]
		totalRequests += int64(m.TotalRequests)
		totalFailures += int64(m.TotalFailures)

		status := sm.determineStatus(id, sm.probeResults[id], sm.requestOutcomes[id])
		if status == StatusBlocked || status == StatusUnreachable {
			blockedProviders++
		} else {
			activeProviders++
		}
	}

	return GlobalState{
		TotalRequests:    totalRequests,
		TotalFailures:    totalFailures,
		ActiveProviders:  activeProviders,
		BlockedProviders: blockedProviders,
		GeneratedAt:      time.Now(),
	}
}

// GetAllProviderStates returns states for all known providers
func (sm *StateManager) GetAllProviderStates() []ProviderState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var results []ProviderState
	for id := range sm.metrics {
		results = append(results, sm.getProviderStateLocked(id))
	}
	return results
}

// getProviderStateLocked returns provider state without locking (caller must lock)
func (sm *StateManager) getProviderStateLocked(id string) ProviderState {
	probe := sm.probeResults[id]
	outcome := sm.requestOutcomes[id]
	rateLimit := sm.rateLimitState[id]
	models := sm.modelsInfo[id]
	metrics := sm.metrics[id]

	if probe == nil {
		probe = &ProbeResult{}
	}
	if outcome == nil {
		outcome = &RequestOutcome{ConsecutiveFailures: 0}
	}
	if rateLimit == nil {
		rateLimit = &RateLimitState{}
	}
	if models == nil {
		models = &ModelsInfo{}
	}
	if metrics == nil {
		metrics = &MetricsInfo{}
	}

	status := sm.determineStatus(id, probe, outcome)

	return ProviderState{
		Name:   id,
		Status: status,
		Probe: ProbeResult{
			Success:     probe.Success,
			LatencyMs:   probe.LatencyMs,
			LastChecked: probe.LastChecked,
			Error:       probe.Error,
		},
		Request: RequestOutcome{
			LastSuccess:         outcome.LastSuccess,
			LastFailure:         outcome.LastFailure,
			LastError:           outcome.LastError,
			ConsecutiveFailures: outcome.ConsecutiveFailures,
		},
		Metrics: MetricsInfo{
			TotalRequests:       int64(metrics.TotalRequests),
			TotalFailures:       int64(metrics.TotalFailures),
			ConsecutiveFailures: outcome.ConsecutiveFailures,
		},
		RateLimit: RateLimitState{
			Limited:        rateLimit.Limited,
			Last429:        rateLimit.Last429,
			RecoveryWindow: rateLimit.RecoveryWindow,
		},
		Models: ModelsInfo{
			Discovered:  models.Discovered,
			LastUpdated: models.LastUpdated,
		},
	}
}

// isUnreachableError determines if an error indicates the provider is unreachable
func isUnreachableError(err *HTTPErrorInfo) bool {
	if err == nil {
		return false
	}
	return err.HTTPCode != nil && (*err.HTTPCode == 404 || *err.HTTPCode >= 500)
}

// isTimeout checks if an error indicates a timeout
func isTimeout(err error) bool {
	return err != nil && (err == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded))
}
