package localdb

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// LogFunc is a function that performs logging
type LogFunc func(string, ...interface{})

// LocalDB is an object that manages a locally running database
type LocalDB struct {
	lf LogFunc
}

// MustRun starts the local database server or aborts the program with failure
func (ldb *LocalDB) MustRun() {
	if ldb.lf == nil {
		ldb.lf = func(s string, args ...interface{}) {}
	}
	fail := func(msg string, args ...interface{}) {
		fmt.Fprintf(os.Stderr, msg, args...)
		os.Exit(1)
	}
	if os.Getenv("POSTGRES_ALREADY_RUNNING") == "" {
		ldb.lf("starting postgres\n")
		err := exec.Command("docker", "run", "-d", "--name", "postgres", "-p", "5432:5432", "-e", "POSTGRES_USER=acyl", "-e", "POSTGRES_PASSWORD=acyl", "postgres:9.6").Run()
		if err != nil {
			fail("error encountered while starting postgres: %v\n", err)
		}
	}
	time.Sleep(5000 * time.Millisecond)
}

// Stop stops the local db server
func (ldb *LocalDB) Stop() {
	ldb.lf("stopping postgres")
	if err := exec.Command("docker", "rm", "-f", "-v", "postgres").Run(); err != nil {
		ldb.lf("error stopping postgres: %v", err)
	}
}

// New returns a new LocalDB object. Run must be called to start the server.
func New(lf LogFunc) *LocalDB {
	return &LocalDB{
		lf: lf,
	}
}
