package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ConnectRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type StreamConnection struct {
	ID      int
	Name    string
	Headers map[string]string
	Conn    net.Conn
}

type outputTarget struct {
	name       string
	mode       string
	addr       string
	handlerURL string
	conn       net.Conn
	mu         sync.Mutex
}

func tcpConnectHandler(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("/connect invalid request: %v", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	headers := make(map[string]string)
	for k, v := range r.Header {
		headers[k] = v[0]
	}

	addr := fmt.Sprintf("%s:%d", req.Host, req.Port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Printf("/connect dial failed addr=%s err=%v", addr, err)
		http.Error(w, "Connection failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Respond immediately to the POST
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("connection established"))

	// Start reading/writing in a separate goroutine
	streamID := int(atomic.AddInt64(&nextInputID, 1))
	streamName := strings.TrimSpace(headers["X-Faas-Stream-Name"])
	if streamName == "" {
		streamName = strings.TrimSpace(headers["X-Faas-Upstream-Function"])
	}
	go handleConnection(StreamConnection{
		ID:      streamID,
		Name:    streamName,
		Headers: headers,
		Conn:    conn,
	})
}

func handleConnection(stream StreamConnection) {
	defer stream.Conn.Close()

	if OnStreamConnected != nil {
		if err := OnStreamConnected(stream); err != nil && err != io.EOF {
			log.Printf("stream handler error stream_id=%d name=%q err=%v", stream.ID, stream.Name, err)
		}
		log.Printf("connection closed stream_id=%d name=%q", stream.ID, stream.Name)
		return
	}

	buf := make([]byte, 1024)
	for {
		n, err := stream.Conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Println("read error:", err)
			}
			break
		}
		out, fnErr := fn(string(buf[:n]), stream.Headers) // returns string, err
		if fnErr != nil {
			log.Printf("fn error: %v", fnErr)
		}
		if out != "" {
			sendOutput(out)
		}

		if ackEnabled() {
			// echo back or process
			_, err = stream.Conn.Write([]byte("ack\n"))
			if err != nil {
				log.Println("write error:", err)
				break
			}
		}
	}
	log.Printf("connection closed stream_id=%d name=%q", stream.ID, stream.Name)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func main() {
	// Optional user hook: user code can assign to OnInit in its own init().
	OnInit()
	initOutputConn()
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/connect", tcpConnectHandler)
	log.Println("function listening on :8000")
	log.Fatal(http.ListenAndServe(":8000", nil))
}

// OnInit is an optional hook that user code can override by assignment.
// Example in user function:
//
//	func init() { OnInit = func() { /* setup */ } }
var OnInit = func() {}
var OnStreamConnected func(StreamConnection) error

var (
	binaryMode bool

	handlerHost  string
	handlerPort  string
	handlerName  string
	handlerNames []string

	listenHost    string
	listenPort    string
	advertiseHost string
	outputTargets []*outputTarget
	nextInputID   int64
)

func initOutputConn() {
	binaryMode = parseBoolEnv("BINARY_MODE")

	handlerHost = os.Getenv("OUTPUT_HANDLER_HOST")
	handlerPort = os.Getenv("OUTPUT_HANDLER_PORT")
	handlerName = os.Getenv("OUTPUT_HANDLER_NAME")
	handlerNames = parseCSVEnv("OUTPUT_HANDLER_NAMES")

	if handlerHost != "" && handlerPort != "" && len(handlerNames) > 0 {
		initNamedHandlerOutputs()
		return
	}

	if handlerHost != "" && handlerPort != "" {
		initOutputTargets([]*outputTarget{newHandlerTarget(handlerName)})
		return
	}

	host := os.Getenv("OUTPUT_HOST")
	portStr := os.Getenv("OUTPUT_PORT")
	if host == "" || portStr == "" {
		log.Println("OUTPUT_HOST/OUTPUT_PORT or OUTPUT_HANDLER_HOST/OUTPUT_HANDLER_PORT not set; output disabled")
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		log.Printf("invalid OUTPUT_PORT %q: %v", portStr, err)
		return
	}

	initOutputTargets([]*outputTarget{{
		mode: "tcp",
		addr: net.JoinHostPort(host, strconv.Itoa(port)),
	}})
}

func sendOutput(payload string) {
	if err := SendOutputBytes([]byte(payload)); err != nil {
		log.Printf("output write failed: %v", err)
	}
}

func SendOutputBytes(payload []byte) error {
	if len(outputTargets) == 0 {
		return nil
	}
	return writeOutput(outputTargets[0], payload)
}

func SendOutputBytesTo(name string, payload []byte) error {
	target := findOutputTargetByName(name)
	if target == nil {
		return fmt.Errorf("output target %q not configured", name)
	}
	return writeOutput(target, payload)
}

func SendOutputBytesToIndex(index int, payload []byte) error {
	if index < 0 || index >= len(outputTargets) {
		return fmt.Errorf("output target index %d out of range", index)
	}
	return writeOutput(outputTargets[index], payload)
}

func OutputTargetNames() []string {
	names := make([]string, 0, len(outputTargets))
	for _, target := range outputTargets {
		names = append(names, target.name)
	}
	return names
}

func ExpectedInputConnections() int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("INPUT_EXPECTED_CONNECTIONS")))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func writeOutput(target *outputTarget, payload []byte) error {
	conn := getOutputConn(target)
	if conn == nil {
		return fmt.Errorf("output target %q unavailable", target.name)
	}

	out := payload
	if !binaryMode {
		out = append(out, '\n')
	}

	if _, err := conn.Write(out); err != nil {
		log.Printf("output write failed target=%q addr=%s err=%v", target.name, target.addr, err)
		resetOutputConn(target)
		// best-effort retry once
		conn = getOutputConn(target)
		if conn != nil {
			if _, err := conn.Write(out); err != nil {
				return fmt.Errorf("output retry failed target=%q addr=%s: %w", target.name, target.addr, err)
			}
		}
	}
	return nil
}

