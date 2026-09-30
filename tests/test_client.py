import time

from webprogress import tqdm
from webprogress.config import settings


def test_client():
    for a in tqdm(range(10), desc="foo", endpoint=settings.base_url):
        time.sleep(1)


if __name__ in {"__main__", "__mp_main__"}:
    test_client()
