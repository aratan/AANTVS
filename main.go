package main

import (
	"bytes"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aantvs/internal/p2p"
)

// Allowed extensions and their MIME types. Nil means not allowed.
var extAllowed = map[string]string{
	".mp4": "video/mp4", ".webm": "video/webm", ".ogg": "video/ogg",
	".jpg":  "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif":  "image/gif",  ".webp": "image/webp",
	".csv":  "text/csv",   ".json": "application/json",  ".txt": "text/plain",
	".pdf":  "application/pdf",
}

// readLimit reads up to maxBytes from r; returns what was read.
func readLimit(r io.Reader, maxBytes int64) ([]byte, error) {
	data := &bytes.Buffer{}
	if _, err := io.CopyN(data, r, int64(maxBytes)); err != nil && err != io.EOF {
		return data.Bytes(), err
	}
	return data.Bytes(), nil
}

// randHex returns n random hex characters.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	_, err := crand.Read(b)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// --- Tipos ---

type Peliculas struct {
	Author string `json:"author"`
	Groups []struct {
		Image    string `json:"image"`
		Name     string `json:"name"`
		Stations []struct {
			Embed           string `json:"embed"`
			Image           string `json:"image"`
			Name            string `json:"name"`
			PlayInNatPlayer string `json:"playInNatPlayer"`
			URL             string `json:"url"`
		} `json:"stations"`
	} `json:"groups"`
	Image string `json:"image"`
	Info  string `json:"info"`
	Name  string `json:"name"`
	URL   string `json:"url"`
}

type MovieCard struct {
	Name            string `json:"name"`
	Image           string `json:"image"`              // poster/screenshot URL
	URL             string `json:"url"`                // video source direct link
	Embed           string `json:"embed"`              // iframe embed URL
	PlayInNatPlayer string `json:"playInNatPlayer"`    // NatPlayer manifest/hash
	SafePageIdx     int    // URL parameter: "pelis?id=X"
}

type pageData struct {
	Title       string
	CompanyName string
	MovieCards  []MovieCard   // replaces Chtml + Lfoto
	Active      MovieCard     // selected movie, replaces Npeli/Nfoto/Nurl/Dhora/Texto
}

// --- Estado (solo lectura después de startup) ---

var (
	t              *template.Template
	routeMatch     = regexp.MustCompile(`^\/(\w+)`)
	peliculas      Peliculas
	initialPageData pageData // solo se escribe en startup, lectura concurrente segura
	p2pSwarm       *p2p.Swarm // reference to P2P swarm for handlers
)

// --- Inicialización ---

func init() {
	var err error
	t, err = template.ParseGlob("*.html")
	if err != nil {
		log.Println("Cannot parse templates:", err)
		os.Exit(1)
	}
}

// --- Handlers HTTP ---

func root(w http.ResponseWriter, r *http.Request) {
	matches := routeMatch.FindStringSubmatch(r.URL.Path)

	if len(matches) >= 1 {
		page := matches[1] + ".html"
		if t.Lookup(page) != nil {
			w.WriteHeader(200)
			t.ExecuteTemplate(w, page, initialPageData)
			return
		}
	} else if r.URL.Path == "/" {
		w.WriteHeader(200)
		t.ExecuteTemplate(w, "index.html", initialPageData)
		return
	}
	w.WriteHeader(404)
	w.Write([]byte("NOT FOUND "))
}

func pelis(w http.ResponseWriter, r *http.Request) {
	idParam := r.URL.Query().Get("id")
	if idParam == "" {
		http.Error(w, "Missing id parameter", http.StatusBadRequest)
		return
	}
	idx, err := strconv.Atoi(idParam)
	if err != nil {
		http.Error(w, "Invalid id parameter", http.StatusBadRequest)
		return
	}

	pd, ok := validateIdx(idx)
	if !ok {
		http.Error(w, "Invalid video index", http.StatusBadRequest)
		return
	}

	t.ExecuteTemplate(w, "index.html", pd)
}

