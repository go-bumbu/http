# Http

The Http module contains a set uf useful http packages that can be used in any Http backend project.

## Import

Import the module
```
go get github.com/go-bumbu/http
```

## Server

The server package contains boilerplate to create an http server.
Features:
* you can specify a main handler and an optional observability handler
  * the observability handler is intended to expose details like metrics, runtime controls or hprof endpoint
* the server can safely shut down with os kill signals
* it exposes a Stop() method to safely shut down both servers.



## Middleware

Middleware contains several middleware handlers to facilitate writing backends
* delay: useful during development for AJAX calls, adds delay to a response
* jsonErr: wraps all http errors into a json response, also allows to generalize errors
  * setting the flag _genericMessage_ to true, will not return the error string but the generic error string matching the response code instead
* prometheus: adds an _http_duration_seconds_ bucket to measure volume and duration of requests per response code
* zerolog: middleware that will write a log message to a zerolog logger capturing every request

## Handlers
* spa: simple Single page application handler to serve SPAs embedded into go code