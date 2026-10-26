package manager

import (
	"archive/zip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"streaming-faas/docker"

	"github.com/google/uuid"
)

const (
	TmpDir = "./tmp"
)

var ErrFunctionNotFound = errors.New("function not found")

type ManagementService struct {
	functionHandlers      map[string]*functionEntry
	functionHandlersMutex sync.Mutex
}

type functionStatus int

const (
	statusCreated functionStatus = iota
	statusStarting
	statusRunning
	statusFailed
)

type functionEntry struct {
	handler *docker.MinimalHandler
	status  functionStatus
	err     error
	waitCh  chan struct{}
}

func New() *ManagementService {

	ms := &ManagementService{
		functionHandlers: make(map[string]*functionEntry),
	}

	return ms
}

func (ms *ManagementService) Upload(name string, zipped string, envs map[string]string) (string, error) {

	// b64 decode zip
	zip, err := base64.StdEncoding.DecodeString(zipped)
	if err != nil {
		log.Println(err)
		return "", err
	}

	// create function handler
	n, err := ms.createFunction(name, zip, "", envs)

	if err != nil {
		log.Println(err)
		return "", err
	}

	return n, nil

}

func (ms *ManagementService) createFunction(name string, funczip []byte, subfolderPath string, envs map[string]string) (string, error) {
	// make a uuidv4 for the function
	uuid, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}

	log.Println("creating function", name, "with uuid", uuid.String())

	// create a new function handler

	p := path.Join(TmpDir, uuid.String())

	err = os.MkdirAll(p, 0777)

	if err != nil {
		return "", err
	}

	log.Println("created folder", p)

	// write zip to file
	zipPath := path.Join(TmpDir, uuid.String()+".zip")
	err = os.WriteFile(zipPath, funczip, 0777)

	if err != nil {
		return "", err
	}

	err = unzip(zipPath, p)

	if err != nil {
		return "", err
	}

	defer func() {
		// remove folder
		err = os.RemoveAll(p)
		if err != nil {
			log.Println("error removing folder", p, err)
		}

		err = os.Remove(zipPath)
		if err != nil {
			log.Println("error removing zip", zipPath, err)
		}

		log.Println("removed folder", p)
		log.Println("removed zip", zipPath)
	}()

	if subfolderPath != "" {
		p = path.Join(p, subfolderPath)
	}

	// if function already exists, keep it while deploying the new version
	var oldHandler *docker.MinimalHandler
	if existingEntry, ok := ms.functionHandlers[name]; ok {
		oldHandler = existingEntry.handler
	}

	// create new function handler
	ms.functionHandlersMutex.Lock()
	defer ms.functionHandlersMutex.Unlock()

	// Create a new minimal handler instance
	fh := &docker.MinimalHandler{}

	// Set it up with the function code
	err = fh.Create(name, p, envs)
	if err != nil {
		return "", err
	}

	ms.functionHandlers[name] = &functionEntry{
		handler: fh,
		status:  statusCreated,
	}

	// destroy the old handler if it exists
	if oldHandler != nil {
		err = oldHandler.Destroy()
		if err != nil {
			return "", err
		}
	}

	return name, nil
}

// EnsureRunning starts the function container if needed and returns the handler.
func (ms *ManagementService) EnsureRunning(name string) (*docker.MinimalHandler, error) {
	ms.functionHandlersMutex.Lock()
	entry, ok := ms.functionHandlers[name]
	if !ok {
		ms.functionHandlersMutex.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrFunctionNotFound, name)
	}

	if entry.status == statusRunning {
		h := entry.handler
		ms.functionHandlersMutex.Unlock()
		return h, nil
	}

	if entry.status == statusStarting {
		waitCh := entry.waitCh
		ms.functionHandlersMutex.Unlock()
		<-waitCh
		ms.functionHandlersMutex.Lock()
		defer ms.functionHandlersMutex.Unlock()
		if entry.status == statusRunning {
			return entry.handler, nil
		}
		if entry.err != nil {
			return nil, entry.err
		}
		return nil, fmt.Errorf("function failed to start: %s", name)
	}

	// statusCreated or statusFailed: attempt start
	entry.status = statusStarting
	entry.err = nil
	entry.waitCh = make(chan struct{})
	h := entry.handler
	ms.functionHandlersMutex.Unlock()

	err := h.Start()
	if err != nil {
		if destroyErr := h.Destroy(); destroyErr != nil {
			log.Printf("failed to clean up function %s after start failure: %v", name, destroyErr)
		}
	}

	ms.functionHandlersMutex.Lock()
	if err != nil {
		entry.status = statusFailed
		entry.err = err
	} else {
		entry.status = statusRunning
	}
	close(entry.waitCh)
	ms.functionHandlersMutex.Unlock()

	if err != nil {
		return nil, err
	}

	return h, nil
}

// unzip extracts a zip archive to a destination directory
func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	// Create destination directory
	err = os.MkdirAll(dest, 0755)
	if err != nil {
		return err
	}

	// Extract files
	for _, f := range r.File {
		path := filepath.Join(dest, f.Name)

		// Check for path traversal
		if !strings.HasPrefix(path, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid file path: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			err := os.MkdirAll(path, f.FileInfo().Mode())
			if err != nil {
				return err
			}
			continue
		}

		// Create parent directories
		err := os.MkdirAll(filepath.Dir(path), 0755)
		if err != nil {
			return err
		}

		// Extract file
		rc, err := f.Open()
		if err != nil {
			return err
		}

		outFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}
