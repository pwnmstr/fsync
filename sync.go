package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filesync/internal/config"
)

type FileMetadata struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
}

type DirectoryState struct {
	MappingID string                  `json:"mapping_id"`
	Files     map[string]FileMetadata `json:"files"`
}

type SyncService struct {
	config     *config.AppConfig
	syncDirMap map[string]string // MappingID -> LocalPath
	mu         sync.Mutex
	server     *http.Server
}

func NewSyncService(cfg *config.AppConfig) *SyncService {
	ss := &SyncService{
		config:     cfg,
		syncDirMap: make(map[string]string),
	}
	ss.UpdateMappings(cfg.Mappings)
	return ss
}

func (ss *SyncService) UpdateMappings(mappings []config.SyncMapping) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.syncDirMap = make(map[string]string)
	for _, m := range mappings {
		ss.syncDirMap[m.ID] = m.LocalPath
	}
}

func (ss *SyncService) StartServer(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/state", ss.handleState)
	mux.HandleFunc("/download", ss.handleDownload)
	mux.HandleFunc("/upload", ss.handleUpload)

	ss.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		if err := ss.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()
}

func (ss *SyncService) StopServer() {
	if ss.server != nil {
		ss.server.Close()
	}
}

// GenerateState walks the directory and generates the current state.
func (ss *SyncService) GenerateState(mappingID string) (*DirectoryState, error) {
	ss.mu.Lock()
	localPath, ok := ss.syncDirMap[mappingID]
	ss.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("mapping ID %s not found", mappingID)
	}

	state := &DirectoryState{
		MappingID: mappingID,
		Files:     make(map[string]FileMetadata),
	}

	err := filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors like permission denied
		}
		
		// Skip root and .backup files
		if path == localPath || strings.HasSuffix(path, ".backup") {
			return nil
		}

		relPath, err := filepath.Rel(localPath, path)
		if err != nil {
			return nil
		}
		
		// Normalize slashes for cross-platform
		relPath = filepath.ToSlash(relPath)

		state.Files[relPath] = FileMetadata{
			Path:    relPath,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		}
		return nil
	})

	return state, err
}

