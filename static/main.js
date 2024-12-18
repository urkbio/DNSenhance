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
    charts.cache = new Chart(document.getElementById('cacheChart'), {
        type: 'pie',
        data: {
            labels: ['缓存命中', '实际查询'],
            datasets: [{
                data: [0, 0],
                backgroundColor: ['#FFC107', '#9C27B0']
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                title: {
                    display: true,
                    text: '缓存命中统计'
                }
            }
        }
    });

    // 拦截统计图表
    charts.block = new Chart(document.getElementById('blockChart'), {
        type: 'pie',
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
            // 更新仪表盘数据
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
        })
        .catch(error => console.error('Error updating stats:', error));
}

// 定期更新统计数据
setInterval(updateStats, 1000);

// 初始化图表
const charts = {
    location: new Chart(document.getElementById('locationChart').getContext('2d'), {
        type: 'doughnut',
        data: {
            labels: ['国内', '国外'],
            datasets: [{
                data: [0, 0],
                backgroundColor: ['#4CAF50', '#2196F3']
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false
        }
    }),
    cache: new Chart(document.getElementById('cacheChart').getContext('2d'), {
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
    }),
    block: new Chart(document.getElementById('blockChart').getContext('2d'), {
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
    })
};

// 立即更新一次数据
updateStats();

// 页面加载完成后初始化
window.onload = function() {
    initCharts();
    updateStats();
    // 每5秒更新一次数据
    setInterval(updateStats, 5000);
}; 