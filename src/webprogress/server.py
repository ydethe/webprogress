from nicegui import Event, app, ui
from fastapi import Request

from .models import ClientPayload


payload_handler = Event[ClientPayload]()


@app.post("/handler")
def sensor_webhook(payload: ClientPayload, request: Request):
    if request.client is not None:
        payload.user_src_address = request.client.host
    payload_handler.emit(payload)


def root(request: Request):
    progress_bar = ui.linear_progress()

    def update_pb(payload: ClientPayload):
        progress_bar.value = payload.progress / payload.total

    payload_handler.subscribe(update_pb)


def run():
    ui.run(root, port=8775, reload=False)
