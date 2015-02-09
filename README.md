# Manners

A *polite* webserver for Go.

Manners allows you to shut your Go webserver down gracefully, without dropping any requests. It can act as a drop-in replacement for the standard library's 
[http.ListenAndServe](http://golang.org/pkg/net/http/#ListenAndServe) function:

```go
func main() {
  handler := MyHTTPHandler()
  manners.ListenAndServe(":7000", handler)
}
```

Then, when you want to shut the server down:

```go
manners.Close()
```

(Note that this does not block until all the requests are finished. Rather, the call to `manners.ListenAndServe` will stop blocking when all the requests are finished.)

### Other goroutines

Manners ensures that all requests are served by incrementing a WaitGroup when a request comes in and decrementing it when the request finishes.

If your request handler spawns other goroutines that are not guaranteed to finish with the request, you can ensure they are also completed with the `StartRoutine` and `FinishRoutine` functions on the server. If you don't do this, your other goroutines may be terminated abruptly when the server exits.

### Handling signals

It's good to close down the server cleanly when OS signals are received. This is easy: just add

```go
manners.CloseOnInterrupt()
```
before the `ListenAndServe` call. This kicks off a separate goroutine to wait for an OS signal, upon which it simply calls `manners.Close()` for you.

### HTTP, HTTPS and FCGI

Manners supports three protocols: HTTP, HTTPS and FCGI. HTTP is illustrated above. 
For HTTPS, Manners can likewise act as a drop-in replacement for the standard library's 
[http.ListenAndServeTLS](http://golang.org/pkg/net/http/#ListenAndServeTLS) function:

```go
func main() {
  handler  := MyHTTPHandler()
  certFile := MyCertificate()
  keyFile  := MyKeyFile()
  manners.ListenAndServeTLS(":https", certFile, keyFile, handler)
}
```

In Manners, FCGI only operates via local a Unix socket connected to a co-hosted proxy, such as Apache or Nginx. 

```go
func main() {
  handler := MyHTTPHandler()
  manners.ListenAndServe("/var/run/goserver", handler)
}
```

To use FCGI, the port string must specify the Unix socket and start with a slash or dot, as in the example above. In this case, Manners will use [fcgi.Serve](http://golang.org/pkg/net/http/fcgi/#Serve).

In each of the protocols, Manners drains down the connections cleanly when `manners.Close()` is called.

### Compatability

Manners 0.3.0 and above uses standard library functionality introduced in Go 1.3.

### Installation

`go get github.com/rickb777/manners`
