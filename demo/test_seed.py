import tempfile
import unittest
import urllib.request
from pathlib import Path
from unittest import mock

import seed


class DemoSeedTest(unittest.TestCase):
    def test_existing_active_base_is_reused_without_create(self) -> None:
        with mock.patch.object(seed, "find_knowledge_base", return_value="kb-demo"), mock.patch.object(
            seed, "request"
        ) as requester:
            self.assertEqual(seed.ensure_knowledge_base(), "kb-demo")
            requester.assert_not_called()

    def test_conflicting_create_is_resolved_by_listing_again(self) -> None:
        with mock.patch.object(
            seed, "find_knowledge_base", side_effect=[None, "kb-raced"]
        ), mock.patch.object(seed, "request", return_value=(409, {})):
            self.assertEqual(seed.ensure_knowledge_base(), "kb-raced")

    def test_duplicate_active_names_are_rejected(self) -> None:
        with mock.patch.object(
            seed,
            "list_knowledge_bases",
            return_value=[
                {"id": "kb-1", "name": seed.KNOWLEDGE_BASE_NAME, "status": "active"},
                {"id": "kb-2", "name": seed.KNOWLEDGE_BASE_NAME, "status": "active"},
            ],
        ):
            with self.assertRaisesRegex(seed.SeedError, "ambiguous"):
                seed.find_knowledge_base()

    def test_multipart_file_preserves_utf8_csv(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "demo.csv"
            path.write_text("question,answer\n问题,答案\n", encoding="utf-8")

            body, content_type = seed.multipart_file("file", path)

            self.assertIn("multipart/form-data; boundary=", content_type)
            self.assertIn('name="file"; filename="demo.csv"'.encode(), body)
            self.assertIn("问题,答案".encode(), body)
            self.assertTrue(body.endswith(b"--\r\n"))

    def test_invalid_success_response_is_mapped_to_seed_error(self) -> None:
        response = mock.MagicMock()
        response.__enter__.return_value = response
        response.status = 200
        response.read.return_value = b"not-json"
        with mock.patch.object(urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(seed.SeedError, "invalid response"):
                seed.request("GET", "/readyz")


if __name__ == "__main__":
    unittest.main()
