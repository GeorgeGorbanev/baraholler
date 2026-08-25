package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/term"
)

const (
	colorReset  = "\x1b[0m"
	colorGray   = "\x1b[90m"
	colorCyan   = "\x1b[36m"
	colorYellow = "\x1b[33m"
	colorRed    = "\x1b[31m"
)

type Format string

const (
	FormatCompact Format = "compact"
	FormatJSON    Format = "json"
)

type outputState struct {
	writer io.Writer
	color  bool
	mu     sync.Mutex
}

type boundAttr struct {
	groups []string
	attr   slog.Attr
}

type compactHandler struct {
	output *outputState
	level  slog.Leveler
	attrs  []boundAttr
	groups []string
}

// New creates a logger in the selected output format.
func New(writer io.Writer, level slog.Leveler, format Format) *slog.Logger {
	if level == nil {
		level = slog.LevelInfo
	}
	if format == FormatJSON {
		return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level}))
	}
	return slog.New(newHandler(writer, level, colorEnabled(writer)))
}

func newHandler(writer io.Writer, level slog.Leveler, color bool) slog.Handler {
	return &compactHandler{
		output: &outputState{
			writer: writer,
			color:  color,
		},
		level: level,
	}
}

func colorEnabled(writer io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := writer.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}

func (h *compactHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *compactHandler) Handle(_ context.Context, record slog.Record) error {
	var buffer []byte
	recordTime := record.Time
	if recordTime.IsZero() {
		recordTime = time.Now()
	}
	buffer = recordTime.AppendFormat(buffer, "2006-01-02 15:04:05")
	buffer = append(buffer, ' ')

	label, color := levelDisplay(record.Level)
	if h.output.color {
		buffer = append(buffer, color...)
	}
	buffer = append(buffer, label...)
	if h.output.color {
		buffer = append(buffer, colorReset...)
	}
	buffer = append(buffer, ' ')
	buffer = appendMessage(buffer, record.Message)

	for _, attr := range h.attrs {
		buffer = appendAttr(buffer, attr.groups, attr.attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		buffer = appendAttr(buffer, h.groups, attr)
		return true
	})
	buffer = append(buffer, '\n')

	h.output.mu.Lock()
	defer h.output.mu.Unlock()
	_, err := h.output.writer.Write(buffer)
	return err
}

func (h *compactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = make([]boundAttr, 0, len(h.attrs)+len(attrs))
	clone.attrs = append(clone.attrs, h.attrs...)
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, boundAttr{
			groups: append([]string(nil), h.groups...),
			attr:   attr,
		})
	}
	return &clone
}

func (h *compactHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = make([]string, 0, len(h.groups)+1)
	clone.groups = append(clone.groups, h.groups...)
	clone.groups = append(clone.groups, name)
	return &clone
}

func levelDisplay(level slog.Level) (string, string) {
	switch {
	case level < slog.LevelInfo:
		return "DBG", colorGray
	case level < slog.LevelWarn:
		return "INF", colorCyan
	case level < slog.LevelError:
		return "WRN", colorYellow
	default:
		return "ERR", colorRed
	}
}

func appendMessage(buffer []byte, message string) []byte {
	for _, r := range message {
		switch r {
		case '\n':
			buffer = append(buffer, `\n`...)
		case '\r':
			buffer = append(buffer, `\r`...)
		case '\t':
			buffer = append(buffer, `\t`...)
		default:
			if unicode.IsControl(r) {
				buffer = strconv.AppendQuoteRune(buffer, r)
			} else {
				buffer = append(buffer, string(r)...)
			}
		}
	}
	return buffer
}

func appendAttr(buffer []byte, groups []string, attr slog.Attr) []byte {
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			groups = appendGroup(groups, attr.Key)
		}
		for _, child := range value.Group() {
			buffer = appendAttr(buffer, groups, child)
		}
		return buffer
	}
	if attr.Equal(slog.Attr{}) {
		return buffer
	}

	buffer = append(buffer, ' ')
	buffer = appendKey(buffer, groups, attr.Key)
	buffer = append(buffer, '=')
	return appendValue(buffer, value)
}

func appendGroup(groups []string, name string) []string {
	result := make([]string, 0, len(groups)+1)
	result = append(result, groups...)
	return append(result, name)
}

func appendKey(buffer []byte, groups []string, key string) []byte {
	for _, group := range groups {
		buffer = append(buffer, group...)
		buffer = append(buffer, '.')
	}
	return append(buffer, key...)
}

func appendValue(buffer []byte, value slog.Value) []byte {
	switch value.Kind() {
	case slog.KindString:
		return appendStringValue(buffer, value.String())
	case slog.KindBool:
		return strconv.AppendBool(buffer, value.Bool())
	case slog.KindInt64:
		return strconv.AppendInt(buffer, value.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(buffer, value.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.AppendFloat(buffer, value.Float64(), 'g', -1, 64)
	case slog.KindDuration:
		return append(buffer, value.Duration().String()...)
	case slog.KindTime:
		return value.Time().AppendFormat(buffer, time.RFC3339Nano)
	case slog.KindAny:
		return appendStringValue(buffer, fmt.Sprint(value.Any()))
	default:
		return appendStringValue(buffer, value.String())
	}
}

func appendStringValue(buffer []byte, value string) []byte {
	if value != "" && !strings.ContainsFunc(value, needsQuoting) {
		return append(buffer, value...)
	}
	return strconv.AppendQuote(buffer, value)
}

func needsQuoting(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == '=' || r == '"' || r == '\\'
}
