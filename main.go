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

// --- Estado global ---
// Mantenido por compatibilidad, idealmente migrar a handlers con estado propio.

var (
	ii, message, foto, video, chtml, lfoto string
	texto, dhora                           string
	p                                      int
	i                                      int // puntero reutilizado en varias funciones
	t                                      *template.Template
	routeMatch                             = regexp.MustCompile(`^\/(\w+)`)

	pd        pageData
	peliculas Peliculas
)

// --- Inicialización ---

func init() {
	var err error
	t, err = template.ParseGlob("*.html")
	if err != nil {
		log.Println("Cannot parse templates:", err)
		os.Exit(-1)
	}
}

// --- Handlers HTTP ---

func root(w http.ResponseWriter, r *http.Request) {
	matches := routeMatch.FindStringSubmatch(r.URL.Path)

	if len(matches) >= 1 {
		page := matches[1] + ".html"
		if t.Lookup(page) != nil {
			w.WriteHeader(200)
			t.ExecuteTemplate(w, page, pd)
			return
		}
	} else if r.URL.Path == "/" {
		w.WriteHeader(200)
		t.ExecuteTemplate(w, "index.html", pd)
		return
	}
	w.WriteHeader(404)
	w.Write([]byte("NOT FOUND "))
}

func pelis(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query()
	pp, _ := strconv.Atoi(id["id"][0])
	p = pp

	if p >= 29 {
		buildList(hora())
	} else {
		buildList(p)
	}

	t.ExecuteTemplate(w, "index.html", pd)
	chtml = ""
	lfoto = ""
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "upload.html")
}

func uploader(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(2000)

	if r.Method == http.MethodPost {
		file, fileinfo, err := r.FormFile("archivo")
		f, err := os.OpenFile("./api/"+fileinfo.Filename, os.O_WRONLY|os.O_CREATE, 0666)
		if err != nil {
			log.Println(err)
			fmt.Fprintf(w, "error al subir %v", err)
			return
		}
		defer f.Close()

		io.Copy(f, file)
		fmt.Fprintf(w, "<div class='jumbotron bg-dark text-warning'>Cargado con exito </div> <p class='lead'>"+fileinfo.Filename+"</p>")
	}
}

// --- Helpers ---

func azar() int {
	min := 0
	max := 20
	i = rand.Intn(max-min) + min
	return i
}

func fetchJSON() {
	url := "https://pastebin.com/raw/s6DTUHCA"
	res, err := http.Get(url)
	if err != nil {
		panic(err.Error())
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		panic(err.Error())
	}

	err = json.Unmarshal(body, &peliculas)
	if err != nil {
		panic(err.Error())
	}
}

func buildList(startIdx int) {
	for idx := 0; idx < 28; idx++ {
		ii = strconv.Itoa(idx)

		message = peliculas.Groups[0].Stations[idx].Name
		foto = peliculas.Groups[0].Stations[idx].Image
		video = peliculas.Groups[0].Stations[idx].URL
		dhora = peliculas.Groups[0].Stations[idx].Embed
		texto = peliculas.Groups[0].Stations[idx].PlayInNatPlayer

		chtml += `<div class="movie">
						<br><video  width="50%" height="50%" controls poster="` + foto + `">
						<source src="` + video + `" type="video/mp4">
						</video>`

		lfoto += `<a href='pelis?id=` + ii + `'> 
		                 <img src='` + foto + `' alt='` + message + `' width='30%' height='50'>`
	}

	lfoto += `</a></div>`

	pd = pageData{
		"Cine Online",
		peliculas.Name,
		peliculas.Groups[0].Stations[startIdx].Name,
		peliculas.Groups[0].Stations[startIdx].Image,
		peliculas.Groups[0].Stations[startIdx].URL,
		peliculas.Groups[0].Stations[startIdx].Embed,
		peliculas.Groups[0].Stations[startIdx].PlayInNatPlayer,
		chtml,
		lfoto,
	}
}

func hora() int {
	t := time.Now()
	fecha := fmt.Sprintf("%02d", t.Hour())
	i, _ = strconv.Atoi(fecha)
	return i
}

// --- Entry point ---

func main() {
	azaro := azar()
	fetchJSON()
	buildList(azaro)
	buildList(hora())

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
