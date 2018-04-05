// Package websocketproxy is a reverse proxy for WebSocket connections.
package websocketproxy

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"errors"
	"time"
    "math/rand"

	"github.com/gorilla/websocket"
)

var (
	// DefaultUpgrader specifies the parameters for upgrading an HTTP
	// connection to a WebSocket connection.
	DefaultUpgrader = &websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	// DefaultDialer is a dialer with all fields set to the default zero values.
	DefaultDialer = websocket.DefaultDialer
)

// WebsocketProxy is an HTTP Handler that takes an incoming WebSocket
// connection and proxies it to another server.
type WebsocketProxy struct {
	// Director, if non-nil, is a function that may copy additional request
	// headers from the incoming WebSocket connection into the output headers
	// which will be forwarded to another server.
	Director func(incoming *http.Request, out http.Header)

	// Backend returns the backend URL which the proxy uses to reverse proxy
	// the incoming WebSocket connection. Request is the initial incoming and
	// unmodified request.
	Backends []func(*http.Request) *url.URL

	// Upgrader specifies the parameters for upgrading a incoming HTTP
	// connection to a WebSocket connection. If nil, DefaultUpgrader is used.
	Upgrader *websocket.Upgrader

	//  Dialer contains options for connecting to the backend WebSocket server.
	//  If nil, DefaultDialer is used.
	Dialer *websocket.Dialer

	ReqCount int

	DesolateBackend map[int]int
}

// ProxyHandler returns a new http.Handler interface that reverse proxies the
// request to the given target.
func ProxyHandler() http.Handler { return NewProxy() }

// NewProxy returns a new Websocket reverse proxy that rewrites the
// URL's to the scheme, host and base path provider in target.
func NewProxy() *WebsocketProxy {
	var backends = make([]func(r *http.Request) *url.URL, 0)
	var desolateBackend = make(map[int]int)
	return &WebsocketProxy{Backends: backends, DesolateBackend: desolateBackend}
}

func (w *WebsocketProxy) getRequestURL(target *url.URL) func(r *http.Request) *url.URL {
	backend := func(r *http.Request) *url.URL {
		// Shallow copy
		u := *target
		u.Fragment = r.URL.Fragment
		u.Path = r.URL.Path
		u.RawQuery = r.URL.RawQuery
		return &u
	}
	return backend
}

func (w *WebsocketProxy) selectBackend() int {
	var index, selectcnt int
	backendcnt := len(w.Backends)
	for{
		if selectcnt >= backendcnt{
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			index = r.Intn(backendcnt)
			break
		}
		w.ReqCount++
		index = w.ReqCount % backendcnt
		if waitcnt, ok := w.DesolateBackend[index]; ok{
			if waitcnt <= 0{
				break
			}else{
				selectcnt++
				w.DesolateBackend[index]--
				continue
			}
		}
		break
	}

	return index
}

// AddBackend append backend to proxy
func (w *WebsocketProxy) AddBackend(target *url.URL) {
	w.Backends = append(w.Backends, w.getRequestURL(target))
}

func (w *WebsocketProxy) tryGetBackendConn(req *http.Request) (*websocket.Conn, http.Header, error){
	backendCount := len(w.Backends)
	for i := 0; i < backendCount; i++{
		connBackend, upgradeHeader, err := w.connectBackend(req)
		if err != nil{
			continue
		}
		log.Printf("client(%s) connected to server(%s)\r\n", req.RemoteAddr, connBackend.RemoteAddr())
		return connBackend, upgradeHeader, err
	}
	return nil, nil, errors.New("No backend available")
}

