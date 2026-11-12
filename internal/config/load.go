package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Load reads and validates the exporter configuration from path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if cfg.Defaults.Protocol == "" {
		cfg.Defaults.Protocol = ProtocolLegacy
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return &cfg, nil
}

func validate(cfg *Config) error {
	for name, module := range cfg.Modules {
		for _, item := range module.Items {
			if item.Type != MetricTypeGauge && item.Type != MetricTypeCounter {
				return fmt.Errorf("module %q item %q: invalid type %q (want gauge or counter)", name, item.Key, item.Type)
			}
		}
		for _, di := range module.DiscoveredItems {
			if di.Type != MetricTypeGauge && di.Type != MetricTypeCounter {
				return fmt.Errorf("module %q discovered item %q: invalid type %q (want gauge or counter)", name, di.DiscoveryKey, di.Type)
			}
		}
	}
	seenGroupNames := make(map[string]bool)
	for _, group := range cfg.Groups {
		if seenGroupNames[group.Name] {
			return fmt.Errorf("duplicate group name %q", group.Name)
		}
		seenGroupNames[group.Name] = true

		for _, moduleName := range group.Modules {
			if _, ok := cfg.Modules[moduleName]; !ok {
				return fmt.Errorf("group %q references unknown module %q", group.Name, moduleName)
			}
		}
	}

	if !isValidProtocol(cfg.Defaults.Protocol) {
		return fmt.Errorf("defaults: invalid protocol %q (want plain, legacy, or json)", cfg.Defaults.Protocol)
	}
	for name, host := range cfg.Hosts {
		if host.Protocol != nil && !isValidProtocol(*host.Protocol) {
			return fmt.Errorf("host %q: invalid protocol %q (want plain, legacy, or json)", name, *host.Protocol)
		}
	}
	return nil
}

func isValidProtocol(p Protocol) bool {
	return p == ProtocolPlain || p == ProtocolLegacy || p == ProtocolJSON
}

// Watch monitors path for changes and invokes onChange with the newly
// loaded config whenever the file is modified. onChange receives a non-nil
// error (and a nil config) if the new file fails to load or validate, or if
// the underlying watcher itself reports an error; the caller is expected to
// keep using its last-known-good config in that case. A single logical save
// (e.g. an editor's write-temp-then-rename sequence) may trigger onChange
// more than once, including a transient call with a spurious error if an
// intermediate filesystem state is momentarily unparseable — callers must
// tolerate this rather than treating every error call as actionable.
// Watch returns a stop function that releases the watcher; stop does not
// wait for any in-flight onChange call to finish before returning.
func Watch(path string, onChange func(cfg *Config, err error)) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: create watcher: %w", err)
	}
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("config: watch %s: %w", path, err)
	}

	done := make(chan struct{})
	go func() {
		absPath, _ := filepath.Abs(path)
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				eventAbsPath, _ := filepath.Abs(event.Name)
				if eventAbsPath != absPath {
					continue
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				cfg, loadErr := Load(path)
				onChange(cfg, loadErr)
			case watchErr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				onChange(nil, fmt.Errorf("config: watcher error for %s: %w", path, watchErr))
			case <-done:
				return
			}
		}
	}()

	stop = func() {
		close(done)
		watcher.Close()
	}
	return stop, nil
}
