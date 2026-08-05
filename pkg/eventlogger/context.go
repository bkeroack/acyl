package eventlogger

import (
	"context"
	"log"
	"os"

	"github.com/bkeroack/acyl/pkg/persistence"
)

const (
	elogContextKey = "event_logger"
)

type EventLoggerContextKey string

// NewEventLoggerContext returns a context with the Logger embedded as a value
func NewEventLoggerContext(ctx context.Context, logger *Logger) context.Context {
	return context.WithValue(ctx, EventLoggerContextKey(elogContextKey), logger)
}

// GetLogger returns the Logger if present or a default logger that logs to stdout if not present
func GetLogger(ctx context.Context) *Logger {
	l, ok := ctx.Value(EventLoggerContextKey(elogContextKey)).(*Logger)
	if l == nil || !ok {
		return &Logger{Sink: os.Stdout, DL: persistence.NewFakeDataLayer()}
	}
	return l
}

// StandardLogger returns a log.Logger that writes to l, or a default logger to stdout if l is nil
func StandardLogger(l *Logger) *log.Logger {
	if l == nil {
		return log.New(os.Stdout, "", log.LstdFlags)
	}
	return log.New(&stdLoggerSink{l: l}, "", log.LstdFlags)
}

// stdLoggerSink conforms to io.Writer for a log.Logger
type stdLoggerSink struct {
	l *Logger
}

func (sls *stdLoggerSink) Write(data []byte) (int, error) {
	sls.l.Printf(string(data))
	return len(data), nil
}
