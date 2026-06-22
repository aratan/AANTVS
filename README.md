# AANTVS

Servidor de streaming P2P con soporte WebRTC para reproducción de películas y series. Consumo de contenido vía Pastebin con réplica descentralizada entre nodos.

<img width="1920" height="1050" alt="Captura de pantalla_20260618_161729" src="https://github.com/user-attachments/assets/ae6853ce-a562-46bd-9b40-6a6bded5749d" />

## Stack

- **Backend**: Go 1.24+ (stdlib + pion/webrtc + pion/stun)
- **Frontend**: HTML + CSS + JavaScript (MSE Player)
- **P2P**: UDP multicast gossip + TCP peering + WebRTC DataChannels
- **NAT Traversal**: STUN + hole-punching + seed relay fallback

## Arquitectura

```
┌─────────────────────────────────────────────────┐
│                   PRESENTATION                   │
│  index.html, api/main.js, api/mse-player.js     │
│  api/qos.js, api/upload.js                      │
└──────────────────────┬──────────────────────────┘
                       │ HTTP/REST
┌──────────────────────▼──────────────────────────┐
│                   APPLICATION                    │
│  main.go (handlers), /api/p2p/* endpoints        │
└──────────────────────┬──────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────┐
│                     DOMAIN                       │
│  internal/p2p/                                   │
│  ├── Config (value object)                       │
│  ├── SwarmCoordinator (aggregate root)           │
│  ├── PeerStore (repository pattern)              │
│  ├── ReputationManager (domain service)          │
│  ├── EquilibriumManager (state machine)          │
│  ├── WebRTCBridge (adapter)                      │
│  └── HolePuncher (adapter)                       │
└─────────────────────────────────────────────────┘
```

## Uso

### 1. Compilar y ejecutar

```bash
go build -o aantvs && ./aantvs
```

Puerto por defecto: `80`. Configurable con variable de entorno:

```bash
PORT=8080 ./aantvs
```

### 2. Colocar videos

Los videos se colocan en el directorio `api/`. Formatos soportados:

| Formato | MIME |
|---------|------|
| `.mp4` | video/mp4 |
| `.webm` | video/webm |
| `.ogg` | video/ogg |

**Ejemplo:**

```bash
# Copiar video al directorio api/
cp mi_video.mp4 api/

# Verificar que está en el inventario
curl http://localhost:8080/api/p2p/inventory
```

Los videos se sirven por chunks (256KB por defecto) vía el endpoint `/api/p2p/stream`.

### 3. Ver videos en el navegador

Una vez colocado el video en `api/`:

```
http://localhost:8080/api/p2p/inventory    # Ver catálogo
http://localhost:8080/api/p2p/stream?id=0  # Servir primer video
```

El endpoint `/api/p2p/inventory` retorna todos los archivos del directorio `api/` con metadata de rareza y peers.

### 4. Subir videos vía web

```
http://localhost:8080/subir
```

Formulario web para subir videos (máximo 50MB). Después de subir, el video queda en `api/` y automáticamente disponible para distribución P2P.

### Configuración P2P

Crear `~/.aantvs/config.json`:

```json
{
  "http": { "port": 80 },
  "p2p": {
    "enabled": true,
    "multicast_group": "239.0.0.1",
    "multicast_port": 5432,
    "heartbeat_interval_ms": 250,
    "ttl": 4
  },
  "p2p_port": 8080,
  "seed_peers": [],
  "stun_servers": ["stun.l.google.com:19302"]
}
```

| Campo | Descripción | Default |
|-------|-------------|---------|
| `http.port` | Puerto del servidor HTTP | `80` |
| `p2p.enabled` | Activar/desactivar P2P | `true` |
| `p2p.multicast_group` | Grupo multicast UDP para descubrimiento | `239.0.0.1` |
| `p2p.multicast_port` | Puerto multicast | `5432` |
| `p2p.heartbeat_interval_ms` | Intervalo de heartbeats (ms) | `250` |
| `p2p.ttl` | TTL del paquete multicast (saltos) | `4` |
| `p2p_port` | Puerto TCP para conexiones P2P | `8080` |
| `seed_peers` | Lista de peers semilla para bootstrap | `[]` |
| `stun_servers` | Servidores STUN para NAT traversal | `[]` |

### Seed Node (Opcional)

Nodo semilla para relay y bootstrap en red:

```bash
go run ./cmd/aantvs-seed -port 9302
```

Los seed nodes ayudan a nuevos peers a encontrarse cuando el multicast no funciona (redes segmentadas, cloud, etc).

## Estructura

