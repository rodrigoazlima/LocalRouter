package config_test

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/config"
)

func TestWatcher_CallsOnChangeAfterFileWrite(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3
providers:
  - id: node-1
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3
        priority: 1
`)
	initial, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var callCount atomic.Int32
	w, err := config.NewWatcher(path, initial, func(_ *config.Config, cfg *config.Config) {
		callCount.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Give watcher goroutines time to initialize
	time.Sleep(100 * time.Millisecond)

	os.WriteFile(path, []byte(`
version: 2
routing:
  default_model: llama3
providers:
  - id: node-1
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3
        priority: 1
  - id: node-2
    type: ollama
    endpoint: http://localhost:11435
    models:
      - id: llama3
        priority: 2
`), 0644)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if callCount.Load() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("onChange not called within 2s")
}

func TestWatcher_InvalidConfig_DoesNotCallOnChange(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3
providers:
  - id: node-1
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3
        priority: 1
`)
	initial, _ := config.Load(path)
	var callCount atomic.Int32
	w, _ := config.NewWatcher(path, initial, func(_ *config.Config, _ *config.Config) { callCount.Add(1) })
	defer w.Stop()

	os.WriteFile(path, []byte(`this: is: invalid: yaml: ::::`), 0644)
	time.Sleep(300 * time.Millisecond)
	if callCount.Load() > 0 {
		t.Fatal("onChange must not fire on invalid config")
	}
}
