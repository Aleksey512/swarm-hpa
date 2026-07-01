package config

import "testing"

func validLabels() map[string]string {
	return map[string]string{
		LabelEnabled: "true",
		LabelMin:     "2",
		LabelMax:     "10",
		LabelMetric:  "cpu",
		LabelTarget:  "70",
	}
}

func cloneWith(mut func(map[string]string)) map[string]string {
	m := validLabels()
	if mut != nil {
		mut(m)
	}
	return m
}

func TestParsePolicyValid(t *testing.T) {
	p, autoscale, heal, rebalance, err := ParsePolicy(validLabels())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !autoscale {
		t.Fatal("expected autoscale=true")
	}
	if !heal {
		t.Fatal("expected heal=true (defaults to enabled)")
	}
	if rebalance {
		t.Fatal("expected rebalance=false (defaults off, even when enabled)")
	}
	if !p.Enabled || p.Min != 2 || p.Max != 10 || p.Target != 70 || p.Metric != "cpu" {
		t.Errorf("unexpected policy: %+v", p)
	}
}

func TestParsePolicySourceAndQuery(t *testing.T) {
	cases := []struct {
		name       string
		mut        func(map[string]string)
		wantSource string
		wantQuery  string
	}{
		{
			name:       "defaults: no source, no query",
			mut:        nil,
			wantSource: "",
			wantQuery:  "",
		},
		{
			name:       "explicit dockerstats source",
			mut:        func(m map[string]string) { m[LabelSource] = ProviderDockerStats },
			wantSource: ProviderDockerStats,
		},
		{
			name: "prometheus source with query",
			mut: func(m map[string]string) {
				m[LabelSource] = ProviderPrometheus
				m[LabelQuery] = `sum(rate(http_requests_total{service="web"}[1m]))`
			},
			wantSource: ProviderPrometheus,
			wantQuery:  `sum(rate(http_requests_total{service="web"}[1m]))`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, autoscale, _, _, err := ParsePolicy(cloneWith(tc.mut))
			if err != nil || !autoscale {
				t.Fatalf("autoscale=%v err=%v", autoscale, err)
			}
			if p.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", p.Source, tc.wantSource)
			}
			if p.Query != tc.wantQuery {
				t.Errorf("Query = %q, want %q", p.Query, tc.wantQuery)
			}
		})
	}
}

func TestParsePolicyNotOptedIn(t *testing.T) {
	cases := map[string]map[string]string{
		"no enabled label":             {"other": "x"},
		"enabled not true":             cloneWith(func(m map[string]string) { m[LabelEnabled] = "1" }),
		"enabled false":                cloneWith(func(m map[string]string) { m[LabelEnabled] = "false" }),
		"enabled false + heal false":   {LabelEnabled: "false", LabelHeal: "false"},
		"all opt-ins explicitly false": {LabelEnabled: "false", LabelHeal: "false", LabelRebalance: "false"},
	}
	for name, labels := range cases {
		t.Run(name, func(t *testing.T) {
			_, autoscale, heal, rebalance, err := ParsePolicy(labels)
			if autoscale || heal || rebalance || err != nil {
				t.Errorf("want all opt-ins false, err=nil; got autoscale=%v heal=%v rebalance=%v err=%v",
					autoscale, heal, rebalance, err)
			}
		})
	}
}

func TestParsePolicyInvalid(t *testing.T) {
	cases := map[string]func(map[string]string){
		"missing min":     func(m map[string]string) { delete(m, LabelMin) },
		"missing max":     func(m map[string]string) { delete(m, LabelMax) },
		"missing target":  func(m map[string]string) { delete(m, LabelTarget) },
		"missing metric":  func(m map[string]string) { delete(m, LabelMetric) },
		"min greater max": func(m map[string]string) { m[LabelMin] = "20" },
		"max zero":        func(m map[string]string) { m[LabelMax] = "0"; m[LabelMin] = "0" },
		"target zero":     func(m map[string]string) { m[LabelTarget] = "0" },
		"target negative": func(m map[string]string) { m[LabelTarget] = "-1" },
		"non-numeric min": func(m map[string]string) { m[LabelMin] = "abc" },
		"non-numeric tgt": func(m map[string]string) { m[LabelTarget] = "high" },
		"unknown source":  func(m map[string]string) { m[LabelSource] = "graphite" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, _, _, err := ParsePolicy(cloneWith(mut))
			if err == nil {
				t.Errorf("want an error (opted in but misconfigured), got nil")
			}
		})
	}
}