func validateIdx(idx int) (pageData, bool) {
	totalStations := len(peliculas.Groups[0].Stations)
	if totalStations == 0 {
		return pageData{}, false
	}
	startIdx := idx
	if startIdx < 0 || startIdx >= totalStations {
		startIdx = totalStations - 1
	}
	pd := buildList(startIdx)
	return pd, true
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "api/upload.html")
}

func uploader(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// 1. Method check
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	const maxFileSize = 50 << 20 // 50 MB consistent on BOTH sides

	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize)
	if err := r.ParseMultipartForm(maxFileSize); err != nil {
		switch {
		case err.Error() == "http: request body too large":
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(map[string]string{"error": "request body exceeds 50 MB limit"})
		default:
			log.Println("uploader:", err)
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid multipart form"})
		}
		return
	}

	if len(r.MultipartForm.File) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "no file provided"})
		return
	}

	// 2. Open the uploaded file (only one allowed per request)
	headers := r.MultipartForm.File["archivo"]
	if len(headers) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "missing 'archivo' field"})
		return
	}

	f, err := headers[0].Open()
	if err != nil {
		log.Println("uploader: fopen:", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to open uploaded file"})
		return
	}

	// 3. Sniff first 512 bytes for MIME detection
	const sniffLen = 512
	sniff, err := readLimit(f, sniffLen)
	if err != nil {
		f.Close()
		log.Println("uploader: sniff:", err)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to inspect file data"})
		return
	}

	ext := strings.ToLower(filepath.Ext(headers[0].Filename))

	// 4. Extension whitelist
	if _, allowed := extAllowed[ext]; !allowed {
		f.Close()
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "file type not allowed"})
		return
	}

	// 5. Real MIME check against extension declaration
	got := http.DetectContentType(sniff)
	expected := extAllowed[ext]
	if expected != got {
		f.Close()
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "file content type mismatch (expected " + expected + ", detected " + got + ")"})
		return
	}

	// 6. Generate collision-proof filename
	hash, randErr := randHex(16)
	if randErr != nil {
		f.Close()
		log.Println("uploader: rand:", randErr)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal error"})
		return
	}

	destName := fmt.Sprintf("%s%s", hash, ext)
	destPath := filepath.Join("api", destName)

	if _, statErr := os.Stat(destPath); statErr == nil {
		f.Close()
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "filename collision"})
		return
	}

	// 7. Save the file
	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		f.Close()
		log.Println("uploader: save:", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to create file"})
		return
	}

	// Seek back to start and copy the whole remaining body
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		f.Close(); out.Close()
		os.Remove(destPath)
		log.Println("uploader: seek:", seekErr)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to save file"})
		return
	}

	n, err := io.Copy(out, f)
	f.Close(); out.Close()

	if err != nil {
		os.Remove(destPath)
		log.Println("uploader: writefile:", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to save file"})
		return
	}

	// 8. Success — structured JSON response
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"file": destName,
		"path": "/api/" + destName,
		"type": ext,
		"size": n,
	})
}

// --- Helpers ---

func azar() int {
	min, max := 0, 20
	return rand.Intn(max-min) + min
}

// jsonUnquoteKeys adds missing opening quotes to JSON object keys.
// Fixes upstream data where keys like name": appear instead of "name":.
func jsonUnquoteKeys(body []byte) []byte {
	// Match key names not preceded by "
	re := regexp.MustCompile(`(^|[^"])(")?(name|image|url|embed|playInNatPlayer|userAgent)\s*"?\s*:`)
	return re.ReplaceAll(body, []byte(`$1"$3":`))
}

func fetchJSON(client *http.Client) error {
	url := "https://pastebin.com/raw/s6DTUHCA"
	res, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetching JSON: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching JSON: unexpected status %d", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("reading JSON body: %w", err)
	}

	// Sanitize JSON: some keys in the upstream API are missing opening quotes
	body = jsonUnquoteKeys(body)

	if err := json.Unmarshal(body, &peliculas); err != nil {
		return fmt.Errorf("unmarshaling JSON: %w", err)
	}

	return nil
}

