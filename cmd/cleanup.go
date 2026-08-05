package cmd

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/bkeroack/acyl/pkg/eventlogger"
	"github.com/bkeroack/acyl/pkg/nitro/metahelm"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/bkeroack/acyl/pkg/secrets"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// cleanupCmd represents the cleanup command
var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Run data cleanup operations",
	Long:  `Run various database cleanup operations, intended to be executed periodically as a cronjob`,
	PreRun: func(cmd *cobra.Command, args []string) {
		logger = log.New(os.Stderr, "", log.LstdFlags)
		sc, err := getSecretClient()
		if err != nil {
			clierr("error getting secrets client: %v", err)
		}
		sf := secrets.NewPVCSecretsFetcher(sc)
		err = sf.PopulatePG(&pgConfig)
		pgConfig.EnableTracing = true
		if err != nil {
			clierr("error getting db secrets: %v", err)
		}
	},
	Run: cleanup,
}

var k8sMaxage, destroyedenvsMaxage, eventlogsMaxage time.Duration

func init() {
	cleanupCmd.Flags().DurationVar(&k8sMaxage, "k8s-objs-max-age", 14*24*time.Hour, "Maximum age for orphaned k8s objects")
	cleanupCmd.Flags().DurationVar(&destroyedenvsMaxage, "destroyed-envs-max-age", 30*24*time.Hour, "Maximum age for destroyed environment DB records")
	cleanupCmd.Flags().DurationVar(&eventlogsMaxage, "event-logs-max-age", 30*24*time.Hour, "Maximum age for event log DB records")
	RootCmd.AddCommand(cleanupCmd)
}

func cleanup(cmd *cobra.Command, args []string) {
	dl, err := persistence.NewPGLayer(&pgConfig, logger)
	if err != nil {
		log.Fatalf("error opening PG database: %v", err)
	}
	defer dl.Close()

	el := eventlogger.Logger{
		ID:   uuid.Must(uuid.NewRandom()),
		DL:   dl,
		Sink: os.Stderr,
	}

	ctx := context.Background()
	lf := logger.Printf

	if err := el.Init([]byte{}, "", 0); err != nil {
		logger.Printf("error creating eventlog: %v", err)
	} else {
		logger.Printf("event log for cleanup: %v", el.ID.String())
		ctx = eventlogger.NewEventLoggerContext(ctx, &el)
		lf = el.Printf
	}

	cleaner := &persistence.Cleaner{
		DB:                        dl.DB(),
		DestroyedEnvRecordsMaxAge: destroyedenvsMaxage,
		EventLogsMaxAge:           eventlogsMaxage,
		LogFunc:                   lf,
	}

	cleaner.Clean()

	ci, err := metahelm.NewChartInstaller(nil, dl, nil, nil, k8sConfig.GroupBindings, k8sConfig.PrivilegedRepoWhitelist, k8sConfig.SecretInjections, k8sClientConfig.JWTPath, true, helmClientConfig)
	if err != nil {
		log.Fatalf("error getting metahelm chart installer: %v", err)
	}

	ci.Cleanup(ctx, k8sMaxage)
}
