/*
Package manners provides a wrapper for a standard net/http server that
ensures all active HTTP client have completed their current request
before the server shuts down.

It can be used a drop-in replacement for the standard http package,
or can wrap a pre-configured Server.

eg.

	http.Handle("/hello", func(w http.ResponseWriter, r *http.Request) {
	  w.Write([]byte("Hello\n"))
	})

	log.Fatal(manners.ListenAndServe(":8080", nil))

or for a customized server:

	s := manners.NewWithServer(&http.Server{
		Addr:           ":8080",
		Handler:        myHandler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	})
	log.Fatal(s.ListenAndServe())

The server will shut down cleanly when the Close() method is called:

	manners.CloseOnInterrupt()
	http.Handle("/hello", myHandler)
	log.Fatal(manners.ListenAndServe(":8080", nil))
*/
package manners

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/fcgi"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// StateHandler can be called by the server if the state of the connection changes.
// Notice that it passed previous state and the new state as parameters.
type StateHandler func(net.Conn, http.ConnState, http.ConnState)

type Options struct {
	Server       *http.Server
	StateHandler StateHandler
	Listener     net.Listener
}

// A GracefulServer maintains a WaitGroup that counts how many in-flight
// requests the server is handling. When it receives a shutdown signal,
// it stops accepting new requests but does not actually shut down until
// all in-flight requests terminate.
//
// GracefulServer embeds the underlying net/http.Server making its non-override
// methods and properties avaiable.
//
// It must be initialized by calling NewServer or NewWithServer
type GracefulServer struct {
	*http.Server

	shutdown         chan bool
	shutdownFinished chan bool
	wg               waitGroup
	listener         *GracefulListener
	stateHandler     StateHandler

	up chan net.Listener // Only used by test code.

	signal os.Signal
}

// NewServer creates a new GracefulServer.
func NewServer() *GracefulServer {
	return NewWithServer(new(http.Server))
}

// NewWithServer wraps an existing http.Server object and returns a
// GracefulServer that supports all of the original Server operations.
func NewWithServer(s *http.Server) *GracefulServer {
	return &GracefulServer{
		Server:           s,
		shutdown:         make(chan bool),
		shutdownFinished: make(chan bool, 1),
		wg:               new(sync.WaitGroup),
	}
}

func NewWithOptions(o Options) *GracefulServer {
	// Set up listener
	var listener *GracefulListener
	if o.Listener != nil {
		g, ok := o.Listener.(*GracefulListener)
		if !ok {
			listener = NewListener(o.Listener)
		} else {
			listener = g
		}
	}

	return &GracefulServer{
		listener:         listener,
		Server:           o.Server,
		stateHandler:     o.StateHandler,
		shutdown:         make(chan bool),
		shutdownFinished: make(chan bool, 1),
		wg:               new(sync.WaitGroup),
	}
}

// Close stops the server from accepting new requets and begins shutting down.
// It returns true if it's the first time Close is called.
func (s *GracefulServer) Close() bool {
	logger.Printf("Shutting down server on %s\n", s.Server.Addr)
	return <-s.shutdown
}

// BlockingClose is similar to Close, except that it blocks until the last
// connection has been closed.
func (s *GracefulServer) BlockingClose() bool {
	logger.Printf("Shutting down server on %s (blocking)\n", s.Server.Addr)
	result := s.Close()
	<-s.shutdownFinished
	return result
}

func isUnixNetwork(addr string) bool {
	return strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, ".")
}

func listenToUnix(bind string) (listener net.Listener, err error) {
	_, err = os.Stat(bind)
	if err == nil {
		// socket exists and is "already in use";
		// presume this is from earlier run and therefore delete it
		err = os.Remove(bind)
		if err != nil {
			return
		}
	} else if !os.IsNotExist(err) {
		return
	}
	listener, err = net.Listen("unix", bind)
	return
}

