package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Configuration struct {
	listenHostname  string
	listenPort      string
	redisHostname   string
	redisPort       string
	redisUsername   string
	redisPassword   string
	redisDB         int
	redisExpiration time.Duration
	cert            *tls.Certificate
}

func getConfiguration() (*Configuration, error) {
	var config Configuration
	config.listenHostname = os.Getenv("SP_HOSTNAME")
	config.listenPort = os.Getenv("SP_PORT")
	config.redisHostname = os.Getenv("SP_REDIS_HOSTNAME")
	config.redisPort = os.Getenv("SP_REDIS_PORT")
	config.redisUsername = os.Getenv("SP_REDIS_USERNAME")
	config.redisPassword = os.Getenv("SP_REDIS_PASSWORD")

	if len(config.listenHostname) == 0 {
		log.Println("[-] SP_HOSTNAME is not set, defaulting to localhost...")
		config.listenHostname = "localhost"
	}
	if len(config.listenPort) == 0 {
		return nil, fmt.Errorf("SP_PORT environment variable is not set")
	}
	if portInt, err := strconv.Atoi(config.listenPort); err != nil || portInt > 65535 {
		return nil, fmt.Errorf("SP_PORT is not a valid port number")
	}

	if len(config.redisHostname) == 0 {
		log.Println("[-] SP_REDIS_HOSTNAME is not set, defaulting to localhost...")
		config.redisHostname = "localhost"
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

	certFilePath := os.Getenv("SP_CERT_FILE")
	keyFilePath := os.Getenv("SP_KEY_FILE")
	if len(certFilePath) == 0 {
		return nil, fmt.Errorf("SP_CERT_FILE environment variable is not set")
	}
	if len(keyFilePath) == 0 {
		return nil, fmt.Errorf("SP_KEY_FILE environment variable is not set")
	}
	cert, err := tls.LoadX509KeyPair(certFilePath, keyFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificate and/or key: %s", err.Error())
	}
	config.cert = &cert

	return &config, nil
}

func copyHeader(dst *strings.Builder, src http.Header) {
	for k, v := range src {
		for _, vv := range v {
			dst.Write([]byte(fmt.Sprintf("%s: %s\r\n", k, vv)))
		}
	}
}

func getRedisKey(req *http.Request) string {
	return req.Method + ":" + req.Host + ":" + req.URL.Path
}

func getResponseStatusLine(statusCode int) []byte {
	return []byte(fmt.Sprintf("HTTP/1.1 %d %s\r\n", statusCode, http.StatusText(statusCode)))
}
