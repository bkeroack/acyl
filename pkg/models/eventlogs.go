package models

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dollarshaveclub/metahelm/pkg/metahelm"
	guuid "github.com/gofrs/uuid"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/pkg/errors"
)

type EventLog struct {
	ID               uuid.UUID          `json:"id"`
	Created          time.Time          `json:"created"`
	Updated          pq.NullTime        `json:"updated"`
	EnvName          string             `json:"env_name"`
	Repo             string             `json:"repo"`
	PullRequest      uint               `json:"pull_request"`
	WebhookPayload   []byte             `json:"webhook_payload"`
	GitHubDeliveryID uuid.UUID          `json:"github_delivery_id"`
	Log              []string           `json:"log"`
	LogKey           uuid.UUID          `json:"log_key"` // additional secret needed for logs via web UI (no API key)
	Status           EventStatusSummary `json:"status"`
}

func (el EventLog) Columns() string {
	return strings.Join([]string{"id", "created", "updated", "env_name", "repo", "pull_request", "webhook_payload", "github_delivery_id", "log", "log_key", "status"}, ",")
}

func (el EventLog) ColumnsWithoutID() string {
	return strings.Join([]string{"created", "updated", "env_name", "repo", "pull_request", "webhook_payload", "github_delivery_id", "log", "log_key", "status"}, ",")
}

func (el EventLog) InsertColumns() string {
	return strings.Join([]string{"id", "env_name", "repo", "pull_request", "webhook_payload", "github_delivery_id", "log", "log_key", "status"}, ",")
}

func (el *EventLog) ScanValues() []interface{} {
	return []interface{}{&el.ID, &el.Created, &el.Updated, &el.EnvName, &el.Repo, &el.PullRequest, &el.WebhookPayload, &el.GitHubDeliveryID, pq.Array(&el.Log), &el.LogKey, &el.Status}
}

func (el *EventLog) InsertValues() []interface{} {
	return []interface{}{&el.ID, &el.EnvName, &el.Repo, &el.PullRequest, &el.WebhookPayload, &el.GitHubDeliveryID, pq.Array(&el.Log), &el.LogKey, &el.Status}
}

func (el EventLog) InsertParams() string {
	params := []string{}
	for i := range strings.Split(el.InsertColumns(), ",") {
		params = append(params, fmt.Sprintf("$%v", i+1))
	}
	return strings.Join(params, ", ")
}

//go:generate stringer -type=NodeChartStatus
//go:generate stringer -type=EventStatus
//go:generate stringer -type=EventStatusType

type NodeChartStatus int

const (
	UnknownChartStatus NodeChartStatus = iota
	WaitingChartStatus
	InstallingChartStatus
	UpgradingChartStatus
	DoneChartStatus
	FailedChartStatus
)

type EventStatus int

const (
	UnknownEventStatus EventStatus = iota
	PendingStatus
	DoneStatus
	FailedStatus
	CancelledStatus
)

type EventStatusType int

const (
	UnknownEventStatusType EventStatusType = iota
	CreateEvent
	UpdateEvent
	DestroyEvent
)

// adapted from https://stackoverflow.com/questions/48050945/how-to-unmarshal-json-into-durations

type ConfigProcessingDuration struct {
	time.Duration
}

func (d ConfigProcessingDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *ConfigProcessingDuration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return errors.Wrap(err, "error unmarshaling duration")
	}
	switch value := v.(type) {
	case float64:
		d.Duration = time.Duration(value)
		return nil
	case string:
		var err error
		if strings.Contains(value, `"`) {
			value, err = strconv.Unquote(value)
			if err != nil {
				return errors.Wrap(err, "error unquoting value")
			}
		}
		d.Duration, err = time.ParseDuration(value)
		if err != nil {
			return errors.Wrap(err, "error parsing duration")
		}
		return nil
	default:
		return errors.New("invalid duration")
	}
}

type RenderedEventStatus struct {
	Description   string `json:"description"`
	LinkTargetURL string `json:"link_target_url"`
}

type EventStatusSummaryConfig struct {
	Type            EventStatusType          `json:"type"`
	Status          EventStatus              `json:"status"`
	RenderedStatus  RenderedEventStatus      `json:"rendered_status"`
	FailedResources metahelm.ChartError      `json:"failed_resources"`
	EnvName         string                   `json:"env_name"`
	K8sNamespace    string                   `json:"k8s_ns"`
	TriggeringRepo  string                   `json:"triggering_repo"`
	PullRequest     uint                     `json:"pull_request"`
	GitHubUser      string                   `json:"github_user"`
	Branch          string                   `json:"branch"`
	Revision        string                   `json:"revision"`
	ProcessingTime  ConfigProcessingDuration `json:"processing_time"`
	Started         time.Time                `json:"started"`
	Completed       time.Time                `json:"completed"`
	RefMap          map[string]string        `json:"ref_map"`
}

type EventStatusTreeNodeImage struct {
	Name      string     `json:"name"`
	ID        guuid.UUID `json:"id"`
	Error     bool       `json:"error"`
	Completed time.Time  `json:"completed"`
	Started   time.Time  `json:"started"`
}

type EventStatusTreeNodeChart struct {
	Status    NodeChartStatus `json:"status"`
	Started   time.Time       `json:"started"`
	Completed time.Time       `json:"completed"`
}

type EventStatusTreeNode struct {
	Parent string                   `json:"parent"`
	Image  EventStatusTreeNodeImage `json:"image"`
	Chart  EventStatusTreeNodeChart `json:"chart"`
}

type EventStatusSummary struct {
	Config EventStatusSummaryConfig       `json:"config"`
	Tree   map[string]EventStatusTreeNode `json:"tree"`
}

// Value implements database/sql/driver Valuer interface.
func (es EventStatusSummary) Value() (driver.Value, error) {
	return json.Marshal(es)
}

// Scan implements database/sql Scanner interface.
func (es *EventStatusSummary) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("unexpected type for value: %T (wanted []byte)", value)
	}
	return json.Unmarshal(b, &es)
}

// check interfaces
var (
	_ driver.Valuer = EventStatusSummary{}
	_ sql.Scanner   = &EventStatusSummary{}
)
