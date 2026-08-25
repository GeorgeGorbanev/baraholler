package config

import (
	"strings"
	"testing"

	"github.com/GeorgeGorbanev/baraholler/internal/logging"
	"github.com/stretchr/testify/require"
)

func TestDecodeValidStrictConfig(t *testing.T) {
	t.Parallel()

	input := `
telegram:
  api_id: 12345
  api_hash: "secret"
  phone: "+995555123456"
  session_path: ".data/session"
storage:
  state_path: ".data/state.db"
logging:
  level: "debug"
  format: " JSON "
rules:
  - name: "laptop"
    sources: ["https://t.me/baraholka_tbi"]
    terms: ["MacBook"]
    notifications:
      telegram:
        enabled: true
        recipient: "@recipient_name"
      macos:
        enabled: false
`
	cfg, err := Decode(strings.NewReader(input))
	require.NoError(t, err)
	require.Equal(t, 12345, cfg.Telegram.APIID)
	require.Equal(t, "laptop", cfg.Rules[0].Name)
	require.Equal(t, "json", cfg.Logging.Format)
	format, err := cfg.LogFormat()
	require.NoError(t, err)
	require.Equal(t, logging.FormatJSON, format)
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	input := `
telegram:
  api_id: 12345
  api_hash: "secret"
  phone: "+995555123456"
  session_path: ".data/session"
  token: "not-supported"
storage: {state_path: ".data/state.db"}
logging: {level: "info"}
rules: []
`
	_, err := Decode(strings.NewReader(input))
	require.ErrorContains(t, err, "field token not found")
}

func TestValidateForOS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		goos    string
		wantErr string
	}{
		{
			name: "missing API ID",
			mutate: func(cfg *Config) {
				cfg.Telegram.APIID = 0
			},
			goos:    "darwin",
			wantErr: "telegram.api_id",
		},
		{
			name: "invalid recipient",
			mutate: func(cfg *Config) {
				cfg.Rules[0].Notifications.Telegram.Recipient = "@bad"
			},
			goos:    "darwin",
			wantErr: "notifications.telegram.recipient",
		},
		{
			name: "shared session and state path",
			mutate: func(cfg *Config) {
				cfg.Storage.StatePath = cfg.Telegram.SessionPath
			},
			goos:    "darwin",
			wantErr: "storage paths conflict",
		},
		{
			name: "duplicate rule",
			mutate: func(cfg *Config) {
				cfg.Rules = append(cfg.Rules, cfg.Rules[0])
			},
			goos:    "darwin",
			wantErr: "name duplicated",
		},
		{
			name: "duplicate source",
			mutate: func(cfg *Config) {
				cfg.Rules[0].Sources = append(cfg.Rules[0].Sources, "https://t.me/BARAHOLKA_TBI")
			},
			goos:    "darwin",
			wantErr: "source duplicated",
		},
		{
			name: "empty term",
			mutate: func(cfg *Config) {
				cfg.Rules[0].Terms = []string{" "}
			},
			goos:    "darwin",
			wantErr: "terms invalid",
		},
		{
			name: "no notifier",
			mutate: func(cfg *Config) {
				cfg.Rules[0].Notifications.Telegram.Enabled = false
			},
			goos:    "darwin",
			wantErr: "notifications missing",
		},
		{
			name: "macOS on Linux",
			mutate: func(cfg *Config) {
				cfg.Rules[0].Notifications.Telegram.Enabled = false
				cfg.Rules[0].Notifications.MacOS.Enabled = true
			},
			goos:    "linux",
			wantErr: "macos notification unsupported: os=linux",
		},
		{
			name: "invalid source",
			mutate: func(cfg *Config) {
				cfg.Rules[0].Sources = []string{"https://t.me/+private-invite"}
			},
			goos:    "darwin",
			wantErr: "reference invalid: expected one public telegram username",
		},
		{
			name: "invalid logging level",
			mutate: func(cfg *Config) {
				cfg.Logging.Level = "trace"
			},
			goos:    "darwin",
			wantErr: "logging.level",
		},
		{
			name: "invalid logging format",
			mutate: func(cfg *Config) {
				cfg.Logging.Format = "logfmt"
			},
			goos:    "darwin",
			wantErr: "logging.format",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(&cfg)
			require.ErrorContains(t, cfg.ValidateForOS(test.goos), test.wantErr)
		})
	}
}

func TestLogFormatDefaultsToCompact(t *testing.T) {
	t.Parallel()

	format, err := (Config{}).LogFormat()
	require.NoError(t, err)
	require.Equal(t, logging.FormatCompact, format)
}

func TestReferenceValidation(t *testing.T) {
	t.Parallel()

	username, err := SourceUsername("https://t.me/Baraholka_Tbi")
	require.NoError(t, err)
	require.Equal(t, "Baraholka_Tbi", username)

	_, err = SourceUsername("http://t.me/baraholka_tbi")
	require.Error(t, err)

	reference, err := RecipientReference("@recipient_name")
	require.NoError(t, err)
	require.Equal(t, "@recipient_name", reference)
}

func validConfig() Config {
	return Config{
		Telegram: TelegramConfig{
			APIID:       12345,
			APIHash:     "secret",
			Phone:       "+995555123456",
			SessionPath: ".data/session",
		},
		Storage: StorageConfig{StatePath: ".data/state.db"},
		Logging: LoggingConfig{Level: "info"},
		Rules: []Rule{{
			Name:    "laptop",
			Sources: []string{"https://t.me/baraholka_tbi"},
			Terms:   []string{"macbook"},
			Notifications: Notifications{
				Telegram: TelegramNotification{Enabled: true, Recipient: "@recipient_name"},
			},
		}},
	}
}
