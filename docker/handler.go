package docker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type MinimalHandler struct {
	name         string
	containerID  string
	ip           string
	hostPort     string
	zipPath      string
	dockerClient *client.Client
	imageName    string
}

const faasNetworkName = "faas-net"

// Create sets up the handler with the function code
func (h *MinimalHandler) Create(name, zipPath string, envs map[string]string) error {
	h.name = name
	h.zipPath = zipPath
	h.imageName = fmt.Sprintf("streaming-faas-%s", name)

	log.Printf("Creating minimal handler for function: %s", name)

	// Initialize Docker client
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	h.dockerClient = cli

	// Build Docker image
	ctx := context.Background()

	if err := ensureNetwork(ctx, h.dockerClient, faasNetworkName); err != nil {
		return err
	}

	// Create build context as a tar stream
	buildContext, err := h.createTarContext(zipPath)
	if err != nil {
		return fmt.Errorf("failed to create build context: %w", err)
	}
	defer buildContext.Close()

	log.Printf("Building Docker image: %s", h.imageName)
	buildResponse, err := h.dockerClient.ImageBuild(ctx, buildContext, types.ImageBuildOptions{
		Tags:           []string{h.imageName},
		Dockerfile:     "Dockerfile",
		Remove:         true,
		SuppressOutput: false,
	})
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	defer buildResponse.Body.Close()

	// Read build output and log it
	buildOutput, err := io.ReadAll(buildResponse.Body)
	if err != nil {
		return fmt.Errorf("failed to read build output: %w", err)
	}

	buildOutputStr := string(buildOutput)
	log.Printf("Docker build output:\n%s", buildOutputStr)

	// Check if build actually succeeded by looking for error indicators
	if strings.Contains(buildOutputStr, "error") || strings.Contains(buildOutputStr, "Error") ||
		strings.Contains(buildOutputStr, "failed") || strings.Contains(buildOutputStr, "Failed") {
		return fmt.Errorf("Docker build failed with output: %s", buildOutputStr)
	}

	// Check if build was successful by inspecting the response
	// Docker build can fail but still return without an error, so we need to parse the output
	if strings.Contains(buildOutputStr, "returned a non-zero exit code") ||
		strings.Contains(buildOutputStr, "returned non-zero code") {
		return fmt.Errorf("Docker build failed with output: %s", buildOutputStr)
	}

	// Try to inspect the image to verify it was created successfully
	_, _, err = h.dockerClient.ImageInspectWithRaw(ctx, h.imageName)
	if err != nil {
		return fmt.Errorf("Docker image %s was not created successfully: %w", h.imageName, err)
	}

	log.Printf("Docker image %s created successfully", h.imageName)

	// Create container (don't start yet)
	exposedPorts := make(nat.PortSet)
	exposedPorts["8000/tcp"] = struct{}{}

	containerConfig := &container.Config{
		Image:        h.imageName,
		ExposedPorts: exposedPorts,
	}
	containerConfig.Env = append(containerConfig.Env, fmt.Sprintf("FUNCTION_NAME=%s", name))
	if len(envs) > 0 {
		for k, v := range envs {
			containerConfig.Env = append(containerConfig.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	hostConfig := &container.HostConfig{
		PublishAllPorts: true,
	}

	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			faasNetworkName: {},
		},
	}

	resp, err := h.dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, networkingConfig, nil, "")
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	h.containerID = resp.ID
	log.Printf("Created container %s for function %s", h.containerID, name)

	return nil
}

func ensureNetwork(ctx context.Context, cli *client.Client, name string) error {
	networks, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list docker networks: %w", err)
	}
	for _, n := range networks {
		if n.Name == name {
			return nil
		}
	}
	if _, err := cli.NetworkCreate(ctx, name, types.NetworkCreate{
		Driver: "bridge",
	}); err != nil {
		return fmt.Errorf("failed to create docker network %q: %w", name, err)
	}
	return nil
}

