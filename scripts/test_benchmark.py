import unittest

import benchmark


class BenchmarkTest(unittest.TestCase):
    def test_percentile_uses_nearest_rank(self) -> None:
        samples = [float(value) for value in range(1, 101)]

        self.assertEqual(benchmark.percentile(samples, 0.50), 50)
        self.assertEqual(benchmark.percentile(samples, 0.95), 95)
        self.assertEqual(benchmark.percentile(samples, 0.99), 99)

    def test_summary_contains_release_percentiles(self) -> None:
        summary = benchmark.summarize([1, 2, 3, 4, 5])

        self.assertEqual(summary["p50Ms"], 3)
        self.assertEqual(summary["p95Ms"], 5)
        self.assertEqual(summary["p99Ms"], 5)


if __name__ == "__main__":
    unittest.main()
