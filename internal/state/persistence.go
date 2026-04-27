package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const settingsDir = ".settings"

// ProviderStateFile returns the path to a provider's state file
func ProviderStateFile(id string) string {
	return filepath.Join(settingsDir, "providers", fmt.Sprintf("%s.json", id))
}

// GlobalStateFile returns the path to the global state file
func GlobalStateFile() string {
	return filepath.Join(settingsDir, "global.json")
}

// EnsureSettingsDir ensures the settings directory exists
func EnsureSettingsDir() error {
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(settingsDir, "providers"), 0755); err != nil {
		return fmt.Errorf("create providers dir: %w", err)
	}
	return nil
}

// SaveProviderState saves a provider's state to disk atomically
func SaveProviderState(state ProviderState) error {
	if err := EnsureSettingsDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	path := ProviderStateFile(state.Name)
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // cleanup on failure
		return fmt.Errorf("rename temp to final: %w", err)
	}

	return nil
}

// LoadProviderState loads a provider's state from disk
func LoadProviderState(id string) (ProviderState, error) {
	path := ProviderStateFile(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ProviderState{Name: id}, nil // return empty state if file doesn't exist
		}
		return ProviderState{}, fmt.Errorf("read state file: %w", err)
	}

	var state ProviderState
	if err := json.Unmarshal(data, &state); err != nil {
		return ProviderState{}, fmt.Errorf("unmarshal state: %w", err)
	}

	return state, nil
}

// SaveGlobalState saves the global state to disk atomically
func SaveGlobalState(state GlobalState) error {
	if err := EnsureSettingsDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	path := GlobalStateFile()
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // cleanup on failure
		return fmt.Errorf("rename temp to final: %w", err)
	}

	return nil
}

// LoadGlobalState loads the global state from disk
func LoadGlobalState() (GlobalState, error) {
	path := GlobalStateFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return GlobalState{}, nil // return empty state if file doesn't exist
		}
		return GlobalState{}, fmt.Errorf("read state file: %w", err)
	}

	var state GlobalState
	if err := json.Unmarshal(data, &state); err != nil {
		return GlobalState{}, fmt.Errorf("unmarshal state: %w", err)
	}

	return state, nil
}

// LoadAllProviderStates loads all provider states from disk
func LoadAllProviderStates() (map[string]ProviderState, error) {
	states := make(map[string]ProviderState)

	providerDir := filepath.Join(settingsDir, "providers")
	entries, err := os.ReadDir(providerDir)
	if err != nil {
		if os.IsNotExist(err) {
			return states, nil
		}
		return nil, fmt.Errorf("read provider dir: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && filepath.Ext(name) == ".json" {
			id := name[:len(name)-5] // remove .json extension

			state, err := LoadProviderState(id)
			if err != nil {
				return nil, fmt.Errorf("load state for %s: %w", id, err)
			}
			states[id] = state
		}
	}

	return states, nil
}

// BuildReportData builds the complete report data from disk and memory
func BuildReportData(sm *StateManager) ReportData {
	report := ReportData{
		Global: GlobalState{
			GeneratedAt: time.Now(),
		},
		Providers: []ProviderState{},
	}

	// Load saved states
	savedStates, _ := LoadAllProviderStates()

	for id, state := range savedStates {
		if state.Name == "" {
			state.Name = id
		}
		report.Providers = append(report.Providers, state)
	}

	// Get in-memory metrics from StateManager
	inMemoryStates := sm.GetAllProviderStates()
	for _, ms := range inMemoryStates {
		found := false
		for i := range report.Providers {
			if report.Providers[i].Name == ms.Name {
				report.Providers[i] = ms
				found = true
				break
			}
		}
		if !found {
			report.Providers = append(report.Providers, ms)
		}
	}

	// Calculate global state
	for _, p := range report.Providers {
		report.Global.TotalRequests += p.Metrics.TotalRequests
		report.Global.TotalFailures += p.Metrics.TotalFailures

		switch p.Status {
		case StatusBlocked, StatusUnreachable:
			report.Global.BlockedProviders++
		default:
			report.Global.ActiveProviders++
		}
	}

	return report
}
