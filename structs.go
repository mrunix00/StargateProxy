package main

import (
	"bytes"
	"fmt"
	"net/http"
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
}

type CachedResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
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
