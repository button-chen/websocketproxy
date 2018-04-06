package main

import (
	"log"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
	"github.com/button-chen/websocketproxy"
)

var (
	backendURL  = "ws://127.0.0.1:9001"
	backendURL2 = "ws://127.0.0.1:9002"
)

func main() {

	supportedSubProtocols := []string{"localSensePush-protocol"}
	upgrader := &websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		Subprotocols: supportedSubProtocols,
	}

	u, _ := url.Parse(backendURL)
	u2, _ := url.Parse(backendURL2)
	proxy := websocketproxy.NewProxy()
	proxy.Upgrader = upgrader
	proxy.AddBackend(u)
	proxy.AddBackend(u2)

	// reverse proxy
	go func(){
		log.Println("start reverse proxy on: 9009")
		err := http.ListenAndServe(":9009", proxy)
		if err != nil {
			log.Println(err)
		}
	}()

	// redirect proxy
	proxy2 := websocketproxy.NewProxy()
	proxy2.Upgrader = upgrader
	proxy2.AddBackend(u)
	proxy2.AddBackend(u2)
	proxy2.ForwardMode = websocketproxy.RedirectForwardMode
	log.Println("start redirect proxy on: 9008")
	err := http.ListenAndServe(":9008", proxy2)
	if err != nil {
		log.Println(err)
	}
}
