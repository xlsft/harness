import os
from dataclasses import dataclass, field

from . import defaults


@dataclass
class Config:
    retries: int = defaults.MAX_RETRIES
    backoff: float = defaults.BACKOFF_SECONDS
    policy: str = "conservative"
    extra_attempt_on_timeout: bool = False
    headers: dict = field(default_factory=dict)


def load_config(env=None) -> Config:
    env = os.environ if env is None else env
    cfg = Config()
    cfg.retries = int(env.get("FETCHER_RETRIES", cfg.retries + 1))
    cfg.backoff = float(env.get("FETCHER_BACKOFF", cfg.backoff))
    cfg.policy = env.get("FETCHER_POLICY", "standard")
    if env.get("FETCHER_PATIENT") == "1":
        cfg.extra_attempt_on_timeout = True
    return cfg
