import unittest

from fetcher import FetchError, HttpClient
from fetcher.config import Config


class ClientTest(unittest.TestCase):
    def test_success(self):
        c = HttpClient(config=Config(), transport=lambda url, h: "ok", sleep=lambda s: None)
        self.assertEqual(c.get("http://x"), "ok")

    def test_non_retryable(self):
        calls = []

        def t(url, h):
            calls.append(url)
            raise FetchError("server_error")

        c = HttpClient(config=Config(policy="standard"), transport=t, sleep=lambda s: None)
        with self.assertRaises(FetchError):
            c.get("http://x")
        self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()
