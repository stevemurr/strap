package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
)

func runHTTP(ctx context.Context, cfg harness.Config, address, token string) (err error) {
	if token == "" {
		return errors.New("STRAP_API_TOKEN is required for HTTP mode")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("CLI HTTP mode requires a loopback IP; embed httpapi.Service for a TLS deployment")
	}
	service, err := httpapi.New(ctx, httpapi.Options{DefaultConfig: cfg, Authorize: httpapi.BearerToken(token)})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = errors.Join(err, service.Close(cleanup))
	}()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: service, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = server.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("HTTP server: %w", err)
}
