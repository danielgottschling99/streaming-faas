package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type ConnectRequest struct {
	FunctionName string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
}

func main() {
	var (
		functionName = flag.String("name", "", "Function name (required)")
		host         = flag.String("host", "", "Upstream host to connect to (required)")
		port         = flag.Int("port", 0, "Upstream port to connect to (required)")
		serverURL    = flag.String("server", "http://localhost:8080", "FaaS server URL")
	)
	flag.Parse()

	if *functionName == "" || *host == "" || *port == 0 {
		fmt.Println("Usage: go run cmd/connect/main.go -name <function-name> -host <upstream-host> -port <upstream-port> [-server <server-url>]")
		fmt.Println("\nExample:")
		fmt.Println("  go run cmd/connect/main.go -name uppercase -host localhost -port 9000")
		os.Exit(1)
	}

	request := ConnectRequest{
		FunctionName: *functionName,
		Host:         *host,
		Port:         *port,
	}

	requestJSON, err := json.Marshal(request)
	if err != nil {
		log.Fatalf("Failed to marshal request: %v", err)
	}

	fmt.Printf("🔌 Connecting '%s' via %s\n", *functionName, *serverURL)
	fmt.Printf("   Upstream: %s:%d\n", *host, *port)

	resp, err := http.Post(*serverURL+"/connect", "application/json", bytes.NewBuffer(requestJSON))
	if err != nil {
		log.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read response: %v", err)
	}

	if resp.StatusCode == http.StatusOK {
		fmt.Printf("✅ Connect succeeded\n")
		if len(responseBody) > 0 {
			fmt.Printf("   Response: %s\n", string(responseBody))
		}
		return
	}

	fmt.Printf("❌ Connect failed with status %d\n", resp.StatusCode)
	if len(responseBody) > 0 {
		fmt.Printf("   Error: %s\n", string(responseBody))
	}
	os.Exit(1)
}
