package manners

import (
	"net"
	"net/http"
	"os")

var (
	defaultServer *GracefulServer
	defaultSignals []os.Signal
	hasSignals = false
)

func preventReEntrance() {
	if defaultServer != nil {
		panic("Program error: the default server must be closed before re-use.")
	}
}

// ListenAndServe provides a graceful version of the function provided by the
// net/http package. Call Close() to stop the server.
func ListenAndServe(addr string, handler http.Handler) error {
	preventReEntrance()
	defaultServer = NewServer(addr, handler)
	if (hasSignals) {
		defaultServer.CloseOnInterrupt(defaultSignals...)
	}
	return defaultServer.ListenAndServe()
}

// ListenAndServeTLS provides a graceful version of the function provided by the
// net/http package. Call Close() to stop the server.
func ListenAndServeTLS(addr string, certFile string, keyFile string, handler http.Handler) error {
	preventReEntrance()
	defaultServer = NewServer(addr, handler)
	if (hasSignals) {
		defaultServer.CloseOnInterrupt(defaultSignals...)
	}
	return defaultServer.ListenAndServeTLS(certFile, keyFile)
}

// Serve provides a graceful version of the function provided by the net/http
// package. Call Close() to stop the server.
func Serve(l net.Listener, handler http.Handler) error {
	preventReEntrance()
	defaultServer = NewWithServer(&http.Server{Handler: handler})
	if (hasSignals) {
		defaultServer.CloseOnInterrupt(defaultSignals...)
	}
	return defaultServer.Serve(l)
}

// Shuts down the default server used by ListenAndServe, ListenAndServeTLS and
// Serve. It returns true if it's the first time Close is called.
func Close() bool {
	outcome := defaultServer.Close()
	defaultServer = nil
	return outcome
}

// CloseOnInterrupt creates a go-routine that will call the Close() function when certain OS
// signals are received. If no signals are specified,
// the following are used: SIGINT, SIGTERM, SIGKILL, SIGQUIT, SIGHUP, SIGUSR1.
// This function must be called before ListenAndServe, ListenAndServeTLS, or Serve.
func CloseOnInterrupt(signals ...os.Signal) {
	defaultSignals = signals
	hasSignals = true
}

// After a signal has cause the server to close, this method allows you to determine which
// signal had been received. If Close was called some other way, this method will return nil.
//
// Note that, by convention, SIGUSR1 is often used to cause a server to close all its current
// connections cleanly, close its log files, and then restart. This facilitates log rotation.
// If you need this behaviour, you will need to provide a loop around the CloseOnInterrupt and
// ListenAndServe calls.
func SignalReceived() os.Signal {
	return defaultServer.SignalReceived()
}
