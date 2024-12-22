// 图表实例
let queryChart = null;
let timeSeriesChart = null;

// 图表颜色配置
const chartColors = {
    total: {
        label: '总查询',
        border: '#2196F3',
        background: 'rgba(33, 150, 243, 0.1)'
    },
    success: {
        label: '成功解析',
        border: '#4CAF50',
        background: 'rgba(76, 175, 80, 0.1)'
    },
    cache_hits: {
        label: '缓存命中',
        border: '#03A9F4',
        background: 'rgba(3, 169, 244, 0.1)'
    },
    failed: {
        label: '解析失败',
        border: '#F44336',
        background: 'rgba(244, 67, 54, 0.1)'
    },
    blocked: {
        label: '已拦截',
        border: '#FFC107',
        background: 'rgba(255, 193, 7, 0.1)'
    },
    cn: {
        label: '国内查询',
        border: '#009688',
        background: 'rgba(0, 150, 136, 0.1)'
    },
    foreign: {
        label: '国外查询',
        border: '#673AB7',
        background: 'rgba(103, 58, 183, 0.1)'
    }
};

// 销毁所有图表
function destroyCharts() {
    if (queryChart) {
        queryChart.destroy();
        queryChart = null;
    }
    if (timeSeriesChart) {
        timeSeriesChart.destroy();
        timeSeriesChart = null;
    }
}

// 初始化时间序列图表
function initTimeSeriesChart() {
    if (timeSeriesChart) {
        timeSeriesChart.destroy();
        timeSeriesChart = null;
    }

    const ctx = document.getElementById('timeSeriesChart');
    if (!ctx) {
        console.error('Cannot find timeSeriesChart canvas element');
        return;
    }

    const datasets = Object.entries(chartColors).map(([key, color]) => ({
        label: color.label,
        borderColor: color.border,
        backgroundColor: color.background,
        data: [],
        fill: true,
        tension: 0.4
    }));

    const config = {
        type: 'line',
        data: { datasets },
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
                    intersect: false,
                    callbacks: {
                        label: function(context) {
                            const value = context.raw.y;
                            return `${context.dataset.label}: ${value.toLocaleString()}`;
                        }
                    }
                }
            },
            scales: {
                x: {
                    type: 'time',
                    time: {
                        unit: 'hour',
                        stepSize: 1,
                        displayFormats: {
                            hour: 'HH:mm',
                            day: 'MM-DD'
                        },
                        tooltipFormat: 'MM-DD HH:mm'
                    },
                    grid: {
                        display: false
                    },
                    adapters: {
                        date: {
                            locale: 'zh-CN'
                        }
                    },
                    ticks: {
                        maxRotation: 0,
                        autoSkip: true,
                        maxTicksLimit: 12,
                        callback: function(value, index, values) {
                            const date = new Date(value);
                            const hour = date.getHours();
                            // 每6小时显示一次完整时间，其他时候只显示小时
                            return hour % 6 === 0 ? 
                                `${date.getMonth() + 1}-${date.getDate()} ${hour}:00` : 
                                `${hour}:00`;
                        }
                    }
                },
                y: {
                    beginAtZero: true,
                    grid: {
                        color: 'rgba(0, 0, 0, 0.1)'
                    },
                    ticks: {
                        callback: function(value) {
                            return value.toLocaleString();
                        }
                    }
                }
            },
            layout: {
                padding: {
                    left: 10,
                    right: 25,
                    top: 25,
                    bottom: 10
                }
            }
        }
    };

    try {
        timeSeriesChart = new Chart(ctx, config);
        console.log('Time series chart initialized');
        return true;
    } catch (error) {
        console.error('Failed to initialize time series chart:', error);
        return false;
    }
}

// 更新时间序列数据
function updateTimeSeriesData(timeSeriesData) {
    if (!timeSeriesData || !Array.isArray(timeSeriesData) || timeSeriesData.length === 0) {
        console.warn('No time series data available');
        return;
    }

    try {
        if (!timeSeriesChart && !initTimeSeriesChart()) {
            console.warn('Cannot update time series chart: initialization failed');
            return;
        }

        const processedData = timeSeriesData
            .map(d => ({
                timestamp: new Date(d.timestamp),
                total: d.total || 0,
                success: d.success || 0,
                cache_hits: d.cache_hits || 0,
                failed: d.failed || 0,
                blocked: d.blocked || 0,
                cn: d.cn || 0,
                foreign: d.foreign || 0
            }))
            .sort((a, b) => a.timestamp - b.timestamp);

        Object.keys(chartColors).forEach((key, index) => {
            timeSeriesChart.data.datasets[index].data = processedData.map(d => ({
                x: d.timestamp,
                y: d[key]
            }));
        });

        timeSeriesChart.update('none');
        console.log('Time series chart updated with', processedData.length, 'data points');
    } catch (error) {
        console.error('Failed to update time series chart:', error);
        destroyCharts();
        initTimeSeriesChart();
    }
}

// 更新查询分布图表
function updateQueryChart(data) {
    const ctx = document.getElementById('queryChart');
    if (!ctx) return;

    const chartData = {
        labels: Object.values(chartColors).map(c => c.label),
        datasets: [{
            data: [
                data.total_queries || 0,
                data.success || 0,
                data.cache_hits || 0,
                data.failed_queries || 0,
                data.blocked_queries || 0,
                data.cn_queries || 0,
                data.foreign_queries || 0
            ],
            backgroundColor: Object.values(chartColors).map(c => c.border),
            borderWidth: 1,
            borderColor: 'rgba(255, 255, 255, 0.5)'
        }]
    };

    const chartOptions = {
        indexAxis: 'y',
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
            legend: {
                display: false
            },
            tooltip: {
                callbacks: {
                    label: function(context) {
                        const value = context.raw;
                        let percentage = 0;
                        if (context.dataIndex > 0) {
                            const total = context.dataset.data[0];
                            percentage = total ? ((value / total) * 100).toFixed(1) : 0;
                        }
                        return `${value.toLocaleString()}${percentage ? ` (${percentage}%)` : ''}`;
                    }
                }
            }
        },
        scales: {
            x: {
                beginAtZero: true,
                grid: {
                    display: true,
                    color: 'rgba(0, 0, 0, 0.1)'
                }
            },
            y: {
                grid: {
                    display: false
                }
            }
        }
    };

    if (queryChart) {
        queryChart.data = chartData;
        queryChart.update('none');
    } else {
        queryChart = new Chart(ctx, {
            type: 'bar',
            data: chartData,
            options: chartOptions
        });
    }
}
