package zookeeper

import (
	"testing"
)

func TestParseServersetMember(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		path       string
		wantErr    bool
		wantLabels map[string]string
	}{
		{
			name: "basic member",
			data: `{"serviceEndpoint":{"host":"10.0.0.1","port":8080},"status":"ALIVE","shard":0}`,
			path: "/discovery/service/member_0000000001",
			wantLabels: map[string]string{
				"__address__":                    "10.0.0.1:8080",
				"__meta_serverset_path":          "/discovery/service/member_0000000001",
				"__meta_serverset_status":        "ALIVE",
				"__meta_serverset_shard":         "0",
				"__meta_serverset_endpoint_host": "10.0.0.1",
				"__meta_serverset_endpoint_port": "8080",
			},
		},
		{
			name: "with additional endpoints",
			data: `{"serviceEndpoint":{"host":"10.0.0.2","port":9090},"additionalEndpoints":{"health":{"host":"10.0.0.2","port":9091},"admin-http":{"host":"10.0.0.2","port":9092}},"status":"ALIVE","shard":1}`,
			path: "/discovery/svc/member_0000000002",
			wantLabels: map[string]string{
				"__address__":                                "10.0.0.2:9090",
				"__meta_serverset_path":                      "/discovery/svc/member_0000000002",
				"__meta_serverset_status":                    "ALIVE",
				"__meta_serverset_shard":                     "1",
				"__meta_serverset_endpoint_host":             "10.0.0.2",
				"__meta_serverset_endpoint_port":             "9090",
				"__meta_serverset_endpoint_host_health":      "10.0.0.2",
				"__meta_serverset_endpoint_port_health":      "9091",
				"__meta_serverset_endpoint_host_admin_http":  "10.0.0.2",
				"__meta_serverset_endpoint_port_admin_http":  "9092",
			},
		},
		{
			name:    "invalid json",
			data:    `not json`,
			path:    "/bad",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			labels, err := parseServersetMember([]byte(tc.data), tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := make(map[string]string)
			for _, l := range labels.Labels {
				got[l.Name] = l.Value
			}

			for k, want := range tc.wantLabels {
				if v, ok := got[k]; !ok {
					t.Errorf("missing label %q", k)
				} else if v != want {
					t.Errorf("label %q = %q, want %q", k, v, want)
				}
			}
		})
	}
}

func TestSanitizeLabelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with-dash", "with_dash"},
		{"with.dot", "with_dot"},
		{"with spaces", "with_spaces"},
		{"CamelCase123", "CamelCase123"},
		{"a/b:c", "a_b_c"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := SanitizeLabelName(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeLabelName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
