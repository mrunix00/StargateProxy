package main

import (
	"context"
	"github.com/redis/go-redis/v9"
	"io"
	"log"
	"net"
	"net/http"
)

func handleHttpsTunneling(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] Tunneling to:", r.Host)
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

func handleHttp(w http.ResponseWriter, req *http.Request, rdb *redis.Client, ctx context.Context, config Configuration) {
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

func handleCachedHttp(w http.ResponseWriter, req *http.Request, rdb *redis.Client, ctx context.Context, config Configuration) {
	val, err := rdb.Get(ctx, req.Method+":"+req.Host+":"+req.URL.Path).Result()
	if err != nil {
		log.Println("[-] Cache miss:", err.Error())
		handleHttp(w, req, rdb, ctx, config)
		return
	}
	log.Println("[*] Cache hit:", req.URL)
	response, err := bytesToCachedResponse([]byte(val))
	if err != nil {
		log.Println("[-] Failed to parse cached response:", err.Error())
		handleHttp(w, req, rdb, ctx, config)
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
	ctx := context.Background()
	server := http.Server{
		Addr: hostAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Println("[*] Received connection from:", r.RemoteAddr)
			switch r.Method {
			case http.MethodConnect:
				handleHttpsTunneling(w, r)
				return
			case http.MethodGet:
				handleCachedHttp(w, r, rdb, ctx, config)
				return
			default:
				handleHttp(w, r, rdb, ctx, config)
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
