package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"github.com/redis/go-redis/v9"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var ctx = context.Background()

func handleHttpsTunneling(w http.ResponseWriter, r *http.Request, rdb *redis.Client, config Configuration) {
	log.Println("[*] Tunneling to:", r.Host)
	w.WriteHeader(http.StatusOK)

	// Hijack the connection to the client
	hijacker, allowed := w.(http.Hijacker)
	if !allowed {
		http.Error(w, "HTTP/1.1 400 Internal Server Error", http.StatusInternalServerError)
		return
	}
	srcConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}

	// Create a TLS connection
	clientTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{*config.cert},
		GetCertificate: func(info *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return config.cert, nil
		},
		InsecureSkipVerify: true,
	}
	clientTLSConn := tls.Server(srcConn, clientTLSConfig)
	defer func(clientTLSConn *tls.Conn) {
		err := clientTLSConn.Close()
		if err != nil {
			log.Println("[-] Failed closing clientTLSConn:", err.Error())
		}
	}(clientTLSConn)
	err = clientTLSConn.Handshake()
	if err != nil {
		http.Error(w, "TLS handshake failed", http.StatusInternalServerError)
		return
	}

	// Read the request from the client
	reader := bufio.NewReader(clientTLSConn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		log.Println("[-] Failed reading request:", err.Error())
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}

	// if the request is a GET request, check if it is cached in Redis
	// and return the cached response if it is
	if req.Method == http.MethodGet {
		val, err := rdb.Get(ctx, req.Method+":"+req.Host+":"+req.URL.Path).Result()
		if err == nil {
			log.Println("[*] Cache hit:", req.Host+req.URL.Path)
			_, err := clientTLSConn.Write([]byte(val))
			if err != nil {
				log.Println("[-] Failed writing cached response:", err.Error())
			}
			return
		}
		log.Println("[*] Cache miss:", req.URL.Host+req.URL.Path)
	}

	// Forward the request to the target server
	req.URL = &url.URL{
		Scheme: "https",
		Host:   req.Host,
		Path:   req.URL.Path,
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Read the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Println("[-] Failed closing response Body:", err.Error())
		}
	}(resp.Body)

	buffer := &strings.Builder{}
	// Write the response status line
	_, err = buffer.Write([]byte(fmt.Sprintf("HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))))
	if err != nil {
		log.Println("[-] Failed writing response status line:", err.Error())
		return
	}

	// Write the response headers
	for k, v := range resp.Header {
		for _, vv := range v {
			buffer.Write([]byte(fmt.Sprintf("%s: %s\r\n", k, vv)))
		}
	}

	buffer.Write([]byte("\r\n"))
	buffer.Write(body)

	// Write the response to the client
	_, err = clientTLSConn.Write([]byte(buffer.String()))
	if err != nil {
		log.Println("[-] Failed writing response:", err.Error())
		return
	}

	// If the request is a GET request, cache the response
	if req.Method == http.MethodGet {
		rdb.Set(ctx, req.Method+":"+req.Host+":"+req.URL.Path, []byte(buffer.String()), config.redisExpiration)
	}
}

func handleHttp(w http.ResponseWriter, req *http.Request, rdb *redis.Client, config Configuration) {
	log.Println("[*] Fetching from upstream:", req.URL)
	transport := http.DefaultTransport
	resp, err := transport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Println("[-] Failed closing response Body:", err.Error())
		}
	}(resp.Body)

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("[-] Failed reading response Body:", err.Error())
		return
	}

	_, err = w.Write(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		log.Println("[-] Failed copying response Body:", err.Error())
		return
	}

	err = rdb.Set(
		ctx,
		req.Method+":"+req.Host+":"+req.URL.Path,
		CachedResponse{resp.StatusCode, w.Header(), body},
		config.redisExpiration).Err()
	if err != nil {
		log.Println("[-] Failed caching response Body:", err.Error())
	}
}

func handleCachedHttp(w http.ResponseWriter, req *http.Request, rdb *redis.Client, config Configuration) {
	val, err := rdb.Get(ctx, req.Method+":"+req.Host+":"+req.URL.Path).Result()
	if err != nil {
		log.Println("[-] Cache miss:", err.Error())
		handleHttp(w, req, rdb, config)
		return
	}
	log.Println("[*] Cache hit:", req.URL)
	response, err := bytesToCachedResponse([]byte(val))
	if err != nil {
		log.Println("[-] Failed to parse cached response:", err.Error())
		handleHttp(w, req, rdb, config)
		return
	}
	copyHeader(w.Header(), response.Headers)
	w.WriteHeader(response.StatusCode)
	_, err = w.Write(response.Body)
	if err != nil {
		log.Println("[-] Failed writing cached response:", err.Error())
	}
}

func startProxy(config Configuration) {
	hostAddr := net.JoinHostPort(config.listenHostname, config.listenPort)
	rdb := redis.NewClient(&redis.Options{
		Addr:     net.JoinHostPort(config.redisHostname, config.redisPort),
		Username: config.redisUsername,
		Password: config.redisPassword,
		DB:       config.redisDB,
	})
	server := http.Server{
		Addr: hostAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Println("[*] Received connection from:", r.RemoteAddr)
			switch r.Method {
			case http.MethodConnect:
				handleHttpsTunneling(w, r, rdb, config)
				return
			case http.MethodGet:
				handleCachedHttp(w, r, rdb, config)
				return
			default:
				handleHttp(w, r, rdb, config)
			}
		}),
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
