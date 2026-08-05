package notifier

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/nlopes/slack"
	"github.com/pkg/errors"
)

func TestSlackRender(t *testing.T) {
	cases := []struct {
		name    string
		input   Notification
		slackf  func(channel, text string, params slack.PostMessageParameters) (string, string, error)
		verifyf func(*slack.PostMessageParameters, error) error
	}{
		{
			name: "good",
			input: Notification{
				Data: models.NotificationData{
					EnvName: "foo-bar",
					Repo:    "foo/bar",
				},
				Template: models.NotificationTemplate{
					Title: "{{ .EnvName }}",
					Sections: []models.NotificationTemplateSection{
						models.NotificationTemplateSection{
							Title: "{{ .EnvName }}",
							Text:  "{{ .Repo }}",
						},
					},
				},
			},
			slackf: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
				return "", "", nil
			},
			verifyf: func(in *slack.PostMessageParameters, err error) error {
				if err != nil {
					return fmt.Errorf("should have succeeded: %w", err)
				}
				if in == nil {
					return errors.New("result is nil")
				}
				if in.Text != "foo-bar" {
					return fmt.Errorf("bad title: %v", in.Text)
				}
				if len(in.Attachments) != 1 {
					return fmt.Errorf("bad attachment count: %v", len(in.Attachments))
				}
				if in.Attachments[0].Title != "foo-bar" {
					return fmt.Errorf("bad section title: %v", in.Attachments[0].Title)
				}
				if in.Attachments[0].Text != "foo/bar" {
					return fmt.Errorf("bad section text: %v", in.Attachments[0].Text)
				}
				return nil
			},
		},
		{
			name: "template error",
			input: Notification{
				Data: models.NotificationData{},
				Template: models.NotificationTemplate{
					Title: "{{ .Invalid }}",
				},
			},
			slackf: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
				return "", "", nil
			},
			verifyf: func(in *slack.PostMessageParameters, err error) error {
				if err == nil {
					return errors.New("should have failed")
				}
				if !strings.Contains(err.Error(), "error rendering template") {
					return fmt.Errorf("unexpected error: %w", err)
				}
				return nil
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := SlackBackend{API: &FakeSlackAPIClient{PostFunc: c.slackf}}
			if err := c.verifyf(s.render(c.input)); err != nil {
				t.Fatalf(err.Error())
			}
		})
	}
}

func TestSlackSend(t *testing.T) {
	cases := []struct {
		name        string
		input       Notification
		users       []string
		channels    []string
		slackf      func(channel, text string, params slack.PostMessageParameters) (string, string, error)
		isErr       bool
		errContains string
	}{
		{
			name: "good",
			input: Notification{
				Data: models.NotificationData{
					EnvName: "foo-bar",
					Repo:    "foo/bar",
				},
				Template: models.NotificationTemplate{
					Title: "{{ .EnvName }}",
					Sections: []models.NotificationTemplateSection{
						models.NotificationTemplateSection{
							Title: "{{ .EnvName }}",
							Text:  "{{ .Repo }}",
						},
					},
				},
			},
			users:    []string{"john", "alice"},
			channels: []string{"engineering"},
			slackf: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
				return "", "", nil
			},
			isErr:       false,
			errContains: "",
		},
		{
			name: "slack error",
			input: Notification{
				Data: models.NotificationData{
					EnvName: "foo-bar",
					Repo:    "foo/bar",
				},
				Template: models.NotificationTemplate{
					Title: "{{ .EnvName }}",
					Sections: []models.NotificationTemplateSection{
						models.NotificationTemplateSection{
							Title: "{{ .EnvName }}",
							Text:  "{{ .Repo }}",
						},
					},
				},
			},
			users:    []string{"john", "alice"},
			channels: []string{"engineering"},
			slackf: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
				return "", "", errors.New("slack error")
			},
			isErr:       true,
			errContains: "slack error",
		},
		{
			name: "slack user error",
			input: Notification{
				Data: models.NotificationData{
					EnvName: "foo-bar",
					Repo:    "foo/bar",
				},
				Template: models.NotificationTemplate{
					Title: "{{ .EnvName }}",
					Sections: []models.NotificationTemplateSection{
						models.NotificationTemplateSection{
							Title: "{{ .EnvName }}",
							Text:  "{{ .Repo }}",
						},
					},
				},
			},
			users:    []string{"john", "alice"},
			channels: []string{"engineering"},
			slackf: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
				if channel == "@alice" {
					return "", "", errors.New("slack error")
				}
				return "", "", nil
			},
			isErr:       true,
			errContains: "slack error",
		},
		{
			name: "render error",
			input: Notification{
				Data: models.NotificationData{},
				Template: models.NotificationTemplate{
					Title: "{{ .Invalid }}",
				},
			},
			users:    []string{"john", "alice"},
			channels: []string{"engineering"},
			slackf: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
				return "", "", nil
			},
			isErr:       true,
			errContains: "error rendering notification",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := SlackBackend{
				Users:    c.users,
				Channels: c.channels,
				API: &FakeSlackAPIClient{
					PostFunc: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
						for _, ch := range c.channels {
							if channel == ch {
								return c.slackf(channel, text, params)
							}
						}
						for _, u := range c.users {
							if channel == "@"+u {
								return c.slackf(channel, text, params)
							}
						}
						return "", "", fmt.Errorf("unknown channel: %v", channel)
					},
				},
			}
			err := s.Send(c.input)
			if err != nil {
				if !c.isErr {
					t.Fatalf("should have succeeded: %v", err)
				}
				if !strings.Contains(err.Error(), c.errContains) {
					t.Fatalf("error missing expected string (%v): %v", c.errContains, err)
				}
			}
		})
	}
}
