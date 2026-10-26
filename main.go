package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"streaming-faas/manager"
)

const (
	ConfigPort = 8080
)

type server struct {
	ms *manager.ManagementService
}

func main() {

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetPrefix("manager: ")

	ms := manager.New()

	s := &server{
		ms: ms,
	}

	// create handlers
	r := http.NewServeMux()
	r.HandleFunc("/upload", s.uploadHandler)
	r.HandleFunc("/connect", s.connectHandler)

	// Start the HTTP server
	log.Printf("Starting FaaS server on port %d", ConfigPort)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", ConfigPort), r))
}

func (s *server) uploadHandler(w http.ResponseWriter, r *http.Request) {
	d := struct {
		FunctionName string            `json:"name"`
		FunctionZip  string            `json:"zip"`
		FunctionEnvs map[string]string `json:"envs,omitempty"` // Optional env vars
	}{}

	err := json.NewDecoder(r.Body).Decode(&d)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		log.Println(err)
		return
	}

	log.Println("got request to upload function: Name", d.FunctionName, "Bytes", len(d.FunctionZip), "Envs", d.FunctionEnvs)

	envs := d.FunctionEnvs
	if envs == nil {
		envs = make(map[string]string)
	}

	res, err := s.ms.Upload(d.FunctionName, d.FunctionZip, envs)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	// return success
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, res)
}

func (s *server) connectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		log.Println(err)
		return
	}

	d := struct {
		FunctionName string `json:"name"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
	}{}

	if err := json.Unmarshal(body, &d); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		log.Println(err)
		return
	}

	if d.FunctionName == "" {
		w.WriteHeader(http.StatusBadRequest)
		log.Println("connect request missing function name")
		return
	}

	h, err := s.ms.EnsureRunning(d.FunctionName)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, manager.ErrFunctionNotFound) {
			status = http.StatusNotFound
		}
		log.Println(err)
		http.Error(w, err.Error(), status)
		return
	}

	connectURL := h.ConnectURL()
	if connectURL == "" {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println("connect URL not available for function", d.FunctionName)
		return
	}

	req, err := http.NewRequest(http.MethodPost, connectURL, bytes.NewReader(body))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	for k, v := range r.Header {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		log.Println(err)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
