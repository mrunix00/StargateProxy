package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func getConfiguration() (*Configuration, error) {
	var config Configuration
	config.listenHostname = os.Getenv("SP_HOSTNAME")
	config.listenPort = os.Getenv("SP_PORT")
	config.redisHostname = os.Getenv("SP_REDIS_HOSTNAME")
	config.redisPort = os.Getenv("SP_REDIS_PORT")
	config.redisUsername = os.Getenv("SP_REDIS_USERNAME")
	config.redisPassword = os.Getenv("SP_REDIS_PASSWORD")

	if len(config.listenPort) == 0 {
		return nil, fmt.Errorf("SP_PORT environment variable is not set")
	}
	if portInt, err := strconv.Atoi(config.listenPort); err != nil || portInt > 65535 {
		return nil, fmt.Errorf("SP_PORT is not a valid port number")
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
		return nil, fmt.Errorf("SP_REDIS_PORT is not a valid port number")
	}

	if len(os.Getenv("SP_REDIS_DB")) == 0 {
		config.redisDB = 0
	} else if db, err := strconv.Atoi(os.Getenv("SP_REDIS_DB")); err == nil {
		config.redisDB = db
	} else {
		return nil, fmt.Errorf("SP_REDIS_DB is not a valid integer")
	}

	if len(os.Getenv("SP_REDIS_EXPIRATION")) == 0 {
		log.Println("[-] SP_REDIS_EXPIRATION is not set, defaulting to 60 minutes...")
		config.redisExpiration = 60 * time.Minute
	} else if expiration, err := strconv.Atoi(os.Getenv("SP_REDIS_EXPIRATION")); err == nil {
		config.redisExpiration = time.Duration(expiration) * time.Minute
	} else {
		return nil, fmt.Errorf("SP_REDIS_EXPIRATION is not a valid integer")
	}

	return &config, nil
}

func forwardData(dst, src net.Conn) {
	_, err := io.Copy(dst, src)
	if err != nil {
		log.Println("[-] Forwarding data failure: ", err.Error())
	}
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

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
