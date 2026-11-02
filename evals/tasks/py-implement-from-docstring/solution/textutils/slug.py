"""URL slug helpers."""

import re
import unicodedata


def slugify(text: str, max_length: int = 50) -> str:
    """Reference solution (docstring trimmed)."""
    s = unicodedata.normalize("NFKD", text).encode("ascii", "ignore").decode("ascii").lower()
    s = re.sub(r"[^a-z0-9]+", "-", s).strip("-")
    if len(s) > max_length:
        words = s.split("-")
        out = ""
        for w in words:
            cand = w if not out else out + "-" + w
            if len(cand) > max_length:
                break
            out = cand
        s = out or words[0][:max_length]
    return s.strip("-") or "n-a"