```
.
├── main.go                    # Servidor HTTP + handlers P2P
├── index.html                 # Template principal (Go html/template)
├── go.mod                     # Module aantvs
├── internal/
│   └── p2p/
│       ├── config.go          # Config loader (~/.aantvs/config.json)
│       ├── peer.go            # PeerStore + heartbeat + reconnect
│       ├── gossip.go          # UDP multicast + gossip protocol
│       ├── webrtc_bridge.go   # WebRTC peer lifecycle (pion/webrtc)
│       ├── holePunch.go       # STUN client + hole-punching
│       ├── equilibrium.go     # RarestFirst ↔ SequentialFirst state machine
│       ├── reputation.go      # Peer reputation scoring + auto-ban
│       └── wiring.go          # StartP2P / WaitForShutdown helpers
├── cmd/
│   └── aantvs-seed/
│       └── main.go            # Seed/relay node standalone binary
├── api/
│   ├── main.js                # UI + MSE player integration
│   ├── mse-player.js          # MSE Player (adaptive buffer)
│   ├── qos.js                 # QoS overlay (real-time metrics)
│   ├── upload.js              # Upload + P2P registration
│   ├── styles.css             # Estilos (incluye QoS overlay)
│   ├── upload.html            # Formulario de subida
│   ├── admin.html             # Panel de administración
│   └── ...
└── openspec/                  # SDD artifacts
```

## Endpoints

| Ruta | Método | Descripción |
|------|--------|-------------|
| `/` | GET | Página principal con catálogo |
| `/pelis?id=N` | GET | Reproductor de contenido |
| `/api/` | GET | Archivos estáticos (CSS, JS, uploads) |
| `/subir` | GET | Formulario de subida |
| `/api` | POST | Uploader (multipart, max 50MB) |
| `/api/p2p/qos` | GET | Métricas de calidad de red P2P (peers, latencia, chunks) |
| `/api/p2p/register` | POST | Registrar archivo para distribución P2P |
| `/api/p2p/report-peer` | POST | Reportar peer con comportamiento anómalo |
| `/api/p2p/stream?id=N&chunk=N` | GET | Servir chunks de video por índice |
| `/api/p2p/inventory` | GET | Catálogo de archivos con datos de rareza |

### Parámetros de `/api/p2p/stream`

| Param | Tipo | Default | Descripción |
|-------|------|---------|-------------|
| `id` | int | `0` | Índice del archivo en el directorio `api/` |
| `chunk` | int | `0` | Índice del chunk a servir |
| `size` | int | `262144` | Tamaño del chunk (64KB-2MB) |
| `mode` | string | `handshake` | Modo: `handshake` o `pipeline` |

### Ejemplos de uso

```bash
# Ver métricas QoS
curl http://localhost:8080/api/p2p/qos

# Listar archivos disponibles
curl http://localhost:8080/api/p2p/inventory

# Servir primer chunk del primer video
curl -o chunk.bin "http://localhost:8080/api/p2p/stream?id=0&chunk=0"

# Registrar un archivo para distribución P2P
curl -X POST http://localhost:8080/api/p2p/register \
  -H "Content-Type: application/json" \
  -d '{"path":"/api/video.mp4","name":"video.mp4","size":104857600,"type":"video/mp4"}'
```

## P2P — Fases de Implementación

### Phase A: Stdlib Gossip ✅
- UDP multicast discovery (239.0.0.1:5432)
- TCP peer connections con heartbeat + reconnect
- IndexUpdate gossip (movie catalog metadata)
- Zero dependencias externas

### Phase B: WebRTC Bridge ✅
- pion/webrtc para browser peers
- SDP offer/answer + DataChannel para chunks
- STUN hole-punching para NAT traversal
- Rate limiting (50KB/s por peer)

### Phase C: Seed + Equilibrium ✅
- Seed node standalone para relay y bootstrap
- EquilibriumManager: RarestFirst ↔ SequentialFirst
- Peer reputation scoring con auto-ban

## Patrones de Diseño

| Patrón | Implementación |
|--------|---------------|
| Repository | PeerStore, ReputationManager |
| State Machine | EquilibriumManager (RAREST_FIRST ↔ SEQ_FIRST) |
| Adapter | WebRTCBridge, HolePuncher, STUNClient |
| Value Object | Config, QoSMetrics, PeerAddress |
| Observer | msePlayer.onProgress → qosOverlay.updateFromPlayer |
| Strategy | RarestFirstStrategy en gossip.go |
| Circuit Breaker | Reconexión con backoff exponencial |
| Facade | p2p.StartP2P() |
| Semaphore | rateLimiter en WebRTCBridge |

## Licencia

CC BY-NC-ND 3.0