func buildList(startIdx int) pageData {
	total := len(peliculas.Groups[0].Stations)
	cards := make([]MovieCard, total)

	for idx := 0; idx < total; idx++ {
		s := &peliculas.Groups[0].Stations[idx]
		cards[idx] = MovieCard{
			Name:            html.EscapeString(s.Name),
			Image:           html.EscapeString(s.Image),
			URL:             html.EscapeString(s.URL),
			Embed:           html.EscapeString(s.Embed),
			PlayInNatPlayer: s.PlayInNatPlayer, // text content, not placed in attribute context
			SafePageIdx:     idx,
		}
	}

	if startIdx < 0 || startIdx >= total {
		startIdx = total - 1
	}

	return pageData{
		Title:       "Cine Online",
		CompanyName: peliculas.Name,
		MovieCards:  cards,
		Active: MovieCard{
			Name:            html.EscapeString(peliculas.Groups[0].Stations[startIdx].Name),
			Image:           html.EscapeString(peliculas.Groups[0].Stations[startIdx].Image),
			URL:             html.EscapeString(peliculas.Groups[0].Stations[startIdx].URL),
			Embed:           html.EscapeString(peliculas.Groups[0].Stations[startIdx].Embed),
			PlayInNatPlayer: peliculas.Groups[0].Stations[startIdx].PlayInNatPlayer,
			SafePageIdx:     startIdx,
		},
	}
}

func hora() int {
	t := time.Now()
	fecha := fmt.Sprintf("%02d", t.Hour())
	i, _ := strconv.Atoi(fecha)
	return i
}

// findLocalFile finds a file by station index in the api/ directory.
func findLocalFile(idx int) string {
	entries, err := os.ReadDir("api")
	if err != nil {
		return ""
	}
	fileIdx := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if _, allowed := extAllowed[ext]; !allowed {
			continue
		}
		if fileIdx == idx {
			return filepath.Join("api", name)
		}
		fileIdx++
	}
	return ""
}

// --- QoS Handler ---

// QoSMetrics holds P2P network quality metrics.
type QoSMetrics struct {
	Peers       int     `json:"peers"`
	Seeds       int     `json:"seeds"`
	P2PRatio    float64 `json:"p2p_ratio"`
	SpeedBPS    int     `json:"speed_bps"`
	BadPieces   int     `json:"bad_pieces"`
	AvgLatency  float64 `json:"avg_latency_ms"`
	BufferPct   float64 `json:"buffer_pct"`
}

// qosHandler returns current P2P swarm metrics as JSON.
// Returns real data when P2P is active, zero values when not.
func qosHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	metrics := QoSMetrics{}

	if p2pSwarm != nil {
		// Get real peer count from libp2p
		metrics.Peers = p2pSwarm.GetLibp2pPeerCount()

		// Calculate bad pieces from reputation stats
		if stats := p2pSwarm.GetReputationStats(); stats != nil {
			for _, s := range stats {
				metrics.BadPieces += s.BadChunks
				metrics.AvgLatency += s.AvgLatency
				if s.TotalChunks > 0 {
					metrics.BufferPct += float64(s.TotalChunks-s.BadChunks) / float64(s.TotalChunks) * 100
				}
			}
			if len(stats) > 0 {
				metrics.AvgLatency /= float64(len(stats))
				metrics.BufferPct /= float64(len(stats))
			}
		}
	}

	json.NewEncoder(w).Encode(metrics)
}

// RegisterRequest is the payload for /api/p2p/register.
type RegisterRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

const chunkSizeBytes = 262144 // 256KB default chunk size

