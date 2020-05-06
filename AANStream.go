package main

import (
	"encoding/json"

	//"ws"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"
)

//Struct:      http://json2struct.mervine.net/
//Probar Json: https://www.jsonformatter.io/
//Ejemplo:     http://pastebin.com/raw/Fw2P6GLn/
//Crear json   http://objgen.com/json

// Estructura para Json 
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

// Declaramos variables a usar
var s, ii, message, foto, video, chtml, lfoto string
var texto, dhora string
var p int
var i int // mi puntero
var t *template.Template
var routeMatch *regexp.Regexp
var azaro, horasys int

var pd pageData
var peliculas Peliculas



// Los Tag en HTML
type pageData struct {
	Title       string
	CompanyName string
	Npeli       string 
	Nfoto       string 
	Nurl        string 
	Dhora		string //nuevo
	Texto 		string //nuevo
	Chtml       string 
	Lfoto       string 
}

//web
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
	// http://127.0.0.1:80/pelis?id=1
	id := r.URL.Query()
	//log.Println("GET pelis : ", id)
	//peli := id.Get("peli")

	firstvalue := id["id"]
	//pasando de slice []string a int simple
	pp, _ := strconv.Atoi(firstvalue[0])
	p = int(pp)
	//fmt.Println(p)
	//a(p)
	if p >= 29 {
		//fmt.Println("mayor de 28")
		c(hora())
	}else{
		//fmt.Println("menor de 29")
		c(p)
	}
	
	t.ExecuteTemplate(w, "index.html", pd)
	//w.Write([]byte("ok"))
	//limpiamos var para no rep la lista
	chtml = ""
	lfoto = ""
}



// esto ya no se ejecuta al inicio
func azar() int {
	//Eleccion al azar
	rand.Seed(time.Now().UnixNano())
	min := 0
	max := 20
	i = (rand.Intn(max-min) + min)
	return i
}


func a(i int) { //API a lo bestia
	// Datos en Json de servidor remoto
	url := "https://pastebin.com/raw/s6DTUHCA"
	///url = "http://127.0.0.1/api/mEEA3Udp.json"
	// Recogemos los datos
	res, erro := http.Get(url)
	if erro != nil {
		panic(erro.Error())
	}
	body, erro := ioutil.ReadAll(res.Body)
	if erro != nil {
		panic(erro.Error())
	}

	erro = json.Unmarshal(body, &peliculas)
	//fmt.Println(peliculas.Name)                       //[i].Type)

	//fmt.Println(peliculas.Groups[0].Stations[i].Name)
	
	//Capitana Marvel (2019)
	//fmt.Println("User Age: " + strconv.Itoa(peliculas.Peliculass[i].Age))
	if erro != nil {
		panic(erro.Error())
	}
}


func b() {
	var err error

	t, err = template.ParseGlob("*.html")
	if err != nil {
		log.Println("Cannot parse templates:", err)
		os.Exit(-1)
	}
}

func c(i int) { //lista TODO: FOR
	for i := 0; i < 28; i++ {
		ii = strconv.Itoa(i) //para enlaces
		//soporte 20 //message = fmt.Sprintf(peliculas.Groups[i].Stations[i].Name)
		message = fmt.Sprintf(peliculas.Groups[0].Stations[i].Name) //Capitana Marvel (2019)
		foto = fmt.Sprintf(peliculas.Groups[0].Stations[i].Image)
		video = fmt.Sprintf(peliculas.Groups[0].Stations[i].URL) /// fin añadidos
		dhora = fmt.Sprintf(peliculas.Groups[0].Stations[i].Embed)
		texto = fmt.Sprintf(peliculas.Groups[0].Stations[i].PlayInNatPlayer)
	
		chtml = chtml + `<div class="movie">
						<br><video  width="50%" height="50%" controls poster="` + foto + `">
						<source src="` + video + `" type="video/mp4">
						</video>`

		lfoto = lfoto + `<a href='pelis?id=` + ii + `'> 
		                 <img src='` + foto + `' alt='` + message + `' width='50' height='50'>`
	}

	lfoto = lfoto + `</a></div>`
	
	routeMatch, _ = regexp.Compile(`^\/(\w+)`)

	pd = pageData{
		"Cine Online",
		peliculas.Name,
		peliculas.Groups[0].Stations[i].Name,
		peliculas.Groups[0].Stations[i].Image,
		peliculas.Groups[0].Stations[i].URL,
		peliculas.Groups[0].Stations[i].Embed,
		peliculas.Groups[0].Stations[i].PlayInNatPlayer,
		chtml,
		lfoto,		
	}
}


// 1. Subir ficheros /upload
func handler(w http.ResponseWriter, r *http.Request) {
	//fmt.Fprintf(w, "Funciona /subir")
	http.ServeFile(w, r, "upload.html")
}

//2. Subir ficheros
func uploader(w http.ResponseWriter, r *http.Request) {
	//limita el tamaño de los archivos a subir
	r.ParseMultipartForm(2000)

	if r.Method == http.MethodPost {

		file, fileinfo, err := r.FormFile("archivo")
		// esto es muy importante es la ruta, tiene que terminar en barra "/"
		f, err := os.OpenFile("./api/"+fileinfo.Filename, os.O_WRONLY|os.O_CREATE, 0666)

		if err != nil {
			log.Println(err)
			fmt.Fprintf(w, "error al subir %v", err)
			return
		}

		defer f.Close()

		io.Copy(f, file)
		fmt.Fprintf(w, "<div class='jumbotron bg-dark text-warning'>Cargado con exito </div> <p class='lead'>" + fileinfo.Filename + "</p>")
	}
}
// hora 
func hora() int {
	// añado 2020
	t := time.Now()
	//fecha := fmt.Sprintf("%d-%02d-%02dT%02d:%02d:%02d", t.Year(), t.Month(), t.Day(),
	
	fecha := fmt.Sprintf("%02d", t.Hour())
	i ,_ = strconv.Atoi(fecha)
	//fmt.Println(i)
	return i
	//azaro por i
}

func main() {
	//ws.Show()
	azaro = azar()
	a(azaro)
	b()
	c(azaro)
	
	c(hora())
	//tengo que añadir el api arriba

	mux := http.NewServeMux()
	go mux.HandleFunc("/", root)          //no lo he tocado
	go mux.HandleFunc("/pelis", pelis)    //peliculas
	

	// Directorio publico contiene jsonseries.json
	FileServer := http.FileServer(http.Dir("api"))
	go mux.Handle("/api/", http.StripPrefix("/api/", FileServer))
	// Subir fuchero
	mux.HandleFunc("/subir", handler)  //1. parte
	mux.HandleFunc("/api", uploader) //2. parte

	port := os.Getenv("PORT")

	if port == "" {
		port = "80"
	}

	// escuchando
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
