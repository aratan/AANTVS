/**
 * QoS Overlay — Real-time P2P network quality metrics for the video player.
 *
 * Fetches swarm status from /api/p2p/qos every 2 seconds and renders
 * a cyberpunk-style overlay showing peer count, transfer rates, and
 * P2P vs HTTP ratio.
 *
 * Usage:
 *   const qos = new QoSOverlay(playerContainer);
 *   qos.start(stationIdx);
 *   qos.stop();
 */

class QoSOverlay {
  constructor(container, options = {}) {
    this.container = container;
    this.pollInterval = options.pollInterval ?? 2000;
    this.apiUrl = options.apiUrl ?? '/api/p2p/qos';
    this.stationIdx = 0;
    this.pollTimer = null;
    this.visible = true;

    this._createDOM();
    this._bindToggle();
  }

  _createDOM() {
    // Toggle button
    this.toggleBtn = document.createElement('button');
    this.toggleBtn.className = 'qos-toggle';
    this.toggleBtn.textContent = 'QoS';
    this.container.appendChild(this.toggleBtn);

    // Overlay panel
    this.overlay = document.createElement('div');
    this.overlay.className = 'qos-overlay';
    this.overlay.innerHTML = `
      <div class="qos-title">P2P Network QoS</div>
      <div class="qos-row"><span class="qos-label">Peers</span><span class="qos-value" id="qos-peers">0</span></div>
      <div class="qos-row"><span class="qos-label">Seeds</span><span class="qos-value" id="qos-seeds">0</span></div>
      <div class="qos-row"><span class="qos-label">P2P Ratio</span><span class="qos-value" id="qos-ratio">0%</span></div>
      <div class="qos-row"><span class="qos-label">Speed</span><span class="qos-value" id="qos-speed">0 KB/s</span></div>
      <div class="qos-row"><span class="qos-label">Bad Pieces</span><span class="qos-value" id="qos-bad">0</span></div>
      <div class="qos-row"><span class="qos-label">Latency</span><span class="qos-value" id="qos-latency">0ms</span></div>
      <div class="qos-bar"><div class="qos-bar-fill" id="qos-buffer" style="width:0%"></div></div>
    `;
    this.container.appendChild(this.overlay);

    // Cache element references
    this.els = {
      peers: this.overlay.querySelector('#qos-peers'),
      seeds: this.overlay.querySelector('#qos-seeds'),
      ratio: this.overlay.querySelector('#qos-ratio'),
      speed: this.overlay.querySelector('#qos-speed'),
      bad: this.overlay.querySelector('#qos-bad'),
      latency: this.overlay.querySelector('#qos-latency'),
      buffer: this.overlay.querySelector('#qos-buffer'),
    };
  }

  _bindToggle() {
    this.toggleBtn.addEventListener('click', () => {
      this.visible = !this.visible;
      this.overlay.classList.toggle('hidden', !this.visible);
      this.toggleBtn.textContent = this.visible ? 'QoS' : 'QoS';
      this.toggleBtn.style.opacity = this.visible ? '1' : '0.5';
    });
  }

  /**
   * Start polling QoS metrics.
   */
  start(stationIdx = 0) {
    this.stationIdx = stationIdx;
    this._poll(); // immediate first poll
    this.pollTimer = setInterval(() => this._poll(), this.pollInterval);
  }

  /**
   * Stop polling.
   */
  stop() {
    if (this.pollTimer) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  /**
   * Update metrics manually (e.g., from MSE player events).
   */
  updateFromPlayer(metrics) {
    if (metrics.bufferLevel !== undefined) {
      const pct = Math.min(100, (metrics.bufferLevel / 5242880) * 100);
      this.els.buffer.style.width = `${pct}%`;
    }
  }

  async _poll() {
    try {
      const res = await fetch(`${this.apiUrl}?id=${this.stationIdx}`);
      if (!res.ok) return;
      const data = await res.json();
      this._render(data);
    } catch {
      // Silently ignore poll errors — overlay stays on last known state
    }
  }

  _render(data) {
    this.els.peers.textContent = data.peers ?? 0;
    this.els.seeds.textContent = data.seeds ?? 0;

    const ratio = data.p2p_ratio ?? 0;
    this.els.ratio.textContent = `${Math.round(ratio * 100)}%`;
    this.els.ratio.className = `qos-value ${ratio < 0.3 ? 'warn' : ratio < 0.1 ? 'error' : ''}`;

    const speed = data.speed_bps ?? 0;
    this.els.speed.textContent = this._formatSpeed(speed);

    const bad = data.bad_pieces ?? 0;
    this.els.bad.textContent = bad;
    this.els.bad.className = `qos-value ${bad > 5 ? 'warn' : bad > 20 ? 'error' : ''}`;

    const latency = data.avg_latency_ms ?? 0;
    this.els.latency.textContent = `${Math.round(latency)}ms`;
    this.els.latency.className = `qos-value ${latency > 200 ? 'warn' : latency > 500 ? 'error' : ''}`;

    const buffer = data.buffer_pct ?? 0;
    this.els.buffer.style.width = `${Math.min(100, buffer)}%`;
  }

  _formatSpeed(bps) {
    if (bps < 1024) return `${bps} B/s`;
    if (bps < 1048576) return `${(bps / 1024).toFixed(1)} KB/s`;
    return `${(bps / 1048576).toFixed(1)} MB/s`;
  }
}

if (typeof module !== 'undefined') {
  module.exports = QoSOverlay;
}
