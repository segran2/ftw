package drivers

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/srcfl/ftw/go/internal/config"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

func TestMergeAllowedHosts(t *testing.T) {
	cases := []struct {
		name     string
		explicit []string
		cfg      map[string]any
		want     []string
	}{
		{
			name:     "explicit only",
			explicit: []string{"10.0.0.1"},
			cfg:      nil,
			want:     []string{"10.0.0.1"},
		},
		{
			name:     "config.host folded in",
			explicit: nil,
			cfg:      map[string]any{"host": "192.168.1.248"},
			want:     []string{"192.168.1.248"},
		},
		{
			name:     "config.host already listed — no duplicate",
			explicit: []string{"192.168.1.248"},
			cfg:      map[string]any{"host": "192.168.1.248"},
			want:     []string{"192.168.1.248"},
		},
		{
			name:     "config.host adds to explicit list",
			explicit: []string{"10.0.0.1"},
			cfg:      map[string]any{"host": "192.168.1.248"},
			want:     []string{"10.0.0.1", "192.168.1.248"},
		},
		{
			name:     "config.url host extracted",
			explicit: nil,
			cfg:      map[string]any{"url": "http://meter.local:8080/api"},
			want:     []string{"meter.local:8080"},
		},
		{
			name:     "config.host with whitespace trimmed",
			explicit: nil,
			cfg:      map[string]any{"host": "  192.168.1.248  "},
			want:     []string{"192.168.1.248"},
		},
		{
			name:     "non-string host ignored",
			explicit: []string{"10.0.0.1"},
			cfg:      map[string]any{"host": 12345},
			want:     []string{"10.0.0.1"},
		},
		{
			name:     "empty host ignored",
			explicit: nil,
			cfg:      map[string]any{"host": ""},
			want:     []string{},
		},
		{
			name:     "nil cfg returns explicit unchanged",
			explicit: []string{"a", "b"},
			cfg:      nil,
			want:     []string{"a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeAllowedHosts(tc.explicit, tc.cfg)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeAllowedHosts() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TCP gets a tighter default than HTTP/WS: when the driver config supplies
// both host and port, the auto-injected allowlist entry is `host:port`
// (not bare host). Raw TCP can reach any service on the same IP, so
// "P1 reader on :23" must not also grant access to SSH on :22.
func TestTcpAllowedHostsFor(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Driver
		want []string
	}{
		{
			name: "config.host+port → tight host:port entry",
			cfg: config.Driver{
				Capabilities: config.Capabilities{TCP: &config.TCPCapability{}},
				Config:       map[string]any{"host": "192.168.1.40", "port": 23},
			},
			want: []string{"192.168.1.40:23"},
		},
		{
			name: "config.host without port falls back to bare host",
			cfg: config.Driver{
				Capabilities: config.Capabilities{TCP: &config.TCPCapability{}},
				Config:       map[string]any{"host": "192.168.1.40"},
			},
			want: []string{"192.168.1.40"},
		},
		{
			name: "explicit allowlist preserved, config.host:port appended",
			cfg: config.Driver{
				Capabilities: config.Capabilities{TCP: &config.TCPCapability{
					AllowedHosts: []string{"10.0.0.5", "loose-host"},
				}},
				Config: map[string]any{"host": "192.168.1.40", "port": 23},
			},
			want: []string{"10.0.0.5", "loose-host", "192.168.1.40:23"},
		},
		{
			name: "duplicate entry dropped",
			cfg: config.Driver{
				Capabilities: config.Capabilities{TCP: &config.TCPCapability{
					AllowedHosts: []string{"192.168.1.40:23"},
				}},
				Config: map[string]any{"host": "192.168.1.40", "port": 23},
			},
			want: []string{"192.168.1.40:23"},
		},
		{
			name: "yaml-decoded port (int) is honoured",
			cfg: config.Driver{
				Capabilities: config.Capabilities{TCP: &config.TCPCapability{}},
				Config:       map[string]any{"host": "192.168.1.40", "port": int64(2300)},
			},
			want: []string{"192.168.1.40:2300"},
		},
		{
			name: "no config at all returns empty (no implicit any-host)",
			cfg: config.Driver{
				Capabilities: config.Capabilities{TCP: &config.TCPCapability{}},
			},
			want: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tcpAllowedHostsFor(tc.cfg)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("tcpAllowedHostsFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A cloud driver owns its fixed network boundary in DRIVER.http_hosts.
// Existing configs may predate that metadata and therefore carry an empty
// capabilities.http.allowed_hosts. Both ordinary startup and connection
// probes must hydrate the same driver-declared hosts before Lua starts.
func TestRegistryHydratesHTTPHostsFromDriverMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud.lua")
	src := `DRIVER = {
  id = "cloud",
  name = "Cloud",
  read_only = true,
  protocols = { "http" },
  capabilities = { "vehicle" },
  http_hosts = { "api.example.test", " identity.example.test ", "api.example.test" },
}
function driver_init(config)
  local r, err = host.http_request{url = "https://not-declared.invalid/data"}
  assert(r == nil, "unexpected request")
  assert(err and err:find("not in allowed_hosts", 1, true),
    "driver-declared allowlist was not installed: " .. tostring(err))
end
function driver_poll() return 60000 end
`
	if err := os.WriteFile(path, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		add  func(*Registry, context.Context, config.Driver) error
		stop func(*Registry, string)
	}{
		{
			name: "startup",
			add: func(r *Registry, ctx context.Context, cfg config.Driver) error {
				return r.Add(ctx, cfg)
			},
			stop: func(r *Registry, name string) { r.Remove(name) },
		},
		{
			name: "probe",
			add: func(r *Registry, ctx context.Context, cfg config.Driver) error {
				return r.AddProbe(ctx, cfg)
			},
			stop: func(r *Registry, name string) { r.RemoveProbe(name) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry(telemetry.NewStore())
			cfg := config.Driver{
				Name: "cloud-" + tc.name,
				Lua:  path,
				Capabilities: config.Capabilities{
					HTTP: &config.HTTPCapability{},
				},
			}
			if err := tc.add(r, context.Background(), cfg); err != nil {
				t.Fatalf("add with DRIVER.http_hosts: %v", err)
			}
			t.Cleanup(func() { tc.stop(r, cfg.Name) })

			r.mu.Lock()
			rd := r.rec[cfg.Name]
			r.mu.Unlock()
			if rd == nil {
				t.Fatal("driver was not registered")
			}
			want := []string{"api.example.test", "identity.example.test"}
			if !reflect.DeepEqual(rd.env.HTTPAllowedHosts, want) {
				t.Fatalf("HTTPAllowedHosts = %v, want %v", rd.env.HTTPAllowedHosts, want)
			}
		})
	}
}