func getOutputConn(target *outputTarget) net.Conn {
	target.mu.Lock()
	defer target.mu.Unlock()

	if target.conn != nil {
		return target.conn
	}
	if target.mode == "handler" {
		return target.conn
	}
	if target.addr == "" {
		return nil
	}

	conn, err := net.Dial("tcp", target.addr)
	if err != nil {
		log.Printf("output dial failed target=%q addr=%s err=%v", target.name, target.addr, err)
		return nil
	}
	target.conn = conn
	return target.conn
}

func resetOutputConn(target *outputTarget) {
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.conn != nil {
		_ = target.conn.Close()
		target.conn = nil
	}
}

func ackEnabled() bool {
	if binaryMode {
		return false
	}
	return true
}

func parseBoolEnv(name string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func initNamedHandlerOutputs() {
	targets := make([]*outputTarget, 0, len(handlerNames))
	for _, name := range handlerNames {
		targets = append(targets, newHandlerTarget(name))
	}
	initOutputTargets(targets)
}

func newHandlerTarget(name string) *outputTarget {
	return &outputTarget{
		name: strings.TrimSpace(name),
		mode: "handler",
	}
}

func initOutputTargets(targets []*outputTarget) {
	outputTargets = targets
	for _, target := range outputTargets {
		if target.mode == "handler" {
			initHandlerOutput(target)
			continue
		}
		_ = getOutputConn(target)
	}
}

func initHandlerOutput(target *outputTarget) {
	listenHost = os.Getenv("OUTPUT_LISTEN_HOST")
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}
	advertiseHost = resolveAdvertiseHost(listenHost)

	targetPort := ""
	if len(outputTargets) == 1 {
		targetPort = strings.TrimSpace(os.Getenv("OUTPUT_LISTEN_PORT"))
	}
	addr := net.JoinHostPort(listenHost, "0")
	if targetPort != "" {
		addr = net.JoinHostPort(listenHost, targetPort)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("output listener error target=%q addr=%s err=%v", target.name, addr, err)
		return
	}
	target.addr = ln.Addr().String()
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		targetPort = strconv.Itoa(tcpAddr.Port)
	}
	log.Printf("output listener bound target=%q on %s:%s (advertise=%s)", target.name, listenHost, targetPort, advertiseHost)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("output accept error target=%q: %v", target.name, err)
			return
		}
		target.mu.Lock()
		target.conn = conn
		target.mu.Unlock()
		log.Printf("output handler connected target=%q from %s", target.name, conn.RemoteAddr())
	}()

	target.handlerURL = "http://" + net.JoinHostPort(handlerHost, handlerPort) + "/connect"
	var body string
	if target.name != "" {
		body = fmt.Sprintf(`{"name":"%s","host":"%s","port":%s}`, target.name, advertiseHost, targetPort)
	} else {
		body = fmt.Sprintf(`{"host":"%s","port":%s}`, advertiseHost, targetPort)
	}
	headers := map[string]string{
		"X-Faas-Upstream-Function": os.Getenv("FUNCTION_NAME"),
	}
	if target.name != "" {
		headers["X-Faas-Stream-Name"] = target.name
	}
	if err := connectWithRetry(target.handlerURL, body, headers, 5, 500*time.Millisecond); err != nil {
		log.Fatalf("output handler connect failed: %v", err)
	}
}

func connectWithRetry(url, body string, headers map[string]string, attempts int, baseDelay time.Duration) error {
	var lastErr error
	for i := 1; i <= attempts; i++ {
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			if strings.TrimSpace(v) == "" {
				continue
			}
			req.Header.Set(k, v)
		}

		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp != nil {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("%s body=%q", resp.Status, string(respBody))
		}

		lastErr = err
		log.Printf("output handler connect attempt %d/%d failed: %v url=%s payload=%s", i, attempts, err, url, body)
		time.Sleep(baseDelay * time.Duration(i))
	}
	return lastErr
}

func findOutputTargetByName(name string) *outputTarget {
	for _, target := range outputTargets {
		if target.name == name {
			return target
		}
	}
	return nil
}

func parseCSVEnv(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		values = append(values, part)
	}
	return values
}

func resolveAdvertiseHost(bindHost string) string {
	if v := os.Getenv("OUTPUT_ADVERTISE_HOST"); v != "" {
		return v
	}
	if bindHost != "" && bindHost != "0.0.0.0" && bindHost != "::" {
		return bindHost
	}
	if ip := firstNonLoopbackIPv4(); ip != "" {
		return ip
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "127.0.0.1"
}

func firstNonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			return ip.String()
		}
	}
	return ""
}
