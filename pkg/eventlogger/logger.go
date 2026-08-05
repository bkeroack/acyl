package eventlogger

import (
	"fmt"
	"io"
	"time"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// Logger is an object that writes log lines to the database in an EventLog as well as to Sink
type Logger struct {
	DL             persistence.EventLoggerDataLayer
	ID, DeliveryID uuid.UUID
	Sink           io.Writer
	// ExcludeID determines whether to omit the ID from log strings
	ExcludeID bool
}

// Init initializes the EventLog object in the database. This must be called exactly once prior to any log lines.
// This method will persist the EventLog in the database with a null environment name
func (l *Logger) Init(webhook []byte, repo string, pr uint) error {
	if l.DL == nil {
		return errors.New("datalayer is nil")
	}
	el := &models.EventLog{
		ID:               l.ID,
		WebhookPayload:   webhook,
		GitHubDeliveryID: l.DeliveryID,
		Repo:             repo,
		PullRequest:      pr,
	}
	return errors.Wrap(l.DL.CreateEventLog(el), "error creating event log")
}

// SetEnvName sets the environment name for this logger. Init must be called first.
func (l *Logger) SetEnvName(name string) error {
	if l.DL != nil {
		return l.DL.SetEventLogEnvName(l.ID, name)
	}
	return nil
}

// Printf writes the formatted log line to the underlying Logger
func (l *Logger) Printf(msg string, params ...interface{}) {
	var idstr string
	if !l.ExcludeID {
		idstr = " event: " + l.ID.String()
	}
	msg = fmt.Sprintf(time.Now().UTC().Format("2006/01/02 15:04:05")+idstr+": "+msg+"\n", params...)
	if l.DL != nil && l.ID != uuid.Nil {
		if err := l.DL.AppendToEventLog(l.ID, msg); err != nil {
			if l.Sink != nil {
				l.Sink.Write([]byte(errors.Wrap(err, "error appending line to event log").Error()))
			}
		}
	}
	if l.Sink != nil {
		l.Sink.Write([]byte(msg))
	}
}
