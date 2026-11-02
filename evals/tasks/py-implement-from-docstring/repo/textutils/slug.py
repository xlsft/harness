"""URL slug helpers."""


def slugify(text: str, max_length: int = 50) -> str:
    """Convert ``text`` into a URL slug.

    Rules, applied in order:

    1. Normalize with Unicode NFKD and drop every non-ASCII character, so
       accented letters lose their accents ("Crème Brûlée" -> "Creme Brulee").
    2. Lowercase.
    3. Replace every run of characters other than ``a-z`` and ``0-9`` with a
       single hyphen, then strip leading and trailing hyphens.
    4. If the slug is longer than ``max_length``, shorten it to the longest
       prefix made of whole hyphen-separated words that fits in ``max_length``.
       If even the first word is longer than ``max_length``, cut that word to
       exactly ``max_length`` characters. The result never ends with a hyphen.
    5. If the result is empty, return ``"n-a"``.

    >>> slugify("Hello, World!")
    'hello-world'
    >>> slugify("Crème Brûlée -- 2nd edition")
    'creme-brulee-2nd-edition'
    >>> slugify("the quick brown fox", max_length=12)
    'the-quick'
    """
    raise NotImplementedError