// Start starts the container and gets the IP
func (h *MinimalHandler) Start() error {
	log.Printf("Starting minimal handler for function: %s", h.name)

	ctx := context.Background()

	// Start the container
	err := h.dockerClient.ContainerStart(ctx, h.containerID, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Get container info to extract IP
	containerInfo, err := h.dockerClient.ContainerInspect(ctx, h.containerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	// Get the IP address from the default network
	if containerInfo.NetworkSettings != nil && containerInfo.NetworkSettings.IPAddress != "" {
		h.ip = containerInfo.NetworkSettings.IPAddress
	} else {
		// Fallback to localhost if no IP found
		h.ip = "127.0.0.1"
	}

	// Get the mapped port for health checks
	var port string
	if containerInfo.NetworkSettings != nil && containerInfo.NetworkSettings.Ports != nil {
		if portBindings, exists := containerInfo.NetworkSettings.Ports["8000/tcp"]; exists && len(portBindings) > 0 {
			port = portBindings[0].HostPort
		}
	}
	h.hostPort = port

	// Perform health check
	err = h.waitForHealthCheck(port)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	log.Printf("Container %s started successfully with IP: %s", h.containerID, h.ip)
	return nil
}

// IPs returns the IP addresses (implements Handler interface)
func (h *MinimalHandler) IPs() []string {
	if h.ip == "" {
		return []string{}
	}
	return []string{h.ip}
}

// ConnectURL returns the HTTP URL for the function's /connect endpoint.
func (h *MinimalHandler) ConnectURL() string {
	if h.hostPort != "" {
		return fmt.Sprintf("http://localhost:%s/connect", h.hostPort)
	}
	if h.ip != "" {
		return fmt.Sprintf("http://%s:8000/connect", h.ip)
	}
	return ""
}

// Destroy stops and removes the container
func (h *MinimalHandler) Destroy() error {
	log.Printf("Destroying minimal handler for function: %s", h.name)

	if h.dockerClient == nil || h.containerID == "" {
		h.containerID = ""
		h.ip = ""
		h.hostPort = ""
		return nil
	}

	ctx := context.Background()
	timeoutSeconds := int((5 * time.Second) / time.Second)
	if err := h.dockerClient.ContainerStop(ctx, h.containerID, container.StopOptions{Timeout: &timeoutSeconds}); err != nil {
		if !client.IsErrNotFound(err) && !strings.Contains(strings.ToLower(err.Error()), "is not running") {
			return fmt.Errorf("failed to stop container %s: %w", h.containerID, err)
		}
	}

	if err := h.dockerClient.ContainerRemove(ctx, h.containerID, container.RemoveOptions{Force: true}); err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("failed to remove container %s: %w", h.containerID, err)
		}
	}

	h.containerID = ""
	h.ip = ""
	h.hostPort = ""

	return nil
}

// Logs returns the container logs
func (h *MinimalHandler) Logs() (io.Reader, error) {
	log.Printf("Getting logs for minimal handler: %s", h.name)

	// TODO: Implement container log retrieval
	// For now, return empty reader
	return strings.NewReader(""), nil
}

