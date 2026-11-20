package config

import (
	"fmt"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestDecisionOnlyConfigRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, override string
		enabled        bool
	}{
		{name: "omitted"},
		{name: "explicitly disabled", override: "decision_only_mode: false"},
		{name: "enabled", override: "decision_only_mode: true", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := fmt.Sprintf("version: v0.3\nlisteners: []\nproviders: {}\nrouting: {}\nglobal:\n  router:\n    %s\n", tc.override)
			cfg, err := ParseYAMLBytes([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DecisionOnlyMode != tc.enabled {
				t.Fatalf("loaded decision_only_mode = %v, want %v", cfg.DecisionOnlyMode, tc.enabled)
			}
			exported := CanonicalGlobalFromRouterConfig(cfg)
			encoded, err := yaml.Marshal(exported)
			if err != nil {
				t.Fatal(err)
			}
			var global CanonicalGlobal
			if err := yaml.Unmarshal(encoded, &global); err != nil {
				t.Fatal(err)
			}
			reloaded := &RouterConfig{}
			if err := applyCanonicalGlobal(reloaded, &global); err != nil {
				t.Fatal(err)
			}
			if reloaded.DecisionOnlyMode != tc.enabled {
				t.Fatalf("round-trip decision_only_mode = %v, want %v", reloaded.DecisionOnlyMode, tc.enabled)
			}
		})
	}
}

func TestDecisionOnlyDefaultsDisabled(t *testing.T) {
	if (RouterConfig{}).DecisionOnlyMode || DefaultCanonicalGlobal().Router.DecisionOnlyMode {
		t.Fatal("decision-only must be disabled by default")
	}
	if err := applyCanonicalGlobal(&RouterConfig{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionOnlyConfigWithoutBackends(t *testing.T) {
	cfg, err := ParseYAMLBytes([]byte(`
version: v0.3
listeners: []
providers:
  defaults:
    default_model: Pro
routing:
  modelCards:
    - name: Pro
global:
  router:
    decision_only_mode: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DecisionOnlyMode || cfg.DefaultModel != "Pro" {
		t.Fatal("expected decision-only config with the Pro default type")
	}
	if len(cfg.GetEndpointsForModel("Pro")) != 0 {
		t.Fatal("test configuration must not contain backend endpoints")
	}
}
