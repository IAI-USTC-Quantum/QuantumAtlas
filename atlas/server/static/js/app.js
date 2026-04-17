/**
 * QuantumAtlas Client-side JavaScript
 */

// Utility functions
function formatDate(dateString) {
    const date = new Date(dateString);
    return date.toLocaleDateString('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit'
    });
}

// Toast notification
function showToast(message, type = 'info') {
    const toast = document.createElement('div');
    toast.className = `fixed bottom-4 right-4 px-6 py-3 rounded-lg text-white ${
        type === 'success' ? 'bg-green-600' :
        type === 'error' ? 'bg-red-600' :
        'bg-blue-600'
    } shadow-lg transition-opacity duration-300`;
    toast.textContent = message;
    document.body.appendChild(toast);

    setTimeout(() => {
        toast.style.opacity = '0';
        setTimeout(() => toast.remove(), 300);
    }, 3000);
}

// API helpers
async function apiRequest(url, options = {}) {
    try {
        const response = await fetch(url, {
            ...options,
            headers: {
                'Content-Type': 'application/json',
                ...options.headers
            }
        });

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }

        return await response.json();
    } catch (error) {
        showToast(error.message, 'error');
        throw error;
    }
}

// Initialize on page load
document.addEventListener('DOMContentLoaded', () => {
    // Highlight code blocks
    if (typeof hljs !== 'undefined') {
        hljs.highlightAll();
    }

    // Render markdown content
    if (typeof marked !== 'undefined') {
        const wikiContent = document.querySelectorAll('.wiki-markdown');
        wikiContent.forEach(el => {
            el.innerHTML = marked.parse(el.textContent);
        });
    }
});