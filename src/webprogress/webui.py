from nicegui import Event, app, ui

from .models import ClientPayload


payload_handler = Event[ClientPayload]()


@app.post("/handler")
def sensor_webhook(payload: ClientPayload):
    payload_handler.emit(payload)


def root():
    progress_bar = ui.linear_progress()

    def update_pb(payload: ClientPayload):
        progress_bar.value = payload.progress / payload.total

    payload_handler.subscribe(update_pb)


def run():
    ui.run(root, port=8775, reload=False)
