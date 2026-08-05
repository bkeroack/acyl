package persistence

/*
Copypasta from testhelper/localdb to avoid import cycle
*/

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

var testlogger = log.New(ioutil.Discard, "", log.LstdFlags)
var testDataPath = "./testdata/db.json"

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
		cmd := exec.Command("docker", "run", "-d", "--name", "postgres", "-p", "5432:5432", "-e", "POSTGRES_USER=acyl", "-e", "POSTGRES_PASSWORD=acyl", "postgres:9.6")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fail("error encountered while starting postgres: %v: %s\n", err, string(out))
		}
	}
	time.Sleep(5000 * time.Millisecond)
}

// Stop stops the local db server
func (ldb *LocalDB) Stop() {
	if os.Getenv("POSTGRES_ALREADY_RUNNING") == "" {
		ldb.lf("stopping postgres")
		if err := exec.Command("docker", "rm", "-f", "-v", "postgres").Run(); err != nil {
			ldb.lf("error stopping postgres: %v", err)
		}
	}
}

// NewLocalDB returns a new LocalDB object. Run must be called to start the server.
func NewLocalDB(lf LogFunc) *LocalDB {
	return &LocalDB{
		lf: lf,
	}
}

var dltype = "postgres"

func TestMain(m *testing.M) {
	var exit1, exit2 int
	defer func() { os.Exit(exit1 + exit2) }()
	ldb := NewLocalDB(log.Printf)
	ldb.MustRun()
	defer ldb.Stop()
	exit1 = m.Run()
	fmt.Printf("Running fake datalayer tests...\n")
	dltype = "fake"
	exit2 = m.Run()
}
