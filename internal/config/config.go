package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/GeorgeGorbanev/baraholler/internal/logging"
	"go.yaml.in/yaml/v3"
)

var (
	phonePattern    = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)
)

type Config struct {
	Telegram TelegramConfig `yaml:"telegram"`
	Storage  StorageConfig  `yaml:"storage"`
	Logging  LoggingConfig  `yaml:"logging"`
	Rules    []Rule         `yaml:"rules"`
}

type TelegramConfig struct {
	APIID       int    `yaml:"api_id"`
	APIHash     string `yaml:"api_hash"`
	Phone       string `yaml:"phone"`
	SessionPath string `yaml:"session_path"`
}

type StorageConfig struct {
	StatePath string `yaml:"state_path"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type Rule struct {
	Name          string        `yaml:"name"`
	Sources       []string      `yaml:"sources"`
	Terms         []string      `yaml:"terms"`
	Notifications Notifications `yaml:"notifications"`
}

type Notifications struct {
	Telegram TelegramNotification `yaml:"telegram"`
	MacOS    MacOSNotification    `yaml:"macos"`
}

type TelegramNotification struct {
	Enabled   bool   `yaml:"enabled"`
	Recipient string `yaml:"recipient"`
}

type MacOSNotification struct {
	Enabled bool `yaml:"enabled"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("config open failed: %w", err)
	}
	defer file.Close()

	cfg, err := Decode(file)
	if err != nil {
		return Config{}, fmt.Errorf("config decode failed: path=%q: %w", path, err)
	}
	return cfg, nil
}

func Decode(reader io.Reader) (Config, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("config invalid: multiple yaml documents unsupported")
		}
		return Config{}, err
	}

	cfg.Normalize()
	if err := cfg.ValidateForOS(runtime.GOOS); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Normalize() {
	c.Telegram.APIHash = strings.TrimSpace(c.Telegram.APIHash)
	c.Telegram.Phone = strings.TrimSpace(c.Telegram.Phone)
	c.Telegram.SessionPath = strings.TrimSpace(c.Telegram.SessionPath)
	c.Storage.StatePath = strings.TrimSpace(c.Storage.StatePath)
	c.Logging.Level = strings.ToLower(strings.TrimSpace(c.Logging.Level))
	c.Logging.Format = strings.ToLower(strings.TrimSpace(c.Logging.Format))
	for i := range c.Rules {
		rule := &c.Rules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		for j := range rule.Sources {
			rule.Sources[j] = strings.TrimSpace(rule.Sources[j])
		}
		for j := range rule.Terms {
			rule.Terms[j] = strings.TrimSpace(rule.Terms[j])
		}
		rule.Notifications.Telegram.Recipient = strings.TrimSpace(rule.Notifications.Telegram.Recipient)
	}
}

