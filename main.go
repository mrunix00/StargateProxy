package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
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

type CachedResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func parseHeaders(headerBytes []byte) http.Header {
	headers := http.Header{}
	scanner := bufio.NewScanner(bytes.NewReader(headerBytes))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			headers.Add(key, value)
		}
	}
	return headers
}

func (cachedResponse CachedResponse) MarshalBinary() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("HTTP/1.1 %d\r\n", cachedResponse.StatusCode))
	for key, values := range cachedResponse.Headers {
		for _, value := range values {
			buffer.WriteString(fmt.Sprintf("%s: %s\r\n", key, value))
		}
	}
	buffer.WriteString("\r\n") // Separate headers from body
	buffer.Write(cachedResponse.Body)
	return buffer.Bytes(), nil
}

func bytesToCachedResponse(data []byte) (CachedResponse, error) {
	// Split headers and body
	parts := bytes.SplitN(data, []byte("\r\n\r\n"), 2)
	if len(parts) != 2 {
		return CachedResponse{}, fmt.Errorf("invalid format")
	}

	// Split status line and headers
	headerParts := bytes.SplitN(parts[0], []byte("\r\n"), 2)
	if len(headerParts) != 2 {
		return CachedResponse{}, fmt.Errorf("invalid header format")
	}

	// Parse status line
	statusLine := string(headerParts[0])
	statusFields := strings.Fields(statusLine)
	if len(statusFields) < 2 {
		return CachedResponse{}, fmt.Errorf("invalid status line")
	}
	statusCode, err := strconv.Atoi(statusFields[1])
	if err != nil {
		return CachedResponse{}, fmt.Errorf("invalid status code")
	}

	headers := parseHeaders(headerParts[1])
	body := parts[1]
	return CachedResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}, nil
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
		0).Err()
	if err != nil {
		log.Println("[-] Failed caching response Body:", err.Error())
	}
}

func handleCachedHttp(w http.ResponseWriter, req *http.Request, rdb *redis.Client, ctx context.Context) {
	val, err := rdb.Get(ctx, req.Method+":"+req.Host+":"+req.URL.Path).Result()
	if err != nil {
		handleHttp(w, req, rdb, ctx)
		return
	}
	response, err := bytesToCachedResponse([]byte(val))
	if err != nil {
		log.Println("[-] Failed to parse cached response:", err.Error())
		handleHttp(w, req, rdb, ctx)
		return
	}
	copyHeader(w.Header(), response.Headers)
	w.WriteHeader(response.StatusCode)
	_, err = w.Write(response.Body)
	if err != nil {
		log.Println("[-] Failed writing cached response:", err.Error())
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
			if http.MethodConnect == r.Method {
				handleHttpsTunneling(w, r)
			} else {
				handleCachedHttp(w, r, rdb, ctx)
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
