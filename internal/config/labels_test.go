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
	p, managed, err := ParsePolicy(validLabels())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !managed {
		t.Fatal("expected managed=true")
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
			p, managed, err := ParsePolicy(cloneWith(tc.mut))
			if err != nil || !managed {
				t.Fatalf("managed=%v err=%v", managed, err)
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
		"no enabled label": {"other": "x"},
		"enabled not true": cloneWith(func(m map[string]string) { m[LabelEnabled] = "1" }),
		"enabled false":    cloneWith(func(m map[string]string) { m[LabelEnabled] = "false" }),
	}
	for name, labels := range cases {
		t.Run(name, func(t *testing.T) {
			_, managed, err := ParsePolicy(labels)
			if managed || err != nil {
				t.Errorf("want managed=false err=nil, got managed=%v err=%v", managed, err)
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
			_, managed, err := ParsePolicy(cloneWith(mut))
			if !managed {
				t.Errorf("want managed=true (opted in but misconfigured)")
			}
			if err == nil {
				t.Errorf("want an error, got nil")
			}
		})
	}
}