func listen(bind string) (listener net.Listener, err error) {
	if isUnixNetwork(bind) {
		logger.Printf("Listening on unix socket %s\n", bind)
		return listenToUnix(bind)
	} else if strings.Contains(bind, ":") {
		logger.Printf("Listening on tcp socket %s\n", bind)
		return net.Listen("tcp", bind)
	} else {
		return nil, fmt.Errorf("error while parsing bind arg %v", bind)
	}
}

// ListenAndServe provides a graceful equivalent of net/http.Serve.ListenAndServe.
func (s *GracefulServer) ListenAndServe() error {
	if s.listener == nil {
		addr := s.Addr
		if addr == "" {
			addr = ":http"
		}
		oldListener, err := listen(addr)
		if err != nil {
			return err
		}
		s.listener = NewListener(oldListener)
	}
	return s.Serve(s.listener)
}

// ListenAndServeTLS provides a graceful equivalent of net/http.Serve.ListenAndServeTLS.
func (s *GracefulServer) ListenAndServeTLS(certFile, keyFile string) error {
	// direct lift from net/http/server.go
	addr := s.Addr
	if addr == "" {
		addr = ":https"
	}
	config := &tls.Config{}
	if s.TLSConfig != nil {
		*config = *s.TLSConfig
	}
	if config.NextProtos == nil {
		config.NextProtos = []string{"http/1.1"}
	}

	var err error
	config.Certificates = make([]tls.Certificate, 1)
	config.Certificates[0], err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	return s.ListenAndServeTLSWithConfig(config)
}

// ListenAndServeTLS provides a graceful equivalent of net/http.Serve.ListenAndServeTLS.
func (s *GracefulServer) ListenAndServeTLSWithConfig(config *tls.Config) error {
	addr := s.Addr
	if addr == "" {
		addr = ":https"
	}

	if s.listener == nil {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}

		tlsListener := NewTLSListener(TCPKeepAliveListener{ln.(*net.TCPListener)}, config)
		s.listener = NewListener(tlsListener)
	}
	return s.Serve(s.listener)
}

func (gs *GracefulServer) GetFile() (*os.File, error) {
	return gs.listener.GetFile()
}

func (gs *GracefulServer) HijackListener(s *http.Server, config *tls.Config) (*GracefulServer, error) {
	listener, err := gs.listener.Clone()
	if err != nil {
		return nil, err
	}

	if config != nil {
		listener = NewTLSListener(TCPKeepAliveListener{listener.(*net.TCPListener)}, config)
	}

	other := NewWithServer(s)
	other.listener = NewListener(listener)
	return other, nil
}

// Serve provides a graceful equivalent net/http.Server.Serve.
//
// If listener is not an instance of *GracefulListener it will be wrapped
// to become one.
func (s *GracefulServer) Serve(listener net.Listener) error {
	// Accept a net.Listener to preserve the interface compatibility with the
	// standard http.Server. If it is not a GracefulListener then wrap it into
	// one.
	gracefulListener, ok := listener.(*GracefulListener)
	if !ok {
		gracefulListener = NewListener(listener)
		listener = gracefulListener
	}
	s.listener = gracefulListener

	// Wrap the server HTTP handler into graceful one, that will close kept
	// alive connections if a new request is received after shutdown.
	gracefulHandler := newGracefulHandler(s.Server.Handler)
	s.Server.Handler = gracefulHandler

	// Start a goroutine that waits for a shutdown signal and will stop the
	// listener when it receives the signal. That in turn will result in
	// unblocking of the http.Serve call.
	go func() {
		s.shutdown <- true
		close(s.shutdown)
		gracefulHandler.Close()
		s.Server.SetKeepAlivesEnabled(false)
		gracefulListener.Close()
	}()

	originalConnState := s.Server.ConnState

	// s.ConnState is invoked by the net/http.Server every time a connection
	// changes state. It keeps track of each connection's state over time,
	// enabling manners to handle persisted connections correctly.
	s.ConnState = func(conn net.Conn, newState http.ConnState) {
		gracefulConn := retrieveGracefulConn(conn)
		oldState := gracefulConn.lastHTTPState
		gracefulConn.lastHTTPState = newState

		switch newState {

		case http.StateNew:
			// New connection -> StateNew
			gracefulConn.protected = true
			s.StartRoutine()

		case http.StateActive:
			// (StateNew, StateIdle) -> StateActive
			if gracefulHandler.IsClosed() {
				gracefulConn.Close()
				break
			}

			if !gracefulConn.protected {
				gracefulConn.protected = true
				s.StartRoutine()
			}

		default:
			// (StateNew, StateActive) -> (StateIdle, StateClosed, StateHiJacked)
			if gracefulConn.protected {
				s.FinishRoutine()
				gracefulConn.protected = false
			}
		}

		if s.stateHandler != nil {
			s.stateHandler(conn, oldState, newState)
		}

		if originalConnState != nil {
			originalConnState(conn, newState)
		}
	}

	// A hook to allow the server to notify others when it is ready to receive
	// requests; only used by tests.
	if s.up != nil {
		s.up <- listener
	}

	var err error
	if isUnixNetwork(s.Server.Addr) {
		os.Chmod(s.Server.Addr, os.ModePerm)
		err = fcgi.Serve(listener, s.Server.Handler)
	} else {
		err = s.Server.Serve(listener)
	}

	// An error returned on shutdown is not worth reporting.
	if _, ok = err.(listenerAlreadyClosed); ok {
		err = nil
	}

	// Wait for pending requests to complete regardless the Serve result.
	s.wg.Wait()
	s.shutdownFinished <- true
	return err
}

