// internal/p2p/proximity_filter.go
package p2p

import (
	"log"
	"net"
	"time"
)

// ProximityConfig define los límites del GC para mantener la red limpia.
type ProximityConfig struct {
	MaxRTT            time.Duration // Latencia máxima aceptable en ms (proxy de distancia física).
	PeerIdleTimeout   time.Duration // Tiempo sin actividad antes de descartar el peer.
	CheckInterval     time.Duration // Frecuencia de ejecución del GC.
	MinReputation     float64       // PR #23: Puntaje mínimo de reputación aceptado.
}

// DefaultProximityConfig crea valores por defecto recomendados para streaming de video 1080p/60fps.
func DefaultProximityConfig() ProximityConfig {
	return ProximityConfig{
		MaxRTT:        300 * time.Millisecond, // > 300ms se considera "Lejos" o inestable para vídeo en tiempo real.
		PeerIdleTimeout: 24 * time.Hour,       // Elimina si no hubo 'ping' en un día.
		CheckInterval:   5 * time.Minute,      // Ejecuta limpieza cada 5 minutos.
		MinReputation:   0.75,                 // Umbral por defecto de reputación (PR #23).
	}
}

// EvaluarValidezPeer verifica si un nodo cumple con los estándares de latencia o inactividad.
func (s *Swarm) EvaluarValidezPeer(addr string, lastFound time.Time, cfg ProximityConfig) (bool, error) {
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

    // 3. Comprobación de Reputación (PR #23 Integration stub)
	s.mu.RLock()
	score, exists := s.reputation[addr]
	s.mu.RUnlock()

	if !exists || score < cfg.MinReputation {
    	return false, ErrLowReputation // Peer con baja reputación descartado por GC
	}

    return true, nil // Peer válido (Cerca y Activo)
}
