# AANTVS

Servidor de streaming de películas y series en Go. Consume contenido desde una API remota (Pastebin) y lo sirve vía HTTP con reproducción HLS + IPFS.

## Stack

- **Lenguaje**: Go 1.21+
- **Frontend**: HTML + Bootstrap 4 + HLS.js + js-ipfs
- **Dependencias externas**: ninguna (solo stdlib)

## Uso

```bash
go build -o aantvs && ./aantvs
```

Por defecto escucha en el puerto `80`. Se puede cambiar con la variable de entorno `PORT`:

```bash
PORT=8080 ./aantvs
```

## Estructura

```
.
├── main.go              # Servidor y lógica principal
├── index.html           # Template principal (Go html/template)
├── api/                 # Archivos estáticos (CSS, JS, uploads)
│   ├── styles.css
│   ├── main.js
│   ├── upload.html
│   ├── favicon.ico
│   ├── loader-animation.svg
│   ├── avisolegal.html
│   ├── cookies.html
│   └── privacidad.html
└── go.mod
```

## Endpoints

| Ruta     | Descripción                         |
| -------- | ----------------------------------- |
| `/`      | Página principal con reproductor    |
| `/pelis` | Selector de contenido por `?id=N`   |
| `/api/`  | Archivos estáticos                  |
| `/subir` | Formulario de subida de archivos    |
| `/api`   | Uploader (POST multipart)           |

## Licencia

CC BY-NC-ND 3.0 — Victor Manuel Arbiol Martinez, 2020