// createTarContext creates a tar stream with function code and runtime files
func (h *MinimalHandler) createTarContext(zipPath string) (io.ReadCloser, error) {
	// Create a temporary directory approach instead of streaming
	// This is more reliable for Docker builds
	buildDir, err := os.MkdirTemp("", "faas-build-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	// We'll clean this up after the tar is created
	defer func() {
		go func() {
			// Clean up after a delay to ensure Docker has read the tar
			time.Sleep(30 * time.Second)
			os.RemoveAll(buildDir)
		}()
	}()

	// Add function handler template
	runtimeDir := "./docker/runtimes/go"
	functionHandlerPath := filepath.Join(runtimeDir, "functionhandlerStream.go")

	handlerContent, err := os.ReadFile(functionHandlerPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read functionhandlerStream.go: %w", err)
	}

	err = os.WriteFile(filepath.Join(buildDir, "functionhandlerStream.go"), handlerContent, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write functionhandlerStream.go: %w", err)
	}

	// Create fn directory for user function files
	fnDir := filepath.Join(buildDir, "fn")
	err = os.MkdirAll(fnDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("failed to create fn directory: %w", err)
	}

	// Copy user function files
	err = filepath.Walk(zipPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Get relative path from zipPath
		relPath, err := filepath.Rel(zipPath, path)
		if err != nil {
			return err
		}

		// Read file content
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Write to fn directory
		dstPath := filepath.Join(fnDir, relPath)

		// Create parent directories if needed
		err = os.MkdirAll(filepath.Dir(dstPath), 0755)
		if err != nil {
			return err
		}

		err = os.WriteFile(dstPath, content, 0644)
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to copy function files: %w", err)
	}

	// Create Dockerfile
	dockerfileContent := `FROM golang:1.22-alpine3.19

WORKDIR /usr/src/app

COPY functionhandlerStream.go ./
COPY fn/* ./

RUN apk add --no-cache ffmpeg

RUN go mod init function 2>/dev/null || true
RUN go mod tidy 2>/dev/null || true
RUN go mod download 2>/dev/null || true
RUN CGO_ENABLED=0 go build -o handler .

EXPOSE 8000

CMD ["./handler"]
`

	err = os.WriteFile(filepath.Join(buildDir, "Dockerfile"), []byte(dockerfileContent), 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	log.Printf("Created build context in: %s", buildDir)

	// Now create a reliable tar stream
	return h.createTarFromDirectory(buildDir)
}

// createTarFromDirectory creates a tar stream from a directory
func (h *MinimalHandler) createTarFromDirectory(sourceDir string) (io.ReadCloser, error) {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		tw := tar.NewWriter(pw)
		defer func() {
			if err := tw.Close(); err != nil {
				log.Printf("Error closing tar writer: %v", err)
				pw.CloseWithError(err)
			}
		}()

		err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				log.Printf("Error walking path %s: %v", path, err)
				return err
			}

			// Skip the root directory itself
			if path == sourceDir {
				return nil
			}

			// Create relative path
			relPath, err := filepath.Rel(sourceDir, path)
			if err != nil {
				log.Printf("Error getting relative path: %v", err)
				return err
			}

			// Skip directories, but process files
			if info.IsDir() {
				return nil
			}

			// Create tar header
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				log.Printf("Error creating tar header for %s: %v", relPath, err)
				return err
			}

			// Use the relative path as the name in the tar
			header.Name = relPath

			// Write header
			if err := tw.WriteHeader(header); err != nil {
				log.Printf("Error writing tar header for %s: %v", relPath, err)
				return err
			}

			// Write file content
			file, err := os.Open(path)
			if err != nil {
				log.Printf("Error opening file %s: %v", path, err)
				return err
			}
			defer file.Close()

			if _, err := io.Copy(tw, file); err != nil {
				log.Printf("Error writing file content for %s: %v", relPath, err)
				return err
			}

			log.Printf("Added file to tar: %s", relPath)
			return nil
		})

		if err != nil {
			log.Printf("Error creating tar from directory: %v", err)
			pw.CloseWithError(err)
			return
		}

		log.Printf("Successfully created tar archive")
	}()

	return pr, nil
}

// waitForHealthCheck waits for the container to respond to health checks
func (h *MinimalHandler) waitForHealthCheck(port string) error {
	maxRetries := 30
	retryDelay := time.Second

	healthURL := fmt.Sprintf("http://localhost:%s/health", port)
	if port == "" {
		// If no port mapping, try container IP directly
		healthURL = fmt.Sprintf("http://%s:8000/health", h.ip)
	}

	for i := 0; i < maxRetries; i++ {
		resp, err := http.Get(healthURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			log.Printf("Health check passed for function %s", h.name)
			return nil
		}

		if resp != nil {
			resp.Body.Close()
		}

		log.Printf("Health check attempt %d/%d failed for function %s, retrying...", i+1, maxRetries, h.name)
		time.Sleep(retryDelay)
	}

	return fmt.Errorf("health check failed after %d attempts: %s", maxRetries, h.containerDiagnostics())
}

func (h *MinimalHandler) containerDiagnostics() string {
	ctx := context.Background()
	info, err := h.dockerClient.ContainerInspect(ctx, h.containerID)
	if err != nil {
		return fmt.Sprintf("inspect container %s: %v", h.containerID, err)
	}

	state := "unknown"
	exitCode := 0
	stateError := ""
	if info.State != nil {
		state = info.State.Status
		exitCode = info.State.ExitCode
		stateError = info.State.Error
	}

	logs, err := h.dockerClient.ContainerLogs(ctx, h.containerID, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "80",
	})
	if err != nil {
		return fmt.Sprintf("state=%s exit_code=%d state_error=%q; logs unavailable: %v", state, exitCode, stateError, err)
	}
	defer logs.Close()
	logBytes, readErr := io.ReadAll(logs)
	if readErr != nil {
		return fmt.Sprintf("state=%s exit_code=%d state_error=%q; logs unreadable: %v", state, exitCode, stateError, readErr)
	}
	return fmt.Sprintf("state=%s exit_code=%d state_error=%q; logs=%q", state, exitCode, stateError, strings.TrimSpace(string(logBytes)))
}