// TestParsePolicyHeal covers the heal-only opt-in and the heal=false opt-out
// introduced by the swarm.autoscaler.heal label.
func TestParsePolicyHeal(t *testing.T) {
	t.Run("heal-only needs no policy", func(t *testing.T) {
		p, autoscale, heal, _, err := ParsePolicy(map[string]string{LabelHeal: "true"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if autoscale {
			t.Error("autoscale should be false for a heal-only service")
		}
		if !heal {
			t.Error("heal should be true")
		}
		if p.Enabled {
			t.Errorf("policy must be the zero value for heal-only, got %+v", p)
		}
	})

	t.Run("enabled defaults heal to true", func(t *testing.T) {
		_, autoscale, heal, _, err := ParsePolicy(validLabels())
		if err != nil || !autoscale || !heal {
			t.Fatalf("autoscale=%v heal=%v err=%v", autoscale, heal, err)
		}
	})

	t.Run("heal=false opts an autoscaled service out of healing", func(t *testing.T) {
		labels := cloneWith(func(m map[string]string) { m[LabelHeal] = "false" })
		_, autoscale, heal, _, err := ParsePolicy(labels)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !autoscale {
			t.Error("autoscale should stay true")
		}
		if heal {
			t.Error("heal should be false (explicit opt-out)")
		}
	})

	t.Run("enabled + heal=true keeps both", func(t *testing.T) {
		labels := cloneWith(func(m map[string]string) { m[LabelHeal] = "true" })
		_, autoscale, heal, _, err := ParsePolicy(labels)
		if err != nil || !autoscale || !heal {
			t.Fatalf("autoscale=%v heal=%v err=%v", autoscale, heal, err)
		}
	})

	t.Run("unparseable heal value is an error", func(t *testing.T) {
		_, _, _, _, err := ParsePolicy(map[string]string{LabelHeal: "maybe"})
		if err == nil {
			t.Error("want an error for an invalid heal boolean")
		}
	})

	t.Run("invalid autoscaler policy errors even with heal=true", func(t *testing.T) {
		labels := cloneWith(func(m map[string]string) {
			m[LabelHeal] = "true"
			delete(m, LabelMin)
		})
		if _, _, _, _, err := ParsePolicy(labels); err == nil {
			t.Error("want an error when enabled=true but the policy is invalid")
		}
	})
}

// TestParsePolicyRebalance covers the independent swarm.autoscaler.rebalance
// opt-in: off by default, enable-able alone (rebalance-only) or alongside
// autoscale/heal, and defaulting off even for autoscaled services.
func TestParsePolicyRebalance(t *testing.T) {
	t.Run("rebalance-only needs no policy", func(t *testing.T) {
		p, autoscale, heal, rebalance, err := ParsePolicy(map[string]string{LabelRebalance: "true"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if autoscale || heal {
			t.Errorf("rebalance-only must not autoscale or heal, got autoscale=%v heal=%v", autoscale, heal)
		}
		if !rebalance {
			t.Error("rebalance should be true")
		}
		if p.Enabled {
			t.Errorf("policy must be the zero value for rebalance-only, got %+v", p)
		}
	})

	t.Run("defaults off for an autoscaled service", func(t *testing.T) {
		_, _, _, rebalance, err := ParsePolicy(validLabels())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rebalance {
			t.Error("rebalance must default to false even when enabled=true")
		}
	})

	t.Run("enabled + rebalance=true keeps all three", func(t *testing.T) {
		labels := cloneWith(func(m map[string]string) { m[LabelRebalance] = "true" })
		_, autoscale, heal, rebalance, err := ParsePolicy(labels)
		if err != nil || !autoscale || !heal || !rebalance {
			t.Fatalf("autoscale=%v heal=%v rebalance=%v err=%v", autoscale, heal, rebalance, err)
		}
	})

	t.Run("heal-only + rebalance=true", func(t *testing.T) {
		_, autoscale, heal, rebalance, err := ParsePolicy(map[string]string{
			LabelHeal:      "true",
			LabelRebalance: "true",
		})
		if err != nil || autoscale || !heal || !rebalance {
			t.Fatalf("autoscale=%v heal=%v rebalance=%v err=%v", autoscale, heal, rebalance, err)
		}
	})

	t.Run("unparseable rebalance value is an error", func(t *testing.T) {
		_, _, _, _, err := ParsePolicy(map[string]string{LabelRebalance: "sometimes"})
		if err == nil {
			t.Error("want an error for an invalid rebalance boolean")
		}
	})
}