// StartRoutine increments the server's WaitGroup. Use this if a web request
// starts more goroutines and these goroutines are not guaranteed to finish
// before the request.
func (s *GracefulServer) StartRoutine() {
	s.wg.Add(1)
}

// FinishRoutine decrements the server's WaitGroup. Use this to complement
// StartRoutine().
func (s *GracefulServer) FinishRoutine() {
	s.wg.Done()
}

// CloseOnInterrupt creates a go-routine that will call the Close() function when certain OS
// signals are received. If no signals are specified,
// the following are used: SIGINT, SIGTERM, SIGKILL, SIGQUIT, SIGHUP, SIGUSR1.
// This function must be called before ListenAndServe, ListenAndServeTLS, or Serve.
func (s *GracefulServer) CloseOnInterrupt(signals ...os.Signal) *GracefulServer {
	if s == nil {
		panic("Program error: the server must exist before this method is called.")
	}
	go func(rx *GracefulServer) {
		sigchan := make(chan os.Signal, 1)
		if len(signals) > 0 {
			signal.Notify(sigchan, signals...)
		} else {
			signal.Notify(sigchan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL,
				syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGUSR1)
		}
		rx.signal = <-sigchan
		rx.Close()
	}(s)
	return s
}

// SignalReceived gets the signal that caused the server to close, if any. If Close() was called
// some other way, this method will return nil.
//
// Note that, by convention, SIGUSR1 is often used to cause a server to close all its current
// connections cleanly, close its log files, and then restart. This facilitates log rotation.
// If you need this behaviour, you will need to provide a loop around both the CloseOnInterrupt and
// ListenAndServe calls.
func (s *GracefulServer) SignalReceived() os.Signal {
	return s.signal
}

// gracefulHandler is used by GracefulServer to prevent calling ServeHTTP on
// to be closed kept-alive connections during the server shutdown.
type gracefulHandler struct {
	closed  int32 // accessed atomically.
	wrapped http.Handler
}

func newGracefulHandler(wrapped http.Handler) *gracefulHandler {
	return &gracefulHandler{
		wrapped: wrapped,
	}
}

func (gh *gracefulHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if atomic.LoadInt32(&gh.closed) == 0 {
		gh.wrapped.ServeHTTP(w, r)
		return
	}
	r.Body.Close()
	// Server is shutting down at this moment, and the connection that this
	// handler is being called on is about to be closed. So we do not need to
	// actually execute the handler logic.
}

func (gh *gracefulHandler) Close() {
	atomic.StoreInt32(&gh.closed, 1)
}

func (gh *gracefulHandler) IsClosed() bool {
	return atomic.LoadInt32(&gh.closed) == 1
}
