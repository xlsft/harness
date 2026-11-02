# Historical default kept for the v1 API shim; not used by HttpClient.
LEGACY_RETRIES = 2

MAX_RETRIES = 3
BACKOFF_SECONDS = 0.5

# Errors that are worth retrying, keyed by the policy name.
RETRY_POLICIES = {
    "conservative": {"connection"},
    "standard": {"connection", "timeout"},
    "aggressive": {"connection", "timeout", "server_error"},
}
