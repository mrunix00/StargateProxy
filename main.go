package main

import (
	"context"
	"github.com/redis/go-redis/v9"
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
	redisHostname  string
	redisPort      string
	redisUsername  string
	redisPassword  string
	redisDB        int
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
	config.redisHostname = os.Getenv("SP_REDIS_HOSTNAME")
	config.redisPort = os.Getenv("SP_REDIS_PORT")
	config.redisUsername = os.Getenv("SP_REDIS_USERNAME")
	config.redisPassword = os.Getenv("SP_REDIS_PASSWORD")

	if len(config.listenPort) == 0 {
		return nil, SPError{"SP_PORT environment variable is not set!"}
	}
	if portInt, err := strconv.Atoi(config.listenPort); err != nil || portInt > 65535 {
		return nil, SPError{"SP_PORT is not a valid port number!"}
	}

	if len(config.redisHostname) == 0 {
		log.Println("[-] SP_REDIS_HOSTNAME is not set, defaulting to localhost...")
		config.listenHostname = "localhost"
	}
	if len(config.redisPort) == 0 {
		log.Println("[-] SP_REDIS_PORT is not set, defaulting to :6379...")
		config.redisPort = "6379"
	}
	if portInt, err := strconv.Atoi(config.redisPort); err != nil || portInt > 65535 {
		return nil, SPError{"SP_REDIS_PORT is not a valid port number!"}
	}

	if len(os.Getenv("SP_REDIS_DB")) == 0 {
		config.redisDB = 0
	} else if db, err := strconv.Atoi(os.Getenv("SP_REDIS_DB")); err == nil {
		config.redisDB = db
	} else {
		return nil, SPError{"SP_REDIS_DB is not a valid integer!"}
	}

	return &config, nil
}

func forwardData(dst, src net.Conn) {
	_, err := io.Copy(dst, src)
	if err != nil {
		log.Println("[-] Forwarding data failure: ", err.Error())
	}
}

func handleHttpsTunneling(w http.ResponseWriter, r *http.Request) {
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

func handleHttp(w http.ResponseWriter, req *http.Request, rdb *redis.Client, ctx context.Context) {
	transport := http.DefaultTransport
	resp, err := transport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Println("[-] Failed closing response body:", err.Error())
		}
	}(resp.Body)

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("[-] Failed reading response body:", err.Error())
		return
	}

	_, err = w.Write(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		log.Println("[-] Failed copying response body:", err.Error())
		return
	}

	err = rdb.Set(ctx, req.Method+":"+req.Host+":"+req.URL.Path, body, 0).Err()
	if err != nil {
		log.Println("[-] Failed caching response body:", err.Error())
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
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
			if r.Method == http.MethodConnect {
				handleHttpsTunneling(w, r)
			} else {
				val, err := rdb.Get(ctx, r.Method+":"+r.Host+":"+r.URL.Path).Result()
				if err != nil {
					handleHttp(w, r, rdb, ctx)
				} else {
					log.Println("[*] Cache hit for: ", r.Method+":"+r.Host+":"+r.URL.Path)
					w.WriteHeader(http.StatusOK)
					_, err := w.Write([]byte(val))
					if err != nil {
						log.Println("[-] Failed writing cached response:", err.Error())
					}
					return
				}
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
