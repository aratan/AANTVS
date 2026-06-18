// Switch día/noche
const btnSwitch = document.querySelector('#switch');
if (btnSwitch) {
  btnSwitch.addEventListener('click', () => {
    document.body.classList.toggle('dark');
    btnSwitch.classList.toggle('active');

    if (document.body.classList.contains('dark')) {
      localStorage.setItem('noche', 'true');
    } else {
      localStorage.setItem('noche', 'false');
    }
  });
}

// Restaurar modo guardado
if (localStorage.getItem('noche') === 'true') {
  document.body.classList.add('dark');
}

// Animación de carga y reloj
function carga() {
  setTimeout(() => {
    const logo = document.getElementById('logo');
    const carga = document.getElementById('carga');
    if (logo) logo.innerHTML = document.title;
    if (carga) carga.innerHTML = 'Aquí puede ir tu publicidad.';
    mReloj();
  }, 5000);
}

function mReloj() {
  const t = new Date();
  const hora = String(t.getHours()).padStart(2, '0');
  const minuto = String(t.getMinutes()).padStart(2, '0');
  const segundo = String(t.getSeconds()).padStart(2, '0');
  const horaImprimible = hora + ':' + minuto + ':' + segundo;

  const reloj = document.getElementById('reloj');
  if (reloj) reloj.innerHTML = horaImprimible;

  // Recargar cada 30 minutos
  if (minuto === '30' && segundo === '00') {
    window.location.reload();
  }

  setTimeout(mReloj, 1000);
}
