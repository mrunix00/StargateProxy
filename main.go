package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
)

type Configuration struct {
	listenHostname string
	listenPort     string
}

type SPError struct {
	err string
}

func (sperr SPError) Error() string {
	return sperr.err
}

func getConfiguration() (*Configuration, error) {
	var config Configuration
	config.listenHostname = os.Getenv("SP_HOSTNAME")
	config.listenPort = os.Getenv("SP_PORT")

	if len(config.listenPort) == 0 {
		return nil, SPError{"SP_PORT environment variable is not set!"}
	}
	if portInt, err := strconv.Atoi(config.listenPort); err != nil || portInt > 65535 {
		return nil, SPError{"SP_PORT is not a valid port number!"}
	}

	return &config, nil
}

func forwardData(dst, src net.Conn) {
	_, err := io.Copy(dst, src)
	if err != nil {
		log.Println("[-] Forwarding data failure: ", err.Error())
	}
}

func handleConnection(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] Received connection from:", r.RemoteAddr)
	if r.Method != http.MethodConnect {
		http.Error(w, "HTTP/1.1 405 Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	destConn, err := net.Dial("tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		log.Println("[-]", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	defer func(destConn net.Conn) {
		if err := destConn.Close(); err != nil {
			log.Println("[-] Failed closing destination connection:", err.Error())
		}
	}(destConn)

	hijacker, allowed := w.(http.Hijacker)
	if !allowed {
		http.Error(w, "HTTP/1.1 400 Internal Server Error", http.StatusInternalServerError)
		return
	}

	srcConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}
	defer func(srcConn net.Conn) {
		if err := srcConn.Close(); err != nil {
			log.Println("[-] Failed closing client connection:", err.Error())
		}
	}(srcConn)

	go forwardData(destConn, srcConn)
	forwardData(srcConn, destConn)
}

func startProxy(config Configuration) {
	hostAddr := net.JoinHostPort(config.listenHostname, config.listenPort)
	server := http.Server{
		Addr:      hostAddr,
		Handler:   http.HandlerFunc(handleConnection),
		TLSConfig: nil,
	}
	log.Println("[*] Listening on: ", hostAddr)
	err := server.ListenAndServe()
	if err != nil {
		log.Fatalln("[-] Cannot listen on ", hostAddr, " : ", err.Error())
	}
}

func main() {
	config, err := getConfiguration()
	if err != nil {
		log.Fatalln("[-] Configuration error: ", err.Error())
	}

	startProxy(*config)
}
