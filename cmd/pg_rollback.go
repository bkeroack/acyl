package cmd

import (
	"log"
	"os"

	"github.com/DavidHuie/gomigrate"
	"github.com/bkeroack/acyl/pkg/config"
	"github.com/spf13/cobra"

	"database/sql"

	_ "github.com/lib/pq"
)

var pgRollbackCmd = &cobra.Command{
	Use:   "pg-rollback",
	Short: "rollback last Postgres migration",
	Run:   pgRollback,
}

var rollbackPGConfig config.PGConfig

func init() {
	pgRollbackCmd.PersistentFlags().StringVar(
		&rollbackPGConfig.PostgresURI, "postgres-uri",
		"postgresql://acyl:acyl@localhost:5432?sslmode=disable",
		"the address to the Postgres database",
	)
	pgRollbackCmd.PersistentFlags().StringVar(
		&rollbackPGConfig.PostgresMigrationsPath, "postgres-migrations-dir",
		"migrations",
		"the path to the Postgres migrations directory",
	)
	RootCmd.AddCommand(pgRollbackCmd)
}

func pgRollback(cmd *cobra.Command, args []string) {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	db, err := sql.Open("postgres", rollbackPGConfig.PostgresURI)
	if err != nil {
		logger.Fatalf("error creating postgres connection: %s", err)
	}

	migrator, err := gomigrate.NewMigrator(db, gomigrate.Postgres{},
		rollbackPGConfig.PostgresMigrationsPath)
	if err != nil {
		logger.Fatalf("error creating migrator: %s", err)
	}

	if err := migrator.Rollback(); err != nil {
		logger.Fatalf("error applying migration: %s", err)
	}
}
