import time

from webprogress.progress import tqdm_webprogress


def test_client():
    for a in tqdm_webprogress(range(10), desc="foo", key="", endpoint="http://127.0.0.1:8775"):
        time.sleep(1)


if __name__ in {"__main__", "__mp_main__"}:
    test_client()
