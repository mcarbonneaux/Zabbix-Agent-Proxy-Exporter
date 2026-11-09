package config

import (
	"testing"
	"time"
)

func testConfig() *Config {
	ten := 10150
	tenSec := 10 * time.Second
	return &Config{
		Defaults: Defaults{Port: 10050, Timeout: 5 * time.Second},
		Hosts: map[string]HostConfig{
			"srv-old-01": {},
			"srv-old-02": {Port: &ten, Timeout: &tenSec},
		},
		Modules: map[string]Module{
			"linux": {},
			"mysql": {},
			"other": {},
		},
		Groups: []Group{
			{Name: "tous-linux", Hosts: []string{"srv-old-01", "srv-old-02"}, Modules: []string{"linux"}},
			{Name: "base-de-donnees", Hosts: []string{"srv-old-02"}, Modules: []string{"mysql"}},
		},
	}
}

func TestResolveUnionsModulesAcrossGroups(t *testing.T) {
	cfg := testConfig()

	resolved, err := Resolve(cfg, "srv-old-02", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	modules := map[string]bool{}
	for _, m := range resolved.Modules {
		modules[m] = true
	}
	if !modules["linux"] || !modules["mysql"] || len(modules) != 2 {
		t.Fatalf("got modules %v, want exactly [linux mysql]", resolved.Modules)
	}
}

func TestResolveURLModulesReplaceGroupModules(t *testing.T) {
	cfg := testConfig()

	resolved, err := Resolve(cfg, "srv-old-02", []string{"other"}, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved.Modules) != 1 || resolved.Modules[0] != "other" {
		t.Fatalf("got modules %v, want exactly [other]", resolved.Modules)
	}
}

func TestResolveReturnsErrorWhenNoModuleResolved(t *testing.T) {
	cfg := testConfig()

	_, err := Resolve(cfg, "unknown-host", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when no module resolves for target, got nil")
	}
}

func TestResolvePortTimeoutCascade(t *testing.T) {
	cfg := testConfig()

	// srv-old-01 has no host override -> falls back to defaults.
	resolved, err := Resolve(cfg, "srv-old-01", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Port != 10050 {
		t.Fatalf("got port %d, want default 10050", resolved.Port)
	}
	if resolved.Timeout != 5*time.Second {
		t.Fatalf("got timeout %v, want default 5s", resolved.Timeout)
	}

	// srv-old-02 has a host override -> takes precedence over defaults.
	resolved, err = Resolve(cfg, "srv-old-02", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Port != 10150 {
		t.Fatalf("got port %d, want host override 10150", resolved.Port)
	}
	if resolved.Timeout != 10*time.Second {
		t.Fatalf("got timeout %v, want host override 10s", resolved.Timeout)
	}

	// Explicit URL params take precedence over everything.
	urlPort := 20000
	urlTimeout := 20 * time.Second
	resolved, err = Resolve(cfg, "srv-old-02", nil, &urlPort, &urlTimeout)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Port != 20000 {
		t.Fatalf("got port %d, want URL override 20000", resolved.Port)
	}
	if resolved.Timeout != 20*time.Second {
		t.Fatalf("got timeout %v, want URL override 20s", resolved.Timeout)
	}
}

func TestResolveUsesURLModulesWhenTargetInNoGroups(t *testing.T) {
	cfg := testConfig()

	resolved, err := Resolve(cfg, "brand-new-host-not-in-any-group", []string{"linux"}, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved.Modules) != 1 || resolved.Modules[0] != "linux" {
		t.Fatalf("got modules %v, want exactly [linux]", resolved.Modules)
	}
}
