import requests
from os import getenv
import socket
import getpass

from tqdm.auto import tqdm as tqdm_auto

from .models import ClientPayload


class tqdm(tqdm_auto):
    def __init__(self, *args, **kwargs):
        """
        Parameters
        ----------
        key: str, required. webprogress client token (generated in the web UI
            after logging in). Routes updates to your dashboard.
            [default: ${WEBPROGRESS_KEY}].
        endpoint: str, required. Base URL of the webprogress server.
            [default: ${WEBPROGRESS_ENDPOINT}].

        See `tqdm.auto.tqdm.__init__` for other parameters.
        """
        kwargs = kwargs.copy()
        self.__key = kwargs.pop("key", getenv("WEBPROGRESS_KEY"))
        self.__endpoint = kwargs.pop("endpoint", getenv("WEBPROGRESS_ENDPOINT"))

        super().__init__(*args, **kwargs)

    def display(self, msg: str | None = None, pos: int | None = None):
        super().display(msg, pos)

        # Example of self.format_dict
        # {
        #     "n": 5,
        #     "total": 10,
        #     "elapsed": 8.812697172164917,
        #     "ncols": 191,
        #     "nrows": 15,
        #     "prefix": "foo",
        #     "ascii": False,
        #     "unit": "it",
        #     "unit_scale": False,
        #     "rate": 0.9990209449783868,
        #     "bar_format": None,
        #     "postfix": None,
        #     "unit_divisor": 1000,
        #     "initial": 0,
        #     "colour": None,
        # }
        # self.format_meter(**self.format_dict)
        payload_data = dict(**self.format_dict)
        if payload_data.get("rate", None) is None:
            payload_data["rate"] = 0.0
        if payload_data.get("initial", None) is None:
            payload_data["initial"] = 0.0
        if payload_data.get("colour", None) is None:
            payload_data["colour"] = "#0000ff"

        p = ClientPayload(
            user_hostname=socket.gethostname(),
            user_login=getpass.getuser(),
            progress=payload_data["n"],
            total=payload_data["total"],
            description=payload_data["prefix"],
            elapsed=payload_data["elapsed"],
            unit=payload_data["unit"],
            unit_scale=payload_data["unit_scale"],
            rate=payload_data["rate"],
            unit_divisor=payload_data["unit_divisor"],
            initial=payload_data["initial"],
            colour=payload_data["colour"],
            key=self.__key,
        )
        try:
            requests.post(url=f"{self.__endpoint}/handler", json=p.model_dump(), timeout=0.5)
        except requests.exceptions.RequestException:
            # Never let a down, slow, or rejecting server break the tracked loop.
            pass

    def clear(self, *args, **kwargs):
        super().clear(*args, **kwargs)
        if not self.disable:
            pass
