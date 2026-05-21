package root

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDockerAgentArgs_NoDuplicateArgs is a regression test for a bug where the
// agent file and --config-dir were appended twice, causing the agent file to be
// passed as the first message inside the sandbox.
func TestDockerAgentArgs_NoDuplicateArgs(t *testing.T) {
	cmd := &cobra.Command{
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	var sandboxFlag bool
	cmd.PersistentFlags().BoolVar(&sandboxFlag, "sandbox", false, "")

	args := []string{"./pokemon.yaml"}
	require.NoError(t, cmd.ParseFlags([]string{"--sandbox"}))

	got := dockerAgentArgs(cmd, args, "/some/config/dir")

	// The agent file must appear exactly once.
	count := 0
	for _, a := range got {
		if a == "./pokemon.yaml" {
			count++
		}
	}
	assert.Equal(t, 1, count, "agent file should appear once in args, got: %v", got)

	// --config-dir must appear exactly once.
	configDirCount := 0
	for _, a := range got {
		if a == "--config-dir" {
			configDirCount++
		}
	}
	assert.Equal(t, 1, configDirCount, "--config-dir should appear once in args, got: %v", got)

	// The agent file should come before --config-dir so the cobra run command
	// sees it as the first positional argument (the agent) and not as a message.
	agentIdx := slices.Index(got, "./pokemon.yaml")
	cfgIdx := slices.Index(got, "--config-dir")
	assert.Less(t, agentIdx, cfgIdx, "agent file should precede --config-dir, got: %v", got)

	// --sandbox and --sbx flags must be stripped so we don't recurse into
	// another sandbox.
	assert.NotContains(t, got, "--sandbox")
	assert.NotContains(t, got, "--sbx")

	// --yolo is added by default so tool calls run unattended in the sandbox.
	assert.Contains(t, got, "--yolo")
}

// TestDockerAgentArgs_PreservesUserYolo ensures that if the user explicitly
// set --yolo, it is not duplicated.
func TestDockerAgentArgs_PreservesUserYolo(t *testing.T) {
	cmd := &cobra.Command{
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	var sandboxFlag, yolo bool
	cmd.PersistentFlags().BoolVar(&sandboxFlag, "sandbox", false, "")
	cmd.PersistentFlags().BoolVar(&yolo, "yolo", false, "")

	require.NoError(t, cmd.ParseFlags([]string{"--sandbox", "--yolo"}))

	got := dockerAgentArgs(cmd, []string{"./agent.yaml"}, "/cfg")

	yoloCount := 0
	for _, a := range got {
		if a == "--yolo" {
			yoloCount++
		}
	}
	assert.Equal(t, 1, yoloCount, "--yolo should not be duplicated, got: %v", got)
}

func TestGatewayHostPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"bare host", "example.com", "example.com"},
		{"bare authority", "example.com:443", "example.com:443"},
		{"https URL", "https://example.com/proxy", "example.com"},
		{"https URL with port", "https://example.com:8443/proxy", "example.com:8443"},
		{"production gateway", "https://ai-backend-service.docker.com/proxy", "ai-backend-service.docker.com"},
		{"staging gateway with path", "https://ai-backend-service-stage.docker.com/proxy", "ai-backend-service-stage.docker.com"},
		{"bare authority with path", "example.com:443/proxy", "example.com:443"},
		{"bare authority with query", "example.com:443?foo=bar", "example.com:443"},
		{"protocol-relative authority", "//example.com/proxy", "example.com"},
		{"https URL with userinfo", "https://user:pw@example.com/proxy", "example.com"},
		{"https URL with fragment", "https://example.com/proxy#frag", "example.com"},
		{"IPv6 host", "https://[::1]:8443/proxy", "[::1]:8443"},
		{"scheme without host", "https:///proxy", ""},
		{"only fragment", "#fragment", ""},
		{"only path", "/path", ""},
		{"opaque scheme", "mailto:foo@example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, gatewayHostPort(tt.raw))
		})
	}
}

func TestDisplayGatewayURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"no userinfo", "https://example.com/proxy", "https://example.com/proxy"},
		{"bare authority unchanged", "example.com:443", "example.com:443"},
		{
			name: "username only is masked",
			raw:  "https://user@example.com/proxy",
			want: "https://***@example.com/proxy",
		},
		{
			name: "username and password are masked",
			raw:  "https://user:supersecret@example.com:443/proxy?token=abc",
			want: "https://***@example.com:443/proxy?token=abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := displayGatewayURL(tt.raw)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "supersecret",
				"displayGatewayURL must not preserve a password")
		})
	}
}

func TestPrintModelsGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		gateway string
		want    string
	}{
		{
			name:    "no gateway",
			gateway: "",
			want:    "Models gateway: none configured\n",
		},
		{
			name:    "URL gateway shows allow-listed host",
			gateway: "https://ai-backend-service-stage.docker.com/proxy",
			want:    "Models gateway: https://ai-backend-service-stage.docker.com/proxy (allowlisting ai-backend-service-stage.docker.com in the sandbox proxy)\n",
		},
		{
			name:    "bare authority is its own host",
			gateway: "ai-backend-service.docker.com:443",
			want:    "Models gateway: ai-backend-service.docker.com:443\n",
		},
		{
			name:    "URL with credentials is rendered without them",
			gateway: "https://user:supersecret@gw.example.com/proxy",
			want:    "Models gateway: https://***@gw.example.com/proxy (allowlisting gw.example.com in the sandbox proxy)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			printModelsGateway(&buf, tt.gateway)
			assert.Equal(t, tt.want, buf.String())
			assert.NotContains(t, buf.String(), "supersecret",
				"printed gateway must never include credentials")
		})
	}
}
