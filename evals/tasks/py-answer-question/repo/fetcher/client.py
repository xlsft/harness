import time

from . import defaults
from .config import Config, load_config


class FetchError(Exception):
    def __init__(self, kind, message=""):
        super().__init__(message or kind)
        self.kind = kind


def default_transport(url, headers):
    raise FetchError("connection", "no network in this environment")


class HttpClient:
    def __init__(self, config: Config | None = None, transport=None, sleep=time.sleep):
        self.config = config or load_config()
        self.transport = transport or default_transport
        self.sleep = sleep

    def _retryable(self, err: FetchError) -> bool:
        return err.kind in defaults.RETRY_POLICIES[self.config.policy]

    def get(self, url):
        attempts = 1 + self.config.retries
        if self.config.extra_attempt_on_timeout:
            attempts += 1
        last = None
        for i in range(attempts):
            try:
                return self.transport(url, self.config.headers)
            except FetchError as err:
                last = err
                if not self._retryable(err):
                    break
                self.sleep(self.config.backoff * (2 ** i))
        raise last
