/** upload.js — drag&drop, preview, async fetch */

(() => {
  const dropzone   = document.getElementById('dropzone');
  const fileInput   = document.getElementById('fileInput');
  const browseBtn   = document.getElementById('browseBtn');
  const previewBox  = document.getElementById('preview');
  const imgEl       = document.getElementById('preview-img');
  const vidEl       = document.getElementById('preview-vid');
  const fileInfoEl  = document.getElementById('fileInfo');
  const uploadBtn   = document.getElementById('uploadBtn');
  const statusEl    = document.getElementById('status');

  let currentFile = null;

  const MAX_FILE_SIZE = 50 * 1024 * 1024; // 50 MB
  const videoTypes    = ['video/mp4', 'video/webm', 'video/ogg'];
  const imageTypes    = ['image/jpeg', 'image/png', 'image/gif', 'image/webp'];

  /* ---- Helpers ---- */

  function show(element) { element.classList.remove('hidden'); }
  function hide(el)       { el.classList.add('hidden');     }
  function fmtSize(bytes) { return (bytes / (1024 * 1024)).toFixed(2) + ' MB'; }
  function setStatus(msg, type) {
    statusEl.textContent = msg;
    statusEl.className   = 'status status--' + type;
    show(statusEl);
  }

  /* ---- Dropzone interactions ---- */

  browseBtn.addEventListener('click', () => fileInput.click());
  dropzone.addEventListener('click',  () => fileInput.click());

  ['dragenter', 'dragover'].forEach(evt => {
    dropzone.addEventListener(evt, e => { e.preventDefault(); dropzone.classList.add('drag-over'); });
  });
  ['dragleave', 'drop'].forEach(evt => {
    dropzone.addEventListener(evt, e => { e.preventDefault(); dropzone.classList.remove('drag-over'); });
  });

  dropzone.addEventListener('drop', e => {
    const file = e.dataTransfer?.files?.[0];
    if (file) handleFile(file);
  });

  fileInput.addEventListener('change', () => {
    const file = fileInput.files?.[0];
    if (file) handleFile(file);
  });

  /* ---- Preview ---- */

  function handleFile(file) {
    // Client-side size check
    if (file.size > MAX_FILE_SIZE) {
      setStatus('Fichero demasiado grande — máx. ' + fmtSize(MAX_FILE_SIZE), 'error');
      fileInput.value = '';
      return;
    }

    hide(previewBox);
    hide(imgEl);
    hide(vidEl);
    imgEl.src = '';
    vidEl.src = '';

    if (imageTypes.includes(file.type)) {
      show(imgEl);
      imgEl.src = URL.createObjectURL(file);
    } else if (videoTypes.includes(file.type) || file.type.startsWith('video/')) {
      show(vidEl);
      vidEl.src = URL.createObjectURL(file);
    }

    fileInfoEl.textContent  = file.name + ' — ' + fmtSize(file.size);
    currentFile             = file;
    show(previewBox);
  }

  /* ---- Upload ---- */

  uploadBtn.addEventListener('click', async () => {
    if (!currentFile) return;

    // Revalidate size (server-side, but good UX to warn first)
    if (currentFile.size > MAX_FILE_SIZE) {
      setStatus('Fichero demasiado grande — máx. ' + fmtSize(MAX_FILE_SIZE), 'error');
      return;
    }

    uploadBtn.disabled   = true;
    uploadBtn.textContent = 'Subiendo…';
    setStatus('Subiendo fichero…', 'loading');

    const formData = new FormData();
    formData.append('archivo', currentFile, currentFile.name);

    try {
      const resp  = await fetch('/api', { method: 'POST', body: formData });
      const data  = await resp.json();

      if (data.ok) {
        setStatus('✅ Cargado — ' + data.file + ' (' + fmtSize(data.size) + ') → <a href="' + data.path + '" target="_blank" class="browse-link">' + data.path + '</a>', 'success');
        fileInput.value         = '';
        imgEl.src               = '';
        vidEl.src               = '';
        currentFile             = null;
        hide(previewBox);
      } else {
        setStatus('❌ ' + (data.error || 'Error desconocido'), 'error');
      }
    } catch (err) {
      setStatus('❌ Error de red — ' + err.message, 'error');
    } finally {
      uploadBtn.disabled   = false;
      uploadBtn.textContent = 'Subir';
    }
  });

})();
