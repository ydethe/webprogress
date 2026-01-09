import time
from nicegui import Event, app, ui

sensor = Event[float]()

@app.post('/sensor')
def sensor_webhook(temperature: float):
    sensor.emit(temperature)

def root():
    chart = ui.echart({
        'xAxis': {'type': 'time', 'axisLabel': {'hideOverlap': True}},
        'yAxis': {'type': 'value', 'min': 'dataMin'},
        'series': [{'type': 'line', 'data': [], 'smooth': True}],
    })

    def update_chart(temperature: float):
        data = chart.options['series'][0]['data']
        data.append([time.time(), temperature])
        if len(data) > 10:
            data.pop(0)

    sensor.subscribe(update_chart)

ui.run(root)