func (w *WebsocketProxy) connectBackend(req *http.Request) (*websocket.Conn, http.Header, error){
	index := w.selectBackend()
	backendURL := w.Backends[index](req)
	dialer := w.Dialer
	if w.Dialer == nil {	
		dialer = DefaultDialer
	}
	// Pass headers from the incoming request to the dialer to forward them to
	// the final destinations.
	requestHeader := http.Header{}
	if origin := req.Header.Get("Origin"); origin != "" {
		requestHeader.Add("Origin", origin)
	}
	for _, prot := range req.Header[http.CanonicalHeaderKey("Sec-WebSocket-Protocol")] {
		requestHeader.Add("Sec-WebSocket-Protocol", prot)
	}
	for _, cookie := range req.Header[http.CanonicalHeaderKey("Cookie")] {
		requestHeader.Add("Cookie", cookie)
	}

	// Pass X-Forwarded-For headers too, code below is a part of
	// httputil.ReverseProxy. See http://en.wikipedia.org/wiki/X-Forwarded-For
	// for more information
	// TODO: use RFC7239 http://tools.ietf.org/html/rfc7239
	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		// If we aren't the first proxy retain prior
		// X-Forwarded-For information as a comma+space
		// separated list and fold multiple headers into one.
		if prior, ok := req.Header["X-Forwarded-For"]; ok {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		requestHeader.Set("X-Forwarded-For", clientIP)
	}

	// Set the originating protocol of the incoming HTTP request. The SSL might
	// be terminated on our site and because we doing proxy adding this would
	// be helpful for applications on the backend.
	requestHeader.Set("X-Forwarded-Proto", "http")
	if req.TLS != nil {
		requestHeader.Set("X-Forwarded-Proto", "https")
	}

	// Enable the director to copy any additional headers it desires for
	// forwarding to the remote server.
	if w.Director != nil {
		w.Director(req, requestHeader)
	}

	// Connect to the backend URL, also pass the headers we get from the requst
	// together with the Forwarded headers we prepared above.
	// TODO: support multiplexing on the same backend connection instead of
	// opening a new TCP connection time for each request. This should be
	// optional:
	// http://tools.ietf.org/html/draft-ietf-hybi-websocket-multiplexing-01
	connBackend, resp, err := dialer.Dial(backendURL.String(), requestHeader)
	if err != nil {
		log.Printf("server(%s) not available\r\n", backendURL.Host)
		w.DesolateBackend[index] = 5;
		return nil, nil, err
	}

	// Only pass those headers to the upgrader.
	upgradeHeader := http.Header{}
	if hdr := resp.Header.Get("Sec-Websocket-Protocol"); hdr != "" {
		upgradeHeader.Set("Sec-Websocket-Protocol", hdr)
	}
	if hdr := resp.Header.Get("Set-Cookie"); hdr != "" {
		upgradeHeader.Set("Set-Cookie", hdr)
	}
	w.DesolateBackend[index] = 0;
	return connBackend, upgradeHeader, nil
}


// ServeHTTP implements the http.Handler that proxies WebSocket connections.
func (w *WebsocketProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {

	connBackend, upgradeHeader, err := w.tryGetBackendConn(req)
	if err != nil{
		log.Println(err)
		http.Error(rw, "internal server error (code: 2)", http.StatusInternalServerError)
		return
	}
	defer connBackend.Close()

	upgrader := w.Upgrader
	if w.Upgrader == nil {
		upgrader = DefaultUpgrader
	}

	// Now upgrade the existing incoming request to a WebSocket connection.
	// Also pass the header that we gathered from the Dial handshake.
	connPub, err := upgrader.Upgrade(rw, req, upgradeHeader)
	if err != nil {
		log.Printf("websocketproxy: couldn't upgrade %s\n", err)
		return
	}
	defer connPub.Close()

	errc := make(chan error, 2)

	replicateWebsocketConn := func(dst, src *websocket.Conn, dstName, srcName string) {
		var err error
		for {
			msgType, msg, err := src.ReadMessage()
			if err != nil {
				log.Printf("websocketproxy: error when copying from %s to %s using ReadMessage: %v", srcName, dstName, err)
				break
			}
			err = dst.WriteMessage(msgType, msg)
			if err != nil {
				log.Printf("websocketproxy: error when copying from %s to %s using WriteMessage: %v", srcName, dstName, err)
				break
			} else {
				//log.Printf("websocketproxy: copying from %s to %s completed without error.", srcName, dstName)
			}
		}
		errc <- err
	}

	go replicateWebsocketConn(connPub, connBackend, "client", "backend")
	go replicateWebsocketConn(connBackend, connPub, "backend", "client")

	<-errc
}