func (c Config) ValidateForOS(goos string) error {
	var problems []error
	if c.Telegram.APIID <= 0 {
		problems = append(problems, errors.New("telegram.api_id invalid: expected a value greater than zero"))
	}
	if strings.TrimSpace(c.Telegram.APIHash) == "" {
		problems = append(problems, errors.New("telegram.api_hash missing"))
	}
	if !phonePattern.MatchString(strings.TrimSpace(c.Telegram.Phone)) {
		problems = append(problems, errors.New("telegram.phone invalid: expected e.164 format such as +995555123456"))
	}
	if strings.TrimSpace(c.Telegram.SessionPath) == "" {
		problems = append(problems, errors.New("telegram.session_path missing"))
	}
	if strings.TrimSpace(c.Storage.StatePath) == "" {
		problems = append(problems, errors.New("storage.state_path missing"))
	}
	if c.Telegram.SessionPath != "" && c.Storage.StatePath != "" && filepath.Clean(c.Telegram.SessionPath) == filepath.Clean(c.Storage.StatePath) {
		problems = append(problems, errors.New("storage paths conflict: telegram.session_path and storage.state_path reference the same file"))
	}
	if _, err := c.LogLevel(); err != nil {
		problems = append(problems, err)
	}
	if _, err := c.LogFormat(); err != nil {
		problems = append(problems, err)
	}
	if len(c.Rules) == 0 {
		problems = append(problems, errors.New("rules missing"))
	}

	names := make(map[string]struct{}, len(c.Rules))
	for i, rule := range c.Rules {
		prefix := fmt.Sprintf("rules[%d]", i)
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			problems = append(problems, fmt.Errorf("%s.name missing", prefix))
		} else if _, exists := names[name]; exists {
			problems = append(problems, fmt.Errorf("%s.name duplicated: %q", prefix, name))
		} else {
			names[name] = struct{}{}
		}

		if len(rule.Sources) == 0 {
			problems = append(problems, fmt.Errorf("%s.sources missing", prefix))
		}
		seenSources := make(map[string]struct{}, len(rule.Sources))
		for _, source := range rule.Sources {
			canonical, err := SourceUsername(source)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s source invalid: value=%q: %w", prefix, source, err))
				continue
			}
			canonical = strings.ToLower(canonical)
			if _, exists := seenSources[canonical]; exists {
				problems = append(problems, fmt.Errorf("%s source duplicated: value=%q", prefix, source))
			}
			seenSources[canonical] = struct{}{}
		}

		if len(rule.Terms) == 0 {
			problems = append(problems, fmt.Errorf("%s.terms missing", prefix))
		}
		for _, term := range rule.Terms {
			if strings.TrimSpace(term) == "" {
				problems = append(problems, fmt.Errorf("%s.terms invalid: empty value", prefix))
			}
		}

		telegramEnabled := rule.Notifications.Telegram.Enabled
		macEnabled := rule.Notifications.MacOS.Enabled
		if !telegramEnabled && !macEnabled {
			problems = append(problems, fmt.Errorf("%s notifications missing", prefix))
		}
		if telegramEnabled {
			if _, err := RecipientReference(rule.Notifications.Telegram.Recipient); err != nil {
				problems = append(problems, fmt.Errorf("%s.notifications.telegram.recipient invalid: %w", prefix, err))
			}
		}
		if macEnabled && goos != "darwin" {
			problems = append(problems, fmt.Errorf("%s macos notification unsupported: os=%s", prefix, goos))
		}
	}

	return errors.Join(problems...)
}

func (c Config) LogLevel() (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(c.Logging.Level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging.level invalid: expected debug, info, warn, or error")
	}
}

func (c Config) LogFormat() (logging.Format, error) {
	switch strings.ToLower(strings.TrimSpace(c.Logging.Format)) {
	case "", string(logging.FormatCompact):
		return logging.FormatCompact, nil
	case string(logging.FormatJSON):
		return logging.FormatJSON, nil
	default:
		return "", fmt.Errorf("logging.format invalid: expected compact or json")
	}
}

func SourceUsername(reference string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(reference))
	if err != nil {
		return "", errors.New("reference invalid: expected a public https://t.me/username link")
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "t.me") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("reference invalid: expected a public https://t.me/username link")
	}
	username := strings.Trim(parsed.Path, "/")
	if strings.Contains(username, "/") || !usernamePattern.MatchString(username) {
		return "", errors.New("reference invalid: expected one public telegram username")
	}
	return username, nil
}

func RecipientReference(reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if strings.HasPrefix(reference, "@") {
		username := strings.TrimPrefix(reference, "@")
		if !usernamePattern.MatchString(username) {
			return "", errors.New("recipient reference invalid: expected @username or a public https://t.me/username link")
		}
		return "@" + username, nil
	}
	username, err := SourceUsername(reference)
	if err != nil {
		return "", errors.New("recipient reference invalid: expected @username or a public https://t.me/username link")
	}
	return "https://t.me/" + username, nil
}
