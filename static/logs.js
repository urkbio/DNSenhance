let autoRefreshEnabled = true;
let refreshInterval;

function refreshLogs() {
    const tbody = document.getElementById('logsTableBody');
    if (tbody) {
        tbody.style.opacity = '0.5';  // 添加加载效果
    }

    return fetch('/api/logs')
        .then(response => response.text())
        .then(html => {
            if (tbody) {
                tbody.innerHTML = html;
                tbody.style.opacity = '1';  // 恢复正常显示
                const searchInput = document.getElementById('searchInput');
                if (searchInput.value.trim()) {
                    searchLogs();
                }
            }
        })
        .catch(error => {
            console.error('Error refreshing logs:', error);
            if (tbody) {
                tbody.style.opacity = '1';  // 出错时也恢复正常显示
            }
        });
}

function toggleAutoRefresh() {
    const btn = document.getElementById('autoRefreshBtn');
    autoRefreshEnabled = !autoRefreshEnabled;
    
    if (autoRefreshEnabled) {
        startAutoRefresh();
    } else {
        stopAutoRefresh();
    }
}

function startAutoRefresh() {
    const btn = document.getElementById('autoRefreshBtn');
    autoRefreshEnabled = true;
    btn.innerHTML = '<i class="mdi mdi-sync"></i> 停止自动刷新';
    btn.style.backgroundColor = '#F44336';
    refreshLogs();  // 立即刷新一次
    refreshInterval = setInterval(refreshLogs, 5000);
}

function stopAutoRefresh() {
    const btn = document.getElementById('autoRefreshBtn');
    autoRefreshEnabled = false;
    btn.innerHTML = '<i class="mdi mdi-sync"></i> 开启自动刷新';
    btn.style.backgroundColor = '#2196F3';
    if (refreshInterval) {
        clearInterval(refreshInterval);
        refreshInterval = null;
    }
}

function searchLogs() {
    const searchTerm = document.getElementById('searchInput').value.toLowerCase().trim();
    const rows = document.getElementsByClassName('log-row');
    let hasResults = false;

    // 如果搜索框有内容，停止自动刷新
    if (searchTerm) {
        stopAutoRefresh();
    } else {
        // 如果搜索框为空，重新开启自动刷新
        startAutoRefresh();
        return; // 不需要继续搜索
    }

    for (let row of rows) {
        const domain = row.querySelector('.domain').textContent.toLowerCase();
        if (domain.includes(searchTerm)) {
            row.style.display = '';
            hasResults = true;
        } else {
            row.style.display = 'none';
        }
    }

    const noResults = document.getElementById('noResults');
    noResults.style.display = hasResults ? 'none' : 'block';
}

// 页面加载时初始化
window.onload = function() {
    refreshLogs();  // 初始加载日志
    startAutoRefresh();  // 默认开启自动刷新
    
    // 监听搜索框输入
    const searchInput = document.getElementById('searchInput');
    let searchTimeout;
    
    searchInput.addEventListener('input', function() {
        // 清除之前的定时器
        if (searchTimeout) {
            clearTimeout(searchTimeout);
        }
        
        // 设置新的定时器，300ms 后执行搜索
        searchTimeout = setTimeout(() => {
            searchLogs();
        }, 300);
    });

    // 监听搜索框失去焦点
    searchInput.addEventListener('blur', function() {
        if (!this.value.trim()) {
            startAutoRefresh();
        }
    });

    // 回车触发搜索
    searchInput.addEventListener('keyup', function(e) {
        if (e.key === 'Enter') {
            searchLogs();
        }
    });
}; 