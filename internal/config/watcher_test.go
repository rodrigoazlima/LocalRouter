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
local:
  nodes:
    - id: node-1
      type: ollama
      endpoint: http://localhost:11434
remote:
  providers: []
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
local:
  nodes:
    - id: node-1
      type: ollama
      endpoint: http://localhost:11434
    - id: node-2
      type: openai-compatible
      endpoint: http://localhost:1234
remote:
  providers: []
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
local:
  nodes:
    - id: node-1
      type: ollama
      endpoint: http://localhost:11434
remote:
  providers: []
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
