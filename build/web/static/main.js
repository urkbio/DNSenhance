let charts = {};

function initCharts() {
    // 位置分布图表
    charts.location = new Chart(document.getElementById('locationChart'), {
        type: 'pie',
        data: {
            labels: ['国内查询', '国外查询'],
            datasets: [{
                data: [0, 0],
                backgroundColor: ['#4CAF50', '#2196F3']
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: {
                    position: 'bottom'
                },
                title: {
                    display: true,
                    text: '查询域名分布'
                }
            }
        }
    });

    // 缓存命中图表
    charts.cache = new Chart(document.getElementById('cacheChart'), {
        type: 'doughnut',
        data: {
            labels: ['命中', '未命中'],
            datasets: [{
                data: [0, 0],
                backgroundColor: ['#4CAF50', '#FF5722']
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: {
                    position: 'bottom'
                },
                title: {
                    display: true,
                    text: '缓存命中率'
                }
            }
        }
    });

    // 拦截统计图表
    charts.block = new Chart(document.getElementById('blockChart'), {
        type: 'doughnut',
        data: {
            labels: ['已拦截', '已放行'],
            datasets: [{
                data: [0, 0],
                backgroundColor: ['#F44336', '#4CAF50']
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: {
                    position: 'bottom'
                },
                title: {
                    display: true,
                    text: '域名拦截统计'
                }
            }
        }
    });
}

function updateStats() {
    fetch('/api/stats')
        .then(response => response.json())
        .then(data => {
            // 更新仪表盘卡片
            document.getElementById('allTimeQueries').textContent = data.allTimeQueries.toLocaleString();
            document.getElementById('currentQPS').textContent = data.currentQPS;
            document.getElementById('peakQPS').textContent = `峰值: ${data.peakQPS} QPS`;
            document.getElementById('uptime').textContent = data.uptime;
            document.getElementById('startTime').textContent = `启动于: ${data.startTime}`;
            document.getElementById('hitRate').textContent = data.hitRate.toFixed(1) + '%';
            document.getElementById('cacheHits').textContent = `总命中: ${data.cacheHits.toLocaleString()}`;
            document.getElementById('queryStats').textContent = 
                `${data.cnQueries.toLocaleString()}/${data.foreignQueries.toLocaleString()}`;
            document.getElementById('avgLatency').textContent = 
                `${(data.dns_latency.avg).toFixed(1)}ms`;

            // 更新图表数据
            if (charts.location) {
                const locationData = {
                    labels: ['国内查询', '国外查询'],
                    datasets: [{
                        data: [data.cnQueries, data.foreignQueries],
                        backgroundColor: ['#4CAF50', '#2196F3']
                    }]
                };
                charts.location.data = locationData;
                charts.location.update();
            }

            if (charts.cache) {
                const cacheData = {
                    labels: ['命中', '未命中'],
                    datasets: [{
                        data: [data.cacheHits, data.totalQueries - data.cacheHits],
                        backgroundColor: ['#4CAF50', '#FF5722']
                    }]
                };
                charts.cache.data = cacheData;
                charts.cache.update();
            }

            if (charts.block) {
                const blockData = {
                    labels: ['已拦截', '已放行'],
                    datasets: [{
                        data: [data.blockedQueries, data.totalQueries - data.blockedQueries],
                        backgroundColor: ['#F44336', '#4CAF50']
                    }]
                };
                charts.block.data = blockData;
                charts.block.update();
            }

            // 更新域名统计
            updateDomainStats('topDomains', data.topDomains);
            updateDomainStats('topBlocked', data.topBlocked);
        })
        .catch(error => console.error('Error:', error));
}

function updateDomainStats(elementId, data) {
    const element = document.getElementById(elementId);
    if (element && data?.length > 0) {
        // 按访问量降序排序
        const sortedData = [...data].sort((a, b) => b.count - a.count);
        const html = sortedData.map(item => `
            <tr>
                <td class="domain-cell">${escapeHtml(item.domain)}</td>
                <td class="text-right">${item.count.toLocaleString()}</td>
            </tr>
        `).join('');
        // 清空现有内容并添加新内容
        while (element.firstChild) {
            element.removeChild(element.firstChild);
        }
        element.innerHTML = html;
        
        // 确保表格可以滚动
        const tableResponsive = element.closest('.table-responsive');
        if (tableResponsive) {
            tableResponsive.scrollTop = 0;
        }
    }
}

// 添加 HTML 转义函数
function escapeHtml(unsafe) {
    return unsafe
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#039;");
}

// 确保在页面加载完成后再初始化
window.onload = function() {
    initCharts();
    updateStats();
    setInterval(updateStats, 10000);
}; 