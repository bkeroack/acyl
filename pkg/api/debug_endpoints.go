package api

import (
	"fmt"
	"net/http/pprof"

	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
)

type debugEndpoints struct {
}

func newDebugEndpoints() *debugEndpoints {
	return &debugEndpoints{}
}

func (de *debugEndpoints) register(r *muxtrace.Router) error {
	if r == nil {
		return fmt.Errorf("router is nil")
	}
	r.HandleFunc("/debug/pprof/", middlewareChain(pprof.Index, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/block", middlewareChain(pprof.Index, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/goroutine", middlewareChain(pprof.Index, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/heap", middlewareChain(pprof.Index, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/mutex", middlewareChain(pprof.Index, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/threadcreate", middlewareChain(pprof.Index, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/cmdline", middlewareChain(pprof.Cmdline, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/profile", middlewareChain(pprof.Profile, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/symbol", middlewareChain(pprof.Symbol, ipWhitelistMiddleware.checkIPWhitelist))
	r.HandleFunc("/debug/pprof/trace", middlewareChain(pprof.Trace, ipWhitelistMiddleware.checkIPWhitelist))
	return nil
}
