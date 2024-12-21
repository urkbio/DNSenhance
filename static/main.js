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
                title: {
                    display: true,
                    text: '查询域名分布'
                }
            }
        }
    });

    // 缓存命中图表
    charts.cache = new Chart(document.getElementById('cacheChart').getContext('2d'), {
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
            maintainAspectRatio: false
        }
    });

    // 拦截统计图表
    charts.block = new Chart(document.getElementById('blockChart').getContext('2d'), {
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
            maintainAspectRatio: false
        }
    });
}

function updateStats() {
    fetch('/api/stats')
        .then(response => response.json())
        .then(data => {
            console.log('Received data:', data);
            if (!data) {
                throw new Error('No data received');
            }
            if (!data.topDomains) {
                console.warn('No topDomains in data:', data);
            }
            if (!data.topBlocked) {
                console.warn('No topBlocked in data:', data);
            }

            // 更新仪表盘数据
            if (data) {
                document.getElementById('allTimeQueries').textContent = 
                    data.all_time_queries.toLocaleString();
                document.getElementById('currentQPS').textContent = data.currentQPS;
                document.getElementById('peakQPS').textContent = `峰值: ${data.peakQPS} QPS`;
                document.getElementById('uptime').textContent = data.uptime;
                document.getElementById('startTime').textContent = `启动于: ${data.startTime}`;
                document.getElementById('hitRate').textContent = `${data.hitRate.toFixed(1)}%`;
                document.getElementById('cacheHits').textContent = `总命中: ${data.cacheHits}`;
                document.getElementById('queryStats').textContent = `${data.cnQueries}/${data.foreignQueries}`;

                // 更新图表数据
                if (charts.location) {
                    charts.location.data.datasets[0].data = [data.cnQueries, data.foreignQueries];
                    charts.location.update();
                }
                if (charts.cache) {
                    charts.cache.data.datasets[0].data = [data.cacheHits, data.totalQueries - data.cacheHits];
                    charts.cache.update();
                }
                if (charts.block) {
                    charts.block.data.datasets[0].data = [data.blockedQueries, data.totalQueries - data.blockedQueries];
                    charts.block.update();
                }

                // 更新域名列表
                const topDomainsElement = document.getElementById('topDomains');
                const topBlockedElement = document.getElementById('topBlocked');

                console.log('Domain stats:', {
                    hasTopDomainsElement: !!topDomainsElement,
                    hasTopBlockedElement: !!topBlockedElement,
                    topDomainsLength: data.topDomains?.length,
                    topBlockedLength: data.topBlocked?.length
                });

                if (topDomainsElement && data.topDomains?.length > 0) {
                    const sortedTopDomains = data.topDomains.sort((a, b) => b.count - a.count);
                    const topDomainsHtml = sortedTopDomains.map(item => `
                        <tr>
                            <td class="domain-cell">${escapeHtml(item.domain)}</td>
                            <td class="text-right">${item.count.toLocaleString()}</td>
                        </tr>
                    `).join('');
                    console.log('Generated top domains HTML:', topDomainsHtml);
                    topDomainsElement.innerHTML = topDomainsHtml;
                }

                if (topBlockedElement && data.topBlocked?.length > 0) {
                    const sortedTopBlocked = data.topBlocked.sort((a, b) => b.count - a.count);
                    const topBlockedHtml = sortedTopBlocked.map(item => `
                        <tr>
                            <td class="domain-cell">${escapeHtml(item.domain)}</td>
                            <td class="text-right">${item.count.toLocaleString()}</td>
                        </tr>
                    `).join('');
                    console.log('Generated top blocked HTML:', topBlockedHtml);
                    topBlockedElement.innerHTML = topBlockedHtml;
                }
            }

            // 更新DNS性能统计
            if (data.dns_latency) {
                document.getElementById('avgLatency').textContent = 
                    `${(data.dns_latency.avg * 1000).toFixed(2)} ms`;
                document.getElementById('p95Latency').textContent = 
                    `${(data.dns_latency.p95 * 1000).toFixed(2)} ms`;
                document.getElementById('p99Latency').textContent = 
                    `${(data.dns_latency.p99 * 1000).toFixed(2)} ms`;
            }

            // 更新上游服务器统计
            const upstreamStats = document.getElementById('upstreamStats');
            if (upstreamStats && data.upstream_stats) {
                const html = data.upstream_stats
                    .sort((a, b) => b.usage - a.usage)
                    .map(stat => {
                        return `
                            <tr>
                                <td class="endpoint">${escapeHtml(stat.endpoint)}</td>
                                <td class="number">${stat.usage.toLocaleString()}</td>
                            </tr>
                        `;
                    }).join('');
                upstreamStats.innerHTML = html || `
                    <tr>
                        <td colspan="2" class="text-center">暂无数据</td>
                    </tr>
                `;
            }
        })
        .catch(error => {
            console.error('Error updating stats:', error);
            console.error('Error stack:', error.stack);
            console.error('Error details:', {
                message: error.message,
                name: error.name
            });
        });
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
    setInterval(updateStats, 1000);
}; 