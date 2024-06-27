package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
)

type Configuration struct {
	listenHostname string
	listenPort     string
	targetHostname string
	targetPort     string
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
	config.targetHostname = os.Getenv("SP_TARGET_HOSTNAME")
	config.targetPort = os.Getenv("SP_TARGET_PORT")

	if len(config.targetHostname) == 0 {
		return nil, SPError{"SP_HOSTNAME environment variable is not set!"}
	}
	if len(config.listenPort) == 0 {
		return nil, SPError{"SP_PORT environment variable is not set!"}
	}
	if portInt, err := strconv.Atoi(config.listenPort); err != nil || portInt > 65535 {
		return nil, SPError{"SP_PORT is not a valid port number!"}
	}
	if len(config.targetHostname) == 0 {
		return nil, SPError{"SP_TARGET_HOSTNAME environment variable is not set!"}
	}
	if len(config.targetPort) == 0 {
		return nil, SPError{"SP_TARGET_PORT environment variable is not set!"}
	}
	if portInt, err := strconv.Atoi(config.targetPort); err != nil || portInt > 65535 {
		return nil, SPError{"SP_TARGET_PORT is not a valid port number!"}
	}

	return &config, nil
}

func handleProxyConnection(clientConnection net.Conn, config Configuration) {
	buffer := make([]byte, 4096)
	defer clientConnection.Close()
	fmt.Fprintf(os.Stdout, "[*] Received connection from: %s\n", clientConnection.RemoteAddr())
	nOfBytes, err := clientConnection.Read(buffer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Failed to read from client: %s\n", err.Error())
		return
	}

	var method, host, port string
	_, err = fmt.Sscanf(string(buffer[:nOfBytes]), "%s%s%s", &method, &host, &port)
	if err != nil {
		clientConnection.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		fmt.Fprintf(os.Stderr, "[-] Failed to parse request: %s\n", err.Error())
		return
	}
	if method != "CONNECT" {
		clientConnection.Write([]byte("HTTP/1.1 405 Method Not Allowed\r\n\r\n"))
		fmt.Fprintf(os.Stderr, "[-] Method not allowed: %s\n", method)
		return
	}

	targetConnection, err := net.Dial("tcp", net.JoinHostPort(config.targetHostname, config.targetPort))
	if err != nil {
		clientConnection.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		fmt.Fprintf(os.Stderr, "[-] Failed to connect to target: %s\n", err.Error())
		return
	}
	defer targetConnection.Close()

	_, err = clientConnection.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Failed to write to client: %s\n", err.Error())
		return
	}
	fmt.Fprintf(os.Stdout, "[+] Connection established to: %s\n", clientConnection.RemoteAddr())

	go io.Copy(targetConnection, clientConnection)
	io.Copy(clientConnection, targetConnection)
}

func startProxy(config Configuration) {
	fmt.Println("[*] Starting proxy...")
	listeningAddr := net.JoinHostPort(config.listenHostname, config.listenPort)
	listener, err := net.Listen("tcp", listeningAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Listening error: %s\n", err.Error())
		os.Exit(1)
	}
	defer func(listener net.Listener) {
		err := listener.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] Closing listener failure: %s\n", err.Error())
			os.Exit(1)
		}
	}(listener)

	fmt.Fprintf(os.Stdout, "[+] Listening on: %s\n", listeningAddr)

	for {
		connection, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] Accepting connection failure: %s\n", err.Error())
		}

		go handleProxyConnection(connection, config)
	}
}

func main() {
	config, err := getConfiguration()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Configuration error: %s\n", err.Error())
		os.Exit(1)
	}

	startProxy(*config)
}
