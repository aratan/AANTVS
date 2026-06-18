// === Theme Toggle ===
const themeToggle = document.getElementById('themeToggle');
const themeIcon = themeToggle?.querySelector('i');

function setTheme(isDark) {
  document.body.classList.toggle('light', !isDark);
  if (themeIcon) {
    themeIcon.className = isDark ? 'fas fa-moon' : 'fas fa-sun';
  }
  localStorage.setItem('theme', isDark ? 'dark' : 'light');
}

// Initialize theme
const savedTheme = localStorage.getItem('theme') || 'dark';
setTheme(savedTheme === 'dark');

themeToggle?.addEventListener('click', () => {
  const isLight = document.body.classList.contains('light');
  setTheme(!isLight);
});

// === Navbar Scroll Effect ===
const navbar = document.querySelector('.navbar');
let lastScroll = 0;

window.addEventListener('scroll', () => {
  const currentScroll = window.pageYOffset;
  
  if (currentScroll > 50) {
    navbar?.classList.add('scrolled');
  } else {
    navbar?.classList.remove('scrolled');
  }
  
  lastScroll = currentScroll;
});

// === Video Player ===
const playBtn = document.getElementById('playBtn');
const playerSection = document.getElementById('playerSection');
const playerClose = document.getElementById('playerClose');
const mainVideo = document.getElementById('video');

playBtn?.addEventListener('click', () => {
  playerSection?.classList.add('active');
  document.body.style.overflow = 'hidden';
  // Play video if it's loaded
  if (mainVideo) {
    mainVideo.play().catch(() => {});
  }
});

playerClose?.addEventListener('click', () => {
  playerSection?.classList.remove('active');
  document.body.style.overflow = '';
  if (mainVideo) {
    mainVideo.pause();
  }
});

// Close player with Escape key
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && playerSection?.classList.contains('active')) {
    playerSection.classList.remove('active');
    document.body.style.overflow = '';
    if (mainVideo) {
      mainVideo.pause();
    }
  }
});

// === Row Navigation ===
document.querySelectorAll('.row-nav').forEach(btn => {
  btn.addEventListener('click', () => {
    const row = btn.closest('.row-container')?.querySelector('.row-scroll');
    if (!row) return;
    
    const scrollAmount = row.clientWidth * 0.8;
    const isLeft = btn.classList.contains('row-nav-left');
    
    row.scrollBy({
      left: isLeft ? -scrollAmount : scrollAmount,
      behavior: 'smooth'
    });
  });
});

// === Movie Card Hover Effect ===
document.querySelectorAll('.movie').forEach(card => {
  card.addEventListener('mouseenter', () => {
    // Pause other cards' animations
    document.querySelectorAll('.movie').forEach(c => {
      if (c !== card) c.style.zIndex = '';
    });
    card.style.zIndex = '10';
  });
  
  card.addEventListener('mouseleave', () => {
    card.style.zIndex = '';
  });
  
  // Click to play
  card.addEventListener('click', () => {
    // Get video URL from the card if available
    const video = card.querySelector('video');
    if (video) {
      // If card has a video, show it in the player
      playerSection?.classList.add('active');
      document.body.style.overflow = 'hidden';
    }
  });
});

// === Search Button (placeholder) ===
const searchBtn = document.getElementById('searchBtn');
searchBtn?.addEventListener('click', () => {
  // TODO: Implement search functionality
  alert('Búsqueda próximamente');
});

// === Smooth Scroll for Anchor Links ===
document.querySelectorAll('a[href^="#"]').forEach(anchor => {
  anchor.addEventListener('click', function(e) {
    e.preventDefault();
    const target = document.querySelector(this.getAttribute('href'));
    if (target) {
      target.scrollIntoView({
        behavior: 'smooth',
        block: 'start'
      });
    }
  });
});

// === Lazy Load Images ===
if ('IntersectionObserver' in window) {
  const imageObserver = new IntersectionObserver((entries, observer) => {
    entries.forEach(entry => {
      if (entry.isIntersecting) {
        const img = entry.target;
        if (img.dataset.src) {
          img.src = img.dataset.src;
          img.removeAttribute('data-src');
        }
        observer.unobserve(img);
      }
    });
  });

  document.querySelectorAll('img[data-src]').forEach(img => {
    imageObserver.observe(img);
  });
}

// === Add Loading State ===
function showLoading(element) {
  element.innerHTML = `
    <div class="loading">
      <i class="fas fa-spinner"></i>
      <span>Cargando...</span>
    </div>
  `;
}

// === Error Handling ===
function handleError(error) {
  console.error('Error:', error);
  // Could show a toast notification here
}

// === Keyboard Navigation ===
document.addEventListener('keydown', (e) => {
  // Space to play/pause
  if (e.code === 'Space' && playerSection?.classList.contains('active')) {
    e.preventDefault();
    if (mainVideo?.paused) {
      mainVideo.play();
    } else {
      mainVideo?.pause();
    }
  }
  
  // Arrow keys for volume
  if (playerSection?.classList.contains('active')) {
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (mainVideo) mainVideo.volume = Math.min(1, mainVideo.volume + 0.1);
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (mainVideo) mainVideo.volume = Math.max(0, mainVideo.volume - 0.1);
    }
  }
});

// === Initialize ===
document.addEventListener('DOMContentLoaded', () => {
  // Add loaded class to body
  document.body.classList.add('loaded');
  
  console.log('🎬 AANTVS initialized');
});
