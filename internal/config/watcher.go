package config

import (
	"log"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	path     string
	mu       sync.Mutex
	version  int
	current  *Config
	onChange func(old, new *Config)
	stop     chan struct{}
}

func NewWatcher(path string, initial *Config, onChange func(old, new *Config)) (*Watcher, error) {
	w := &Watcher{
		path:     path,
		version:  1,
		current:  initial,
		onChange: onChange,
		stop:     make(chan struct{}),
	}
	go w.watchLoop()
	go w.periodicRefresh()
	return w, nil
}

func (w *Watcher) Stop() { close(w.stop) }

func (w *Watcher) Current() *Config {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current
}

func (w *Watcher) watchLoop() {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("config watcher: fsnotify init: %v", err)
		return
	}
	defer fw.Close()
	if err := fw.Add(w.path); err != nil {
		log.Printf("config watcher: add failed: %v", err)
		return
	}

	reload := make(chan struct{}, 1)
	var debounce *time.Timer

	for {
		select {
		case <-w.stop:
			return
		case event, ok := <-fw.Events:
			if !ok {
				return
			}
			_ = event // silence unused warning if no logging
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(100*time.Millisecond, func() {
				select {
				case reload <- struct{}{}:
					// Reload queued
				default:
					// Reload channel full, skip
				}
			})
		case <-reload:
			w.reload()
		case err := <-fw.Errors:
			log.Printf("config watcher: %v", err)
		}
	}
}

func (w *Watcher) periodicRefresh() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.reload()
		}
	}
}

func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		log.Printf("config reload failed (keeping current): %v", err)
		return
	}
	w.mu.Lock()
	w.version++
	cfg.Version = w.version
	prev := w.current
	w.current = cfg
	cb := w.onChange
	w.mu.Unlock()
	cb(prev, cfg)
}
