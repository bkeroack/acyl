package notifier

import (
	"os"
	"strings"
	"testing"

	"github.com/bkeroack/acyl/pkg/models"
)

func TestTerminalSend(t *testing.T) {
	margin := 60
	cases := []struct {
		name        string
		input       Notification
		isErr       bool
		errContains string
	}{
		{
			name: "good",
			input: Notification{
				Data: models.NotificationData{
					EnvName:      "random-name",
					Repo:         "acme/widgets",
					PullRequest:  34,
					SourceBranch: "spam-customers",
					BaseBranch:   "master",
				},
				Template: models.NotificationTemplate{
					Title: "🛠 Creating Environment",
					Sections: []models.NotificationTemplateSection{
						models.NotificationTemplateSection{
							Title: "{{ .EnvName }}",
							Text:  "{{ .Repo }}\nPR #{{ .PullRequest }}: {{ .SourceBranch }} ➡️ {{ .BaseBranch }}",
							Style: "good",
						},
						// long line
						models.NotificationTemplateSection{
							Text:  "Lorem Ipsum is simply dummy text of the printing and typesetting industry. Lorem Ipsum is simply dummy text of the printing and typesetting industry. Lorem Ipsum is simply dummy text of the printing and typesetting industry. Lorem Ipsum has been the industry's standard dummy",
							Style: "warning",
						},
						// exact margin
						models.NotificationTemplateSection{
							Text:  strings.Repeat("a", margin-2),
							Style: "danger",
						},
					},
				},
			},
			isErr:       false,
			errContains: "",
		},
		{
			name: "render error",
			input: Notification{
				Data: models.NotificationData{},
				Template: models.NotificationTemplate{
					Title: "{{ .Invalid }}",
				},
			},
			isErr:       true,
			errContains: "error rendering template",
		},
		{
			name: "default create",
			input: Notification{
				Data: models.NotificationData{
					EnvName:      "random-name",
					Repo:         "acme/widgets",
					PullRequest:  34,
					SourceBranch: "spam-customers",
					BaseBranch:   "master",
				},
				Template: models.DefaultNotificationTemplates["create"],
			},
			isErr:       false,
			errContains: "",
		},
		{
			name: "default update",
			input: Notification{
				Data: models.NotificationData{
					EnvName:       "random-name",
					Repo:          "acme/widgets",
					PullRequest:   34,
					SourceBranch:  "spam-customers",
					BaseBranch:    "master",
					CommitMessage: "some change to the code that probably didn't break anything",
				},
				Template: models.DefaultNotificationTemplates["update"],
			},
			isErr:       false,
			errContains: "",
		},
		{
			name: "default destroy",
			input: Notification{
				Data: models.NotificationData{
					EnvName:      "random-name",
					Repo:         "acme/widgets",
					PullRequest:  34,
					SourceBranch: "spam-customers",
					BaseBranch:   "master",
				},
				Template: models.DefaultNotificationTemplates["destroy"],
			},
			isErr:       false,
			errContains: "",
		},
		{
			name: "default destroy (env limit exceeded)",
			input: Notification{
				Data: models.NotificationData{
					EnvName:      "random-name",
					Repo:         "acme/widgets",
					PullRequest:  34,
					SourceBranch: "spam-customers",
					BaseBranch:   "master",
					Event:        EnvironmentLimitExceeded.String(),
				},
				Template: models.DefaultNotificationTemplates["destroy"],
			},
			isErr:       false,
			errContains: "",
		},
		{
			name: "default success",
			input: Notification{
				Data: models.NotificationData{
					EnvName:      "random-name",
					Repo:         "acme/widgets",
					PullRequest:  34,
					SourceBranch: "spam-customers",
					BaseBranch:   "master",
				},
				Template: models.DefaultNotificationTemplates["success"],
			},
			isErr:       false,
			errContains: "",
		},
		{
			name: "default failure",
			input: Notification{
				Data: models.NotificationData{
					EnvName:      "random-name",
					Repo:         "acme/widgets",
					PullRequest:  34,
					SourceBranch: "spam-customers",
					BaseBranch:   "master",
					ErrorMessage: "some error happened",
				},
				Template: models.DefaultNotificationTemplates["failure"],
			},
			isErr:       false,
			errContains: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			term := &TerminalBackend{
				Output: os.Stderr,
				Margin: uint(margin),
			}
			err := term.Send(c.input)
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
