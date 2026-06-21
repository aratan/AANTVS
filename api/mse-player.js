/**
 * MSE Player — Media Source Extensions player for chunk-based video streaming.
 *
 * Supports:
 *   - Sequential chunk fetching from /api/chunk/:stationIdx/:chunkIdx
 *   - Adaptive buffer management (128KB → 2MB)
 *   - Reconnection on network errors
 *   - Keyboard controls (space, arrows)
 *
 * Usage:
 *   const player = new MSEPlayer(videoElement, { stationIdx: 0 });
 *   await player.load();
 *   player.play();
 */

class MSEPlayer {
  constructor(videoEl, options = {}) {
    this.video = videoEl;
    this.stationIdx = options.stationIdx ?? 0;
    this.baseUrl = options.baseUrl ?? '/api/p2p/stream';
    this.handshakeChunkSize = options.handshakeChunkSize ?? 131072;   // 128KB
    this.pipelineChunkSize = options.pipelineChunkSize ?? 2097152;    // 2MB
    this.bufferTarget = options.bufferTarget ?? 5242880;              // 5MB target buffer
    this.reconnectDelay = options.reconnectDelay ?? 1000;
    this.maxReconnects = options.maxReconnects ?? 10;

    this.mediaSource = null;
    this.sourceBuffer = null;
    this.sessionId = crypto.randomUUID().slice(0, 8);
    this.chunkIdx = 0;
    this.playing = false;
    this.aborted = false;
    this.reconnectCount = 0;

    // Adaptive buffer: handshake → pipeline
    this.mode = 'handshake'; // 'handshake' or 'pipeline'
    this.currentChunkSize = this.handshakeChunkSize;
    this.modeSwitchBufferTime = 5; // seconds of buffer before switching to pipeline
    this.consecutiveSuccesses = 0;
    this.consecutiveFailures = 0;
    this.latencyHistory = [];

    // Metrics
    this.metrics = {
      chunksLoaded: 0,
      bytesLoaded: 0,
      loadTimes: [],
      bufferLevels: [],
      errors: 0,
      mode: 'handshake',
      modeTransitions: 0,
    };

    this._onProgress = null;
    this._onError = null;
    this._onComplete = null;
    this._onModeChange = null;
  }

  /**
   * Initialize MediaSource and attach to video element.
   */
  async load() {
    return new Promise((resolve, reject) => {
      this.mediaSource = new MediaSource();
      this.video.src = URL.createObjectURL(this.mediaSource);

      this.mediaSource.addEventListener('sourceopen', () => {
        this._initSourceBuffer()
          .then(resolve)
          .catch(reject);
      }, { once: true });

      this.mediaSource.addEventListener('error', (e) => {
        this.metrics.errors++;
        if (this._onError) this._onError(e);
        reject(new Error('MediaSource error'));
      });
    });
  }

  async _initSourceBuffer() {
    // Try common codecs in order of preference
    const codecs = [
      'video/mp4; codecs="avc1.64001E, mp4a.40.2"',  // H.264 Baseline + AAC
      'video/mp4; codecs="avc1.42E01E, mp4a.40.2"',  // H.264 Constrained Baseline
      'video/mp4; codecs="avc1.640028, mp4a.40.2"',  // H.264 High
      'video/webm; codecs="vp8, vorbis"',              // VP8 + Vorbis
    ];

    let chosenCodec = null;
    for (const codec of codecs) {
      if (MediaSource.isTypeSupported(codec)) {
        chosenCodec = codec;
        break;
      }
    }

    if (!chosenCodec) {
      throw new Error('No supported codec found');
    }

    this.sourceBuffer = this.mediaSource.addSourceBuffer(chosenCodec);
    this.sourceBuffer.mode = 'segments';

    this.sourceBuffer.addEventListener('updateend', () => {
      this._onBufferUpdate();
    });

    return chosenCodec;
  }

  /**
   * Start playing from the current chunk index.
   */
  async play() {
    if (this.playing) return;
    this.playing = true;
    this.aborted = false;
    this.reconnectCount = 0;

    await this._fetchLoop();
  }

  /**
   * Pause playback.
   */
  pause() {
    this.playing = false;
  }

