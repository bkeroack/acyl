package api

import (
	"log"
	"os"
	"testing"

	"github.com/bkeroack/acyl/pkg/testhelper/localdb"
)

func TestMain(m *testing.M) {
	var exit int
	defer func() { os.Exit(exit) }()
	if os.Getenv("ACYL_NO_DB") == "" {
		ldb := localdb.New(log.Printf)
		ldb.MustRun()
		defer ldb.Stop()
	}
	exit = m.Run()
}
