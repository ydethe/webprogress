import requests
from os import getenv

from tqdm.auto import tqdm as tqdm_auto

from .models import ClientPayload


class tqdm_webprogress(tqdm_auto):
    def __init__(self, *args, **kwargs):
        """
        Parameters
        ----------
        key: str, required. Slack token
            [default: ${WEBPROGRESS_KEY}].
        endpoint: str, required. Slack channel
            [default: ${WEBPROGRESS_ENDPOINT}].

        See `tqdm.auto.tqdm.__init__` for other parameters.
        """
        kwargs = kwargs.copy()
        self.__key = kwargs.pop("key", getenv("WEBPROGRESS_KEY"))
        self.__endpoint = kwargs.pop("endpoint", getenv("WEBPROGRESS_ENDPOINT"))

        super().__init__(*args, **kwargs)

    def display(self, **kwargs):
        super().display(**kwargs)

        self.format_meter(**self.format_dict)

        p = ClientPayload(
            progress=self.format_dict["n"],
            total=self.format_dict["total"],
            description=self.format_dict["prefix"],
            key=self.__key,
        )
        requests.post(url=f"{self.__endpoint}/handler", json=p.model_dump())

    def clear(self, *args, **kwargs):
        super().clear(*args, **kwargs)
        if not self.disable:
            pass
