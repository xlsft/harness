import doctest
import unittest

import textutils.slug
from textutils import slugify


class EvalSlugifyTest(unittest.TestCase):
    def test_docstring_examples(self):
        failures, _ = doctest.testmod(textutils.slug)
        self.assertEqual(failures, 0)

    def test_unicode_and_separators(self):
        self.assertEqual(slugify("  Ünïcödé   ---  rocks__ "), "unicode-rocks")
        self.assertEqual(slugify("C++ & Go: 2024/25"), "c-go-2024-25")
        self.assertEqual(slugify("日本語"), "n-a")
        self.assertEqual(slugify(""), "n-a")
        self.assertEqual(slugify("---"), "n-a")
        self.assertEqual(slugify("ﬁle №5"), "file-no5")

    def test_truncation(self):
        self.assertEqual(slugify("the quick brown fox", max_length=9), "the-quick")
        self.assertEqual(slugify("the quick brown fox", max_length=10), "the-quick")
        self.assertEqual(slugify("the quick brown fox", max_length=15), "the-quick-brown")
        self.assertEqual(slugify("the quick brown fox", max_length=19), "the-quick-brown-fox")
        self.assertEqual(slugify("supercalifragilistic word", max_length=5), "super")
        self.assertEqual(slugify("a" * 60), "a" * 50)
        self.assertEqual(len(slugify("word " * 30)), 49)


if __name__ == "__main__":
    unittest.main()
