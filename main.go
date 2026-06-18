package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

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

type pageData struct {
	Title       string
	CompanyName string
	Npeli       string
	Nfoto       string
	Nurl        string
	Dhora       string
	Texto       string
	Chtml       string
	Lfoto       string
}

// --- Estado (solo lectura después de startup) ---

var (
	t              *template.Template
	routeMatch     = regexp.MustCompile(`^\/(\w+)`)
	peliculas      Peliculas
	initialPageData pageData // solo se escribe en startup, lectura concurrente segura
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

	var pd pageData
	if idx >= 29 {
		pd = buildList(hora())
	} else {
		pd = buildList(idx)
	}

	t.ExecuteTemplate(w, "index.html", pd)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "api/upload.html")
}

func uploader(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50<<20) // 50 MB max
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		log.Println("Error parsing form:", err)
		http.Error(w, "Error processing upload", http.StatusBadRequest)
		return
	}

	file, fileinfo, err := r.FormFile("archivo")
	if err != nil {
		log.Println("Error getting form file:", err)
		http.Error(w, "Error processing upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	safeName := filepath.Base(fileinfo.Filename)
	if safeName == "." || safeName == "/" {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	f, err := os.OpenFile("./api/"+safeName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		log.Println("Error saving file:", err)
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if _, err := io.Copy(f, file); err != nil {
		log.Println("Error writing file:", err)
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "<div class='jumbotron bg-dark text-warning'>Cargado con exito</div>")
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
	var chtml, lfoto string

	for idx := 0; idx < 28; idx++ {
		ii := strconv.Itoa(idx)
		foto := peliculas.Groups[0].Stations[idx].Image
		video := peliculas.Groups[0].Stations[idx].URL
		message := peliculas.Groups[0].Stations[idx].Name

		chtml += `<div class="movie">
						<br><video  width="50%" height="50%" controls poster="` + foto + `">
						<source src="` + video + `" type="video/mp4">
						</video>`

		lfoto += `<a href='pelis?id=` + ii + `' class='movie'>
		                 <img src='` + foto + `' alt='` + message + `'>
		                 <div class='movie-info'>
		                   <div class='movie-title'>` + message + `</div>
		                 </div>
		               </a>`
	}

	return pageData{
		Title:       "Cine Online",
		CompanyName: peliculas.Name,
		Npeli:       peliculas.Groups[0].Stations[startIdx].Name,
		Nfoto:       peliculas.Groups[0].Stations[startIdx].Image,
		Nurl:        peliculas.Groups[0].Stations[startIdx].URL,
		Dhora:       peliculas.Groups[0].Stations[startIdx].Embed,
		Texto:       peliculas.Groups[0].Stations[startIdx].PlayInNatPlayer,
		Chtml:       chtml,
		Lfoto:       lfoto,
	}
}

func hora() int {
	t := time.Now()
	fecha := fmt.Sprintf("%02d", t.Hour())
	i, _ := strconv.Atoi(fecha)
	return i
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

	log.Println("Server Stream Active port: " + port + "\nVictor Manuel Arbiol Martinez 2020\nv1.1.2 Licencia: CC BY-NC-ND 3.0\nhttps://aratan.github.io/AANTV-Stream/")
	log.Fatal(server.ListenAndServe())
}
