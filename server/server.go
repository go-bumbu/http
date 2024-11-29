package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Server struct {
	server    http.Server
	obsServer *http.Server
	logger    func(msg string, isErr bool)
}

type Cfg struct {
	Addr       string
	Handler    http.Handler
	SkipObs    bool
	ObsAddr    string
	ObsHandler http.Handler
	Logger     func(msg string, isErr bool)
}

// New creates a new sever instance that can be started individually
func New(cfg Cfg) (*Server, error) {

	if cfg.Addr == "" {
		return nil, fmt.Errorf("server address canot be empty")

	}
	if !cfg.SkipObs && cfg.ObsAddr == "" {
		return nil, fmt.Errorf("obserbavility server address canot be empty")
	}

	s := Server{
		logger: cfg.Logger,
		server: http.Server{
			Addr:    cfg.Addr,
			Handler: cfg.Handler,
		},
	}

	if !cfg.SkipObs {
		s.obsServer = &http.Server{
			Addr:    cfg.ObsAddr,
			Handler: cfg.ObsHandler,
		}
	}
	return &s, nil
}

func (srv *Server) logMsg(msg string, isErr bool) {
	if srv.logger != nil {
		srv.logger(msg, isErr)
	}
}

// Start to listen on the configured address
func (srv *Server) Start() error {

	stopDone := make(chan bool, 1)
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	// handle shutdown
	go func() {
		<-signalCh
		srv.Stop()
		stopDone <- true
	}()

	// observability server
	if srv.obsServer != nil {
		go func() {
			ln, err := net.Listen("tcp", srv.obsServer.Addr)
			if err != nil {
				panic(fmt.Sprintf("error starting obserbability server: %v", err))
			}
			srv.logMsg(fmt.Sprintf("obserbability server started on: %s", srv.obsServer.Addr), false)

			if err := srv.obsServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				panic(fmt.Sprintf("error in obserbability server: %v", err))
			}

		}()
	}

	ln, err := net.Listen("tcp", srv.server.Addr)
	if err != nil {
		return err
	}
	srv.logMsg(fmt.Sprintf("server started on: %s", srv.server.Addr), false)

	if err = srv.server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopDone
	return nil
}

// Stop shut down the server cleanly
func (srv *Server) Stop() {

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.server.Shutdown(ctx); err != nil {
		srv.logMsg(fmt.Sprintf("server shutdown: %v", err), true)
	}
	srv.logMsg("server stopped", false)

	if srv.obsServer != nil {
		if err := srv.obsServer.Shutdown(ctx); err != nil {
			srv.logMsg(fmt.Sprintf("observability server shutdown: %v", err), true)
		}
	}
	srv.logMsg("observability  server stopped", false)
}
