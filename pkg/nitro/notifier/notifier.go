package notifier

import (
	"github.com/bkeroack/acyl/pkg/models"
	"golang.org/x/sync/errgroup"
)

//go:generate stringer -type=NotificationEvent

// NotificationEvent models events that trigger notifications
type NotificationEvent int

const (
	// CreateEnvironment occurs when an environment is created
	CreateEnvironment NotificationEvent = iota
	// UpdateEnvironment occurs when an environment is updated
	UpdateEnvironment
	// DestroyEnvironment occurs when an environment is destroyed
	DestroyEnvironment
	// Success occurs when a create or update operation succeeds
	Success
	// Failure occurs when a create or update operation fails
	Failure
	// EnvironmentLimitExceeded occurs when an environment is destroyed due to the environment limit
	EnvironmentLimitExceeded
)

// Key maps NotificationEvents to notification template names
func (ne NotificationEvent) Key() string {
	switch ne {
	case CreateEnvironment:
		return "create"
	case UpdateEnvironment:
		return "update"
	case DestroyEnvironment:
		return "destroy"
	case Success:
		return "success"
	case Failure:
		return "failure"
	case EnvironmentLimitExceeded:
		return "destroy"
	default:
		return "<unknown>"
	}
}

// Notification models an event notification
type Notification struct {
	Event    NotificationEvent
	Template models.NotificationTemplate
	Data     models.NotificationData
}

// Backend describes an object that can send a notification somewhere
type Backend interface {
	Send(n Notification) error
}

// Router describes an object that routes notifications
type Router interface {
	FanOut(n Notification) error
}

// MultiRouter is an object that routes notifications to all configured backends
type MultiRouter struct {
	Backends []Backend
}

// FanOut distributes n to all configured backends concurrently
func (mr *MultiRouter) FanOut(n Notification) error {
	g := errgroup.Group{}
	for _, be := range mr.Backends {
		be := be
		g.Go(func() error {
			return be.Send(n)
		})
	}
	return g.Wait()
}
