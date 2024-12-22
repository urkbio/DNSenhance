// 更新统计数据
function updateStats() {
    console.log('开始更新统计数据...');
    fetch('/api/stats')
        .then(response => {
            console.log('收到响应:', response.status);
            return response.json();
        })
        .then(data => {
            console.log('解析数据:', data);
            
            // 更新基本统计
            document.getElementById('currentQPS').textContent = data.current_qps || 0;
            document.getElementById('peakQPS').textContent = `峰值: ${data.peak_qps || 0} QPS`;
            document.getElementById('avgLatency').textContent = `${data.avg_latency ? data.avg_latency.toFixed(2) : '0.00'}ms`;
            document.getElementById('allTimeQueries').textContent = (data.all_time_queries || 0).toLocaleString();
            document.getElementById('hitRate').textContent = `${data.cache_hit_rate ? data.cache_hit_rate.toFixed(2) : '0.00'}%`;
            document.getElementById('cacheHits').textContent = `总命中: ${(data.cache_hits || 0).toLocaleString()}`;
            document.getElementById('queryStats').textContent = `${(data.cn_queries || 0).toLocaleString()}/${(data.foreign_queries || 0).toLocaleString()}`;

            // 计算运行时间
            if (data.start_time) {
                const startTime = new Date(data.start_time);
                const now = new Date();
                const uptime = now - startTime;
                const days = Math.floor(uptime / (1000 * 60 * 60 * 24));
                const hours = Math.floor((uptime % (1000 * 60 * 60 * 24)) / (1000 * 60 * 60));
                const minutes = Math.floor((uptime % (1000 * 60 * 60)) / (1000 * 60));
                document.getElementById('uptime').textContent = `${days}d ${hours}h ${minutes}m`;
                document.getElementById('startTime').textContent = `启动于: ${startTime.toLocaleTimeString()}`;
            }

            // 更新图表
            updateCharts(data);

            // 更新域名列表
            if (data.top_domains) {
                updateTopDomains(data.top_domains, data.top_blocked || []);
            }
        })
        .catch(error => {
            console.error('获取统计数据失败:', error);
            // 清空所有数据
            document.getElementById('currentQPS').textContent = '0';
            document.getElementById('peakQPS').textContent = '峰值: 0 QPS';
            document.getElementById('avgLatency').textContent = '0.00ms';
            document.getElementById('allTimeQueries').textContent = '0';
            document.getElementById('hitRate').textContent = '0.00%';
            document.getElementById('cacheHits').textContent = '总命中: 0';
            document.getElementById('queryStats').textContent = '0/0';
            document.getElementById('uptime').textContent = '0m';
            document.getElementById('startTime').textContent = '启动于: --:--:--';
        });
}

// 更新图表
function updateCharts(data) {
    try {
        // 位置分布图表
        const locationData = {
            labels: ['国内', '国外'],
            datasets: [{
                data: [data.cn_queries || 0, data.foreign_queries || 0],
                backgroundColor: ['#4CAF50', '#2196F3'],
                borderWidth: 0
            }]
        };

        const locationChart = document.getElementById('locationChart');
        if (locationChart) {
            if (!locationChart.chart) {
                console.log('创建位置分布图表');
                locationChart.chart = new Chart(locationChart, {
                    type: 'doughnut',
                    data: locationData,
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        plugins: {
                            legend: {
                                position: 'bottom'
                            }
                        },
                        animation: {
                            duration: 0
                        }
                    }
                });
            } else {
                console.log('更新位置分布图表');
                locationChart.chart.data = locationData;
                locationChart.chart.update('none');
            }
        }

        // 状态分布图表
        const statusData = {
            labels: ['成功', '缓存命中', '失败', '已拦截'],
            datasets: [{
                data: [
                    Math.max(0, (data.total_queries || 0) - (data.cache_hits || 0) - (data.failed_queries || 0) - (data.blocked_queries || 0)),
                    data.cache_hits || 0,
                    data.failed_queries || 0,
                    data.blocked_queries || 0
                ],
                backgroundColor: ['#4CAF50', '#2196F3', '#F44336', '#FFC107'],
                borderWidth: 0
            }]
        };

        const statusChart = document.getElementById('statusChart');
        if (statusChart) {
            if (!statusChart.chart) {
                console.log('创建状态分布图表');
                statusChart.chart = new Chart(statusChart, {
                    type: 'doughnut',
                    data: statusData,
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        plugins: {
                            legend: {
                                position: 'bottom'
                            }
                        },
                        animation: {
                            duration: 0
                        }
                    }
                });
            } else {
                console.log('更新状态分布图表');
                statusChart.chart.data = statusData;
                statusChart.chart.update('none');
            }
        }
    } catch (error) {
        console.error('更新图表失败:', error);
    }
}

// 更新热门域名列表
function updateTopDomains(topDomains, topBlocked) {
    try {
        const normalList = document.getElementById('topDomains');
        const blockedList = document.getElementById('topBlocked');

        if (normalList) {
            // 更新正常域名列表
            normalList.innerHTML = '';
            topDomains.forEach(item => {
                const row = document.createElement('tr');
                row.innerHTML = `
                    <td>${item.domain}</td>
                    <td>${(item.count || 0).toLocaleString()}</td>
                `;
                normalList.appendChild(row);
            });
        }

        if (blockedList && topBlocked) {
            // 更新被拦截域名列表
            blockedList.innerHTML = '';
            topBlocked.forEach(item => {
                const row = document.createElement('tr');
                row.innerHTML = `
                    <td>${item.domain}</td>
                    <td>${(item.count || 0).toLocaleString()}</td>
                `;
                blockedList.appendChild(row);
            });
        }
    } catch (error) {
        console.error('更新域名列表失败:', error);
    }
}

// 定期更新统计数据
document.addEventListener('DOMContentLoaded', () => {
    console.log('页面加载完成，开始初始化...');
    // 确保 Chart.js 已加载
    if (typeof Chart === 'undefined') {
        console.error('Chart.js 未加载！');
        return;
    }
    console.log('Chart.js 已加载');
    
    // 初始更新
    updateStats();
    
    // 定期更新
    setInterval(updateStats, 1000);
});