func p2pRegisterHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}
	if req.Path == "" || req.Size == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "missing path or size"})
		return
	}
	numChunks := int(req.Size / chunkSizeBytes)
	if req.Size%chunkSizeBytes != 0 {
		numChunks++
	}
	log.Printf("p2p: registered '%s' — %d chunks (%d bytes)", req.Name, numChunks, req.Size)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "chunks": numChunks, "size": req.Size, "name": req.Name})
}

// ReportPeerRequest is the payload for /api/p2p/report-peer.
type ReportPeerRequest struct {
	PeerID string `json:"peer_id"`
	Reason string `json:"reason"`
}

func reportPeerHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	var req ReportPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}
	if req.PeerID == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "missing peer_id"})
		return
	}
	log.Printf("p2p: peer report — %s: %s", req.PeerID, req.Reason)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "peer_id": req.PeerID, "reason": req.Reason})
}

// StreamRequest holds query params for /api/p2p/stream.
type StreamRequest struct {
	StationIdx int    `form:"id"`
	ChunkIdx   int    `form:"chunk"`
	Session    string `form:"session"`
	Size       int    `form:"size"`
	Mode       string `form:"mode"` // "handshake" or "pipeline"
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/octet-stream")

	q := r.URL.Query()
	req := StreamRequest{
		StationIdx: 0,
		ChunkIdx:   0,
		Size:       chunkSizeBytes,
		Mode:       "handshake",
	}
	if id := q.Get("id"); id != "" {
		fmt.Sscanf(id, "%d", &req.StationIdx)
	}
	if chunk := q.Get("chunk"); chunk != "" {
		fmt.Sscanf(chunk, "%d", &req.ChunkIdx)
	}
	if size := q.Get("size"); size != "" {
		fmt.Sscanf(size, "%d", &req.Size)
	}
	if mode := q.Get("mode"); mode != "" {
		req.Mode = mode
	}

	// Clamp size
	if req.Size < 65536 {
		req.Size = 65536
	}
	if req.Size > 2097152 {
		req.Size = 2097152
	}

	w.Header().Set("X-Chunk-Mode", req.Mode)
	w.Header().Set("X-Chunk-Size", fmt.Sprintf("%d", req.Size))

	log.Printf("p2p: stream — station=%d chunk=%d mode=%s size=%d", req.StationIdx, req.ChunkIdx, req.Mode, req.Size)

	// Serve real video file from api directory
	entries, err := os.ReadDir("api")
	if err != nil || len(entries) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Find the file at the requested index
	fileIdx := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if _, allowed := extAllowed[ext]; !allowed {
			continue
		}
		if fileIdx == req.StationIdx {
			// Serve the file
			filePath := filepath.Join("api", name)
			f, err := os.Open(filePath)
			if err != nil {
				log.Printf("p2p: stream error opening %s: %v", filePath, err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			defer f.Close()

			// Calculate offset for chunk
			offset := int64(req.ChunkIdx * req.Size)
			if offset >= info.Size() {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}

			// Seek to chunk position
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				log.Printf("p2p: stream seek error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			// Read chunk
			buf := make([]byte, req.Size)
			n, err := io.ReadFull(f, buf)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				log.Printf("p2p: stream read error: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Length", fmt.Sprintf("%d", n))
			w.Write(buf[:n])
			return
		}
		fileIdx++
	}

	// Station index not found locally — try to find a peer with the file via p2p
	if p2pSwarm != nil && p2pSwarm.Libp2pHost() != nil {
		log.Printf("p2p: station %d not local, trying peers...", req.StationIdx)

		// Build the file name from local inventory scan (reuse existing index)
		// We need to find which file corresponds to this station index
		localFile := findLocalFile(req.StationIdx)
		fileName := ""
		if localFile != "" {
			fileName = filepath.Base(localFile)
		} else {
			// File not local — try to find it in remote inventory
			// Scan entries again to get the file name at this index
			fIdx := 0
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				ext := strings.ToLower(filepath.Ext(name))
				if _, allowed := extAllowed[ext]; !allowed {
					continue
				}
				fIdx++
			}
			// If we still don't have the name, we can't request from peers
			if fileName == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}

		peers := p2pSwarm.Libp2pHost().GetPeers()
		for _, peerID := range peers {
			log.Printf("p2p: requesting chunk %d of '%s' from peer %s", req.ChunkIdx, fileName, peerID)
			chunk, err := p2pSwarm.Libp2pHost().RequestChunk(
				r.Context(), peerID, fileName, req.ChunkIdx, req.Size)
			if err != nil {
				log.Printf("p2p: chunk request to %s failed: %v", peerID, err)
				continue
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(chunk)))
			w.Header().Set("X-P2P-Peer", peerID.String())
			w.Write(chunk)
			return
		}
		log.Printf("p2p: no peer could serve station %d chunk %d", req.StationIdx, req.ChunkIdx)
	}

	// Station index not found locally or via peers
	w.WriteHeader(http.StatusNotFound)
}

func inventoryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Build local inventory
	localItems := make([]p2p.InventoryItem, 0)
	entries, err := os.ReadDir("api")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			name := entry.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if _, allowed := extAllowed[ext]; !allowed {
				continue
			}
			localItems = append(localItems, p2p.InventoryItem{
				Name: name,
				Path: "/api/" + name,
				Size: info.Size(),
				Type: extAllowed[ext],
			})
		}
	}

	// Get combined inventory from swarm
	items := localItems
	if p2pSwarm != nil {
		items = p2pSwarm.GetCombinedInventory(localItems)
	}

	// Apply filters
	filter := r.URL.Query().Get("filter")
	minPeers := 0
	if mp := r.URL.Query().Get("min_peers"); mp != "" {
		fmt.Sscanf(mp, "%d", &minPeers)
	}

	filtered := make([]p2p.InventoryItem, 0)
	for _, item := range items {
		if filter != "" && !strings.Contains(strings.ToLower(item.Name), strings.ToLower(filter)) {
			continue
		}
		// For now, count how many peers have this item (simplified)
		peerCount := 1 // local peer
		if p2pSwarm != nil {
			peerCount += len(p2pSwarm.GetAlivePeers())
		}
		if peerCount >= minPeers {
			filtered = append(filtered, item)
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":    true,
		"items": filtered,
		"total": len(filtered),
	})
}

// --- Entry point ---

func main() {
	client := &http.Client{Timeout: 15 * time.Second}

	if err := fetchJSON(client); err != nil {
		log.Fatalf("Failed to fetch data: %v", err)
	}

	initialPageData = buildList(hora())

	mux := http.NewServeMux()
	mux.HandleFunc("/", root)
	mux.HandleFunc("/pelis", pelis)
	mux.Handle("/api/", http.StripPrefix("/api/", http.FileServer(http.Dir("api"))))
	mux.HandleFunc("/subir", uploadHandler)
	mux.HandleFunc("/api", uploader)
	mux.HandleFunc("/api/p2p/qos", qosHandler)
	mux.HandleFunc("/api/p2p/register", p2pRegisterHandler)
	mux.HandleFunc("/api/p2p/report-peer", reportPeerHandler)
	mux.HandleFunc("/api/p2p/stream", streamHandler)
	mux.HandleFunc("/api/p2p/inventory", inventoryHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "80"
	}

	server := &http.Server{
		Addr:           ":" + port,
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	// Start P2P module
	shutdownP2P, swarm, err := p2p.StartP2P()
	if err != nil {
		log.Printf("p2p: failed to start: %v", err)
	} else {
		p2pSwarm = swarm
		defer shutdownP2P()
	}

	// Signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down...", sig)
		if shutdownP2P != nil {
			shutdownP2P()
		}
		server.Close()
	}()

	log.Println("Server Stream Active port: " + port + "\nAANTVS — Licencia: CC BY-NC-ND 3.0\nhttps://aratan.github.io/AANTV-Stream/")
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
