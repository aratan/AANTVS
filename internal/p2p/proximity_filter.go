package p2p

import (
	"errors"
	"net"
	"time"
)

// --- Definición de errores locales para evitar dependencias externas ---
var (
	ErrPeerInactive  = errors.New("peer inactivo por inactividad prolongada")
	ErrPeerTooFar    = errors.New("peer descartado por alta latencia/proximidad")
	ErrLowReputation = errors.New("peer con reputación insuficiente")
)

// ProximityConfig define los límites del GC para mantener la red limpia.
type ProximityConfig struct {
	MaxRTT          time.Duration // Latencia máxima aceptable en ms (proxy de distancia física).
	PeerIdleTimeout time.Duration // Tiempo sin actividad antes de descartar el peer.
	CheckInterval   time.Duration // Frecuencia de ejecución del GC.
}

// DefaultProximityConfig crea valores por defecto recomendados para streaming de video 1080p/60fps.
func DefaultProximityConfig() ProximityConfig {
	return ProximityConfig{
		MaxRTT:          300 * time.Millisecond, // > 300ms se considera "Lejos" o inestable para vídeo en tiempo real.
		PeerIdleTimeout: 24 * time.Hour,         // Elimina si no hubo 'ping' en un día.
		CheckInterval:   5 * time.Minute,        // Ejecuta limpieza cada 5 minutos.
	}
}

// EvaluarValidezPeer verifica si un nodo cumple con los estándares de latencia o inactividad.
func (s *Swarm) EvaluarValidezPeer(peerID string, addr string, lastFound time.Time, cfg ProximityConfig) (bool, error) {
	// 1. Verificar Inactividad (Cuantos años lleva apagado?)
	if time.Since(lastFound) > cfg.PeerIdleTimeout {
		return false, ErrPeerInactive
	}

	// 2. Simular 'Medición de Distancia' via RTT (Real Round-Trip Time)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
	if err != nil {
		return false, err // No conecta = Lejos/Gateado/Bloqueado
	}
	rtt := time.Since(start)
	conn.Close()

	if rtt > cfg.MaxRTT {
		return false, ErrPeerTooFar // Latencia alta = Distancia física o mala conexión en camino.
	}

	// 3. Verificación de Reputación (PR #23 Integration)
	// ReputationManager usa scores de -100 a 100, con 50 como neutral.
	// Un peer debe tener score >= 60 (goodThreshold) para ser considerado válido.
	score := s.reputation.GetScore(peerID)
	if score < goodThreshold {
		return false, ErrLowReputation // Peer con baja reputación descartado por GC
	}

	return true, nil // Peer válido (Cerca y Activo)
}

// goodThreshold es el puntaje mínimo de reputación para ser considerado válido.
const goodThreshold float64 = 60