  /**
   * Stop playback and clean up resources.
   */
  destroy() {
    this.aborted = true;
    this.playing = false;

    if (this.sourceBuffer && !this.sourceBuffer.updating) {
      try {
        this.sourceBuffer.remove(0, this.video.duration || Infinity);
      } catch (e) {
        // Ignore — may already be removed
      }
    }

    if (this.mediaSource && this.mediaSource.readyState === 'open') {
      try {
        this.mediaSource.endOfStream();
      } catch (e) {
        // Ignore
      }
    }

    this.video.removeAttribute('src');
    this.video.load();
  }

  /**
   * Seek to a position (in seconds).
   */
  seek(time) {
    if (!this.video.duration) return;
    const chunkDuration = this.video.duration / Math.max(1, this.chunkIdx);
    this.chunkIdx = Math.floor(time / chunkDuration);
    this.currentChunkSize = this.initialChunkSize;
  }

  /**
   * Register event callbacks.
   */
  onProgress(fn) { this._onProgress = fn; }
  onError(fn) { this._onError = fn; }
  onComplete(fn) { this._onComplete = fn; }
  onModeChange(fn) { this._onModeChange = fn; }

  /**
   * Get current metrics.
   */
  getMetrics() {
    return {
      ...this.metrics,
      currentChunkSize: this.currentChunkSize,
      bufferLevel: this.sourceBuffer ? this.sourceBuffer.buffered.length : 0,
      currentTime: this.video.currentTime,
      duration: this.video.duration || 0,
      sessionId: this.sessionId,
    };
  }

  // --- Internal methods ---

  async _fetchLoop() {
    while (this.playing && !this.aborted) {
      // Check buffer level — pause fetching if buffer is full enough
      if (this._getBufferLevel() > this.bufferTarget) {
        await this._sleep(100);
        continue;
      }

      try {
        const chunk = await this._fetchChunk(this.stationIdx, this.chunkIdx);
        await this._appendChunk(chunk);

        this.metrics.chunksLoaded++;
        this.metrics.bytesLoaded += chunk.byteLength;
        this.reconnectCount = 0;

        // Adaptive chunk size: grow if buffer is low
        this._adaptChunkSize();

        if (this._onProgress) {
          this._onProgress(this.getMetrics());
        }

        this.chunkIdx++;
      } catch (err) {
        if (this.aborted) break;

        this.metrics.errors++;
        console.error(`[MSE] Chunk ${this.chunkIdx} failed:`, err.message);

        if (this._isNetworkError(err)) {
          this.reconnectCount++;
          if (this.reconnectCount > this.maxReconnects) {
            console.error('[MSE] Max reconnects reached, stopping');
            this.playing = false;
            if (this._onError) this._onError(err);
            break;
          }
          const delay = this.reconnectDelay * Math.pow(2, Math.min(this.reconnectCount - 1, 5));
          console.log(`[MSE] Reconnecting in ${delay}ms (attempt ${this.reconnectCount})`);
          await this._sleep(delay);
        } else {
          // Non-network error — skip chunk and continue
          this.chunkIdx++;
        }
      }
    }

    if (this._onComplete && !this.aborted) {
      this._onComplete(this.getMetrics());
    }
  }

  async _fetchChunk(stationIdx, chunkIdx) {
    const url = `${this.baseUrl}?id=${stationIdx}&chunk=${chunkIdx}&session=${this.sessionId}&size=${this.currentChunkSize}&mode=${this.mode}`;
    const start = performance.now();

    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }

    const buffer = await response.arrayBuffer();
    const elapsed = performance.now() - start;
    this.metrics.loadTimes.push(elapsed);
    this.latencyHistory.push(elapsed);

    // Keep last 10 latency samples
    if (this.latencyHistory.length > 10) {
      this.latencyHistory.shift();
    }

