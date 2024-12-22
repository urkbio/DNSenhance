// 时间序列图表实例
let timeSeriesChart = null;

// 销毁时间序列图表
function destroyTimeSeriesChart() {
    if (timeSeriesChart) {
        timeSeriesChart.destroy();
        timeSeriesChart = null;
    }
}

// 初始化时间序列图表
function initTimeSeriesChart() {
    const ctx = document.getElementById('timeSeriesChart');
    if (!ctx) {
        console.error('Cannot find timeSeriesChart canvas element');
        return;
    }

    const config = {
        type: 'line',
        data: {
            datasets: [
                {
                    label: '总查询',
                    borderColor: '#2196F3',
                    backgroundColor: 'rgba(33, 150, 243, 0.1)',
                    data: [],
                    fill: true
                },
                {
                    label: '成功解析',
                    borderColor: '#4CAF50',
                    backgroundColor: 'rgba(76, 175, 80, 0.1)',
                    data: [],
                    fill: true
                },
                {
                    label: '缓存命中',
                    borderColor: '#03A9F4',
                    backgroundColor: 'rgba(3, 169, 244, 0.1)',
                    data: [],
                    fill: true
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: {
                mode: 'index',
                intersect: false
            },
            plugins: {
                legend: {
                    position: 'top',
                    labels: {
                        usePointStyle: true,
                        padding: 15,
                        font: { size: 11 }
                    }
                },
                tooltip: {
                    mode: 'index',
                    intersect: false
                }
            },
            scales: {
                x: {
                    type: 'time',
                    time: {
                        unit: 'minute',
                        displayFormats: {
                            minute: 'HH:mm'
                        },
                        tooltipFormat: 'MM/dd HH:mm'
                    },
                    grid: {
                        display: false
                    }
                },
                y: {
                    beginAtZero: true,
                    grid: {
                        color: 'rgba(0, 0, 0, 0.1)'
                    }
                }
            }
        }
    };

    try {
        timeSeriesChart = new Chart(ctx, config);
        console.log('Time series chart initialized');
    } catch (error) {
        console.error('Failed to initialize time series chart:', error);
    }
}

// 更新时间序列数据
function updateTimeSeriesData(timeSeriesData) {
    if (!timeSeriesChart) {
        console.log('Initializing time series chart');
        initTimeSeriesChart();
    }

    if (!timeSeriesData || !Array.isArray(timeSeriesData) || timeSeriesData.length === 0) {
        console.warn('No time series data available');
        return;
    }

    try {
        // 处理数据
        const processedData = timeSeriesData
            .map(d => ({
                timestamp: new Date(d.timestamp),
                total: d.total || 0,
                success: d.success || 0,
                cache_hits: d.cache_hits || 0
            }))
            .sort((a, b) => a.timestamp - b.timestamp);

        // 更新数据集
        timeSeriesChart.data.datasets[0].data = processedData.map(d => ({
            x: d.timestamp,
            y: d.total
        }));

        timeSeriesChart.data.datasets[1].data = processedData.map(d => ({
            x: d.timestamp,
            y: d.success
        }));

        timeSeriesChart.data.datasets[2].data = processedData.map(d => ({
            x: d.timestamp,
            y: d.cache_hits
        }));

        // 更新图表
        timeSeriesChart.update('none');
        console.log('Time series chart updated with', processedData.length, 'data points');
    } catch (error) {
        console.error('Failed to update time series chart:', error);
        // 如果更新失败，尝试重新初始化
        destroyTimeSeriesChart();
        initTimeSeriesChart();
    }
}