func (ss *SyncService) handleState(w http.ResponseWriter, r *http.Request) {
	mappingID := r.URL.Query().Get("id")
	state, err := ss.GenerateState(mappingID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(state)
}

func isSubdirectory(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func (ss *SyncService) handleDownload(w http.ResponseWriter, r *http.Request) {
	mappingID := r.URL.Query().Get("id")
	filePath := r.URL.Query().Get("path")

	ss.mu.Lock()
	localPath, ok := ss.syncDirMap[mappingID]
	ss.mu.Unlock()

	if !ok {
		http.Error(w, "Mapping not found", http.StatusNotFound)
		return
	}

	fullPath := filepath.Join(localPath, filepath.FromSlash(filePath))
	if !isSubdirectory(localPath, fullPath) {
		http.Error(w, "Invalid path", http.StatusForbidden)
		return
	}
	http.ServeFile(w, r, fullPath)
}

func (ss *SyncService) handleUpload(w http.ResponseWriter, r *http.Request) {
	mappingID := r.FormValue("id")
	filePath := r.FormValue("path")
	modTimeStr := r.FormValue("mod_time")

	ss.mu.Lock()
	localPath, ok := ss.syncDirMap[mappingID]
	ss.mu.Unlock()

	if !ok {
		http.Error(w, "Mapping not found", http.StatusNotFound)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error reading file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	fullPath := filepath.Join(localPath, filepath.FromSlash(filePath))
	if !isSubdirectory(localPath, fullPath) {
		http.Error(w, "Invalid path", http.StatusForbidden)
		return
	}
	
	// Create directories if they don't exist
	os.MkdirAll(filepath.Dir(fullPath), 0755)

	// Three-way merge / Conflict resolution
	// If file exists, check if it's a conflict
	if _, err := os.Stat(fullPath); err == nil {
		// Backup old file
		backupPath := fullPath + ".backup"
		os.Rename(fullPath, backupPath)
		fmt.Printf("Conflict resolved, backed up to %s\n", backupPath)
	}

	out, err := os.Create(fullPath)
	if err != nil {
		http.Error(w, "Error creating file", http.StatusInternalServerError)
		return
	}
	defer out.Close()
	io.Copy(out, file)

	// Try to restore modification time if provided
	if modTimeStr != "" {
		if modTime, err := time.Parse(time.RFC3339Nano, modTimeStr); err == nil {
			os.Chtimes(fullPath, modTime, modTime)
		}
	}
	
	w.WriteHeader(http.StatusOK)
}

// PerformSync triggers a sync with a peer for all known mappings.
func (ss *SyncService) PerformSync(peerIP string, peerPort int) error {
	ss.mu.Lock()
	mappings := make([]string, 0, len(ss.syncDirMap))
	for id := range ss.syncDirMap {
		mappings = append(mappings, id)
	}
	ss.mu.Unlock()

	for _, id := range mappings {
		err := ss.syncMappingWithPeer(id, peerIP, peerPort)
		if err != nil {
			fmt.Printf("Error syncing mapping %s with %s: %v\n", id, peerIP, err)
		}
	}
	return nil
}

func (ss *SyncService) syncMappingWithPeer(mappingID, peerIP string, peerPort int) error {
	peerURL := fmt.Sprintf("http://%s:%d/state?id=%s", peerIP, peerPort, mappingID)
	resp, err := http.Get(peerURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("peer returned %d", resp.StatusCode)
	}

	var remoteState DirectoryState
	if err := json.NewDecoder(resp.Body).Decode(&remoteState); err != nil {
		return err
	}

	localState, err := ss.GenerateState(mappingID)
	if err != nil {
		return err
	}

	ss.mu.Lock()
	localDir := ss.syncDirMap[mappingID]
	ss.mu.Unlock()

	// Compare states
	// 1. Download new or updated files from remote
	for path, remoteFile := range remoteState.Files {
		localFile, exists := localState.Files[path]
		
		if remoteFile.IsDir {
			fullPath := filepath.Join(localDir, filepath.FromSlash(path))
			os.MkdirAll(fullPath, 0755)
			continue
		}

		if !exists || remoteFile.ModTime.After(localFile.ModTime) {
			// Download
			err := ss.downloadFile(mappingID, path, remoteFile.ModTime, peerIP, peerPort, localDir)
			if err != nil {
				fmt.Printf("Failed to download %s: %v\n", path, err)
			}
		}
	}

	// 2. Upload our newer files to remote
	for path, localFile := range localState.Files {
		if localFile.IsDir {
			continue
		}
		remoteFile, exists := remoteState.Files[path]
		if !exists || localFile.ModTime.After(remoteFile.ModTime) {
			// Upload
			err := ss.uploadFile(mappingID, path, localFile.ModTime, peerIP, peerPort, localDir)
			if err != nil {
				fmt.Printf("Failed to upload %s: %v\n", path, err)
			}
		}
	}

	return nil
}

func (ss *SyncService) downloadFile(mappingID, path string, modTime time.Time, peerIP string, peerPort int, localDir string) error {
	escapedPath := url.QueryEscape(strings.ReplaceAll(path, "\\", "/"))
	urlStr := fmt.Sprintf("http://%s:%d/download?id=%s&path=%s", peerIP, peerPort, mappingID, escapedPath)
	resp, err := http.Get(urlStr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %d", resp.StatusCode)
	}

	fullPath := filepath.Join(localDir, filepath.FromSlash(path))
	os.MkdirAll(filepath.Dir(fullPath), 0755)

	// Backup if exists
	if _, err := os.Stat(fullPath); err == nil {
		os.Rename(fullPath, fullPath+".backup")
	}

	out, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err == nil {
		os.Chtimes(fullPath, modTime, modTime)
	}
	return err
}

func (ss *SyncService) uploadFile(mappingID, path string, modTime time.Time, peerIP string, peerPort int, localDir string) error {
	urlStr := fmt.Sprintf("http://%s:%d/upload", peerIP, peerPort)
	fullPath := filepath.Join(localDir, filepath.FromSlash(path))

	file, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	
	writer.WriteField("id", mappingID)
	writer.WriteField("path", path)
	writer.WriteField("mod_time", modTime.Format(time.RFC3339Nano))
	
	part, err := writer.CreateFormFile("file", filepath.Base(fullPath))
	if err != nil {
		return err
	}
	
	io.Copy(part, file)
	writer.Close()

	req, err := http.NewRequest("POST", urlStr, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload failed: %d", resp.StatusCode)
	}
	return nil
}