    return buffer;
  }

  async _appendChunk(data) {
    return new Promise((resolve, reject) => {
      if (!this.sourceBuffer) {
        reject(new Error('SourceBuffer not initialized'));
        return;
      }

      if (this.sourceBuffer.updating) {
        // Wait for current update to finish
        const handler = () => {
          this.sourceBuffer.removeEventListener('updateend', handler);
          this._appendChunk(data).then(resolve).catch(reject);
        };
        this.sourceBuffer.addEventListener('updateend', handler);
        return;
      }

      try {
        this.sourceBuffer.appendBuffer(new Uint8Array(data));
        const handler = () => {
          this.sourceBuffer.removeEventListener('updateend', handler);
          resolve();
        };
        this.sourceBuffer.addEventListener('updateend', handler, { once: true });
      } catch (err) {
        if (err.name === 'QuotaExceededError') {
          // Buffer full — evict oldest data
          this._evictBuffer().then(() => {
            this._appendChunk(data).then(resolve).catch(reject);
          });
        } else {
          reject(err);
        }
      }
    });
  }

  _evictBuffer() {
    return new Promise((resolve) => {
      if (!this.sourceBuffer || this.sourceBuffer.updating) {
        resolve();
        return;
      }

      const buffered = this.sourceBuffer.buffered;
      if (buffered.length === 0) {
        resolve();
        return;
      }

      // Remove first 10% of buffered data
      const start = buffered.start(0);
      const end = buffered.end(0);
      const removeEnd = start + (end - start) * 0.1;

      this.sourceBuffer.remove(start, removeEnd);
      this.sourceBuffer.addEventListener('updateend', () => resolve(), { once: true });
    });
  }

  _getBufferLevel() {
    if (!this.sourceBuffer || this.sourceBuffer.buffered.length === 0) {
      return 0;
    }
    const buffered = this.sourceBuffer.buffered;
    const start = buffered.start(0);
    const end = buffered.end(0);
    return (end - start) * this._estimateBitrate();
  }

  _estimateBitrate() {
    if (this.metrics.loadTimes.length < 3) return 500000; // 500kbps default
    const recentTimes = this.metrics.loadTimes.slice(-10);
    const avgTime = recentTimes.reduce((a, b) => a + b, 0) / recentTimes.length;
    const avgChunkSize = this.metrics.bytesLoaded / Math.max(1, this.metrics.chunksLoaded);
    return (avgChunkSize * 8) / (avgTime / 1000); // bits per second
  }

  _adaptChunkSize() {
    const bufferLevel = this._getBufferLevel();
    const bufferSeconds = this._getBufferSeconds();
    const avgLatency = this._getAvgLatency();

    // Mode switching: handshake → pipeline
    if (this.mode === 'handshake') {
      // Switch to pipeline when buffer is healthy (>5s) and latency is low (<500ms)
      if (bufferSeconds > this.modeSwitchBufferTime && avgLatency < 500) {
        this.mode = 'pipeline';
        this.currentChunkSize = this.pipelineChunkSize;
        this.metrics.mode = 'pipeline';
        this.metrics.modeTransitions++;
        if (this._onModeChange) this._onModeChange('pipeline');
        console.log(`[MSE] Mode → PIPELINE (buffer=${bufferSeconds.toFixed(1)}s, latency=${avgLatency.toFixed(0)}ms)`);
      }
    } else {
      // Switch back to handshake if buffer drops or latency spikes
      if (bufferSeconds < 2 || avgLatency > 2000) {
        this.mode = 'handshake';
        this.currentChunkSize = this.handshakeChunkSize;
        this.metrics.mode = 'handshake';
        this.metrics.modeTransitions++;
        if (this._onModeChange) this._onModeChange('handshake');
        console.log(`[MSE] Mode → HANDSHAKE (buffer=${bufferSeconds.toFixed(1)}s, latency=${avgLatency.toFixed(0)}ms)`);
      }
    }

    // Adaptive sizing within current mode
    if (bufferLevel < this.bufferTarget * 0.3) {
      this.currentChunkSize = Math.max(this.handshakeChunkSize, this.currentChunkSize * 0.75);
    } else if (bufferLevel > this.bufferTarget * 0.8 && this.mode === 'pipeline') {
      this.currentChunkSize = Math.min(this.pipelineChunkSize, this.currentChunkSize * 1.5);
    }
  }

  _getBufferSeconds() {
    if (!this.sourceBuffer || this.sourceBuffer.buffered.length === 0) return 0;
    const buffered = this.sourceBuffer.buffered;
    const start = buffered.start(0);
    const end = buffered.end(0);
    return end - this.video.currentTime;
  }

  _getAvgLatency() {
    if (this.latencyHistory.length === 0) return 1000;
    return this.latencyHistory.reduce((a, b) => a + b, 0) / this.latencyHistory.length;
  }

  _isNetworkError(err) {
    return err.name === 'TypeError' ||
           err.message.includes('fetch') ||
           err.message.includes('network') ||
           err.message.includes('Failed to fetch');
  }

  _sleep(ms) {
    return new Promise(r => setTimeout(r, ms));
  }
}

// Export for module usage
if (typeof module !== 'undefined') {
  module.exports = MSEPlayer;
}
