package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecuteHelp(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer

	require.NoError(t, execute(t.Context(), []string{"--help"}, strings.NewReader(""), &output, &errorOutput))
	require.Contains(t, output.String(), "baraholler check-config")
	require.Empty(t, errorOutput.String())
}

func TestExecuteVersion(t *testing.T) {
	originalVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = originalVersion })

	var output bytes.Buffer
	require.NoError(t, execute(t.Context(), []string{"version"}, strings.NewReader(""), &output, &bytes.Buffer{}))
	require.Equal(t, "baraholler v1.2.3\n", output.String())
}

func TestExecuteCheckConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	contents := `telegram:
  api_id: 12345
  api_hash: "secret"
  phone: "+995555123456"
  session_path: ".data/telegram.session"
storage:
  state_path: ".data/baraholler.db"
logging:
  level: "info"
rules:
  - name: "laptop"
    sources: ["https://t.me/sample_marketplace"]
    terms: ["macbook"]
    notifications:
      telegram:
        enabled: true
        recipient: "@recipient_username"
      macos:
        enabled: false
`
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))

	var output bytes.Buffer
	err := execute(
		t.Context(),
		[]string{"check-config", "-config", configPath},
		strings.NewReader(""),
		&output,
		&bytes.Buffer{},
	)
	require.NoError(t, err)
	require.Contains(t, output.String(), "is valid")
}

func TestExecuteSubcommandHelpReturnsFlagHelp(t *testing.T) {
	var errorOutput bytes.Buffer
	err := execute(
		t.Context(),
		[]string{"check-config", "--help"},
		strings.NewReader(""),
		&bytes.Buffer{},
		&errorOutput,
	)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, errorOutput.String(), "-config")
}

func TestExecuteRejectsUnknownCommand(t *testing.T) {
	var errorOutput bytes.Buffer
	err := execute(
		t.Context(),
		[]string{"unknown"},
		strings.NewReader(""),
		&bytes.Buffer{},
		&errorOutput,
	)
	require.ErrorContains(t, err, `command unknown: "unknown"`)
	require.Contains(t, errorOutput.String(), "Usage:")
}
