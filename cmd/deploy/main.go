package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type DeployRequest struct {
	FunctionName string            `json:"name"`
	FunctionZip  string            `json:"zip"`
	FunctionEnvs map[string]string `json:"envs,omitempty"`
}

func main() {
	var (
		functionName = flag.String("name", "", "Function name (required)")
		directory    = flag.String("dir", "", "Directory path containing the function code (required)")
		serverURL    = flag.String("server", "http://localhost:8080", "FaaS server URL")
		envVars      = flag.String("env", "", "Environment variables as key=value pairs separated by commas")
	)
	flag.Parse()

	if *functionName == "" || *directory == "" {
		fmt.Println("Usage: go run deploy.go -name <function-name> -dir <directory-path> [-server <server-url>] [-env key1=val1,key2=val2]")
		fmt.Println("\nExample:")
		fmt.Println("  go run deploy.go -name hello-world -dir ./example-function")
		fmt.Println("  go run deploy.go -name hello-world -dir ./example-function -server http://remote-server:8080 -env DEBUG=true,LOG_LEVEL=info")
		os.Exit(1)
	}

	// Check if directory exists
	if _, err := os.Stat(*directory); os.IsNotExist(err) {
		log.Fatalf("Directory does not exist: %s", *directory)
	}

	fmt.Printf("📦 Zipping function from directory: %s\n", *directory)

	// Create base64 zip
	zipBytes, err := createZipFromDirectory(*directory)
	if err != nil {
		log.Fatalf("Failed to create zip: %v", err)
	}

	zipBase64 := base64.StdEncoding.EncodeToString(zipBytes)

	// Parse environment variables
	envs := make(map[string]string)
	if *envVars != "" {
		pairs := strings.Split(*envVars, ",")
		var lastKey string
		for _, pair := range pairs {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 {
				lastKey = strings.TrimSpace(kv[0])
				envs[lastKey] = strings.TrimSpace(kv[1])
				continue
			}
			if lastKey != "" {
				envs[lastKey] = envs[lastKey] + "," + strings.TrimSpace(pair)
			}
		}
	}

	// Create request payload
	request := DeployRequest{
		FunctionName: *functionName,
		FunctionZip:  zipBase64,
		FunctionEnvs: envs,
	}

	requestJSON, err := json.Marshal(request)
	if err != nil {
		log.Fatalf("Failed to marshal request: %v", err)
	}

	fmt.Printf("🚀 Deploying function '%s' to %s\n", *functionName, *serverURL)
	fmt.Printf("   Zip size: %.2f KB\n", float64(len(zipBytes))/1024)
	if len(envs) > 0 {
		fmt.Printf("   Environment variables: %v\n", envs)
	}

	// Send request to FaaS server
	resp, err := http.Post(*serverURL+"/upload", "application/json", bytes.NewBuffer(requestJSON))
	if err != nil {
		log.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read response: %v", err)
	}

	if resp.StatusCode == http.StatusOK {
		fmt.Printf("✅ Function '%s' deployed successfully!\n", *functionName)
		fmt.Printf("   Response: %s\n", string(responseBody))
	} else {
		fmt.Printf("❌ Deployment failed with status %d\n", resp.StatusCode)
		fmt.Printf("   Error: %s\n", string(responseBody))
		os.Exit(1)
	}
}

func createZipFromDirectory(sourceDir string) ([]byte, error) {
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)

	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Get relative path
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		// Skip hidden files and common non-source files
		if strings.HasPrefix(relPath, ".") || strings.Contains(relPath, "/.") {
			return nil
		}

		// Skip common build artifacts and temporary files
		skipPatterns := []string{"node_modules/", ".git/", "dist/", "build/", "__pycache__/", "*.tmp", "*.log"}
		for _, pattern := range skipPatterns {
			if strings.Contains(relPath, pattern) || strings.HasSuffix(relPath, strings.TrimPrefix(pattern, "*")) {
				return nil
			}
		}

		fmt.Printf("   Adding: %s\n", relPath)

		// Create zip entry
		writer, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		// Read and write file content
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})

	if err != nil {
		return nil, err
	}

	// Close the zip writer to finalize the archive
	err = zipWriter.Close()
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
